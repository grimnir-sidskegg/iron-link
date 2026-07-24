//go:build !linux

package engine

// ProbeForwarding is a no-op off Linux: auto_redirect (and thus the
// forwarded-traffic-into-INPUT interaction with the host firewall) is
// Linux-only, so the check is not applicable. Linux stays false → omitted.
func ProbeForwarding() ForwardingReport { return ForwardingReport{} }
