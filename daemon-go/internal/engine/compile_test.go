// Construct-time verification of the Profile → config compilation (no root, no
// network): every compiled xray client config must be ACCEPTED BY THE REAL
// CORE (BuildXray starts the instance — schema errors surface here), and the
// shape assertions lock the stream-settings mapping.
package engine

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	ilproxy "ironlink/daemon/internal/proxy"
)

// hy2TestPin is a synthetic certificate pin in the colon-hex form panels
// emit: 32 bytes, so the value that parses is the value the real core accepts.
const hy2TestPin = "0A:0A:0A:0A:0A:0A:0A:0A:0A:0A:0A:0A:0A:0A:0A:0A:0A:0A:0A:0A:0A:0A:0A:0A:0A:0A:0A:0A:0A:0A:0A:0A"

// testRealityParams are syntactically VALID for xray's client REALITY build:
// fingerprint must be a known uTLS name, publicKey base64url of 32 bytes,
// shortId hex ≤ 16 chars — garbage values are rejected at BuildXray.
func testReality() ilproxy.Security {
	return ilproxy.Security{Kind: ilproxy.SecurityReality, Reality: &ilproxy.RealityParams{
		SNI: "google.com",
		Fp:  "chrome",
		Pbk: "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY",
		Sid: "01ab",
	}}
}

func testVless(security ilproxy.Security, transport ilproxy.Transport, flow string) *ilproxy.VlessConfig {
	return &ilproxy.VlessConfig{
		ServerName: "test node",
		UUID:       "88f2c0dc-a8e3-49f4-89b9-b3b54f1cad3a",
		Address:    "203.0.113.1", // TEST-NET-3: never dialed at construct time
		Port:       443,
		Encryption: "none",
		Flow:       flow,
		Security:   security,
		Transport:  transport,
	}
}

// TestCompileXrayClientBuildsAllVariants: each security × transport variant
// compiles into a config the pinned xray core accepts and starts.
func TestCompileXrayClientBuildsAllVariants(t *testing.T) {
	cases := []struct {
		name      string
		security  ilproxy.Security
		transport ilproxy.Transport
		flow      string
	}{
		{"reality-tcp-vision", testReality(), ilproxy.Transport{Kind: ilproxy.TransportTCP}, "xtls-rprx-vision"},
		{"reality-xhttp", testReality(), ilproxy.Transport{Kind: ilproxy.TransportXhttp, Xhttp: &ilproxy.XhttpParams{Mode: "auto"}}, ""},
		{"tls-ws", ilproxy.Security{Kind: ilproxy.SecurityTLS, TLS: &ilproxy.TLSParams{SNI: "cdn.example.com", Fp: "chrome"}}, ilproxy.Transport{Kind: ilproxy.TransportWs, Ws: &ilproxy.WsParams{Path: "/ws", Host: "cdn.example.com"}}, ""},
		{"tls-grpc", ilproxy.Security{Kind: ilproxy.SecurityTLS, TLS: &ilproxy.TLSParams{}}, ilproxy.Transport{Kind: ilproxy.TransportGrpc, Grpc: &ilproxy.GrpcParams{ServiceName: "svc"}}, ""},
		{"none-tcp", ilproxy.Security{Kind: ilproxy.SecurityNone}, ilproxy.Transport{Kind: ilproxy.TransportTCP}, ""},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := CompileXrayClient(testVless(tc.security, tc.transport, tc.flow),
				fmt.Sprintf("@il-test-stub-%d", i))
			if err != nil {
				t.Fatalf("CompileXrayClient: %v", err)
			}
			xinst, err := BuildXray(cfg)
			if err != nil {
				t.Fatalf("the real core rejected the compiled config: %v\n%s", err, cfg)
			}
			xinst.Close()
		})
	}
}

// dig walks nested JSON objects/arrays: string keys index objects, int keys
// index arrays.
func dig(t *testing.T, v any, path ...any) any {
	t.Helper()
	for _, p := range path {
		switch key := p.(type) {
		case string:
			m, ok := v.(map[string]any)
			if !ok {
				t.Fatalf("dig %v: not an object at %q (got %T)", path, key, v)
			}
			v = m[key]
		case int:
			a, ok := v.([]any)
			if !ok || key >= len(a) {
				t.Fatalf("dig %v: not an array (or too short) at [%d] (got %T)", path, key, v)
			}
			v = a[key]
		}
	}
	return v
}

