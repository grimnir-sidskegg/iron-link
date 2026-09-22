package subscription

import (
	"slices"
	"strings"
	"testing"

	"ironlink/daemon/internal/api"
	"ironlink/daemon/internal/proxy"
)

func TestParseXrayFixture(t *testing.T) {
	o, err := Parse(readFixture(t, "xray.json"), "sub", FormatAuto)
	if err != nil {
		t.Fatal(err)
	}
	if o.Format != FormatXray {
		t.Fatalf("format = %s, want xray", o.Format)
	}
	// 12 entries: the "Auto" balancer (3 members), the regional balancer (3
	// members, one unique to it), two singles duplicating balancer members,
	// six more singles, and a wireguard entry we cannot express.
	if o.Entries != 12 || len(o.Nodes) != 11 || o.Duplicates != 4 || o.Unrecognized != 1 {
		t.Fatalf("accounting = %d entries / %d nodes / %d duplicates / %d unrecognized, want 12/11/4/1",
			o.Entries, len(o.Nodes), o.Duplicates, o.Unrecognized)
	}
	if o.Nodes[0].SubID == nil || *o.Nodes[0].SubID != "sub" {
		t.Errorf("sub id not attached: %v", o.Nodes[0].SubID)
	}

	// A server listed both as a dedicated entry and as a balancer member keeps
	// the dedicated entry's name (singles dedup first), with the balancer's
	// copy counted a duplicate.
	ams, ok := findProfile(t, o, "🇳🇱 Amsterdam 1").(*proxy.VlessConfig)
	if !ok {
		t.Fatal("🇳🇱 Amsterdam 1 is not a vless node")
	}
	if ams.UUID != "00000000-0000-4000-8000-000000000001" || ams.ServerPort() != 443 {
		t.Errorf("deduped single identity: %+v", ams)
	}
	if ams.TransportLabel() != "tcp" || ams.SecurityLabel() != "tls" || ams.Security.SNI() != "ams.example.com" {
		t.Errorf("deduped single stream: %s/%s sni=%q", ams.TransportLabel(), ams.SecurityLabel(), ams.Security.SNI())
	}
	for i := range o.Nodes {
		if n := o.Nodes[i].DisplayName(); strings.HasSuffix(n, "198.51.100.10") || strings.HasSuffix(n, "198.51.100.11") {
			t.Errorf("balancer copy %q must have lost the dedup to its dedicated entry", n)
		}
	}

	// Balancer members that exist nowhere else survive under the entry name
	// suffixed with their address.
	nyc, ok := findProfile(t, o, "Auto · 198.51.100.12").(*proxy.VlessConfig)
	if !ok {
		t.Fatal("Auto · 198.51.100.12 is not a vless node")
	}
	if nyc.UUID != "00000000-0000-4000-8000-000000000003" || nyc.Security.SNI() != "nyc.example.com" {
		t.Errorf("balancer-only member: %+v", nyc)
	}
	vie, ok := findProfile(t, o, "Europe · 198.51.100.13").(*proxy.VlessConfig)
	if !ok {
		t.Fatal("Europe · 198.51.100.13 is not a vless node")
	}
	if vie.UUID != "00000000-0000-4000-8000-000000000004" || vie.Security.SNI() != "vie.example.com" {
		t.Errorf("regional-only member: %+v", vie)
	}

	// hysteriaSettings version 2 → hysteria2:// (auth → password); the
	// finalmask blob changed nothing, and the emoji name survived the
	// fragment round-trip.
	hy2, ok := findProfile(t, o, "🚀 Fast Tunnel").(*proxy.Hysteria2Config)
	if !ok {
		t.Fatal("🚀 Fast Tunnel is not a hysteria2 node")
	}
	if hy2.ServerAddress() != "203.0.113.20" || hy2.ServerPort() != 8443 {
		t.Errorf("hysteria2 endpoint: %s:%d", hy2.ServerAddress(), hy2.ServerPort())
	}
	if hy2.Password != "hy2-placeholder-auth" || hy2.SNI != "cdn.example.com" || !hy2.Insecure {
		t.Errorf("hysteria2 fields: %+v", hy2)
	}
	if hy2.TransportLabel() != "quic" || hy2.SecurityLabel() != "tls" {
		t.Errorf("hysteria2 labels: %s/%s", hy2.TransportLabel(), hy2.SecurityLabel())
	}

	// ws + tls vless single.
	ws, ok := findProfile(t, o, "WS Rotterdam").(*proxy.VlessConfig)
	if !ok {
		t.Fatal("WS Rotterdam is not a vless node")
	}
	if ws.TransportLabel() != "ws" || ws.Transport.Ws == nil ||
		ws.Transport.Ws.Path != "/ws" || ws.Transport.Ws.Host != "ws.example.org" {
		t.Errorf("ws transport: %+v", ws.Transport)
	}
	if ws.SecurityLabel() != "tls" || ws.Security.SNI() != "ws.example.org" {
		t.Errorf("ws security: %s sni=%q", ws.SecurityLabel(), ws.Security.SNI())
	}

	// reality vless single.
	rl, ok := findProfile(t, o, "Reality Vienna").(*proxy.VlessConfig)
	if !ok {
		t.Fatal("Reality Vienna is not a vless node")
	}
	if rl.Flow != "xtls-rprx-vision" || rl.SecurityLabel() != "reality" {
		t.Errorf("reality fields: flow=%q security=%s", rl.Flow, rl.SecurityLabel())
	}
	r := rl.Security.Reality
	if r == nil || r.SNI != "www.example.com" || r.Fp != "chrome" ||
		r.Pbk != "xray-reality-pbk-placeholder" || r.Sid != "01ab" {
		t.Errorf("reality params: %+v", r)
	}

	// vmess vnext → the base64-JSON vmess:// form.
	vm, ok := findProfile(t, o, "Osaka").(*proxy.VmessConfig)
	if !ok {
		t.Fatal("Osaka is not a vmess node")
	}
	if vm.UUID != "00000000-0000-4000-8000-000000000007" || vm.AlterID != 0 || vm.Cipher != "auto" {
		t.Errorf("vmess identity: %+v", vm)
	}
	if vm.TransportLabel() != "ws" || vm.Transport.Ws == nil ||
		vm.Transport.Ws.Path != "/vm" || vm.Transport.Ws.Host != "osaka.example.com" {
		t.Errorf("vmess transport: %+v", vm.Transport)
	}
	if vm.Security.SNI() != "osaka.example.com" {
		t.Errorf("vmess sni: %q", vm.Security.SNI())
	}

	// trojan servers[] over grpc.
	tj, ok := findProfile(t, o, "Warsaw").(*proxy.TrojanConfig)
	if !ok {
		t.Fatal("Warsaw is not a trojan node")
	}
	if tj.Password != "trojan-placeholder-pass" || tj.ServerAddress() != "203.0.113.31" {
		t.Errorf("trojan fields: %+v", tj)
	}
	if tj.TransportLabel() != "grpc" || tj.Transport.Grpc == nil || tj.Transport.Grpc.ServiceName != "TrojanService" {
		t.Errorf("trojan transport: %+v", tj.Transport)
	}

	// shadowsocks servers[].
	ss, ok := findProfile(t, o, "Cairo").(*proxy.ShadowsocksConfig)
	if !ok {
		t.Fatal("Cairo is not a shadowsocks node")
	}
	if ss.Method != "aes-256-gcm" || ss.Password != "ss-placeholder-pass" || ss.ServerPort() != 8388 {
		t.Errorf("shadowsocks fields: %+v", ss)
	}

	// hysteriaSettings version 1 → hysteria:// (the fields hysteria.go reads).
	hy1, ok := findProfile(t, o, "Legacy Hy1").(*proxy.HysteriaConfig)
	if !ok {
		t.Fatal("Legacy Hy1 is not a hysteria node")
	}
	if hy1.Auth != "hy1-placeholder-auth" || hy1.SNI != "legacy.example.com" ||
		hy1.ALPN != "hysteria" || hy1.ServerPort() != 9443 {
		t.Errorf("hysteria v1 fields: %+v", hy1)
	}
}

