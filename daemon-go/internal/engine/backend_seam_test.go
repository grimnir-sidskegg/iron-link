// End-to-end proof of the engine-agnostic seam THROUGH the real backendOutbound
// path — not just the low-level dispatch primitive the spikes cover. One
// XrayBackend hosts TWO nodes (two tagged outbounds + two inboundTag->outboundTag
// rules, exactly the shape compileXrayClientNodes emits); each node is wrapped as
// a backendOutbound and dialed. The assertion: each backendOutbound reaches ITS
// intended node for BOTH TCP and UDP, and ListenPacket cancels its dispatch ctx
// on Close (no goroutine leak).
//
// Scaffolding is reused from the spike files (same package): markerA/markerB,
// markerServer/udpMarkerServer (the redirect-pinned egress markers),
// routingInstance (two freedom outbounds egress-a/egress-b + the two inbound-tag
// rules node-a->egress-a, node-b->egress-b), waitGoroutines. node-a / node-b are
// the per-node dispatch ids the backendOutbounds carry; egress-a/egress-b are the
// instance's outbounds. Loopback (127.0.0.1) is local scaffolding; wire
// destinations are RFC-5737 documentation addresses, never real infrastructure.
package engine

import (
	"context"
	"io"
	"net"
	"runtime"
	"testing"
	"time"

	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// twoNodeXrayBackend builds one XrayBackend over a routingInstance (two nodes)
// and returns it with a backendOutbound per node. The backend is NOT Closed here
// — routingInstance registers the instance's t.Cleanup — so the caller may drive
// PacketConn lifecycle without racing a backend teardown.
func twoNodeXrayBackend(t *testing.T, addrA, addrB string) (*XrayBackend, *backendOutbound, *backendOutbound) {
	t.Helper()
	be := newXrayBackend(routingInstance(t, addrA, addrB))
	obA := &backendOutbound{typ: be.Type(), tag: "member-a", nodeID: "node-a", be: be}
	obB := &backendOutbound{typ: be.Type(), tag: "member-b", nodeID: "node-b", be: be}
	return be, obA, obB
}

// TestBackendSeamTCPPerNode: two backendOutbounds over ONE XrayBackend each reach
// their own node's egress over TCP, for the SAME wire destination
// (destination-independent), proving the seam dispatches by node id through the
// instance's inbound-tag rules.
func TestBackendSeamTCPPerNode(t *testing.T) {
	addrA := markerServer(t, markerA)
	addrB := markerServer(t, markerB)
	_, obA, obB := twoNodeXrayBackend(t, addrA, addrB)

	read := func(ob *backendOutbound) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// The wire destination is overridden by freedom's redirect, so it is the
		// SAME for both members — the egress read back is decided by node id alone.
		conn, err := ob.DialContext(ctx, N.NetworkTCP, M.ParseSocksaddr("203.0.113.9:80"))
		if err != nil {
			return "", err
		}
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		buf, err := io.ReadAll(conn) // marker server writes then closes -> clean EOF
		return string(buf), err
	}

	for i := 0; i < 3; i++ {
		if got, err := read(obA); err != nil || got != markerA {
			t.Fatalf("iter %d: node-a backendOutbound reached %q (err %v), want %q", i, got, err, markerA)
		}
		if got, err := read(obB); err != nil || got != markerB {
			t.Fatalf("iter %d: node-b backendOutbound reached %q (err %v), want %q", i, got, err, markerB)
		}
	}
}

