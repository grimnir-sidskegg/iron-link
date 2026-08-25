// De-risking spike (PROOF only, no production code touched): definitively
// characterize how xray's forced-outbound-tag primitive behaves on the UDP path,
// so the future Backend seam's ListenPacket(ctx, nodeID, dest) contract can be
// grounded in observed behaviour rather than assumption.
//
// The TCP spike (forcedtag_spike_test.go) proved that a fresh ctx per Dial forces
// egress deterministically, and that the dispatcher CONSUMES the forced tag by
// clearing it on the context's shared *session.Content. UDP is different in one
// decisive way: xray's udp.Dispatcher (transport/internet/udp/dispatcher.go)
// caches ONE outbound link PER PacketConn — not per destination — in
// getInboundRay. The first WriteTo establishes that link (consuming the forced
// tag exactly once); every later WriteTo, to ANY destination, reuses the cached
// link WITHOUT re-dispatching. So within a PacketConn's lifetime the destination
// is irrelevant to outbound selection.
//
// These tests nail down five questions:
//  1. MULTI-DEST on one PacketConn — do all destinations egress via the forced
//     outbound, or does only the first, with the rest leaking to the default?
//  2. The re-dispatch leak — because the forced tag is one-shot, what happens if
//     the same ctx is ever dispatched again (idle-timeout reconnect / ctx reuse)?
//     And does inbound-tag routing (a STABLE ctx attribute) avoid it?
//  3. CONCURRENCY — two PacketConns on distinct forced tags, interleaved, no
//     cross-talk (run under -race).
//  4. NEGATIVE — a non-existent forced tag must hard-fail, not silently default.
//  5. LIFECYCLE — what Close() actually tears down.
//
// Egress is observed exactly as in the TCP spike: freedom "redirect"
// (DestinationOverride) pins each outbound to a DIFFERENT loopback UDP marker
// server, and freedom's PacketWriter rewrites EVERY packet's destination to that
// override — so the marker read back names the outbound actually used, regardless
// of the wire destination. Loopback (127.0.0.1) is local scaffolding; placeholder
// wire destinations are RFC-5737 TEST-NET addresses (documentation range), never
// real infrastructure. Reuses markerA/markerB, udpMarkerServer and
// forcedTagInstance from forcedtag_spike_test.go.
package engine

import (
	"context"
	"fmt"
	"net"
	"runtime"
	"sync"
	"testing"
	"time"

	xcore "github.com/xtls/xray-core/core"

	"github.com/xtls/xray-core/common/session"
)

// udpDest builds a fixed placeholder wire destination. freedom's redirect
// overrides it entirely, so it never leaves the box; its only job is to be a
// DISTINCT valid destination so destination-independence is observable. All are
// RFC-5737 TEST-NET-2 documentation addresses, never real infra.
func udpDest(lastOctet, port int) *net.UDPAddr {
	return &net.UDPAddr{IP: net.ParseIP(fmt.Sprintf("198.51.100.%d", lastOctet)), Port: port}
}

// threeDests returns three distinct placeholder wire destinations.
func threeDests() []*net.UDPAddr {
	return []*net.UDPAddr{udpDest(11, 5311), udpDest(12, 5312), udpDest(13, 5313)}
}

// readUDPBounded reads one datagram from pc, but never blocks the test forever:
// xray's dispatcherConn.SetReadDeadline is a documented no-op, and on a hard-fail
// no datagram ever arrives, so a naive ReadFrom could hang the suite. The reader
// goroutine still unblocks (and exits) when the PacketConn is closed, because
// Close() closes the read-side "done" and ReadFrom then returns io.EOF.
func readUDPBounded(t *testing.T, pc net.PacketConn, timeout time.Duration) (string, error) {
	t.Helper()
	type res struct {
		s   string
		err error
	}
	ch := make(chan res, 1)
	go func() {
		buf := make([]byte, 128)
		n, _, err := pc.ReadFrom(buf)
		ch <- res{string(buf[:n]), err}
	}()
	select {
	case r := <-ch:
		return r.s, r.err
	case <-time.After(timeout):
		return "", fmt.Errorf("read timed out after %s (no datagram)", timeout)
	}
}

