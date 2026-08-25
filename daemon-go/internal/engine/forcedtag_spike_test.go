// De-risking spike (PROOF only, no production code touched): prove that a
// SINGLE xray *core.Instance carrying MULTIPLE distinct outbounds can be forced,
// per-dial, to egress through a SPECIFIC chosen outbound — deterministically,
// independent of the destination and of outbound declaration order — using
// xray's forced-outbound-tag context primitive:
//
//	ctx = session.SetForcedOutboundTagToContext(ctx, "<tag>")   // BEFORE Dial
//	conn, _ := xcore.Dial(ctx, inst, dest)
//
// Source study of app/dispatcher.routedDispatch: the forced tag is honoured
// BEFORE the router runs (PickRoute is skipped) and resolves via
// outbound.Manager.GetHandler(tag); a non-existent tag hard-fails the link
// rather than silently taking a default. This test exercises that primitive
// DIRECTLY, mirroring what a future engine-agnostic bridge would do per node.
//
// Egress is made observable with xray "freedom" outbounds whose "redirect"
// (DestinationOverride) pins each outbound to a DIFFERENT loopback marker
// server, so the marker read back names the outbound that was actually used.
// Loopback (127.0.0.1) is local test scaffolding, not real infrastructure; the
// placeholder wire destination is an RFC-5737 TEST-NET address.
package engine

import (
	"context"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/xtls/xray-core/common/session"
	xnet "github.com/xtls/xray-core/common/net"
	xcore "github.com/xtls/xray-core/core"
)

const (
	markerA = "EGRESS-A"
	markerB = "EGRESS-B"
)

// placeholderDest is a fixed, non-loopback wire destination reused across every
// dial in this spike. freedom's "redirect" overrides it entirely, so it never
// leaves the box — its only job is to be the SAME valid destination for both
// forced tags, which is what makes destination-independence observable. It is an
// RFC-5737 TEST-NET address (documentation range), never real infra.
func placeholderDest() xnet.Destination {
	return xnet.TCPDestination(xnet.IPAddress(net.ParseIP("203.0.113.9").To4()), 80)
}

// markerServer starts a loopback TCP server that greets every accepted
// connection with marker and closes. The returned host:port is where an
// outbound must land for its dials to read marker back.
func markerServer(t *testing.T, marker string) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen marker server %q: %v", marker, err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return // listener closed on cleanup
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = c.Write([]byte(marker))
			}(c)
		}
	}()
	return l.Addr().String()
}

// udpMarkerServer starts a loopback UDP server that answers every datagram with
// marker (sent back to the sender). Returns its host:port.
func udpMarkerServer(t *testing.T, marker string) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen udp marker server %q: %v", marker, err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return // closed on cleanup
			}
			_ = n
			_, _ = pc.WriteTo([]byte(marker), from)
		}
	}()
	return pc.LocalAddr().String()
}

// forcedTagInstance builds ONE xray instance with two freedom outbounds:
// "egress-a" pinned to addrA, "egress-b" pinned to addrB. egress-a is declared
// FIRST, so it is also the default (first-added) handler — which lets the same
// instance prove both order-independence and the no-forced-tag default.
func forcedTagInstance(t *testing.T, addrA, addrB string) *xcore.Instance {
	t.Helper()
	cfg := fmt.Sprintf(`{
      "log": {"loglevel":"warning"},
      "outbounds": [
        {"protocol":"freedom","tag":"egress-a","settings":{"redirect":%q}},
        {"protocol":"freedom","tag":"egress-b","settings":{"redirect":%q}}
      ]
    }`, addrA, addrB)
	inst, err := BuildXray([]byte(cfg))
	if err != nil {
		t.Fatalf("BuildXray (two-outbound forced-tag config): %v", err)
	}
	t.Cleanup(func() { _ = inst.Close() })
	return inst
}

// setupForcedTag stands up the two marker servers and the two-outbound instance
// and returns the instance. egress-a -> markerA, egress-b -> markerB.
func setupForcedTag(t *testing.T) *xcore.Instance {
	t.Helper()
	addrA := markerServer(t, markerA)
	addrB := markerServer(t, markerB)
	return forcedTagInstance(t, addrA, addrB)
}

