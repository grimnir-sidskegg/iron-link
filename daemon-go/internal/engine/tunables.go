package engine

import "fmt"

// Tunables are the engine-relevant user settings, mapped from the store's
// settings document by the manager — the engine stays independent of the
// store package. A nil SessionPlan.Tunables means DefaultTunables(),
// which mirrors store.DefaultSettings (and the values that were hardcoded
// before settings existed), so plans built without a manager — tests, the
// validation harness — keep the proven shapes.
type Tunables struct {
	// LogLevel: both cores' log config ("error"/"warn"/"info"/"debug").
	LogLevel string
	// IPVersion: "v4" / "v6" / "both" — which addresses the TUN carries.
	// DNSStrategy is a SEPARATE STORED field, but the two are not free of each
	// other at COMPILE time: a single-family TUN cannot carry the other family,
	// so a v4/v6 ip_version PINS the effective strategy to the matching *_only
	// (see dnsStrategy). "both" honors the stored strategy.
	IPVersion string
	// DNSStrategy is the user's STORED sing-box DNS record strategy
	// ("prefer_ipv4" default, or "prefer_ipv6" / "ipv4_only" / "ipv6_only"). It
	// is kept independent in the store (so flipping ip_version back to "both"
	// restores it), but the EFFECTIVE strategy the engine compiles is
	// dnsStrategy(DNSStrategy, IPVersion) — a single-family TUN overrides it.
	DNSStrategy string
	// DNS upstreams in priority order; the FIRST answers queries (the
	// rest are compiled and tagged, reachable by future DNS rules).
	DNS []DNSUpstream
	// DNSViaTunnel detours query traffic through the selector. The
	// node-domain bootstrap resolver always dials direct — resolving the
	// proxy's own domain through the proxy is circular.
	DNSViaTunnel bool
	// LanBypass is the TUN route_exclude_address list. Semantics:
	// nil = the default link-local / ULA set, EMPTY non-nil slice =
	// bypass disabled (exclude nothing). NB: the RFC1918 IPv4 ranges are NOT
	// here — they are steered direct by the immutable private-direct route rule
	// (route.go privateDirectCIDRs), so DNS aimed at a LAN resolver enters the
	// TUN and is hijacked rather than route-excluded (and thus leaked).
	LanBypass []string
	// TUN inbound knobs.
	TunMTU      int
	TunStack    string // "system" / "gvisor" / "mixed"
	StrictRoute bool
}

// DNSUpstream is one resolver: sing-box 1.12 typed server ("udp" / "tls" /
// "https") at a literal IP.
type DNSUpstream struct {
	Type    string
	Address string
}

// DefaultTunables mirrors store.DefaultSettings' engine subset.
func DefaultTunables() Tunables {
	return Tunables{
		LogLevel:    "info",
		IPVersion:   "both",
		DNSStrategy: "prefer_ipv4",
		DNS:         []DNSUpstream{{Type: "tls", Address: "8.8.8.8"}},
		// Link-local / ULA only. The RFC1918 IPv4 ranges were intentionally
		// dropped from the default route_exclude set — the immutable
		// private-direct route rule now keeps LAN devices reachable while the
		// DNS hijack captures LAN-resolver queries (see route.go).
		LanBypass: []string{
			"169.254.0.0/16", "fc00::/7", "fe80::/10",
		},
		TunMTU:      1500,
		TunStack:    "mixed",
		StrictRoute: true,
	}
}

// tunables resolves the plan's effective settings.
func (p *SessionPlan) tunables() Tunables {
	if p.Tunables == nil {
		return DefaultTunables()
	}
	return *p.Tunables
}

// dnsStrategy resolves the EFFECTIVE sing-box DNS record strategy from the
// user's stored preference and the TUN's ip_version.
//
// Invariant: a single-family TUN cannot carry the other family, so DNS must
// never hand an app an address the tunnel would black-hole. A prefer_* strategy
// is only a SORT ORDER — sing-box still resolves and returns the other family's
// records, and the app's own getaddrinfo (RFC 6724) re-sorts and may prefer the
// address the v4/v6-only TUN can't route. So ip_version=v4 PINS the effective
// strategy to ipv4_only (strip AAAA) and v6 to ipv6_only, OVERRIDING the stored
// preference; only ip_version=both honors it (empty/unknown → prefer_ipv4).
//
// The stored preference is kept independent in the store (flipping ip_version
// back to "both" restores it); enforcement lives HERE so a hand-edited
// settings.json or an older client can't ship an inconsistent config.
func dnsStrategy(strategy, ipVersion string) string {
	switch ipVersion {
	case "v4":
		return "ipv4_only"
	case "v6":
		return "ipv6_only"
	}
	switch strategy {
	case "prefer_ipv4", "prefer_ipv6", "ipv4_only", "ipv6_only":
		return strategy
	default:
		return "prefer_ipv4"
	}
}

// tunAddresses returns the TUN interface addresses for the IP version.
func tunAddresses(ipVersion string) []any {
	const v4, v6 = "172.18.0.1/30", "fdfe:dcba:9876::1/126"
	switch ipVersion {
	case "v4":
		return []any{v4}
	case "v6":
		return []any{v6}
	default:
		return []any{v4, v6}
	}
}

func dnsTag(i int) string {
	if i == 0 {
		return "dns-primary"
	}
	return fmt.Sprintf("dns-%d", i)
}
