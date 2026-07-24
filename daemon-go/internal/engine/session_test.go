//go:build with_gvisor

// Runtime verification: opens a REAL TUN (box.Start) and pushes a probe through
// TUN -> sing-box route -> xray-reality (core.Dial) -> xray freedom -> internet,
// closing the 204 end-to-end flow the G0 spike left open (it had no working DNS).
//
// auto_route is ON, so this HIJACKS the host's traffic into the TUN for the
// duration. It therefore self-skips unless run as root with the opt-in env set:
//
//	sudo IRON_LINK_RUNTIME_TEST=1 go test -tags "with_gvisor,with_utls,with_clash_api" -run Runtime ./internal/engine/
package engine

import (
	"net/http"
	"os"
	"testing"
	"time"
)

func TestRuntime204(t *testing.T) {
	if os.Geteuid() != 0 || os.Getenv("IRON_LINK_RUNTIME_TEST") != "1" {
		t.Skip("needs root + IRON_LINK_RUNTIME_TEST=1 (opens a TUN, hijacks host traffic)")
	}

	sbCfg, xrayCfg := FreedomValidationConfigs("iltun0")
	sess, err := Start(sbCfg, xrayCfg)
	if err != nil {
		t.Fatalf("Start session (open TUN): %v", err)
	}
	defer sess.Close()

	time.Sleep(800 * time.Millisecond) // let auto_route / DNS settle

	resp, err := (&http.Client{Timeout: 12 * time.Second}).Get("https://www.gstatic.com/generate_204")
	if err != nil {
		t.Fatalf("204 probe through TUN failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("want HTTP 204 through TUN -> sing-box -> core.Dial -> xray, got %d", resp.StatusCode)
	}
}

// TestRuntimeDirectTun isolates the BASE TUN path with NO xray: TUN -> sing-box
// direct -> internet, proven-config structure. 204 here means the base TUN/DNS is
// fine and the loop is specifically xray-under-TUN; a failure here means the base
// config itself is wrong. Same root + opt-in gate as TestRuntime204.
func TestRuntimeDirectTun(t *testing.T) {
	if os.Geteuid() != 0 || os.Getenv("IRON_LINK_RUNTIME_TEST") != "1" {
		t.Skip("needs root + IRON_LINK_RUNTIME_TEST=1 (opens a TUN, hijacks host traffic)")
	}

	sbCfg, xrayCfg := DirectTunValidationConfig("iltun1")
	sess, err := Start(sbCfg, xrayCfg)
	if err != nil {
		t.Fatalf("Start direct-TUN session: %v", err)
	}
	defer sess.Close()

	time.Sleep(800 * time.Millisecond)

	resp, err := (&http.Client{Timeout: 12 * time.Second}).Get("https://www.gstatic.com/generate_204")
	if err != nil {
		t.Fatalf("204 probe through direct TUN failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("want HTTP 204 through TUN -> sing-box direct, got %d", resp.StatusCode)
	}
}

// TestRuntimeRealNode204 is the FULL G2 gate: the compiled config for a REAL
// node (vless/Reality/xhttp — the case that forces xray) carries the host's
// hijacked traffic end-to-end: TUN → sing-box route → xray-reality →
// core.Dial → xray → THE REAL NODE → internet → 204. Needs root + the opt-in
// env (auto_route hijacks host traffic) + a node:
//
//	sudo IRON_LINK_RUNTIME_TEST=1 IRON_LINK_TEST_NODE_URL='vless://…' \
//	  go test -tags "with_gvisor,with_utls,with_clash_api" -run RuntimeRealNode ./internal/engine/
func TestRuntimeRealNode204(t *testing.T) {
	if os.Geteuid() != 0 || os.Getenv("IRON_LINK_RUNTIME_TEST") != "1" {
		t.Skip("needs root + IRON_LINK_RUNTIME_TEST=1 (opens a TUN, hijacks host traffic)")
	}
	v := realNodeFromEnv(t)

	sbCfg, xrayCfg, err := NodeTUNConfigs(v, "iltun2")
	if err != nil {
		t.Fatalf("NodeTUNConfigs: %v", err)
	}
	sess, err := Start(sbCfg, xrayCfg)
	if err != nil {
		t.Fatalf("Start session (open TUN, real node): %v", err)
	}
	defer sess.Close()

	time.Sleep(800 * time.Millisecond) // let auto_route / DNS settle

	resp, err := (&http.Client{Timeout: 20 * time.Second}).Get("https://www.gstatic.com/generate_204")
	if err != nil {
		t.Fatalf("204 probe through TUN + real node failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("want HTTP 204 through TUN -> bridge -> real node, got %d", resp.StatusCode)
	}
}
