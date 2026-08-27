package engine

import (
	"context"
	"net"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/sagernet/sing/common/control"
)

// tunBypass holds the active TUN session's interface-bind control — sing-box's
// own AutoDetectInterfaceFunc, which pins a dial to the physical default
// interface (excluding our TUN, the same way sing-box keeps its own `direct`
// outbound from looping back into the tunnel). It is nil when no TUN session is
// up: a raw dial is already direct then and needs no exemption.
//
// Off Linux this is how a probe dial escapes an active TUN — there is no fwmark
// there (see dialmark_other.go), and a marked-but-unbound dial would be
// recaptured by auto_route. On Linux the SO_MARK fwmark is the exemption
// (dialmark_linux.go); the holder is still set but markControl ignores it.
var tunBypass atomic.Pointer[control.Func]

// SetTunBypass installs the interface-bind control when a TUN session starts.
// fn is sing-box's AutoDetectInterfaceFunc: a real bind func under a TUN (an
// interface monitor exists), nil for a SOCKS/no-TUN session — in which case the
// bypass is simply cleared. Called by Session.Start; safe for concurrent use.
func SetTunBypass(fn control.Func) {
	if fn == nil {
		tunBypass.Store(nil)
		return
	}
	tunBypass.Store(&fn)
}

// ClearTunBypass removes the control when the session closes (raw probe dials
// go back to being direct, since the TUN is gone).
func ClearTunBypass() { tunBypass.Store(nil) }

// bypassControl pins a dial to the active session's physical default interface
// when a TUN is up, else is a no-op. It is the off-Linux body of markControl.
func bypassControl(network, address string, c syscall.RawConn) error {
	if fn := tunBypass.Load(); fn != nil {
		return (*fn)(network, address, c)
	}
	return nil
}

// probeDialer returns the net.Dialer for read-only probes that dial a possibly-
// DOMAIN target. Both legs are TUN-exempt: Control (markControl) keeps the
// CONNECTION off the tunnel, and Resolver (tunExemptResolver) keeps the DNS that
// resolves the domain off it too — without the latter the socket would be exempt
// but the IP it dials would have been picked by the tunnel's hijack-dns. For a
// literal-IP target the resolver is simply never consulted.
func probeDialer(timeout time.Duration) *net.Dialer {
	return &net.Dialer{Timeout: timeout, Control: markControl, Resolver: tunExemptResolver}
}

// TunExemptDialer is probeDialer exposed to the daemon layer: connections
// AND the DNS lookups resolving them escape an active TUN (fwmark on Linux,
// interface-bind elsewhere) and are plain direct dials when no TUN session
// is up. The update check's direct transport dials through it.
func TunExemptDialer(timeout time.Duration) *net.Dialer { return probeDialer(timeout) }

// tunExemptResolver is a Go resolver whose UDP/TCP dials to the system's
// configured DNS servers carry the TUN-exemption control (fwmark on Linux,
// interface-bind off Linux). It replaces net.DefaultResolver for the doctor's
// OS-resolver OBSERVATIONS so that, under an active TUN, they measure the REAL
// network's DNS path instead of the tunnel's hijack-dns. READ-ONLY.
//
// PreferGo forces the pure-Go resolver, so the Dial (and thus the control) is
// honoured even on a cgo build — mirroring probeUDPResolver/udpResolve.
var tunExemptResolver = &net.Resolver{
	PreferGo: true,
	Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
		d := net.Dialer{Timeout: doctorDialTimeout, Control: markControl}
		return d.DialContext(ctx, network, address)
	},
}
