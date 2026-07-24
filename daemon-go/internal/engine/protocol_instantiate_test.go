// From-zero proof that the non-VLESS TCP-family protocols are real in the built
// daemon, not merely serialized to a matching map. For each, a representative
// share link is parsed (proxy.ParseURL), the native sing-box outbound is built,
// and it is fed through the real BuildBox — box.New constructs every outbound
// eagerly, so an emitted config sing-box rejects fails HERE. The proxy package's
// own tests only assert the emitted map's fields; this is the layer that proves
// the core accepts it. (The QUIC family is proven in quic_instantiate_test.go;
// the xray xhttp path in plan_test.go's TestPlanWithXrayMember.)
package engine

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"testing"

	ilproxy "ironlink/daemon/internal/proxy"
)

func mustInstantiate(t *testing.T, ob map[string]any) {
	t.Helper()
	cfg, err := json.Marshal(map[string]any{
		"log":       map[string]any{"disabled": true},
		"outbounds": []any{ob},
	})
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	b, err := BuildBox(cfg, nil, nil)
	if b != nil {
		b.Close()
	}
	if err != nil {
		t.Fatalf("box.New rejected the outbound (not registered / bad config?): %v", err)
	}
}

// TestTCPFamilyOutboundsInstantiate parses a real share link for each non-VLESS
// TCP-family protocol and proves sing-box constructs its outbound.
func TestTCPFamilyOutboundsInstantiate(t *testing.T) {
	// vmess carries its config as base64(JSON) — build a minimal tcp/no-tls one.
	vmessLink := "vmess://" + base64.StdEncoding.EncodeToString([]byte(
		`{"v":"2","ps":"vm","add":"203.0.113.20","port":"443","id":"b831381d-6324-4d53-ad4f-8cda48b30811","aid":"0","net":"tcp","tls":""}`))

	// 2022-blake3-aes-256-gcm requires a 32-byte PSK; sing-box validates the
	// length at construction, so use a real 32-byte key (base64, URL-escaped).
	ss2022Key := url.QueryEscape(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	ss2022Link := "ss://2022-blake3-aes-256-gcm:" + ss2022Key + "@203.0.113.22:8388#ss22"

	cases := []struct {
		name string
		link string
	}{
		{"shadowsocks", "ss://YWVzLTI1Ni1nY206cGFzcw==@203.0.113.21:443#ss"},
		{"shadowsocks-2022", ss2022Link},
		{"vmess", vmessLink},
		{"trojan-tls", "trojan://pass@203.0.113.23:443?security=tls&sni=cdn.example.com#tr"},
		{"trojan-ws", "trojan://pw@203.0.113.24:443?security=tls&sni=cdn.example.com&type=ws&path=%2Fws&host=cdn.example.com#trws"},
		{"anytls", "anytls://pass@203.0.113.25:443?sni=cdn.example.com#at"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := ilproxy.ParseURL(tc.link)
			if err != nil {
				t.Fatalf("ParseURL: %v", err)
			}
			ob, err := p.SingBoxOutbound("probe")
			if err != nil {
				t.Fatalf("SingBoxOutbound: %v", err)
			}
			mustInstantiate(t, ob)
		})
	}
}
