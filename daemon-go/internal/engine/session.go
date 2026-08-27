package engine

import (
	"errors"
	"fmt"
	"sync/atomic"

	box "github.com/sagernet/sing-box"
)

// Session is a running data plane: a started sing-box (TUN + routing) plus the
// backends its member outbounds dispatch to (xray today; the seam is
// engine-agnostic). It owns the box's and every backend's lifetime.
type Session struct {
	// box is atomic because ProxyDialContext runs OUTSIDE the manager's mu
	// (the update check must not hold it across a fetch) while Close, always
	// called under mu, swaps the pointer out. Every accessor loads it once.
	box      atomic.Pointer[box.Box]
	backends []Backend
	traffic  TrafficCounter
}

// StartOptions are the per-session instrumentation hooks (§6).
type StartOptions struct {
	// LogSink receives every sing-box log line (level + message). Must be
	// cheap and non-blocking; nil = lines stay on the box's own output.
	LogSink LogSink
}

// Start builds the xray instance and the sing-box from their configs, wires the
// xray-reality bridge, and Starts the box — which OPENS THE TUN DEVICE and so
// needs root / CAP_NET_ADMIN (Linux), root (macOS), or Administrator (Windows;
// sing-tun embeds wintun.dll and loads it in-memory — no driver file ships).
// On any failure it tears down whatever was already built.
//
// Build the binary with `-tags "with_gvisor,with_utls,with_clash_api,with_quic"` for the gvisor TUN stack
// the configs use (and the QUIC outbounds).
func Start(singBoxCfg, xrayCfg []byte) (*Session, error) {
	return StartWithOptions(singBoxCfg, xrayCfg, StartOptions{})
}

// StartWithOptions is Start with the §6 instrumentation hooks attached: the
// session's traffic counter is appended to the box router (every routed
// connection counted in-process), and opts.LogSink taps the box's log output.
func StartWithOptions(singBoxCfg, xrayCfg []byte, opts StartOptions) (*Session, error) {
	xinst, err := BuildXray(xrayCfg)
	if err != nil {
		return nil, fmt.Errorf("build xray: %w", err)
	}
	// One xray backend hosts this session's xray-routed node(s). The seam is
	// engine-agnostic — future engines append their own backends here.
	backends := []Backend{newXrayBackend(xinst)}
	s := &Session{backends: backends}
	b, err := BuildBox(singBoxCfg, backends, opts.LogSink)
	if err != nil {
		_ = closeBackends(backends)
		return nil, fmt.Errorf("build sing-box: %w", err)
	}
	b.Router().AppendTracker(&s.traffic)
	if err := b.Start(); err != nil {
		b.Close()
		_ = closeBackends(backends)
		return nil, fmt.Errorf("start sing-box (open TUN — needs root?): %w", err)
	}
	s.box.Store(b)
	// Under a TUN session sing-box runs a default-interface monitor; reuse its
	// AutoDetectInterfaceFunc so the daemon's OWN probe dials (doctor, latency)
	// can be pinned to the physical interface and escape the active TUN off
	// Linux — exactly as sing-box keeps its own `direct` outbound from looping
	// (Linux uses the SO_MARK fwmark instead). It is nil for a SOCKS session (no
	// interface monitor), which clears the bypass.
	SetTunBypass(b.Network().AutoDetectInterfaceFunc())
	return s, nil
}

// Traffic returns the session's cumulative routed (upload, download) bytes —
// the in-process replacement of the Clash /traffic WS (§6).
func (s *Session) Traffic() (up, down int64) {
	return s.traffic.Totals()
}

// TrafficApps returns a snapshot of the session's cumulative per-app routed
// bytes, keyed by the owning process's executable path (find_process is on; an
// empty path is the unattributed bucket). Each app's bytes are split per route
// outcome (AppBytes.ByRoute) alongside the Up/Down totals.
func (s *Session) TrafficApps() []AppBytes {
	return s.traffic.AppTotals()
}

// Close stops the box (tearing down auto_route / the TUN device) FIRST, then
// every backend, returning any errors joined. Box-first keeps the ordering the
// single-xray session had (stop routing traffic before closing the engines it
// dispatches to).
func (s *Session) Close() error {
	// Drop the interface-bind bypass first: with the TUN going down, probe dials
	// are direct again, and the held func references this box's monitor (closed
	// below). Safe under the manager's at-most-one-session invariant.
	ClearTunBypass()
	var errs []error
	if b := s.box.Swap(nil); b != nil {
		if err := b.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close sing-box: %w", err))
		}
	}
	for _, be := range s.backends {
		if err := be.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close %s backend: %w", be.Type(), err))
		}
	}
	s.backends = nil
	return errors.Join(errs...)
}