// routingInstance builds ONE xray instance with the same two redirect-pinned
// freedom outbounds as forcedTagInstance, PLUS a router whose field rules map an
// INBOUND tag to an outbound: node-a -> egress-a, node-b -> egress-b. This is the
// alternative to the forced-tag primitive: the inbound tag is a stable ctx
// attribute the router re-reads on every dispatch, never consumed/cleared.
func routingInstance(t *testing.T, addrA, addrB string) *xcore.Instance {
	t.Helper()
	cfg := fmt.Sprintf(`{
      "log": {"loglevel":"warning"},
      "outbounds": [
        {"protocol":"freedom","tag":"egress-a","settings":{"redirect":%q}},
        {"protocol":"freedom","tag":"egress-b","settings":{"redirect":%q}}
      ],
      "routing": {
        "domainStrategy": "AsIs",
        "rules": [
          {"type":"field","inboundTag":["node-a"],"outboundTag":"egress-a"},
          {"type":"field","inboundTag":["node-b"],"outboundTag":"egress-b"}
        ]
      }
    }`, addrA, addrB)
	inst, err := BuildXray([]byte(cfg))
	if err != nil {
		t.Fatalf("BuildXray (inbound-tag routing config): %v", err)
	}
	t.Cleanup(func() { _ = inst.Close() })
	return inst
}

// forcedUDPRoundTrip opens a PacketConn on inst, forcing egress via tag, sends one
// datagram to dst and returns the marker read back (which names the egress used).
// cancel is returned so the caller can drive the LIFECYCLE observations; callers
// that don't care may defer it immediately.
func forcedUDPRoundTrip(t *testing.T, inst *xcore.Instance, tag string, dst *net.UDPAddr) (string, error, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	if tag != "" {
		ctx = session.SetForcedOutboundTagToContext(ctx, tag)
	}
	pc, err := xcore.DialUDP(ctx, inst)
	if err != nil {
		cancel()
		return "", err, func() {}
	}
	t.Cleanup(func() { _ = pc.Close() })
	if _, err := pc.WriteTo([]byte("ping"), dst); err != nil {
		return "", err, cancel
	}
	got, err := readUDPBounded(t, pc, 3*time.Second)
	return got, err, cancel
}

// -----------------------------------------------------------------------------
// 1. CRITICAL — multi-dest on ONE PacketConn.
// -----------------------------------------------------------------------------

// TestForcedUDPMultiDestOnePacketConn is the decisive test. It opens ONE
// PacketConn with forced tag egress-b, then writes to THREE distinct wire
// destinations on that same PacketConn and asserts ALL THREE egress via egress-b.
//
// The spike flagged a worry that only the FIRST destination would be pinned and a
// new dest would re-dispatch with the already-cleared tag and leak to the default.
// The source (udp.Dispatcher.getInboundRay caches one link per PacketConn, NOT per
// destination) predicts the opposite: no re-dispatch ever happens for a later
// destination, so all destinations ride the one egress-b link. This proves which
// is true, with evidence.
func TestForcedUDPMultiDestOnePacketConn(t *testing.T) {
	addrA := udpMarkerServer(t, markerA)
	addrB := udpMarkerServer(t, markerB)
	inst := forcedTagInstance(t, addrA, addrB)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = session.SetForcedOutboundTagToContext(ctx, "egress-b")
	pc, err := xcore.DialUDP(ctx, inst)
	if err != nil {
		t.Fatalf("force egress-b DialUDP: %v", err)
	}
	defer pc.Close()

	dests := threeDests()
	for i, dst := range dests {
		if _, err := pc.WriteTo([]byte("ping"), dst); err != nil {
			t.Fatalf("dest #%d %v: WriteTo: %v", i+1, dst, err)
		}
		got, err := readUDPBounded(t, pc, 3*time.Second)
		if err != nil {
			t.Fatalf("dest #%d %v: read: %v", i+1, dst, err)
		}
		if got != markerB {
			t.Fatalf("dest #%d %v egressed via %q, want %q — a later dest LEAKED off the forced outbound",
				i+1, dst, got, markerB)
		}
	}
	t.Logf("MULTI-DEST VERDICT: one PacketConn, forced egress-b, 3 distinct dests %v -> ALL egress-b (%q). "+
		"No leak: udp.Dispatcher caches one link per PacketConn, so later dests never re-dispatch.",
		dests, markerB)
}

