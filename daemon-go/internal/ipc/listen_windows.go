//go:build windows

package ipc

import (
	"net"
	"os"

	"github.com/Microsoft/go-winio"
)

// pipeName is the control named pipe — the fixed, well-known name the client
// dials.
const pipeName = `\\.\pipe\iron-link`

// pipeSDDL is the pipe's DACL. On Windows the daemon runs elevated
// (LocalSystem / Administrator — TUN needs it) while the client stays
// unprivileged; the default pipe DACL would deny the interactive user and force
// them to elevate too. So: SYSTEM (SY) and Administrators (BA) get full control
// (GA); the interactive logged-on user (IU) gets read+write (GR|GW) to connect.
// This is the Windows analogue of the Unix 0600 socket and mirrors the existing
// Rust daemon's DACL.
const pipeSDDL = "D:(A;;GA;;;SY)(A;;GA;;;BA)(A;;GRGW;;;IU)"

// DefaultAddr is the control pipe name (override with IRON_LINK_SOCKET).
func DefaultAddr() string {
	if p := os.Getenv("IRON_LINK_SOCKET"); p != "" {
		return p
	}
	return pipeName
}

// Listen binds the control named pipe with the restrictive DACL above. The
// returned net.Listener plugs straight into Server.Serve, so the transport is
// the only Windows-specific piece. (Compile-verified via GOOS=windows cross-
// build; runtime-verified on a Windows host as part of the G0/G2 work.)
func Listen(addr string) (net.Listener, error) {
	return winio.ListenPipe(addr, &winio.PipeConfig{SecurityDescriptor: pipeSDDL})
}