func TestParseXrayHysteria2Pinned(t *testing.T) {
	// The panel /json shape for a self-signed hysteria2 node: a certificate
	// pin instead of allowInsecure (which xray-core itself rejects today) plus
	// the salamander mask under finalmask. Both must survive the link
	// round-trip, and the pin is what makes the node xray-eligible.
	body := `{"remarks":"Pinned Hy2","outbounds":[{"tag":"proxy","protocol":"hysteria","settings":{"address":"203.0.113.60","port":443,"version":2},"streamSettings":{"network":"hysteria","security":"tls","tlsSettings":{"serverName":"hy2.example.com","alpn":["h3"],"pinnedPeerCertSha256":"` + hy2TestPin + `"},"hysteriaSettings":{"auth":"hy2-placeholder-auth","version":2},"finalmask":{"udp":[{"type":"salamander","settings":{"password":"obfs-placeholder"}}],"quicParams":{"congestion":"bbr"}}}},{"tag":"direct","protocol":"freedom","settings":{}}]}`
	o, err := Parse(body, "s", FormatXray)
	if err != nil {
		t.Fatal(err)
	}
	hy2, ok := findProfile(t, o, "Pinned Hy2").(*proxy.Hysteria2Config)
	if !ok {
		t.Fatal("Pinned Hy2 is not a hysteria2 node")
	}
	if hy2.PinSHA256 != hy2TestPin || hy2.Obfs != "salamander" || hy2.ObfsPassword != "obfs-placeholder" || hy2.Insecure {
		t.Errorf("hysteria2 pin/obfs: %+v", hy2)
	}
	if hy2.SNI != "hy2.example.com" || hy2.Password != "hy2-placeholder-auth" {
		t.Errorf("hysteria2 fields: %+v", hy2)
	}
	if cores := proxy.EligibleCores(hy2); !slices.Contains(cores, api.CoreXray) {
		t.Errorf("pinned hysteria2 node must be xray-eligible: %v", cores)
	}
}

