package subscription

import (
	"testing"

	"ironlink/daemon/internal/proxy"
)

func TestParseSingBoxFixture(t *testing.T) {
	o, err := Parse(readFixture(t, "singbox.json"), "sub", FormatAuto)
	if err != nil {
		t.Fatal(err)
	}
	if o.Format != FormatSingBox {
		t.Fatalf("format = %s, want sing-box", o.Format)
	}
	// 11 outbounds in the config; selector/urltest/direct are infrastructure
	// and never become entries — 8 proxy outbounds, all usable.
	if o.Entries != 8 || len(o.Nodes) != 8 || o.Duplicates != 0 || o.Unrecognized != 0 {
		t.Fatalf("accounting = %d entries / %d nodes / %d duplicates / %d unrecognized, want 8/8/0/0",
			o.Entries, len(o.Nodes), o.Duplicates, o.Unrecognized)
	}
	for i := range o.Nodes {
		switch n := o.Nodes[i].DisplayName(); n {
		case "Proxy", "Fastest", "direct":
			t.Errorf("infrastructure outbound %q must be skipped", n)
		}
	}

	// vless + reality (+ vision flow, utls fingerprint).
	vl, ok := findProfile(t, o, "Stockholm Reality").(*proxy.VlessConfig)
	if !ok {
		t.Fatal("Stockholm Reality is not a vless node")
	}
	if vl.UUID != "00000000-0000-4000-8000-0000000000aa" ||
		vl.ServerAddress() != "198.51.100.20" || vl.ServerPort() != 443 {
		t.Errorf("vless identity: %+v", vl)
	}
	if vl.Flow != "xtls-rprx-vision" || vl.TransportLabel() != "tcp" || vl.SecurityLabel() != "reality" {
		t.Errorf("vless flow/labels: %q %s/%s", vl.Flow, vl.TransportLabel(), vl.SecurityLabel())
	}
	r := vl.Security.Reality
	if r == nil || r.SNI != "www.example.com" || r.Fp != "chrome" ||
		r.Pbk != "singbox-reality-pbk-placeholder" || r.Sid != "abcd0123" {
		t.Errorf("vless reality params: %+v", r)
	}

	// shadowsocks — the SS2022 PSK must round-trip verbatim.
	ss, ok := findProfile(t, o, "Oslo SS").(*proxy.ShadowsocksConfig)
	if !ok {
		t.Fatal("Oslo SS is not a shadowsocks node")
	}
	if ss.Method != "2022-blake3-aes-128-gcm" || ss.Password != "c3MtcGxhY2Vob2xkZXIta2V5" ||
		ss.ServerAddress() != "198.51.100.21" || ss.ServerPort() != 8388 || ss.Plugin != "" {
		t.Errorf("shadowsocks fields: %+v", ss)
	}

	// hysteria2 with salamander obfs.
	hy2, ok := findProfile(t, o, "Helsinki Hy2").(*proxy.Hysteria2Config)
	if !ok {
		t.Fatal("Helsinki Hy2 is not a hysteria2 node")
	}
	if hy2.Password != "hy2-placeholder-pass" || hy2.SNI != "hy2.example.com" || !hy2.Insecure {
		t.Errorf("hysteria2 fields: %+v", hy2)
	}
	if hy2.Obfs != "salamander" || hy2.ObfsPassword != "obfs-placeholder" {
		t.Errorf("hysteria2 obfs: %q/%q", hy2.Obfs, hy2.ObfsPassword)
	}

	// trojan over ws (transport block + Host header).
	tj, ok := findProfile(t, o, "Riga Trojan").(*proxy.TrojanConfig)
	if !ok {
		t.Fatal("Riga Trojan is not a trojan node")
	}
	if tj.Password != "trojan-placeholder-pass" || tj.Security.SNI() != "riga.example.org" {
		t.Errorf("trojan fields: %+v", tj)
	}
	if tj.TransportLabel() != "ws" || tj.Transport.Ws == nil ||
		tj.Transport.Ws.Path != "/trojan" || tj.Transport.Ws.Host != "riga.example.org" {
		t.Errorf("trojan transport: %+v", tj.Transport)
	}

	// tuic v5.
	tu, ok := findProfile(t, o, "Tallinn TUIC").(*proxy.TUICConfig)
	if !ok {
		t.Fatal("Tallinn TUIC is not a tuic node")
	}
	if tu.UUID != "00000000-0000-4000-8000-0000000000bb" || tu.Password != "tuic-placeholder-pass" {
		t.Errorf("tuic identity: %+v", tu)
	}
	if tu.SNI != "tuic.example.com" || tu.CongestionControl != "bbr" || tu.UDPRelayMode != "native" {
		t.Errorf("tuic options: %+v", tu)
	}
	if len(tu.ALPN) != 1 || tu.ALPN[0] != "h3" {
		t.Errorf("tuic alpn: %v", tu.ALPN)
	}

	// vmess over ws+tls (no Host header — the path alone).
	vm, ok := findProfile(t, o, "Kyiv VMess").(*proxy.VmessConfig)
	if !ok {
		t.Fatal("Kyiv VMess is not a vmess node")
	}
	if vm.UUID != "00000000-0000-4000-8000-0000000000cc" || vm.Cipher != "auto" || vm.AlterID != 0 {
		t.Errorf("vmess identity: %+v", vm)
	}
	if vm.TransportLabel() != "ws" || vm.Transport.Ws == nil || vm.Transport.Ws.Path != "/vm" {
		t.Errorf("vmess transport: %+v", vm.Transport)
	}
	if vm.SecurityLabel() != "tls" || vm.Security.SNI() != "kyiv.example.com" {
		t.Errorf("vmess security: %s sni=%q", vm.SecurityLabel(), vm.Security.SNI())
	}

	// anytls.
	at, ok := findProfile(t, o, "Vilnius AnyTLS").(*proxy.AnyTLSConfig)
	if !ok {
		t.Fatal("Vilnius AnyTLS is not an anytls node")
	}
	if at.Password != "anytls-placeholder-pass" || at.SNI != "vilnius.example.com" || at.ServerPort() != 8443 {
		t.Errorf("anytls fields: %+v", at)
	}

	// hysteria v1.
	hy1, ok := findProfile(t, o, "Legacy Hy1").(*proxy.HysteriaConfig)
	if !ok {
		t.Fatal("Legacy Hy1 is not a hysteria node")
	}
	if hy1.Auth != "hy1-placeholder-auth" || hy1.SNI != "legacy.example.com" ||
		hy1.UpMbps != 100 || hy1.DownMbps != 100 || hy1.Obfs != "obfs-placeholder" || hy1.ALPN != "hysteria" {
		t.Errorf("hysteria v1 fields: %+v", hy1)
	}
}