// readMarkerForced dials placeholderDest through inst forcing egress via tag
// (empty tag => no forced tag at all) and returns the marker the reached
// upstream greeted with.
//
// A FRESH context per call is mandatory: the dispatcher CONSUMES the forced tag
// by clearing the attribute on the context's shared *session.Content
// (SetForcedOutboundTagToContext(ctx,"") mutates the map in place), so a reused
// context would only force its first dial. A per-dial context is exactly what
// the real bridge's DialContext receives, so this mirrors production shape.
func readMarkerForced(t *testing.T, inst *xcore.Instance, tag string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if tag != "" {
		ctx = session.SetForcedOutboundTagToContext(ctx, tag)
	}
	conn, err := xcore.Dial(ctx, inst, placeholderDest())
	if err != nil {
		return "", err
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf, err := io.ReadAll(conn) // marker server closes after writing -> clean EOF
	return string(buf), err
}

// TestForcedTagSelectsEgressDeterministically is the core proof: forcing each
// tag reaches ITS outbound, for the SAME destination — destination-independent —
// and forcing the SECOND-declared outbound reaches it, not the first/default —
// order-independent. Repeated to show it is deterministic, not a coin flip.
func TestForcedTagSelectsEgressDeterministically(t *testing.T) {
	inst := setupForcedTag(t)

	for i := 0; i < 5; i++ {
		gotA, err := readMarkerForced(t, inst, "egress-a")
		if err != nil {
			t.Fatalf("iter %d: force egress-a dial: %v", i, err)
		}
		if gotA != markerA {
			t.Fatalf("iter %d: forced egress-a reached %q, want %q", i, gotA, markerA)
		}

		// SAME destination, different forced tag -> different egress:
		// destination-independent. egress-b is the SECOND-declared (non-default)
		// outbound: reaching it proves order-independence.
		gotB, err := readMarkerForced(t, inst, "egress-b")
		if err != nil {
			t.Fatalf("iter %d: force egress-b dial: %v", i, err)
		}
		if gotB != markerB {
			t.Fatalf("iter %d: forced egress-b reached %q, want %q (order-dependent?)", i, gotB, markerB)
		}
	}
	t.Logf("deterministic + destination-independent + order-independent over 5x2 dials: egress-a=%q egress-b=%q", markerA, markerB)
}

// TestForcedTagNonExistentHardFails: forcing a tag that is NOT a compiled
// outbound must HARD-FAIL the dial (dispatcher closes the writer and interrupts
// the reader), NOT silently fall through to a default outbound. Evidence: the
// read yields an error and/or no valid marker; a valid marker here would be the
// silent-default bug.
func TestForcedTagNonExistentHardFails(t *testing.T) {
	inst := setupForcedTag(t)

	got, err := readMarkerForced(t, inst, "egress-does-not-exist")
	if got == markerA || got == markerB {
		t.Fatalf("forcing a non-existent tag SILENTLY egressed to %q — must hard-fail, not fall through to a default", got)
	}
	if err == nil && got != "" {
		t.Fatalf("forcing a non-existent tag returned data %q with no error — expected an interrupted/closed link", got)
	}
	t.Logf("non-existent tag hard-failed as required: read=%q err=%v", got, err)
}

// TestForcedTagUnsetTakesDefault: with NO forced tag, egress must go to the
// DEFAULT (first-declared) outbound — egress-a here. This is the negative
// control for the positive test: it proves egress-a is not reached by accident.
func TestForcedTagUnsetTakesDefault(t *testing.T) {
	inst := setupForcedTag(t)

	got, err := readMarkerForced(t, inst, "") // no forced tag
	if err != nil {
		t.Fatalf("unforced dial: %v", err)
	}
	if got != markerA {
		t.Fatalf("unforced dial reached %q, want the first-declared default %q", got, markerA)
	}
	t.Logf("no forced tag -> default (first-declared) outbound egress-a=%q", got)
}

// TestForcedTagUDPPinsPacketConn covers the UDP path: DialUDP with the forced
// tag on the context pins the whole PacketConn to one outbound. Same primitive,
// same instance shape; freedom's redirect overrides the UDP destination too.
func TestForcedTagUDPPinsPacketConn(t *testing.T) {
	addrA := udpMarkerServer(t, markerA)
	addrB := udpMarkerServer(t, markerB)
	inst := forcedTagInstance(t, addrA, addrB)

	roundTrip := func(tag, want string) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		ctx = session.SetForcedOutboundTagToContext(ctx, tag)
		pc, err := xcore.DialUDP(ctx, inst)
		if err != nil {
			t.Fatalf("force %s DialUDP: %v", tag, err)
		}
		defer pc.Close()

		dst := &net.UDPAddr{IP: net.ParseIP("203.0.113.9"), Port: 80} // redirect overrides
		if _, err := pc.WriteTo([]byte("ping"), dst); err != nil {
			t.Fatalf("force %s WriteTo: %v", tag, err)
		}
		_ = pc.SetReadDeadline(time.Now().Add(3 * time.Second))
		buf := make([]byte, 64)
		n, _, err := pc.ReadFrom(buf)
		if err != nil {
			t.Fatalf("force %s ReadFrom: %v", tag, err)
		}
		if got := string(buf[:n]); got != want {
			t.Fatalf("forced %s UDP reached %q, want %q", tag, got, want)
		}
	}

	roundTrip("egress-a", markerA)
	roundTrip("egress-b", markerB) // second-declared: order-independent on UDP too
	t.Logf("UDP: forced tag pins the PacketConn per outbound (egress-a=%q, egress-b=%q)", markerA, markerB)
}
