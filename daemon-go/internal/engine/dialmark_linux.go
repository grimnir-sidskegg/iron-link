//go:build linux

package engine

import (
	"syscall"

	"golang.org/x/sys/unix"
)

// markControl tags a dial socket with AutoRedirectOutputMark so a raw probe
// dial (the TCP diagnosis stage) is EXEMPT from the auto_redirect TUN — the
// same own-output fwmark sing-box puts on its sockets. Without it, a raw dial
// from the daemon during an active TUN session would be re-captured by the
// tunnel and never reach the bare endpoint.
//
// Setting SO_MARK needs CAP_NET_ADMIN, which the privileged daemon has (it
// opens the TUN). The mark is therefore BEST-EFFORT: when unprivileged (an
// EPERM from setsockopt), there is no active TUN to bypass anyway, so we dial
// unmarked rather than fail the probe. Only a RawConn access failure is fatal.
func markControl(network, address string, c syscall.RawConn) error {
	return c.Control(func(fd uintptr) {
		_ = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_MARK, AutoRedirectOutputMark)
	})
}