func TestParseXraySingleObject(t *testing.T) {
	// A body that is ONE full profile object (not an array) is the same
	// dialect, and auto-detection must land on xray, not fall through.
	body := `{
		"remarks": "Solo Object",
		"outbounds": [
			{"tag": "proxy", "protocol": "vless",
			 "settings": {"vnext": [{"address": "203.0.113.50", "port": 443, "users": [{"id": "00000000-0000-4000-8000-000000000050", "encryption": "none"}]}]},
			 "streamSettings": {"network": "tcp", "security": "none"}},
			{"tag": "direct", "protocol": "freedom", "settings": {}}
		]
	}`
	o, err := Parse(body, "s", FormatAuto)
	if err != nil {
		t.Fatal(err)
	}
	if o.Format != FormatXray || len(o.Nodes) != 1 || o.Entries != 1 {
		t.Fatalf("single object: format=%s nodes=%d entries=%d", o.Format, len(o.Nodes), o.Entries)
	}
	solo := findProfile(t, o, "Solo Object")
	if solo.Kind() != proxy.ProtocolVless || solo.SecurityLabel() != "none" {
		t.Errorf("solo node: %s/%s", solo.Kind(), solo.SecurityLabel())
	}
}

func TestParseXrayStructuralGates(t *testing.T) {
	// Outbounds keyed by "type" are a sing-box config — not this dialect.
	if _, _, ok := parseXray(readFixture(t, "singbox.json")); ok {
		t.Error("sing-box config must not match xray")
	}
	// And the sing-box parser must refuse the xray fixture in return.
	if _, _, ok := parseSingBox(readFixture(t, "xray.json")); ok {
		t.Error("xray profile array must not match sing-box")
	}
	for _, body := range []string{
		`{"log": {}, "inbounds": []}`,         // object without protocol outbounds
		`[{"remarks": "x", "outbounds": []}]`, // no outbound carries "protocol"
		`[1, 2, 3]`,                           // array of non-objects
		"vless://uuid@203.0.113.1:443#link",   // share link
		"proxies: []",                         // clash YAML
	} {
		if _, _, ok := parseXray(body); ok {
			t.Errorf("body %q must not match xray", body)
		}
	}
}
