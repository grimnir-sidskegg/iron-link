// The routing-rule condition-field schema (ROADMAP P0): the daemon serves
// this — computed from ITS pinned sing-box (the condition table is pinned
// against option.RawDefaultRule by the reflection test in schema_test.go) and
// ITS platform — so clients render routing forms from data instead of
// carrying typed models.
package routing

import (
	"slices"

	"ironlink/daemon/internal/api"
)

// Condition kinds (api.ConditionSpec.Kind) — the closed enum of input shapes.
const (
	kindStrings = "strings" // JSON array of strings
	kindPorts   = "ports"   // array of 1..65535 ints
	kindNumbers = "numbers" // array of ints
	kindBool    = "bool"
	kindString  = "string" // single string
	kindNumber  = "number" // single int
)

// conditionSpec is one row of the condition table. Platforms is the GOOS-style
// set the condition can match on; nil means every platform.
type conditionSpec struct {
	Key, Kind, Hint string
	Platforms       []string
}

var (
	desktopOS = []string{"linux", "darwin", "windows"}
	unixOS    = []string{"linux", "darwin"}
	wifiOS    = []string{"darwin", "ios", "android"}
	mobileOS  = []string{"android", "ios"}
	androidOS = []string{"android"}
)

// conditions is the sing-box 1.12 routing-rule condition table, in wire
// order. geosite / geoip / source_geoip are EXCLUDED on purpose: REMOVED in
// sing-box 1.12, they error at rule build.
var conditions = []conditionSpec{
	{Key: "inbound", Kind: kindStrings, Hint: "inbound listener tags"},
	{Key: "ip_version", Kind: kindNumber, Hint: "4 or 6"},
	{Key: "network", Kind: kindStrings, Hint: "tcp / udp"},
	{Key: "auth_user", Kind: kindStrings, Hint: "SOCKS auth users"},
	{Key: "protocol", Kind: kindStrings, Hint: "sniffed protocol: tls / http / quic / dns / ssh / …"},
	{Key: "client", Kind: kindStrings, Hint: "connection client type"},
	{Key: "domain", Kind: kindStrings, Hint: "exact domains"},
	{Key: "domain_suffix", Kind: kindStrings, Hint: "e.g. .youtube.com"},
	{Key: "domain_keyword", Kind: kindStrings, Hint: "substrings, e.g. google"},
	{Key: "domain_regex", Kind: kindStrings, Hint: "regular expressions"},
	{Key: "source_ip_cidr", Kind: kindStrings, Hint: "source CIDRs, e.g. 10.0.0.0/8"},
	{Key: "source_ip_is_private", Kind: kindBool, Hint: "source address is non-public"},
	{Key: "ip_cidr", Kind: kindStrings, Hint: "destination CIDRs"},
	{Key: "ip_is_private", Kind: kindBool, Hint: "destination address is non-public"},
	{Key: "source_port", Kind: kindPorts, Hint: "source ports, e.g. 12345"},
	{Key: "source_port_range", Kind: kindStrings, Hint: "ranges, e.g. 1000:2000"},
	{Key: "port", Kind: kindPorts, Hint: "destination ports, e.g. 80 443"},
	{Key: "port_range", Kind: kindStrings, Hint: "ranges, e.g. 8000:8080"},
	{Key: "clash_mode", Kind: kindString, Hint: "rule/global/direct mode tag"},
	{Key: "rule_set", Kind: kindStrings, Hint: "rule-set tags defined in this config"},
	{Key: "rule_set_ip_cidr_match_source", Kind: kindBool, Hint: "match rule-set IPs against the SOURCE address"},
	{Key: "invert", Kind: kindBool, Hint: "negate this rule's conditions"},

	{Key: "process_name", Kind: kindStrings, Hint: "process executable name, e.g. firefox", Platforms: desktopOS},
	{Key: "process_path", Kind: kindStrings, Hint: "full executable path", Platforms: desktopOS},
	{Key: "process_path_regex", Kind: kindStrings, Hint: "path regular expressions", Platforms: desktopOS},

	{Key: "user", Kind: kindStrings, Hint: "OS user name", Platforms: unixOS},
	{Key: "user_id", Kind: kindNumbers, Hint: "OS numeric uid", Platforms: unixOS},

	{Key: "package_name", Kind: kindStrings, Hint: "Android package, e.g. org.mozilla.firefox", Platforms: androidOS},
	{Key: "wifi_ssid", Kind: kindStrings, Hint: "current Wi-Fi SSID", Platforms: wifiOS},
	{Key: "wifi_bssid", Kind: kindStrings, Hint: "current Wi-Fi BSSID", Platforms: wifiOS},
	{Key: "network_type", Kind: kindStrings, Hint: "wifi / cellular / ethernet / other", Platforms: mobileOS},
	{Key: "network_is_expensive", Kind: kindBool, Hint: "metered/expensive network", Platforms: mobileOS},
	{Key: "network_is_constrained", Kind: kindBool, Hint: "constrained network (iOS)", Platforms: mobileOS},
}

// SchemaFor computes the routing schema for one platform (the daemon passes
// runtime.GOOS): the full condition table with `supported` = goos ∈ the
// condition's platform set. Pure data — no lock, no I/O.
//
// Targets: DpiBypass is deliberately excluded (the compiler rejects it
// today); the HijackDns/Sniff/Resolve actions are out of schema v1.
func SchemaFor(goos string) *api.RoutingSchema {
	out := &api.RoutingSchema{
		Conditions:     make([]api.ConditionSpec, 0, len(conditions)),
		Targets:        []string{"DefaultProxy", "Direct", "Block", "Node"},
		RuleSetFormats: []string{"binary", "source"},
	}
	for _, c := range conditions {
		out.Conditions = append(out.Conditions, api.ConditionSpec{
			Key:       c.Key,
			Kind:      c.Kind,
			Hint:      c.Hint,
			Supported: c.Platforms == nil || slices.Contains(c.Platforms, goos),
		})
	}
	return out
}
