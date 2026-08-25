// Package engine embeds sing-box + xray and wires the data plane: a sing-box
// TUN inbound + routing whose per-node traffic is handed to a Backend IN-MEMORY
// (no socket between the cores). This file is the load-bearing bridge — now
// ENGINE-AGNOSTIC: the sing-box outbound delegates to a Backend (backend.go)
// keyed by node id, so xray is just the first backend (xray_backend.go) and
// future engines slot in without touching this file or the TUN/routing side.
package engine

import (
	"context"
	"net"

	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// backendOutbound is a sing-box adapter.Outbound whose Dial/Listen delegate to a
// Backend, keyed by the node this outbound stands for. sing-box hands it a
// routed (network, destination); it forwards to the backend with the node id,
// and the backend performs the actual cross-core dispatch. typ is the sing-box
// outbound type (backend.Type()); tag is the sing-box outbound tag (the selector
// member — a node UUID); nodeID is the backend-side node identifier.
type backendOutbound struct {
	typ    string
	tag    string
	nodeID string
	be     Backend
}

func (o *backendOutbound) Type() string           { return o.typ }
func (o *backendOutbound) Tag() string            { return o.tag }
func (o *backendOutbound) Network() []string      { return []string{N.NetworkTCP, N.NetworkUDP} }
func (o *backendOutbound) Dependencies() []string { return nil }

// DialContext forwards the routed stream connection to the backend for this
// outbound's node. Both sides meet at a standard net.Conn.
func (o *backendOutbound) DialContext(ctx context.Context, network string, dest M.Socksaddr) (net.Conn, error) {
	return o.be.DialContext(ctx, o.nodeID, network, dest)
}

// ListenPacket forwards the routed datagram path to the backend for this
// outbound's node.
func (o *backendOutbound) ListenPacket(ctx context.Context, dest M.Socksaddr) (net.PacketConn, error) {
	return o.be.ListenPacket(ctx, o.nodeID, dest)
}