// -----------------------------------------------------------------------------
// 2. The re-dispatch leak, and the inbound-tag routing fix.
// -----------------------------------------------------------------------------

// TestForcedUDPTagConsumedOnceLeaksOnReDispatch pins down the REAL UDP fragility.
// The forced tag is a one-shot attribute: routedDispatch clears it on the shared
// *session.Content the first time it is read. Multi-dest on a single PacketConn
// is safe only because that PacketConn never re-dispatches. But if the SAME ctx
// (same Content) is ever dispatched again — a second PacketConn built from it, or
// the underlying udp link reconnecting after its 60s idle timer — the tag is gone
// and egress silently falls to the DEFAULT outbound.
//
// This reproduces it deterministically: two PacketConns from ONE forced-egress-b
// ctx. The first consumes the tag and egresses egress-b; the second, sharing the
// now-cleared Content, leaks to the first-declared default egress-a.
func TestForcedUDPTagConsumedOnceLeaksOnReDispatch(t *testing.T) {
	addrA := udpMarkerServer(t, markerA)
	addrB := udpMarkerServer(t, markerB)
	inst := forcedTagInstance(t, addrA, addrB)

	// ONE ctx / ONE *session.Content shared by both PacketConns.
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	base = session.SetForcedOutboundTagToContext(base, "egress-b")

	pc1, err := xcore.DialUDP(base, inst)
	if err != nil {
		t.Fatalf("pc1 DialUDP: %v", err)
	}
	defer pc1.Close()
	pc2, err := xcore.DialUDP(base, inst)
	if err != nil {
		t.Fatalf("pc2 DialUDP: %v", err)
	}
	defer pc2.Close()

	// pc1 first: its dispatch reads AND clears the forced tag on the shared
	// Content. Receiving egress-b proves the dispatch (and the clear) happened.
	if _, err := pc1.WriteTo([]byte("ping"), udpDest(11, 5311)); err != nil {
		t.Fatalf("pc1 WriteTo: %v", err)
	}
	got1, err := readUDPBounded(t, pc1, 3*time.Second)
	if err != nil {
		t.Fatalf("pc1 read: %v", err)
	}
	if got1 != markerB {
		t.Fatalf("pc1 (first dispatch of forced egress-b) egressed %q, want %q", got1, markerB)
	}

	// pc2 now dispatches on the SAME, now-cleared Content -> silent default.
	if _, err := pc2.WriteTo([]byte("ping"), udpDest(12, 5312)); err != nil {
		t.Fatalf("pc2 WriteTo: %v", err)
	}
	got2, err := readUDPBounded(t, pc2, 3*time.Second)
	if err != nil {
		t.Fatalf("pc2 read: %v", err)
	}
	if got2 != markerA {
		t.Fatalf("pc2 egressed %q; expected the leak to the default egress-a (%q) that proves the forced tag is one-shot", got2, markerA)
	}
	t.Logf("RE-DISPATCH LEAK CONFIRMED: sharing one forced-tag ctx across dispatches, "+
		"pc1=%q (egress-b, consumed the tag) then pc2=%q (LEAKED to default egress-a). "+
		"A forced-tag PacketConn is safe ONLY for the life of its single link; any re-dispatch on the same ctx defaults.",
		got1, got2)
}

