// The G2 real-node gate, no-root half: the COMPILED config for a REAL node
// carries live traffic through the in-memory bridge. Needs a node, so it is
// env-gated:
//
//	IRON_LINK_TEST_NODE_URL='vless://…' go test -run RealNode ./internal/engine/
//
// The TUN half of the gate (root, hijacks host traffic) is
// TestRuntimeRealNode204 in session_test.go.
package engine

import (
	"context"
	"net"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"

	xproxy "golang.org/x/net/proxy"

	"ironlink/daemon/internal/api"
	ilproxy "ironlink/daemon/internal/proxy"
)

// realNodeFromEnv parses IRON_LINK_TEST_NODE_URL or skips the test.
func realNodeFromEnv(t *testing.T) *ilproxy.VlessConfig {
	t.Helper()
	raw := os.Getenv("IRON_LINK_TEST_NODE_URL")
	if raw == "" {
		t.Skip("set IRON_LINK_TEST_NODE_URL to a vless:// share link to run the real-node gate")
	}
	p, err := ilproxy.ParseURL(raw)
	if err != nil {
		t.Fatalf("parse IRON_LINK_TEST_NODE_URL: %v", err)
	}
	v, ok := p.(*ilproxy.VlessConfig)
	if !ok {
		t.Fatalf("IRON_LINK_TEST_NODE_URL parsed to %T, want a VLESS node", p)
	}
	return v
}

// TestRealNodeViaSocks drives the whole compiled data plane minus the TUN —
// share link → ParseURL → NodeSocksConfigs → sing-box SOCKS inbound → route →
// core.Dial → xray vless/Reality → THE REAL NODE → internet — and expects a
// 204. No root; this is the half of the G2 gate runnable anywhere.
func TestRealNodeViaSocks(t *testing.T) {
	v := realNodeFromEnv(t)

	port := freePort(t)
	sbCfg, xrayCfg, err := NodeSocksConfigs(v, "127.0.0.1", port)
	if err != nil {
		t.Fatalf("NodeSocksConfigs: %v", err)
	}
	sess, err := Start(sbCfg, xrayCfg)
	if err != nil {
		t.Fatalf("Start (socks + real node): %v", err)
	}
	defer sess.Close()

	d, err := xproxy.SOCKS5("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), nil, xproxy.Direct)
	if err != nil {
		t.Fatalf("socks5 dialer: %v", err)
	}
	client := &http.Client{
		Transport: &http.Transport{DialContext: d.(xproxy.ContextDialer).DialContext},
		Timeout:   20 * time.Second,
	}
	resp, err := client.Get("https://www.gstatic.com/generate_204")
	if err != nil {
		t.Fatalf("204 probe through the real node failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("want HTTP 204 through the real node, got %d", resp.StatusCode)
	}
}

// TestRealNodeUniversalLatency proves the UNIVERSAL latency probe against a real
// node of ANY protocol — set IRON_LINK_TEST_NODE_URL to an anytls/hysteria2/tuic/
// hysteria/ss/vmess/trojan/vless share link. It routes the node to the same core
// the live session would (sing-box native, or xray for xhttp) and times a real
// round trip. This is the end-to-end proof of ProbeLatencySingBox that the unit
// gate cannot give (no real node). No live TUN here, so the probe is unmarked.
//
//	IRON_LINK_TEST_NODE_URL='hysteria2://…' go test -tags '…' -run RealNodeUniversalLatency ./internal/engine/
func TestRealNodeUniversalLatency(t *testing.T) {
	raw := os.Getenv("IRON_LINK_TEST_NODE_URL")
	if raw == "" {
		t.Skip("set IRON_LINK_TEST_NODE_URL to any share link to run the universal-probe gate")
	}
	p, err := ilproxy.ParseURL(raw)
	if err != nil {
		t.Fatalf("parse IRON_LINK_TEST_NODE_URL: %v", err)
	}
	core, err := ilproxy.SelectCore(p, api.CoreSingBox, nil)
	if err != nil {
		t.Fatalf("select core for %s: %v", p.Kind(), err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	var d time.Duration
	if core == api.CoreXray {
		d, err = ProbeLatency(ctx, p, "realnode-uni", ProbeURL)
	} else {
		d, err = ProbeLatencySingBox(ctx, p, ProbeURL, false) // no live TUN in this gate → no fwmark
	}
	if err != nil {
		t.Fatalf("universal probe (%s, %s, via %s): %v", p.Kind(), p.ServerNetwork(), core, err)
	}
	t.Logf("%s node latency via %s: %v", p.Kind(), core, d)
	if d <= 0 {
		t.Errorf("latency = %v, want > 0", d)
	}
}
