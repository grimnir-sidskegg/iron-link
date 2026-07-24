//go:build !windows

package main

// maybeRunWindows is a no-op off Windows: the daemon always runs in the
// foreground (under sudo for TUN). The Windows build provides the real SCM
// service run loop plus the install/uninstall/start/stop subcommands.
func maybeRunWindows() (handled bool, err error) { return false, nil }
