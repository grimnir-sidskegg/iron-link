// Package engine embeds sing-box + xray and wires the data plane: a sing-box
// TUN inbound + routing whose xray-routed traffic is handed to an xray instance
// IN-MEMORY via core.Dial — no socket between the cores. This file is the
// load-bearing bridge, ported verbatim from the verified spike
// (spike-go-cores/glue + tuncheck): construct + runtime proven on Linux.
package engine

import (
	"context"
	"net"

	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	xnet "github.com/xtls/xray-core/common/net"
	xcore "github.com/xtls/xray-core/core"
)

// OutboundType is the sing-box outbound type tag for the in-memory xray bridge.
const OutboundType = "xray-reality"

// xrayOutbound is a sing-box adapter.Outbound whose DialContext dispatches
// straight into an xray instance's routing via core.Dial — the cross-core hop
// that never opens a loopback port.
type xrayOutbound struct {
	tag  string
	inst *xcore.Instance
}

func (o *xrayOutbound) Type() string           { return OutboundType }
func (o *xrayOutbound) Tag() string            { return o.tag }
func (o *xrayOutbound) Network() []string      { return []string{N.NetworkTCP, N.NetworkUDP} }
func (o *xrayOutbound) Dependencies() []string { return nil }

// DialContext: sing-box hands us (network, destination); we convert and dispatch
// it into xray via core.Dial. Both sides meet at a standard net.Conn.
func (o *xrayOutbound) DialContext(ctx context.Context, network string, dest M.Socksaddr) (net.Conn, error) {
	return xcore.Dial(ctx, o.inst, toXray(network, dest))
}

func (o *xrayOutbound) ListenPacket(ctx context.Context, dest M.Socksaddr) (net.PacketConn, error) {
	return xcore.DialUDP(ctx, o.inst)
}

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
