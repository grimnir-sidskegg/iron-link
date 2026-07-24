package engine

import (
	"errors"
	"syscall"
	"testing"

	"github.com/sagernet/sing/common/control"
)

// TestTunBypassHolder covers the set/clear/no-op contract of the interface-bind
// holder that off-Linux markControl delegates to (dialmark.go). The real syscall
// binding needs a live TUN and is out of unit scope; this proves the dispatch.
func TestTunBypassHolder(t *testing.T) {
	t.Cleanup(ClearTunBypass)

	// No session up: bypassControl must be a pure no-op (raw dial is direct).
	if err := bypassControl("tcp", "1.1.1.1:443", nil); err != nil {
		t.Fatalf("bypassControl with no bypass set: want nil, got %v", err)
	}

	// A TUN session installs its AutoDetectInterfaceFunc; bypassControl must call
	// it with the same arguments and propagate its result.
	sentinel := errors.New("bound")
	var gotNetwork, gotAddress string
	var fn control.Func = func(network, address string, _ syscall.RawConn) error {
		gotNetwork, gotAddress = network, address
		return sentinel
	}
	SetTunBypass(fn)
	if err := bypassControl("udp", "8.8.8.8:53", nil); !errors.Is(err, sentinel) {
		t.Fatalf("bypassControl with bypass set: want sentinel, got %v", err)
	}
	if gotNetwork != "udp" || gotAddress != "8.8.8.8:53" {
		t.Fatalf("bypass func got (%q,%q), want (udp,8.8.8.8:53)", gotNetwork, gotAddress)
	}

	// A nil func (a SOCKS/no-TUN session) clears the bypass.
	SetTunBypass(nil)
	if err := bypassControl("tcp", "1.1.1.1:443", nil); err != nil {
		t.Fatalf("bypassControl after SetTunBypass(nil): want nil, got %v", err)
	}

	// Explicit clear after a real func is installed.
	SetTunBypass(fn)
	ClearTunBypass()
	if err := bypassControl("tcp", "1.1.1.1:443", nil); err != nil {
		t.Fatalf("bypassControl after ClearTunBypass: want nil, got %v", err)
	}
}
