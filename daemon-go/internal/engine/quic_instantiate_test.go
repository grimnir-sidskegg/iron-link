//go:build with_quic

// Proof under the SHIPPING tags that the QUIC family (hysteria2/tuic/hysteria)
// is actually registered and instantiable in the built daemon — not merely
// serialized to a matching schema. Each profile's native sing-box outbound is
// fed through the real BuildBox (box.New constructs every outbound eagerly via
// include.OutboundRegistry()); a missing with_quic registration would fail here
// with "unknown outbound type". This is the test that was impossible before the
// sing-box v1.13 bump unified the qpack graph and let with_quic compile.
package engine

import (
	"encoding/json"
	"testing"

	ilproxy "ironlink/daemon/internal/proxy"
)

func buildBoxWithOutbound(t *testing.T, ob map[string]any) error {
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
	return err
}

func TestQUICOutboundsInstantiate(t *testing.T) {
	profiles := map[string]ilproxy.Profile{
		"hysteria2": &ilproxy.Hysteria2Config{
			ServerName: "h2", Address: "203.0.113.10", Port: 443, Password: "pw", SNI: "cdn.example.com",
		},
		"tuic": &ilproxy.TUICConfig{
			ServerName: "tuic", Address: "203.0.113.11", Port: 443,
			UUID: "b831381d-6324-4d53-ad4f-8cda48b30811", Password: "pw", SNI: "cdn.example.com",
		},
		"hysteria": &ilproxy.HysteriaConfig{
			ServerName: "hy1", Address: "203.0.113.12", Port: 443, Auth: "pw", SNI: "cdn.example.com",
			UpMbps: 50, DownMbps: 100,
		},
	}
	for name, p := range profiles {
		t.Run(name, func(t *testing.T) {
			ob, err := p.SingBoxOutbound("probe")
			if err != nil {
				t.Fatalf("SingBoxOutbound: %v", err)
			}
			if err := buildBoxWithOutbound(t, ob); err != nil {
				t.Fatalf("box.New could not instantiate the %s outbound (not registered in this build?): %v", name, err)
			}
		})
	}
}
