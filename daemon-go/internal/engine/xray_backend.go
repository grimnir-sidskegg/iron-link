// The xray Backend: one *xcore.Instance dispatched IN-MEMORY via core.Dial /
// core.DialUDP (no loopback socket between the cores). This is the xray-specific
// half of the engine-agnostic seam (backend.go); everything core-specific — the
// inbound-tag dispatch, the sing-box↔xray destination conversion, the UDP
// lifecycle fix — lives here, behind the Backend interface.

package engine

import (
	"context"
	"net"
	"sync"

	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/session"
	xcore "github.com/xtls/xray-core/core"
)

// OutboundType is the sing-box outbound type tag for the in-memory xray bridge.
// Kept verbatim ("xray-reality") for wire/config compatibility — it is the
// selector member type the plan emits and the configs declare.
const OutboundType = "xray-reality"

// XrayBackend implements Backend over ONE xray instance that may host several
// nodes: each node is a uniquely-tagged outbound reached by an
// inboundTag->outboundTag routing rule (see compile.go). Dispatch pins the node
// by setting ContextWithInbound{Tag: nodeID} on the dispatch context — the
// STABLE routing attribute the spikes proved robust (forcedOutboundTag is a
// one-shot that leaks to the default outbound on any re-dispatch).
type XrayBackend struct {
	inst *xcore.Instance
}

// newXrayBackend wraps an already-built, already-started xray instance. The
// backend takes over the instance's lifetime: Close closes it.
func newXrayBackend(inst *xcore.Instance) *XrayBackend { return &XrayBackend{inst: inst} }

func (b *XrayBackend) Type() string { return OutboundType }

// DialContext dispatches a TCP connection for nodeID: it stamps the node's
// inbound tag on the context (so the instance's per-node routing rule egresses
// through that node's outbound) and hands the converted destination to core.Dial.
func (b *XrayBackend) DialContext(ctx context.Context, nodeID, network string, dest M.Socksaddr) (net.Conn, error) {
	ctx = session.ContextWithInbound(ctx, &session.Inbound{Tag: nodeID})
	return xcore.Dial(ctx, b.inst, toXray(network, dest))
}

// ListenPacket opens a UDP packet conn for nodeID. xray's dispatcherConn.Close()
// only closes the READ side; the underlying outbound link and its goroutine are
// reaped by CANCELLING the dispatch context (or by the udp.Dispatcher's 60s idle
// timer). So the dispatch ctx is made cancelable and BOUND to the returned
// PacketConn's lifetime — cancelPacketConn.Close cancels it — else a
// goroutine+socket leaks per UDP session for up to a minute (the spike finding).
func (b *XrayBackend) ListenPacket(ctx context.Context, nodeID string, dest M.Socksaddr) (net.PacketConn, error) {
	dctx, cancel := context.WithCancel(ctx)
	dctx = session.ContextWithInbound(dctx, &session.Inbound{Tag: nodeID})
	pc, err := xcore.DialUDP(dctx, b.inst)
	if err != nil {
		cancel()
		return nil, err
	}
	return &cancelPacketConn{PacketConn: pc, cancel: cancel}, nil
}

func (b *XrayBackend) Close() error { return b.inst.Close() }

// cancelPacketConn cancels the dispatch context when the wrapped PacketConn is
// closed, reaping xray's outbound link and its goroutine promptly (see
// XrayBackend.ListenPacket). The cancel runs exactly once.
type cancelPacketConn struct {
	net.PacketConn
	cancel context.CancelFunc
	once   sync.Once
}

func (c *cancelPacketConn) Close() error {
	err := c.PacketConn.Close()
	c.once.Do(c.cancel)
	return err
}

// toXray converts a sing-box (network, destination) pair into the xray
// destination core.Dial / the UDP path expects. It is the ONLY sing-box↔xray
// destination conversion, and it stays inside the xray backend.
func toXray(network string, dest M.Socksaddr) xnet.Destination {
	var addr xnet.Address
	if dest.Fqdn != "" {
		addr = xnet.DomainAddress(dest.Fqdn)
	} else {
		addr = xnet.IPAddress(dest.Addr.AsSlice())
	}
	port := xnet.Port(dest.Port)
	if network == N.NetworkUDP {
		return xnet.UDPDestination(addr, port)
	}
	return xnet.TCPDestination(addr, port)
}
