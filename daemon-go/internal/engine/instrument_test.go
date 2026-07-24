// §6 instrumentation verification (no root): the traffic counter counts REAL
// routed bytes, the log sink receives the box's lines, and the latency probe
// measures through an in-process xray instance against a local server.
package engine

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	xproxy "golang.org/x/net/proxy"

	ilproxy "ironlink/daemon/internal/proxy"
)

// TestTrafficCounterCountsRealBytes drives a 204 through a socks session
// (bridge → xray freedom → real internet, like TestBridgeViaSocks) and
// expects BOTH directions to have counted: the TLS exchange alone moves
// kilobytes.
func TestTrafficCounterCountsRealBytes(t *testing.T) {
	port := freePort(t)
	sbCfg, xrayCfg := SocksValidationConfigs("127.0.0.1", port)
	sess, err := Start(sbCfg, xrayCfg)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer sess.Close()

	if up, down := sess.Traffic(); up != 0 || down != 0 {
		t.Fatalf("fresh session traffic = %d/%d, want 0/0", up, down)
	}

	d, err := xproxy.SOCKS5("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), nil, xproxy.Direct)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{
		Transport: &http.Transport{DialContext: d.(xproxy.ContextDialer).DialContext},
		Timeout:   15 * time.Second,
	}
	resp, err := client.Get("https://www.gstatic.com/generate_204")
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	resp.Body.Close()
	client.CloseIdleConnections()

	// The counter ticks as the copy loops run; give the teardown a beat.
	time.Sleep(200 * time.Millisecond)
	up, down := sess.Traffic()
	if up == 0 || down == 0 {
		t.Errorf("routed traffic not counted: up=%d down=%d", up, down)
	}
}

// TestAppForBuckets: a connection with a resolved process path gets its own
// per-app bucket; one with no ProcessInfo (or an empty path) falls into the
// shared unattributed bucket. appFor must return the SAME bucket for repeat
// calls with the same path (so the count funcs accumulate, not fork).
func TestAppForBuckets(t *testing.T) {
	var c TrafficCounter
	firefox := adapter.InboundContext{ProcessInfo: &adapter.ConnectionOwner{ProcessPath: "/usr/bin/firefox"}}
	if c.appFor(firefox) != c.appFor(firefox) {
		t.Error("appFor returned different buckets for the same path")
	}

	c.counterFor(firefox, outcomeProxy).up.Add(100)
	c.counterFor(adapter.InboundContext{}, outcomeProxy).down.Add(7)                             // no ProcessInfo → unattributed
	c.counterFor(adapter.InboundContext{ProcessInfo: &adapter.ConnectionOwner{}}, outcomeProxy).down.Add(3) // empty path → unattributed

	got := map[string]AppBytes{}
	for _, a := range c.AppTotals() {
		got[a.Path] = a
	}
	if got["/usr/bin/firefox"].Up != 100 {
		t.Errorf("firefox up = %d, want 100", got["/usr/bin/firefox"].Up)
	}
	if got[unattributedKey].Down != 10 {
		t.Errorf("unattributed down = %d, want 10 (7+3)", got[unattributedKey].Down)
	}
}

// TestRouteOutcome: the session's outbound tags classify to the right bucket —
// "direct"→direct, "block"→blocked, the reserved DPI tags→dpi, and everything
// else (the selector "proxy" plus any node's own tag) →proxy.
func TestRouteOutcome(t *testing.T) {
	cases := map[string]string{
		"direct":        outcomeDirect,
		"block":         outcomeBlocked,
		"dpi":           outcomeDPI,
		"byedpi":        outcomeDPI,
		SelectorTag:     outcomeProxy, // the selector "proxy"
		"Grimnir [tcp]": outcomeProxy, // a node's own tag
	}
	for tag, want := range cases {
		if got := routeOutcome(tag); got != want {
			t.Errorf("routeOutcome(%q) = %q, want %q", tag, got, want)
		}
	}
}