// TestBackendSeamUDPPerNode: the same per-node dispatch over UDP through
// backendOutbound.ListenPacket. Each member's PacketConn egresses via its own
// node, and a later destination on the same PacketConn stays pinned (the
// inbound-tag attribute is re-read, never consumed).
func TestBackendSeamUDPPerNode(t *testing.T) {
	addrA := udpMarkerServer(t, markerA)
	addrB := udpMarkerServer(t, markerB)
	_, obA, obB := twoNodeXrayBackend(t, addrA, addrB)

	roundTrip := func(ob *backendOutbound, want string) {
		t.Helper()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		pc, err := ob.ListenPacket(ctx, M.ParseSocksaddr("198.51.100.11:5311"))
		if err != nil {
			t.Fatalf("%s ListenPacket: %v", ob.nodeID, err)
		}
		defer pc.Close()
		// Two distinct wire destinations on one PacketConn: both must stay on the
		// member's node (freedom's redirect overrides each to the marker server).
		for _, dst := range []*net.UDPAddr{
			{IP: net.ParseIP("198.51.100.11"), Port: 5311},
			{IP: net.ParseIP("198.51.100.12"), Port: 5312},
		} {
			if _, err := pc.WriteTo([]byte("ping"), dst); err != nil {
				t.Fatalf("%s WriteTo %v: %v", ob.nodeID, dst, err)
			}
			got, err := readUDPBounded(t, pc, 3*time.Second)
			if err != nil {
				t.Fatalf("%s read %v: %v", ob.nodeID, dst, err)
			}
			if got != want {
				t.Fatalf("%s reached %q at %v, want %q", ob.nodeID, got, dst, want)
			}
		}
	}

	roundTrip(obA, markerA)
	roundTrip(obB, markerB)
}

// TestBackendSeamListenPacketCancelsOnClose asserts the UDP lifecycle contract:
// backendOutbound.ListenPacket binds a cancelable dispatch ctx to the returned
// PacketConn and cancels it on Close, so closing the conn reaps xray's outbound
// link+goroutine promptly (the spike proved Close alone does NOT — the link
// otherwise lingers to the 60s idle timer). No leak per closed UDP session.
func TestBackendSeamListenPacketCancelsOnClose(t *testing.T) {
	addrA := udpMarkerServer(t, markerA)
	addrB := udpMarkerServer(t, markerB)
	_, obA, _ := twoNodeXrayBackend(t, addrA, addrB)

	// Warm up lazy instance goroutines with a throwaway round trip fully torn
	// down, so the baseline is steady state.
	{
		pc, err := obA.ListenPacket(context.Background(), M.ParseSocksaddr("198.51.100.11:5311"))
		if err != nil {
			t.Fatalf("warmup ListenPacket: %v", err)
		}
		_, _ = pc.WriteTo([]byte("ping"), &net.UDPAddr{IP: net.ParseIP("198.51.100.11"), Port: 5311})
		_, _ = readUDPBounded(t, pc, 3*time.Second)
		_ = pc.Close()
	}
	baseline, _ := waitGoroutines(runtime.NumGoroutine(), 3*time.Second)

	// A fresh PacketConn + one round trip so the outbound link and its goroutine
	// are live above the baseline.
	pc, err := obA.ListenPacket(context.Background(), M.ParseSocksaddr("198.51.100.11:5311"))
	if err != nil {
		t.Fatalf("ListenPacket: %v", err)
	}
	if _, err := pc.WriteTo([]byte("ping"), &net.UDPAddr{IP: net.ParseIP("198.51.100.11"), Port: 5311}); err != nil {
		t.Fatalf("WriteTo: %v", err)
	}
	if got, err := readUDPBounded(t, pc, 3*time.Second); err != nil || got != markerA {
		t.Fatalf("round trip before close: got %q err %v", got, err)
	}

	// Close MUST cancel the dispatch ctx (no ctx-cancel from us): goroutines must
	// return to baseline promptly. Without the cancel-on-Close wrapper this stays
	// elevated until the 60s idle timer, which this bounded wait would catch.
	if err := pc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got, reaped := waitGoroutines(baseline+2, 5*time.Second); !reaped {
		t.Fatalf("Close did not reap the UDP dispatch link: goroutines=%d baseline=%d "+
			"(backendOutbound.ListenPacket must cancel its dispatch ctx on Close)", got, baseline)
	}
}
