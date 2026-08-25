// The engine-agnostic data-plane seam. sing-box owns the TUN inbound and the
// routing; whenever the route hands a connection to a per-node member outbound,
// that outbound (backendOutbound, bridge.go) delegates the actual dispatch to a
// Backend keyed by node id. xray is the first Backend (XrayBackend); future
// engines (zapret, webrtc-turn) implement this same interface and slot in
// WITHOUT touching the sing-box TUN/routing side.
//
// Per-node dispatch is by INBOUND-TAG routing (the settled decision proven by
// the forced-tag spikes): the Backend sets a stable per-node tag on the dispatch
// context, and the engine instance carries a per-node rule that maps that tag to
// the node's egress. Inbound-tag is re-read on every dispatch, so it survives
// multi-destination reuse and UDP idle reconnect — unlike a one-shot forced tag.

package engine

import (
	"context"
	"errors"
	"net"
	"time"

	M "github.com/sagernet/sing/common/metadata"
)

// Backend is one engine's per-node data plane. A single Backend instance may
// host many nodes; every call carries the nodeID that selects which node the
// traffic egresses through.
type Backend interface {
	// Type is the sing-box outbound type string this backend registers under
	// (e.g. "xray-reality"). BuildBox registers ONE outbound constructor per
	// Type, and the wire/config keeps addressing the member by that type.
	Type() string
	// DialContext dispatches a stream (TCP) connection for nodeID to dest.
	DialContext(ctx context.Context, nodeID, network string, dest M.Socksaddr) (net.Conn, error)
	// ListenPacket opens a datagram (UDP) conn for nodeID. The returned
	// PacketConn MUST release the backend's per-session dispatch resources on
	// Close (see XrayBackend.ListenPacket for why a bare Close can leak).
	ListenPacket(ctx context.Context, nodeID string, dest M.Socksaddr) (net.PacketConn, error)
	// Close releases the backend's underlying engine instance.
	Close() error
}

// ProbingBackend is a Backend that can also measure a node's latency in-process
// (the universal latency probe's per-engine hook). Optional: a backend that
// cannot self-probe simply does not implement it, and the caller falls back to
// the ephemeral-instance probes in instrument.go.
type ProbingBackend interface {
	Backend
	Probe(ctx context.Context, nodeID, url string) (time.Duration, error)
}

// closeBackends closes every backend, joining any errors — the teardown helper
// shared by the failure paths in StartWithOptions.
func closeBackends(backends []Backend) error {
	var errs []error
	for _, be := range backends {
		if err := be.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
