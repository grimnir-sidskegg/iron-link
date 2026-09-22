package subscription

import (
	"os"
	"testing"

	"ironlink/daemon/internal/proxy"
)

// hy2TestPin is a synthetic certificate pin in the colon-hex form panels and
// mihomo emit: 32 bytes, so the value that parses is the value xray accepts
// (shared with the xray tests; the clash fixture carries the same literal).
const hy2TestPin = "0A:0A:0A:0A:0A:0A:0A:0A:0A:0A:0A:0A:0A:0A:0A:0A:0A:0A:0A:0A:0A:0A:0A:0A:0A:0A:0A:0A:0A:0A:0A:0A"

// readFixture loads a testdata body (shared with the SIP008 tests).
func readFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// findProfile locates an outcome node by display name (shared with the SIP008
// tests) — fixture order is not part of the contract, names are.
func findProfile(t *testing.T, o *Outcome, name string) proxy.Profile {
	t.Helper()
	for i := range o.Nodes {
		if o.Nodes[i].DisplayName() == name {
			return o.Nodes[i].Profile()
		}
	}
	t.Fatalf("node %q not in outcome (%d nodes)", name, len(o.Nodes))
	return nil
}

func TestParseClashFixture(t *testing.T) {
	o, err := Parse(readFixture(t, "clash.yaml"), "sub", FormatAuto)
	if err != nil {
		t.Fatal(err)
	}
	if o.Format != FormatClash {
		t.Fatalf("format = %s, want clash", o.Format)
	}
	// 9 proxies: 7 usable nodes, the duplicated ss server, the snell entry.
	if o.Entries != 9 || len(o.Nodes) != 7 || o.Duplicates != 1 || o.Unrecognized != 1 {
		t.Fatalf("accounting = %d entries / %d nodes / %d duplicates / %d unrecognized, want 9/7/1/1",
			o.Entries, len(o.Nodes), o.Duplicates, o.Unrecognized)
	}
	if o.Nodes[0].SubID == nil || *o.Nodes[0].SubID != "sub" {
		t.Errorf("sub id not attached: %v", o.Nodes[0].SubID)
	}

	// ss — and the emoji in the name must survive the fragment round-trip.
	ss, ok := findProfile(t, o, "US Basic ⚡").(*proxy.ShadowsocksConfig)
	if !ok {
		t.Fatal("US Basic ⚡ is not a shadowsocks node")
	}
	if ss.Kind() != proxy.ProtocolShadowsocks || ss.ServerAddress() != "198.51.100.10" || ss.ServerPort() != 8388 {
		t.Errorf("ss endpoint: %s %s:%d", ss.Kind(), ss.ServerAddress(), ss.ServerPort())
	}
	if ss.Method != "aes-256-gcm" || ss.Password != "ss-placeholder-pass" || ss.Plugin != "" {
		t.Errorf("ss fields: %+v", ss)
	}

	// vmess over ws+tls.
	vm, ok := findProfile(t, o, "WS VMess").(*proxy.VmessConfig)
	if !ok {
		t.Fatal("WS VMess is not a vmess node")
	}
	if vm.UUID != "6ba7b811-9dad-11d1-80b4-00c04fd430c8" || vm.AlterID != 0 || vm.Cipher != "auto" {
		t.Errorf("vmess identity: %+v", vm)
	}
	if vm.ServerAddress() != "198.51.100.20" || vm.ServerPort() != 443 {
		t.Errorf("vmess endpoint: %s:%d", vm.ServerAddress(), vm.ServerPort())
	}
	if vm.TransportLabel() != "ws" || vm.SecurityLabel() != "tls" {
		t.Errorf("vmess labels: %s/%s", vm.TransportLabel(), vm.SecurityLabel())
	}
	if vm.Transport.Ws == nil || vm.Transport.Ws.Path != "/ws" || vm.Transport.Ws.Host != "cdn.example.com" {
		t.Errorf("vmess ws opts: %+v", vm.Transport.Ws)
	}
	if vm.Security.SNI() != "cdn.example.com" {
		t.Errorf("vmess sni: %q", vm.Security.SNI())
	}

	// vless + reality.
	vl, ok := findProfile(t, o, "Reality Vision").(*proxy.VlessConfig)
	if !ok {
		t.Fatal("Reality Vision is not a vless node")
	}
	if vl.ServerAddress() != "203.0.113.30" || vl.ServerPort() != 443 || vl.Flow != "xtls-rprx-vision" {
		t.Errorf("vless fields: %+v", vl)
	}
	if vl.SecurityLabel() != "reality" || vl.TransportLabel() != "tcp" {
		t.Errorf("vless labels: %s/%s", vl.SecurityLabel(), vl.TransportLabel())
	}
	r := vl.Security.Reality
	if r == nil || r.SNI != "www.example.com" || r.Fp != "chrome" ||
		r.Pbk != "placeholder-public-key" || r.Sid != "0123ab" {
		t.Errorf("vless reality params: %+v", r)
	}

	// trojan over grpc (sni comes from the clash `sni` key).
	tj, ok := findProfile(t, o, "gRPC Trojan").(*proxy.TrojanConfig)
	if !ok {
		t.Fatal("gRPC Trojan is not a trojan node")
	}
	if tj.Password != "trojan-placeholder-pass" || tj.Security.SNI() != "trojan.example.org" {
		t.Errorf("trojan fields: %+v", tj)
	}
	if tj.TransportLabel() != "grpc" || tj.Transport.Grpc == nil || tj.Transport.Grpc.ServiceName != "TrojanService" {
		t.Errorf("trojan transport: %+v", tj.Transport)
	}

	// hysteria2 with salamander obfs; the clash alpn has no field and is
	// dropped; fingerprint becomes the pin.
	hy, ok := findProfile(t, o, "Hy2 Node").(*proxy.Hysteria2Config)
	if !ok {
		t.Fatal("Hy2 Node is not a hysteria2 node")
	}
	if hy.Password != "hy2-placeholder-pass" || hy.SNI != "hy2.example.org" || !hy.Insecure {
		t.Errorf("hysteria2 fields: %+v", hy)
	}
	if hy.Obfs != "salamander" || hy.ObfsPassword != "obfs-placeholder" {
		t.Errorf("hysteria2 obfs: %q/%q", hy.Obfs, hy.ObfsPassword)
	}
	if hy.PinSHA256 != hy2TestPin {
		t.Errorf("hysteria2 pin (mihomo fingerprint): %q", hy.PinSHA256)
	}

	// tuic v5.
	tu, ok := findProfile(t, o, "TUIC Node").(*proxy.TUICConfig)
	if !ok {
		t.Fatal("TUIC Node is not a tuic node")
	}
	if tu.UUID != "6ba7b814-9dad-11d1-80b4-00c04fd430c8" || tu.Password != "tuic-placeholder-pass" {
		t.Errorf("tuic identity: %+v", tu)
	}
	if tu.SNI != "tuic.example.org" || tu.CongestionControl != "bbr" || tu.UDPRelayMode != "native" {
		t.Errorf("tuic options: %+v", tu)
	}
	if len(tu.ALPN) != 1 || tu.ALPN[0] != "h3" {
		t.Errorf("tuic alpn: %v", tu.ALPN)
	}

	// anytls.
	at, ok := findProfile(t, o, "AnyTLS Node").(*proxy.AnyTLSConfig)
	if !ok {
		t.Fatal("AnyTLS Node is not an anytls node")
	}
	if at.Password != "anytls-placeholder-pass" || at.SNI != "anytls.example.org" || at.ServerPort() != 8443 {
		t.Errorf("anytls fields: %+v", at)
	}
}

