package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	box "github.com/sagernet/sing-box"
)

// TestSingBoxProbeMechanism verifies the universal-probe plumbing without a real
// node: BuildBox + Start + Outbound(tag) + DialContext through a constructed
// outbound. A plain "direct" outbound (sing-box's freedom equivalent) dials a
// LOCAL server — the unmarked (tunExempt=false) path the probe takes when no TUN
// is up, so this runs in the non-root CI gate. The own-traffic routing_mark
// itself needs CAP_NET_ADMIN and is exercised only under a live TUN (root); the
// per-protocol node outbounds this stands in for are proven constructable in
// protocol_instantiate_test.go / quic_instantiate_test.go, and a real-node
// end-to-end measurement is the env-gated realnode test.
func TestSingBoxProbeMechanism(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	ob := map[string]any{"type": "direct", "tag": "probe"}
	cfg, err := json.Marshal(map[string]any{
		"log":       map[string]any{"disabled": true},
		"outbounds": []any{ob},
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := BuildBox(cfg, nil, nil)
	if err != nil {
		t.Fatalf("BuildBox: %v", err)
	}
	defer b.Close()
	if err := b.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	outbound, ok := b.Outbound().Outbound("probe")
	if !ok {
		t.Fatal("probe outbound not constructed")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	d, err := probeThroughSingBox(ctx, outbound, srv.URL)
	if err != nil {
		t.Fatalf("probeThroughSingBox (routing_mark faulted the dial?): %v", err)
	}
	// Returning without error IS the success signal: the probe dialled through
	// the constructed outbound and read the response. The duration is NOT
	// asserted > 0 — a localhost round trip can finish inside the OS timer
	// granularity (Windows' monotonic clock is coarse enough that time.Since
	// rounds to exactly 0), which is valid; latencies to a real node are always
	// measurably positive. Only a negative duration (a broken clock) is wrong.
	if d < 0 {
		t.Errorf("latency = %v, want >= 0", d)
	}
}

// TestProbeBoxAutoDetectInterface pins the off-Linux ephemeral-probe TUN
// exemption wiring: a probe box built WITH route.auto_detect_interface (the
// tunExempt off-Linux branch of ProbeLatencySingBox) must yield a NetworkManager
// that reports auto-detect on and exposes the interface-bind func that pins the
// outbound's underlay dial to the physical interface; built WITHOUT the route
// block it must report off. The bind syscall itself is sing-box's and is
// exercised only under a live TUN — this proves the config→behaviour link with
// no TUN/remote (so it runs in the non-root gate).
func TestProbeBoxAutoDetectInterface(t *testing.T) {
	probeBox := func(withRoute bool) *box.Box {
		cfgMap := map[string]any{
			"log":       map[string]any{"disabled": true},
			"outbounds": []any{map[string]any{"type": "direct", "tag": "probe"}},
		}
		if withRoute {
			cfgMap["route"] = map[string]any{"auto_detect_interface": true}
		}
		cfg, err := json.Marshal(cfgMap)
		if err != nil {
			t.Fatal(err)
		}
		b, err := BuildBox(cfg, nil, nil)
		if err != nil {
			t.Fatalf("BuildBox(auto_detect=%v): %v", withRoute, err)
		}
		if err := b.Start(); err != nil {
			b.Close()
			t.Fatalf("Start(auto_detect=%v): %v", withRoute, err)
		}
		t.Cleanup(func() { b.Close() })
		return b
	}

	on := probeBox(true)
	if !on.Network().AutoDetectInterface() {
		t.Fatal("route.auto_detect_interface set but NetworkManager.AutoDetectInterface() is false")
	}
	if on.Network().AutoDetectInterfaceFunc() == nil {
		t.Fatal("auto_detect_interface on but no interface-bind func built — probe dials would not escape the TUN")
	}

	off := probeBox(false)
	if off.Network().AutoDetectInterface() {
		t.Fatal("no route block but NetworkManager.AutoDetectInterface() reports enabled")
	}
}
