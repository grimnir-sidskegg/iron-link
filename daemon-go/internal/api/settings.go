package api

// Settings is the daemon-global settings document: the wire payload of
// `get_settings`/`set_settings` AND the on-disk shape of `settings.json`
// (the daemon is the only writer; clients never touch the file). The
// defaults (store.DefaultSettings) mirror the previously hardcoded
// values, so a missing file is behavior-neutral.
//
// Validation lives in the store package (the api package stays a dumb,
// stdlib-only mirror). Engine-relevant fields (everything except
// RestoreOnStart / SubscriptionUserAgent / LatencyProbe) take effect at
// the NEXT activation — `set_settings` replies with `needs_reactivation`
// when a session is running and one of them changed.
type Settings struct {
	// LogLevel: "error" / "warn" / "info" / "debug" — both cores' log
	// config and the GUI log feed.
	LogLevel string `json:"log_level"`
	// IPVersion: "v4" / "v6" / "both" — which addresses the TUN interface
	// carries. The DNS resolution strategy is a SEPARATE knob (dns.strategy);
	// they used to be one field, which let a v4 TUN silently force ipv4_only
	// DNS and strand v6-only system resolvers on dual-stack networks.
	IPVersion string      `json:"ip_version"`
	DNS       DNSSettings `json:"dns"`
	LanBypass LanBypass   `json:"lan_bypass"`
	// RestoreOnStart: restore the last session when the daemon starts.
	RestoreOnStart bool `json:"restore_on_start"`
	// SubscriptionUserAgent: providers gate the body format on the UA.
	SubscriptionUserAgent string `json:"subscription_user_agent"`
	// SocksPort: the 127.0.0.1 SOCKS inbound port in non-TUN mode.
	SocksPort    int           `json:"socks_port"`
	Tun          TunSettings   `json:"tun"`
	LatencyProbe ProbeSettings `json:"latency_probe"`
}

// DNSSettings configures the sing-box resolver app traffic uses.
type DNSSettings struct {
	// Servers, in priority order; the FIRST server answers queries today
	// (per-domain DNS rules arrive with the routing-editor work).
	Servers []DNSServer `json:"servers"`
	// Strategy is the sing-box DNS record strategy, INDEPENDENT of the TUN's
	// IPVersion: "prefer_ipv4" (the default), "prefer_ipv6", "ipv4_only",
	// "ipv6_only". On a dual-stack network this is the knob that stabilizes
	// which family apps reach sites over (and avoids dropping AAAA records the
	// system would otherwise reach over IPv6). An empty value means the default.
	Strategy string `json:"strategy"`
	// ViaTunnel routes DNS queries through the active proxy instead of
	// dialing direct. Node-domain bootstrap resolution always stays
	// direct (the chicken-and-egg is structural).
	ViaTunnel bool `json:"via_tunnel"`
}

// DNSServer is one upstream: Type "udp" / "tls" / "https", Address a
// LITERAL IP (hostname upstreams need a resolver themselves — deferred).
type DNSServer struct {
	Type    string `json:"type"`
	Address string `json:"address"`
}

// LanBypass keeps local-network traffic out of the TUN.
type LanBypass struct {
	Enabled bool `json:"enabled"`
	// CIDRs is the user-editable exclusion list (defaults: link-local / ULA
	// only). The RFC1918 IPv4 ranges are deliberately NOT here — they are
	// steered direct by the daemon's immutable private-direct route rule, so
	// DNS aimed at a LAN resolver is captured by the DNS hijack instead of
	// bypassing the stack (and being poisoned/stale).
	CIDRs []string `json:"cidrs"`
}

// TunSettings are the TUN-inbound knobs exposed to the user.
type TunSettings struct {
	MTU int `json:"mtu"`
	// Stack: "system" / "gvisor" / "mixed".
	Stack       string `json:"stack"`
	StrictRoute bool   `json:"strict_route"`
}

// ProbeSettings configure the latency probe (in a censored environment
// the default probe endpoint itself may be blocked).
type ProbeSettings struct {
	URL        string `json:"url"`
	BudgetSecs int    `json:"budget_secs"`
}