// TestPerOutcomeBuckets: one app's bytes split per route outcome; the AppBytes
// snapshot carries the per-outcome split AND the Up/Down totals as the sum
// across outcomes.
func TestPerOutcomeBuckets(t *testing.T) {
	var c TrafficCounter
	firefox := adapter.InboundContext{ProcessInfo: &adapter.ConnectionOwner{ProcessPath: "/usr/bin/firefox"}}

	c.counterFor(firefox, outcomeProxy).up.Add(100)
	c.counterFor(firefox, outcomeProxy).down.Add(900)
	c.counterFor(firefox, outcomeDirect).up.Add(10)
	c.counterFor(firefox, outcomeDirect).down.Add(40)

	got := map[string]AppBytes{}
	for _, a := range c.AppTotals() {
		got[a.Path] = a
	}
	ff := got["/usr/bin/firefox"]
	if ff.Up != 110 || ff.Down != 940 {
		t.Errorf("firefox totals = %d/%d, want 110/940 (sum across outcomes)", ff.Up, ff.Down)
	}
	if rb := ff.ByRoute[outcomeProxy]; rb.Up != 100 || rb.Down != 900 {
		t.Errorf("firefox proxy = %d/%d, want 100/900", rb.Up, rb.Down)
	}
	if rb := ff.ByRoute[outcomeDirect]; rb.Up != 10 || rb.Down != 40 {
		t.Errorf("firefox direct = %d/%d, want 10/40", rb.Up, rb.Down)
	}
	if _, ok := ff.ByRoute[outcomeBlocked]; ok {
		t.Error("firefox has a blocked bucket it never accrued to")
	}
}

// TestProcessSearchLinesDropped: the per-connection process-search lines
// find_process emits at INFO must NOT reach the sink (they'd flood the GUI
// feed), but ordinary lines must pass through.
func TestProcessSearchLinesDropped(t *testing.T) {
	var got []string
	w := sinkWriter{sink: func(_, message string) { got = append(got, message) }}
	// Shaped like the platform formatter's output: level + id prefix + message.
	w.WriteMessage(0, "INFO[0000] [123 1s] found process path: /usr/bin/firefox, user: alice")
	w.WriteMessage(0, "INFO[0000] [124 1s] failed to search process: not found")
	w.WriteMessage(0, "INFO[0000] inbound/tun: started at tun777")
	if len(got) != 1 || got[0] != "INFO[0000] inbound/tun: started at tun777" {
		t.Errorf("sink received %v, want only the non-process line", got)
	}
}

// TestProbeThroughXray measures against a LOCAL server through a freedom xray
// — the probe path (core.Dial → routing → HTTP timing) without a real node.
func TestProbeThroughXray(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	xinst, err := BuildXray([]byte(`{
      "log": {"loglevel":"warning"},
      "inbounds": [{"listen":"@il-probe-test","protocol":"socks","settings":{"udp":false}}],
      "outbounds": [{"protocol":"freedom","tag":"direct"}]
    }`))
	if err != nil {
		t.Fatal(err)
	}
	defer xinst.Close()

	d, err := probeThroughXray(context.Background(), xinst, srv.URL)
	if err != nil {
		t.Fatalf("probeThroughXray: %v", err)
	}
	// A loopback round trip can finish inside one tick of Windows's coarse
	// monotonic clock (~1–16ms), so a valid measurement reads as exactly 0.
	// The error-free return above already proves the measure path ran; only a
	// negative or wildly large duration is actually implausible.
	if d < 0 || d > 5*time.Second {
		t.Errorf("latency = %v, implausible for a local server", d)
	}
}

// TestProbeLatencyUnreachableNodeErrors: a probe against a TEST-NET node must
// fail (with the context deadline), not hang or succeed.
func TestProbeLatencyUnreachableNodeErrors(t *testing.T) {
	v := testVless(testReality(),
		ilproxy.Transport{Kind: ilproxy.TransportXhttp, Xhttp: &ilproxy.XhttpParams{}}, "")
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()
	if _, err := ProbeLatency(ctx, v, "unreachable-test", ProbeURL); err == nil {
		t.Error("probe to a TEST-NET node must error")
	}
}