// TestCompileXrayClientRealityXhttpShape locks the reality+xhttp mapping
// plus the own-traffic invariants: the fwmark on BOTH outbounds and the
// dns→direct routing rule.
func TestCompileXrayClientRealityXhttpShape(t *testing.T) {
	v := testVless(testReality(),
		ilproxy.Transport{Kind: ilproxy.TransportXhttp, Xhttp: &ilproxy.XhttpParams{
			Path:  "/path",
			Host:  "h.example.com",
			Extra: json.RawMessage(`{"scMaxEachPostBytes":1000000}`),
		}}, "")
	raw, err := CompileXrayClient(v, "@il-test-shape")
	if err != nil {
		t.Fatalf("CompileXrayClient: %v", err)
	}
	var cfg any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("compiled config is not JSON: %v", err)
	}

	proxyOut := dig(t, cfg, "outbounds", 0)
	if got := dig(t, proxyOut, "protocol"); got != "vless" {
		t.Errorf("outbounds[0].protocol = %v, want vless", got)
	}
	if got := dig(t, proxyOut, "settings", "vnext", 0, "address"); got != "203.0.113.1" {
		t.Errorf("vnext address = %v", got)
	}
	if got := dig(t, proxyOut, "settings", "vnext", 0, "users", 0, "id"); got != v.UUID {
		t.Errorf("user id = %v", got)
	}

	stream := dig(t, proxyOut, "streamSettings")
	if got := dig(t, stream, "security"); got != "reality" {
		t.Errorf("security = %v, want reality", got)
	}
	if got := dig(t, stream, "realitySettings", "serverName"); got != "google.com" {
		t.Errorf("realitySettings.serverName = %v", got)
	}
	if got := dig(t, stream, "realitySettings", "publicKey"); got != v.Security.Reality.Pbk {
		t.Errorf("realitySettings.publicKey = %v", got)
	}
	if got := dig(t, stream, "network"); got != "xhttp" {
		t.Errorf("network = %v, want xhttp", got)
	}
	if got := dig(t, stream, "xhttpSettings", "mode"); got != "auto" {
		t.Errorf("xhttpSettings.mode = %v, want the auto default", got)
	}
	if got := dig(t, stream, "xhttpSettings", "extra", "scMaxEachPostBytes"); got != float64(1000000) {
		t.Errorf("xhttpSettings.extra not passed through: %v", got)
	}

	// The own-traffic invariants: marked node dial with xray-side resolution,
	// marked direct, dns routed to direct.
	if got := dig(t, stream, "sockopt", "mark"); got != float64(AutoRedirectOutputMark) {
		t.Errorf("proxy sockopt.mark = %v, want %d", got, AutoRedirectOutputMark)
	}
	if got := dig(t, stream, "sockopt", "domainStrategy"); got != "UseIP" {
		t.Errorf("proxy sockopt.domainStrategy = %v, want UseIP", got)
	}
	if got := dig(t, cfg, "outbounds", 1, "streamSettings", "sockopt", "mark"); got != float64(AutoRedirectOutputMark) {
		t.Errorf("direct sockopt.mark = %v, want %d", got, AutoRedirectOutputMark)
	}
	if got := dig(t, cfg, "routing", "rules", 0, "inboundTag", 0); got != "dns-internal" {
		t.Errorf("routing rule inboundTag = %v, want dns-internal", got)
	}
	if got := dig(t, cfg, "routing", "rules", 0, "outboundTag"); got != "direct" {
		t.Errorf("routing rule outboundTag = %v, want direct", got)
	}
	// The dispatch contract: the node outbound's tag and its inbound-tag rule
	// both equal singleNodeTag, so the backend's ContextWithInbound{Tag:
	// singleNodeTag} reaches exactly this node — compiler and dispatcher agree.
	if got := dig(t, proxyOut, "tag"); got != singleNodeTag {
		t.Errorf("outbounds[0].tag = %v, want %q", got, singleNodeTag)
	}
	if got := dig(t, cfg, "routing", "rules", 1, "inboundTag", 0); got != singleNodeTag {
		t.Errorf("per-node rule inboundTag = %v, want %q", got, singleNodeTag)
	}
	if got := dig(t, cfg, "routing", "rules", 1, "outboundTag"); got != singleNodeTag {
		t.Errorf("per-node rule outboundTag = %v, want %q", got, singleNodeTag)
	}
}

