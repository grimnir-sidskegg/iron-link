package engine

import (
	"encoding/json"
	"runtime"
	"strings"
	"testing"

	ilproxy "ironlink/daemon/internal/proxy"
)

// serverExcludeCount is how many entries serverExcludes adds to the TUN's
// route_exclude_address for tunablesPlan (one literal server IP, 203.0.113.1).
// Off Linux that exemption is the fwmark stand-in and is always present; on
// Linux the fwmark handles it, so PlanTUNConfigs adds nothing here. Keep the
// shape assertions below honest about that platform split.
func serverExcludeCount() int {
	if runtime.GOOS == "linux" {
		return 0
	}
	return 1
}

// tunablesPlan is a minimal compile-only plan (no cores started, so no
// with_utls gate — unlike plan_test.go's twoNativePlan).
func tunablesPlan() SessionPlan {
	return SessionPlan{
		Natives: []NamedNode{
			namedNode("node-a", "203.0.113.1", testReality(),
				ilproxy.Transport{Kind: ilproxy.TransportTCP}),
		},
		ActiveTag: "node-a",
	}
}

// decodeCfg parses a compiled sing-box config back into a generic tree.
func decodeCfg(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("decode compiled config: %v", err)
	}
	return cfg
}

func tunInbound(t *testing.T, cfg map[string]any) map[string]any {
	t.Helper()
	inbounds := cfg["inbounds"].([]any)
	return inbounds[0].(map[string]any)
}

func TestPlanTUNDefaultTunablesKeepProvenShape(t *testing.T) {
	plan := tunablesPlan()
	sbCfg, _, err := PlanTUNConfigs(plan, "tun-test")
	if err != nil {
		t.Fatalf("PlanTUNConfigs: %v", err)
	}
	cfg := decodeCfg(t, sbCfg)

	inbound := tunInbound(t, cfg)
	if inbound["mtu"].(float64) != 1500 || inbound["stack"] != "mixed" ||
		inbound["strict_route"] != true {
		t.Fatalf("default TUN knobs drifted: %v", inbound)
	}
	if got := len(inbound["address"].([]any)); got != 2 {
		t.Fatalf("default ip_version=both must carry v4+v6 addresses, got %d", got)
	}
	excludes := inbound["route_exclude_address"].([]any)
	// Link-local / ULA only (169.254.0.0/16, fc00::/7, fe80::/10). The RFC1918
	// IPv4 ranges are NOT route-excluded — they are steered by the immutable
	// private-direct route rule so DNS to a LAN resolver is hijacked, not
	// leaked. Off Linux the server-IP fwmark stand-in adds one more entry.
	if len(excludes) != 3+serverExcludeCount() {
		t.Fatalf("default LAN bypass must be link-local/ULA only, got %v", excludes)
	}
	for _, e := range excludes {
		switch e.(string) {
		case "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16":
			t.Fatalf("RFC1918 range %v must NOT be in the default route_exclude "+
				"(it would let LAN-resolver DNS bypass the hijack): %v", e, excludes)
		}
	}

	dns := cfg["dns"].(map[string]any)
	if dns["strategy"] != "prefer_ipv4" {
		t.Fatalf("default strategy drifted: %v", dns["strategy"])
	}
	servers := dns["servers"].([]any)
	primary := servers[0].(map[string]any)
	if primary["tag"] != "dns-primary" || primary["type"] != "tls" || primary["server"] != "8.8.8.8" {
		t.Fatalf("default DNS upstream drifted: %v", primary)
	}
	if _, hasDetour := primary["detour"]; hasDetour {
		t.Fatal("via_tunnel off must not add a detour")
	}
	route := cfg["route"].(map[string]any)
	if route["default_domain_resolver"] != "dns-primary" {
		t.Fatalf("domain resolver = %v, want dns-primary", route["default_domain_resolver"])
	}
	if cfg["log"].(map[string]any)["level"] != "info" {
		t.Fatalf("default log level drifted")
	}
}