// TestInboundTagRoutingUDPPinsAllDestsAndSurvivesReDispatch is the mitigation
// side of the comparison. Instead of the one-shot forced tag, egress is chosen by
// a router rule keyed on the INBOUND tag, which is a stable ctx attribute the
// router re-reads on every dispatch and never clears. It proves inbound-tag
// routing (a) pins multi-dest on one PacketConn just like the forced tag, and
// (b) does NOT leak on re-dispatch: a second PacketConn built from the SAME ctx
// still egresses correctly — exactly the case the forced tag failed above.
func TestInboundTagRoutingUDPPinsAllDestsAndSurvivesReDispatch(t *testing.T) {
	addrA := udpMarkerServer(t, markerA)
	addrB := udpMarkerServer(t, markerB)
	inst := routingInstance(t, addrA, addrB)

	// Sanity: the rule discriminates. node-a -> egress-a, node-b -> egress-b.
	for _, tc := range []struct{ tag, want string }{{"node-a", markerA}, {"node-b", markerB}} {
		ctx, cancel := context.WithCancel(context.Background())
		ctx = session.ContextWithInbound(ctx, &session.Inbound{Tag: tc.tag})
		pc, err := xcore.DialUDP(ctx, inst)
		if err != nil {
			cancel()
			t.Fatalf("inbound %s DialUDP: %v", tc.tag, err)
		}
		if _, err := pc.WriteTo([]byte("ping"), udpDest(11, 5311)); err != nil {
			cancel()
			t.Fatalf("inbound %s WriteTo: %v", tc.tag, err)
		}
		got, err := readUDPBounded(t, pc, 3*time.Second)
		_ = pc.Close()
		cancel()
		if err != nil {
			t.Fatalf("inbound %s read: %v", tc.tag, err)
		}
		if got != tc.want {
			t.Fatalf("inbound tag %s routed to %q, want %q", tc.tag, got, tc.want)
		}
	}

	// (a) multi-dest on one PacketConn, chosen by inbound tag node-b.
	ctxMulti, cancelMulti := context.WithCancel(context.Background())
	defer cancelMulti()
	ctxMulti = session.ContextWithInbound(ctxMulti, &session.Inbound{Tag: "node-b"})
	pcMulti, err := xcore.DialUDP(ctxMulti, inst)
	if err != nil {
		t.Fatalf("multi DialUDP: %v", err)
	}
	defer pcMulti.Close()
	for i, dst := range threeDests() {
		if _, err := pcMulti.WriteTo([]byte("ping"), dst); err != nil {
			t.Fatalf("multi dest #%d: WriteTo: %v", i+1, err)
		}
		got, err := readUDPBounded(t, pcMulti, 3*time.Second)
		if err != nil {
			t.Fatalf("multi dest #%d: read: %v", i+1, err)
		}
		if got != markerB {
			t.Fatalf("multi dest #%d routed to %q, want %q", i+1, got, markerB)
		}
	}

	// (b) re-dispatch on a SHARED inbound-tag ctx does NOT leak: both PacketConns
	// egress-b, unlike the forced-tag case which defaulted the second.
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	base = session.ContextWithInbound(base, &session.Inbound{Tag: "node-b"})
	pc1, err := xcore.DialUDP(base, inst)
	if err != nil {
		t.Fatalf("shared pc1 DialUDP: %v", err)
	}
	defer pc1.Close()
	pc2, err := xcore.DialUDP(base, inst)
	if err != nil {
		t.Fatalf("shared pc2 DialUDP: %v", err)
	}
	defer pc2.Close()
	for name, pc := range map[string]net.PacketConn{"pc1": pc1, "pc2": pc2} {
		if _, err := pc.WriteTo([]byte("ping"), udpDest(11, 5311)); err != nil {
			t.Fatalf("shared %s WriteTo: %v", name, err)
		}
		got, err := readUDPBounded(t, pc, 3*time.Second)
		if err != nil {
			t.Fatalf("shared %s read: %v", name, err)
		}
		if got != markerB {
			t.Fatalf("shared inbound-tag ctx: %s egressed %q, want %q (routing must NOT be consumed)", name, got, markerB)
		}
	}
	t.Logf("INBOUND-TAG ROUTING: pins multi-dest AND survives re-dispatch on a shared ctx (both PacketConns egress-b). "+
		"The routing attribute is stable where the forced tag is one-shot -> routing is the robust choice for UDP backends.")
}