func TestParseClashStructuralGates(t *testing.T) {
	// A JSON body without `proxies` is valid YAML but not the dialect.
	if _, _, ok := parseClash(`{"servers": []}`); ok {
		t.Error("JSON without proxies must not match clash")
	}
	// A proxies list with no usable element is no match either.
	for _, body := range []string{
		"proxies: []",
		"proxies:\n  - name: incomplete",
		"mode: rule\nlog-level: info",
		"just a scalar",
	} {
		if _, _, ok := parseClash(body); ok {
			t.Errorf("body %q must not match clash", body)
		}
	}
}

func TestParseClashSkipsInexpressibleNodes(t *testing.T) {
	// One good ss anchors the parse; the h2 vmess and the shadow-tls plugin
	// have no expressible form and must count unrecognized; the obfs plugin
	// converts to the obfs-local SIP003 pair.
	body := `proxies:
  - {name: keeper, type: ss, server: 198.51.100.90, port: 8388, cipher: aes-128-gcm, password: pw}
  - {name: h2 node, type: vmess, server: 198.51.100.91, port: 443, uuid: 6ba7b815-9dad-11d1-80b4-00c04fd430c8, network: h2}
  - {name: stls node, type: ss, server: 198.51.100.92, port: 8388, cipher: aes-128-gcm, password: pw, plugin: shadow-tls, plugin-opts: {host: cloud.example.com}}
  - {name: obfs node, type: ss, server: 198.51.100.93, port: 8388, cipher: aes-128-gcm, password: pw, plugin: obfs, plugin-opts: {mode: http, host: obfs.example.com}}
`
	o, err := Parse(body, "s", FormatClash)
	if err != nil {
		t.Fatal(err)
	}
	if o.Entries != 4 || len(o.Nodes) != 2 || o.Unrecognized != 2 {
		t.Fatalf("accounting = %d entries / %d nodes / %d unrecognized, want 4/2/2",
			o.Entries, len(o.Nodes), o.Unrecognized)
	}
	obfs, ok := findProfile(t, o, "obfs node").(*proxy.ShadowsocksConfig)
	if !ok {
		t.Fatal("obfs node is not a shadowsocks node")
	}
	if obfs.Plugin != "obfs-local" || obfs.PluginOpts != "obfs=http;obfs-host=obfs.example.com" {
		t.Errorf("obfs plugin conversion: %q / %q", obfs.Plugin, obfs.PluginOpts)
	}
}