func TestParseSingBoxFallbackPerOutbound(t *testing.T) {
	// An unknown outbound type fails the strict full-config decode; the
	// per-outbound fallback must keep the good node and count the alien one
	// unrecognized (while direct stays invisible).
	body := `{"outbounds": [
		{"type": "vless", "tag": "Solo", "server": "203.0.113.60", "server_port": 443, "uuid": "00000000-0000-4000-8000-000000000060"},
		{"type": "mystery", "tag": "alien", "server": "203.0.113.61"},
		{"type": "direct", "tag": "direct"}
	]}`
	o, err := Parse(body, "s", FormatAuto)
	if err != nil {
		t.Fatal(err)
	}
	if o.Format != FormatSingBox {
		t.Fatalf("format = %s, want sing-box", o.Format)
	}
	if o.Entries != 2 || len(o.Nodes) != 1 || o.Unrecognized != 1 {
		t.Fatalf("accounting = %d entries / %d nodes / %d unrecognized, want 2/1/1",
			o.Entries, len(o.Nodes), o.Unrecognized)
	}
	solo := findProfile(t, o, "Solo")
	if solo.Kind() != proxy.ProtocolVless || solo.ServerAddress() != "203.0.113.60" {
		t.Errorf("surviving node: %s %s", solo.Kind(), solo.ServerAddress())
	}
}

func TestParseSingBoxStructuralGates(t *testing.T) {
	for _, body := range []string{
		`{"proxies": []}`,                        // no outbounds at all
		`{"outbounds": []}`,                      // empty outbounds
		`{"outbounds": [{"protocol": "vless"}]}`, // xray keying
		`[{"type": "vless"}]`,                    // array, not a config object
		"proxies: []",                            // clash YAML
		"vless://uuid@203.0.113.1:443#link",      // share link
	} {
		if _, ok := parseSingBox(body); ok {
			t.Errorf("body %q must not match sing-box", body)
		}
	}
}