// TestCompileXrayClientHysteria2 locks the xray-side hysteria2 mapping under
// the real core: a pinned + salamander node compiles, loads and STARTS, its
// shape and the own-traffic sockopt are as compiled, and the two nodes xray
// cannot take are rejected where they should be — insecure without a pin at
// CompileXrayClient, a malformed pin (stored verbatim) at BuildXray.
func TestCompileXrayClientHysteria2(t *testing.T) {
	p := &ilproxy.Hysteria2Config{ServerName: "hy2", Address: "203.0.113.1", Port: 443, Password: "pw",
		SNI: "cdn.example.com", Obfs: "salamander", ObfsPassword: "x", PinSHA256: hy2TestPin}
	raw, err := CompileXrayClient(p, "@il-test-hy2")
	if err != nil {
		t.Fatalf("CompileXrayClient: %v", err)
	}
	xinst, err := BuildXray(raw)
	if err != nil {
		t.Fatalf("the real core rejected the compiled config: %v\n%s", err, raw)
	}
	xinst.Close()

	var cfg any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("compiled config is not JSON: %v", err)
	}
	proxyOut := dig(t, cfg, "outbounds", 0)
	for _, c := range []struct {
		path []any
		want any
	}{
		{[]any{"protocol"}, "hysteria"},
		{[]any{"settings", "version"}, float64(2)},
		{[]any{"streamSettings", "network"}, "hysteria"},
		{[]any{"streamSettings", "security"}, "tls"},
		{[]any{"streamSettings", "tlsSettings", "serverName"}, "cdn.example.com"},
		{[]any{"streamSettings", "tlsSettings", "pinnedPeerCertSha256"}, hy2TestPin},
		{[]any{"streamSettings", "hysteriaSettings", "auth"}, "pw"},
		{[]any{"streamSettings", "finalmask", "udp", 0, "type"}, "salamander"},
		{[]any{"streamSettings", "sockopt", "mark"}, float64(AutoRedirectOutputMark)},
		{[]any{"streamSettings", "sockopt", "domainStrategy"}, "UseIP"},
	} {
		if got := dig(t, proxyOut, c.path...); got != c.want {
			t.Errorf("outbounds[0].%v = %v, want %v", c.path, got, c.want)
		}
	}

	// negative 1: insecure without a pin never reaches xray
	if _, err := CompileXrayClient(&ilproxy.Hysteria2Config{Address: "203.0.113.1", Port: 443, Password: "pw", Insecure: true}, "@il-test-hy2-bad"); err == nil {
		t.Fatal("insecure hysteria2 node without a pin compiled for xray")
	}
	// negative 2: a malformed pin is stored verbatim and rejected by the core's config build
	raw, err = CompileXrayClient(&ilproxy.Hysteria2Config{Address: "203.0.113.1", Port: 443, Password: "pw", PinSHA256: "AA:BB"}, "@il-test-hy2-pin")
	if err != nil {
		t.Fatalf("CompileXrayClient (malformed pin): %v", err)
	}
	if xinst, err := BuildXray(raw); err == nil {
		xinst.Close()
		t.Fatal("2-byte pin accepted")
	} else if !strings.Contains(err.Error(), "pinnedPeerCertSha256") {
		t.Fatalf("unexpected rejection: %v", err)
	}
}

// TestNodeSocksConfigsStart: the no-root node pair (sing-box SOCKS inbound +
// real-node xray client) assembles AND STARTS with the real cores — the
// construct-time proof for the path TestRealNodeViaSocks then drives live.
func TestNodeSocksConfigsStart(t *testing.T) {
	v := testVless(testReality(), ilproxy.Transport{Kind: ilproxy.TransportXhttp, Xhttp: &ilproxy.XhttpParams{}}, "")
	sbCfg, xrayCfg, err := NodeSocksConfigs(v, "127.0.0.1", freePort(t))
	if err != nil {
		t.Fatalf("NodeSocksConfigs: %v", err)
	}
	sess, err := Start(sbCfg, xrayCfg)
	if err != nil {
		t.Fatalf("Start (socks + real-node xray config): %v", err)
	}
	sess.Close()
}

// namedNode builds a plan member whose UUID tag and display name are both the
// given string, so a plan whose ActiveTag is that string still validates (the
// member tag is the ID). Tests that need distinct id/name override .ID after.
func namedNode(name, addr string, security ilproxy.Security, transport ilproxy.Transport) NamedNode {
	v := testVless(security, transport, "")
	v.ServerName = name
	v.Address = addr
	return NamedNode{ID: name, Name: name, Profile: v}
}
