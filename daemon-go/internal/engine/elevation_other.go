//go:build !windows

package engine

import (
	"errors"
	"os"
)

// EnsureTUNPrivilege returns a clear error when the daemon lacks the privilege
// to open a TUN: root (euid 0) on Linux/macOS. Checked ONLY on TUN activation —
// SOCKS proxy mode needs no root.
func EnsureTUNPrivilege() error {
	if os.Geteuid() == 0 {
		return nil
	}
	return errors.New("TUN mode needs root: start the daemon with sudo. " +
		"Proxy (SOCKS) mode works without root.")
}