// -----------------------------------------------------------------------------
// 3. CONCURRENCY — no cross-talk between two forced PacketConns (run under -race).
// -----------------------------------------------------------------------------

// TestForcedUDPConcurrentNoCrossTalk opens two PacketConns on the SAME instance,
// one forced egress-a, one forced egress-b (each with its OWN ctx / Content, as a
// real bridge would give each node), and hammers them concurrently with
// interleaved WriteTo/ReadFrom across a mix of destinations. Each must stay
// exclusively on its own outbound. Run under -race to catch shared-state hazards.
func TestForcedUDPConcurrentNoCrossTalk(t *testing.T) {
	addrA := udpMarkerServer(t, markerA)
	addrB := udpMarkerServer(t, markerB)
	inst := forcedTagInstance(t, addrA, addrB)

	openForced := func(tag string) net.PacketConn {
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		ctx = session.SetForcedOutboundTagToContext(ctx, tag)
		pc, err := xcore.DialUDP(ctx, inst)
		if err != nil {
			t.Fatalf("force %s DialUDP: %v", tag, err)
		}
		t.Cleanup(func() { _ = pc.Close() })
		return pc
	}

	pcA := openForced("egress-a")
	pcB := openForced("egress-b")

	const rounds = 40
	dests := threeDests()
	errCh := make(chan error, 2)
	var wg sync.WaitGroup
	worker := func(pc net.PacketConn, want string) {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			dst := dests[i%len(dests)] // rotate destinations to force the multi-dest path too
			if _, err := pc.WriteTo([]byte("ping"), dst); err != nil {
				errCh <- fmt.Errorf("want %s round %d: WriteTo: %w", want, i, err)
				return
			}
			got, err := readUDPBounded(t, pc, 3*time.Second)
			if err != nil {
				errCh <- fmt.Errorf("want %s round %d: read: %w", want, i, err)
				return
			}
			if got != want {
				errCh <- fmt.Errorf("CROSS-TALK: want %s round %d read %q", want, i, got)
				return
			}
		}
		errCh <- nil
	}

	wg.Add(2)
	go worker(pcA, markerA)
	go worker(pcB, markerB)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("CONCURRENCY: two forced PacketConns (egress-a / egress-b), %d interleaved rotating-dest rounds each, no cross-talk", rounds)
}

// -----------------------------------------------------------------------------
// 4. NEGATIVE — a non-existent forced tag must hard-fail, never silently default.
// -----------------------------------------------------------------------------

// TestForcedUDPNonExistentTagHardFails forces a tag that is not a compiled
// outbound. routedDispatch must close the writer and interrupt the reader rather
// than fall through to a default outbound. Evidence: no marker is ever read and
// the read ends in error/EOF (the read side's done is closed when the failed link
// tears down). A markerA/markerB here would be the silent-default bug.
func TestForcedUDPNonExistentTagHardFails(t *testing.T) {
	addrA := udpMarkerServer(t, markerA)
	addrB := udpMarkerServer(t, markerB)
	inst := forcedTagInstance(t, addrA, addrB)

	got, err, cancel := forcedUDPRoundTrip(t, inst, "egress-does-not-exist", udpDest(11, 5311))
	defer cancel()
	if got == markerA || got == markerB {
		t.Fatalf("non-existent forced tag SILENTLY egressed to %q — must hard-fail, not default", got)
	}
	if got != "" {
		t.Fatalf("non-existent forced tag returned data %q — expected an interrupted/closed link", got)
	}
	t.Logf("NEGATIVE: non-existent UDP forced tag hard-failed (no default egress): read=%q err=%v", got, err)
}

// -----------------------------------------------------------------------------
// 5. LIFECYCLE — what Close() tears down, and what actually reaps the link.
// -----------------------------------------------------------------------------

