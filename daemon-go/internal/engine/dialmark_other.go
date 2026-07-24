//go:build !linux

package engine

import "syscall"

// markControl off Linux pins the dial to the active session's physical default
// interface (bypassControl → sing-box's AutoDetectInterfaceFunc), so a probe
// dial under a live TUN egresses the REAL network instead of being recaptured
// by auto_route. There is no fwmark here (SO_MARK is Linux-only), and interface
// binding is exactly how sing-box keeps its own outbounds off its TUN. When no
// TUN session is up the holder is nil and this is a no-op (the dial is already
// direct). See dialmark.go (tunBypass / SetTunBypass).
func markControl(network, address string, c syscall.RawConn) error {
	return bypassControl(network, address, c)
}
