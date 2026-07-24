//go:build windows

package engine

import (
	"errors"

	"golang.org/x/sys/windows"
)

// EnsureTUNPrivilege returns a clear, actionable error when the daemon is not
// running elevated. Opening the wintun adapter needs Administrator; without it
// the raw failure is an access-denied buried in "open TUN", which is what
// confused the first Windows run. Checked ONLY on TUN activation — SOCKS proxy
// mode needs no elevation.
func EnsureTUNPrivilege() error {
	if windows.GetCurrentProcessToken().IsElevated() {
		return nil
	}
	return errors.New(
		"TUN mode needs Administrator. Start the daemon elevated — the " +
			"installer's \"iron-link daemon (admin)\" shortcut prompts UAC, or " +
			"right-click the daemon → Run as administrator. Proxy (SOCKS) " +
			"mode works without admin.")
}