// waitGoroutines polls until runtime.NumGoroutine() <= target or timeout, GC'ing
// first each round to settle finalizers; returns the final count and whether the
// target was reached.
func waitGoroutines(target int, timeout time.Duration) (int, bool) {
	deadline := time.Now().Add(timeout)
	for {
		runtime.GC()
		n := runtime.NumGoroutine()
		if n <= target {
			return n, true
		}
		if time.Now().After(deadline) {
			return n, false
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestForcedUDPCloseLifecycle characterizes teardown. The finding that matters for
// the Backend contract: xray's dispatcherConn.Close() only closes the READ side
// (stops delivering to the read cache); it does NOT tear down the underlying
// outbound link or its handleInput goroutine. That link is reaped by CANCELLING
// the dispatch ctx (or by the udp.Dispatcher's 60s idle timer). So a Backend that
// closes PacketConns without cancelling their dispatch ctx leaks a goroutine and a
// socket per closed UDP session for up to a minute. This test asserts the
// ctx-cancel path reaps promptly, and documents that Close() alone does not.
func TestForcedUDPCloseLifecycle(t *testing.T) {
	addrA := udpMarkerServer(t, markerA)
	addrB := udpMarkerServer(t, markerB)
	inst := forcedTagInstance(t, addrA, addrB)

	// Warm up lazy instance goroutines with a throwaway round trip, fully torn
	// down, so the baseline reflects steady state.
	{
		wctx, wcancel := context.WithCancel(context.Background())
		wctx = session.SetForcedOutboundTagToContext(wctx, "egress-a")
		wpc, err := xcore.DialUDP(wctx, inst)
		if err != nil {
			t.Fatalf("warmup DialUDP: %v", err)
		}
		_, _ = wpc.WriteTo([]byte("ping"), udpDest(11, 5311))
		_, _ = readUDPBounded(t, wpc, 3*time.Second)
		_ = wpc.Close()
		wcancel()
	}
	baseline, _ := waitGoroutines(runtime.NumGoroutine(), 3*time.Second)

	// Open a fresh PacketConn and drive one round trip so the outbound link and
	// its handleInput goroutine are live.
	ctx, cancel := context.WithCancel(context.Background())
	ctx = session.SetForcedOutboundTagToContext(ctx, "egress-b")
	pc, err := xcore.DialUDP(ctx, inst)
	if err != nil {
		cancel()
		t.Fatalf("DialUDP: %v", err)
	}
	if _, err := pc.WriteTo([]byte("ping"), udpDest(12, 5312)); err != nil {
		cancel()
		t.Fatalf("WriteTo: %v", err)
	}
	if got, err := readUDPBounded(t, pc, 3*time.Second); err != nil || got != markerB {
		cancel()
		t.Fatalf("round trip before close: got %q err %v", got, err)
	}
	active := runtime.NumGoroutine()
	if active <= baseline {
		t.Logf("note: active goroutine count %d not above baseline %d (scheduler timing); proceeding", active, baseline)
	}

	// Close() alone: the read side stops, but the link/handleInput are NOT reaped
	// promptly (they wait on ctx-cancel or the 60s idle timer). Document it; do
	// not fail on it — the timer eventually reaps regardless.
	_ = pc.Close()
	afterClose, reapedByClose := waitGoroutines(baseline, 1500*time.Millisecond)
	t.Logf("after Close() (ctx still live): goroutines=%d baseline=%d reaped=%v "+
		"(Close only closes the read side; the outbound link lingers until ctx-cancel or the 60s idle timer)",
		afterClose, baseline, reapedByClose)

	// Cancelling the dispatch ctx must reap the link's goroutines promptly.
	cancel()
	afterCancel, reapedByCancel := waitGoroutines(baseline+2, 5*time.Second)
	if !reapedByCancel {
		t.Fatalf("dispatch ctx cancelled but goroutines did not return to baseline: got %d, baseline %d", afterCancel, baseline)
	}
	t.Logf("LIFECYCLE: cancelling the dispatch ctx reaped the link promptly (goroutines %d -> ~baseline %d). "+
		"Backend.ListenPacket MUST bind the dispatch ctx to the PacketConn's lifetime and cancel on close, "+
		"or leak a goroutine+socket per UDP session for up to 60s.", active, afterCancel)
}
