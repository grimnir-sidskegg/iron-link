package engine

import (
	"testing"

	ilproxy "ironlink/daemon/internal/proxy"
)

// TestHasCamouflageGating pins the camouflage gate: the direct probe is TCP+TLS,
// so it applies to TCP-fronted TLS nodes and is withheld from QUIC nodes (which
// have a TLS front but no TCP socket) and from plain no-SNI nodes — a QUIC node
// must NOT be TCP-probed into a spurious "server unreachable".
func TestHasCamouflageGating(t *testing.T) {
	cases := []struct {
		name string
		link string
		want bool
	}{
		{"trojan-tls (tcp+tls)", "trojan://pw@h:443?security=tls&sni=cdn.example.com", true},
		{"vless-reality (tcp+tls)", "vless://u@h:443?security=reality&sni=g.com&fp=chrome&pbk=PBK&sid=01ab&type=tcp", true},
		{"anytls (tcp+tls)", "anytls://pw@h:443?sni=cdn.example.com", true},
		{"hysteria2 (udp/quic)", "hysteria2://pw@h:443?sni=cdn.example.com", false},
		{"tuic (udp/quic)", "tuic://u:pw@h:443?sni=cdn.example.com", false},
		{"hysteria (udp/quic)", "hysteria://h:443?auth=pw&sni=cdn.example.com", false},
		{"shadowsocks (no front)", "ss://YWVzLTI1Ni1nY206cGFzcw==@h:443", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := ilproxy.ParseURL(tc.link)
			if err != nil {
				t.Fatalf("ParseURL(%q): %v", tc.link, err)
			}
			if got := HasCamouflage(p); got != tc.want {
				t.Errorf("HasCamouflage = %v, want %v (ServerNetwork=%q)", got, tc.want, p.ServerNetwork())
			}
		})
	}
}