func TestPlanTUNCustomTunables(t *testing.T) {
	plan := tunablesPlan()
	plan.Tunables = &Tunables{
		LogLevel:     "debug",
		IPVersion:    "v4",
		DNSStrategy:  "prefer_ipv6", // deliberately divergent from IPVersion
		DNS:          []DNSUpstream{{Type: "udp", Address: "1.1.1.1"}, {Type: "https", Address: "9.9.9.9"}},
		DNSViaTunnel: true,
		LanBypass:    []string{"192.168.0.0/16"},
		TunMTU:       9000,
		TunStack:     "system",
		StrictRoute:  false,
	}
	sbCfg, _, err := PlanTUNConfigs(plan, "tun-test")
	if err != nil {
		t.Fatalf("PlanTUNConfigs: %v", err)
	}
	cfg := decodeCfg(t, sbCfg)

	inbound := tunInbound(t, cfg)
	if inbound["mtu"].(float64) != 9000 || inbound["stack"] != "system" ||
		inbound["strict_route"] != false {
		t.Fatalf("custom TUN knobs not applied: %v", inbound)
	}
	addrs := inbound["address"].([]any)
	if len(addrs) != 1 || !strings.Contains(addrs[0].(string), ".") {
		t.Fatalf("ip_version=v4 must keep only the v4 address, got %v", addrs)
	}
	if got := inbound["route_exclude_address"].([]any); len(got) != 1+serverExcludeCount() {
		t.Fatalf("custom LAN bypass not applied: %v", got)
	}

	dns := cfg["dns"].(map[string]any)
	// The address-family invariant OVERRIDES the stored strategy: a v4-only TUN
	// (addresses above) cannot carry IPv6, so the deliberately-divergent stored
	// prefer_ipv6 is pinned down to ipv4_only — otherwise sing-box would return
	// AAAA the app prefers and the TUN black-holes.
	if dns["strategy"] != "ipv4_only" {
		t.Fatalf("strategy = %v, want ipv4_only (ip_version=v4 pins the strategy)", dns["strategy"])
	}
	servers := dns["servers"].([]any)
	// Two user upstreams + the direct bootstrap mirror.
	if len(servers) != 3 {
		t.Fatalf("want 2 upstreams + bootstrap, got %v", servers)
	}
	primary := servers[0].(map[string]any)
	if primary["detour"] != SelectorTag {
		t.Fatalf("via_tunnel must detour the primary through the selector: %v", primary)
	}
	bootstrap := servers[2].(map[string]any)
	if bootstrap["tag"] != "dns-bootstrap" || bootstrap["server"] != "1.1.1.1" {
		t.Fatalf("bootstrap must mirror the primary upstream: %v", bootstrap)
	}
	if _, hasDetour := bootstrap["detour"]; hasDetour {
		t.Fatal("the bootstrap resolver must dial direct (no detour)")
	}
	route := cfg["route"].(map[string]any)
	if route["default_domain_resolver"] != "dns-bootstrap" {
		t.Fatalf("via_tunnel must resolve outbound domains via the bootstrap, got %v",
			route["default_domain_resolver"])
	}
	if cfg["log"].(map[string]any)["level"] != "debug" {
		t.Fatal("log level not applied")
	}
}

// TestDNSStrategyInvariant locks the address-family invariant: a single-family
// ip_version pins the EFFECTIVE strategy to the matching *_only regardless of
// the stored preference (a prefer_* would still emit the other family's records
// the TUN can't carry), while ip_version=both honors the stored value.
func TestDNSStrategyInvariant(t *testing.T) {
	cases := []struct {
		stored, ipVersion, want string
	}{
		{"prefer_ipv4", "v4", "ipv4_only"},
		{"prefer_ipv6", "v4", "ipv4_only"}, // the reported break: v4 TUN + prefer_ipv6
		{"ipv6_only", "v4", "ipv4_only"},   // even an explicit ipv6_only is nonsense on a v4 TUN
		{"prefer_ipv4", "v6", "ipv6_only"},
		{"prefer_ipv6", "v6", "ipv6_only"},
		{"prefer_ipv4", "both", "prefer_ipv4"},
		{"prefer_ipv6", "both", "prefer_ipv6"},
		{"ipv4_only", "both", "ipv4_only"},
		{"", "both", "prefer_ipv4"}, // empty/unknown → default
		{"garbage", "both", "prefer_ipv4"},
	}
	for _, c := range cases {
		if got := dnsStrategy(c.stored, c.ipVersion); got != c.want {
			t.Errorf("dnsStrategy(%q, %q) = %q, want %q", c.stored, c.ipVersion, got, c.want)
		}
	}
}

func TestPlanTUNLanBypassDisabled(t *testing.T) {
	plan := tunablesPlan()
	tunables := DefaultTunables()
	tunables.LanBypass = []string{} // explicit empty = bypass disabled
	plan.Tunables = &tunables
	sbCfg, _, err := PlanTUNConfigs(plan, "tun-test")
	if err != nil {
		t.Fatalf("PlanTUNConfigs: %v", err)
	}
	inbound := tunInbound(t, decodeCfg(t, sbCfg))
	got, present := inbound["route_exclude_address"]
	if runtime.GOOS == "linux" {
		// On Linux the fwmark exempts the daemon's own traffic, so a disabled
		// LAN bypass leaves route_exclude_address out entirely.
		if present {
			t.Fatalf("disabled bypass must omit route_exclude_address entirely, got %v", got)
		}
		return
	}
	// Off Linux the server-IP exemption (the fwmark stand-in) is required for the
	// data plane and is kept even when LAN bypass is disabled — it is not a user
	// knob. Only the LAN entries drop out, leaving just the server IP.
	excludes, _ := got.([]any)
	if len(excludes) != 1 || excludes[0] != "203.0.113.1/32" {
		t.Fatalf("off Linux, disabled LAN bypass must still keep the server exemption, got %v", got)
	}
}

func TestPlanSocksUsesTunables(t *testing.T) {
	plan := tunablesPlan()
	plan.Tunables = &Tunables{
		LogLevel:    "error",
		IPVersion:   "v6",
		DNSStrategy: "ipv6_only",
		DNS:         []DNSUpstream{{Type: "tls", Address: "2606:4700:4700::1111"}},
		LanBypass:   []string{},
		TunMTU:      1500, TunStack: "mixed", StrictRoute: true,
	}
	sbCfg, _, err := PlanSocksConfigs(plan, "127.0.0.1", freePort(t))
	if err != nil {
		t.Fatalf("PlanSocksConfigs: %v", err)
	}
	cfg := decodeCfg(t, sbCfg)
	if cfg["log"].(map[string]any)["level"] != "error" {
		t.Fatal("log level not applied to the socks plan")
	}
	if cfg["dns"].(map[string]any)["strategy"] != "ipv6_only" {
		t.Fatal("dns strategy must be applied to the socks plan")
	}
}
