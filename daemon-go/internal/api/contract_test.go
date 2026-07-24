// The Go half of the cross-language wire-contract test (GO_DAEMON_PLAN.md §10).
//
// The canonical frames live in contract/fixtures/ at the repo root; the Dart
// half is client-flutter/test/contract_test.dart. Each side asserts the
// direction it actually speaks on the wire:
//
//	requests/      the client emits   -> Go MUST decode (asserted here)
//	responses/     Go daemon emits    -> emission pinned byte-for-byte here,
//	events/        decode pinned on the client sides
//
// Every fixture file must have a table entry below and vice versa — adding a
// verb/field without updating ALL languages' tables fails one of the suites.
package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

const fixtureRoot = "../../../contract/fixtures"

func ptr[T any](v T) *T { return &v }

// loadFixtures returns stem -> raw JSON for every *.json in the dir.
func loadFixtures(t *testing.T, rel string) map[string][]byte {
	t.Helper()
	dir := filepath.Join(fixtureRoot, rel)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read fixture dir %s: %v", dir, err)
	}
	out := make(map[string][]byte)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read fixture %s: %v", e.Name(), err)
		}
		out[strings.TrimSuffix(e.Name(), ".json")] = raw
	}
	return out
}

// assertCoverage fails unless the fixture set and the expectation table name
// exactly the same cases.
func assertCoverage[T any](t *testing.T, rel string, fixtures map[string][]byte, table map[string]T) {
	t.Helper()
	var missing, orphaned []string
	for name := range fixtures {
		if _, ok := table[name]; !ok {
			missing = append(missing, name)
		}
	}
	for name := range table {
		if _, ok := fixtures[name]; !ok {
			orphaned = append(orphaned, name)
		}
	}
	sort.Strings(missing)
	sort.Strings(orphaned)
	if len(missing) > 0 {
		t.Errorf("%s: fixtures with no expectation entry: %v", rel, missing)
	}
	if len(orphaned) > 0 {
		t.Errorf("%s: expectation entries with no fixture file: %v", rel, orphaned)
	}
}

// jsonValue re-parses raw JSON into the generic interface{} tree so two
// encodings compare key-order-insensitively.
func jsonValue(t *testing.T, raw []byte) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, raw)
	}
	return v
}

// requestsEqual compares Requests with RoutingConfig compared semantically
// (raw bytes keep the fixture's formatting, which is not part of the contract).
func requestsEqual(t *testing.T, a, b Request) bool {
	t.Helper()
	ac, bc := a.RoutingConfig, b.RoutingConfig
	a.RoutingConfig, b.RoutingConfig = nil, nil
	if !reflect.DeepEqual(a, b) {
		return false
	}
	if (ac == nil) != (bc == nil) {
		return false
	}
	if ac == nil {
		return true
	}
	return reflect.DeepEqual(jsonValue(t, ac), jsonValue(t, bc))
}

// sharedRequests is what the Dart client emits; its exact serializer output
// is pinned on the Dart side. The Go daemon must decode every one of these.
var sharedRequests = map[string]Request{
	"activate_full":      {Command: CmdActivate, Profile: ptr("home"), Node: ptr("tokyo"), Routing: ptr("split"), Tun: ptr(true)},
	"activate_defaults":  {Command: CmdActivate, Tun: ptr(false)},
	"stop_role":          {Command: CmdStop, Role: ptr(RoleTun)},
	"stop_all":           {Command: CmdStop},
	"status":             {Command: CmdStatus},
	"switch_node":        {Command: CmdSwitchNode, Node: ptr("osaka")},
	"test_latency_nodes": {Command: CmdTestLatency, Nodes: ptr([]string{"tokyo", "osaka"})},
	"test_latency_all":   {Command: CmdTestLatency},
	"subscribe":          {Command: CmdSubscribe},

	// The store/diagnose/settings verbs (promoted from go/** at P0).
	"list_profiles":             {Command: CmdListProfiles},
	"create_profile":            {Command: CmdCreateProfile, Profile: ptr("work")},
	"delete_profile":            {Command: CmdDeleteProfile, Profile: ptr("old")},
	"set_active_profile":        {Command: CmdSetActiveProfile, Profile: ptr("main")},
	"list_nodes":                {Command: CmdListNodes, Profile: ptr("main")},
	"select_node":               {Command: CmdSelectNode, Node: ptr("3f2a")},
	"add_node":                  {Command: CmdAddNode, URL: ptr("vless://00000000-0000-0000-0000-000000000000@example.com:443?security=reality&sni=cdn.example.com&pbk=KEY&fp=chrome&type=tcp#Grimnir")},
	"remove_node":               {Command: CmdRemoveNode, Node: ptr("3f2a")},
	"set_node_prefs_pin":        {Command: CmdSetNodePrefs, Node: ptr("3f2a"), CoreOverride: ptr(CoreXray)},
	"set_node_prefs_clear":      {Command: CmdSetNodePrefs, Node: ptr("3f2a")},
	"list_subscriptions":        {Command: CmdListSubs},
	"add_subscription":          {Command: CmdAddSub, URL: ptr("https://example.com/sub"), Name: ptr("main"), AllowInvalidCerts: ptr(true)},
	"refresh_subscriptions_all": {Command: CmdRefreshSubs},
	"refresh_subscriptions_one": {Command: CmdRefreshSubs, Sub: ptr("main")},
	"remove_subscription":       {Command: CmdRemoveSub, Sub: ptr("main")},
	"update_subscription":       {Command: CmdUpdateSub, Sub: ptr("main"), Name: ptr("Main (EU)"), URL: ptr("https://example.com/sub2"), Enabled: ptr(false), AllowInvalidCerts: ptr(true), UpdateIntervalSec: ptr(uint32(43200))},
	"list_routing":              {Command: CmdListRouting},
	"select_routing":            {Command: CmdSelectRouting, Routing: ptr("basic")},
	"get_routing":               {Command: CmdGetRouting, Routing: ptr("r1")},
	"upsert_routing":            {Command: CmdUpsertRouting, RoutingConfig: json.RawMessage(`{"id":"r1","name":"basic","rule_sets":[],"rules":[],"default_target":"DefaultProxy"}`)},
	"remove_routing":            {Command: CmdRemoveRouting, Routing: ptr("r1")},
	"routing_schema":            {Command: CmdRoutingSchema},
	"diagnose":                  {Command: CmdDiagnose, Node: ptr("tokyo")},
	"traffic_apps":              {Command: CmdTrafficApps},
	"doctor":                    {Command: CmdDoctor},
	"doctor_nodes":              {Command: CmdDoctorNodes},
	"network_report":            {Command: CmdNetworkReport},
	"forwarding_check":          {Command: CmdForwardingCheck},
	"get_settings":              {Command: CmdGetSettings},
	"set_settings": {Command: CmdSetSettings, Settings: &Settings{
		LogLevel:  "warn",
		IPVersion: "v4",
		DNS: DNSSettings{
			Servers:   []DNSServer{{Type: "udp", Address: "1.1.1.1"}},
			Strategy:  "ipv4_only",
			ViaTunnel: true,
		},
		LanBypass:             LanBypass{Enabled: true, CIDRs: []string{"10.0.0.0/8", "192.168.0.0/16"}},
		RestoreOnStart:        false,
		SubscriptionUserAgent: "clash-verge/1.7",
		SocksPort:             1080,
		Tun:                   TunSettings{MTU: 9000, Stack: "system", StrictRoute: false},
		LatencyProbe:          ProbeSettings{URL: "https://cp.cloudflare.com/generate_204", BudgetSecs: 20},
	}},
}

// defaultSettingsDoc mirrors store.DefaultSettings — what a fresh daemon
// returns from get_settings (the store package owns the real constructor;
// this literal is the CONTRACT pin of the default document's shape). It pins
// the non-Windows default: RestoreOnStart defaults OFF on Windows, but the
// wire SHAPE is identical, so the fixture (and this test, host-run) stays ON.
func defaultSettingsDoc() *Settings {
	return &Settings{
		LogLevel:  "info",
		IPVersion: "both",
		DNS:       DNSSettings{Strategy: "prefer_ipv4", Servers: []DNSServer{{Type: "tls", Address: "8.8.8.8"}}},
		LanBypass: LanBypass{Enabled: true, CIDRs: []string{
			"169.254.0.0/16", "fc00::/7", "fe80::/10",
		}},
		RestoreOnStart:        true,
		SubscriptionUserAgent: "v2rayN/6.23",
		SocksPort:             10808,
		Tun:                   TunSettings{MTU: 1500, Stack: "mixed", StrictRoute: true},
		LatencyProbe:          ProbeSettings{URL: "https://www.gstatic.com/generate_204", BudgetSecs: 45},
	}
}

// routingSchemaLinuxDoc is the CONTRACT pin of the routing_schema reply for
// goos=linux — an explicit literal, NOT routing.SchemaFor(runtime.GOOS):
// platform-independent (the windows/darwin CI runners pin the same bytes) and
// no internal/routing import (this file is package api; routing imports api,
// so that would be an import cycle). The SchemaFor("linux")-matches-the-
// fixture cross-check lives in internal/routing/schema_test.go.
func routingSchemaLinuxDoc() *RoutingSchema {
	c := func(key, kind, hint string, supported bool) ConditionSpec {
		return ConditionSpec{Key: key, Kind: kind, Hint: hint, Supported: supported}
	}
	return &RoutingSchema{
		Conditions: []ConditionSpec{
			c("inbound", "strings", "inbound listener tags", true),
			c("ip_version", "number", "4 or 6", true),
			c("network", "strings", "tcp / udp", true),
			c("auth_user", "strings", "SOCKS auth users", true),
			c("protocol", "strings", "sniffed protocol: tls / http / quic / dns / ssh / …", true),
			c("client", "strings", "connection client type", true),
			c("domain", "strings", "exact domains", true),
			c("domain_suffix", "strings", "e.g. .youtube.com", true),
			c("domain_keyword", "strings", "substrings, e.g. google", true),
			c("domain_regex", "strings", "regular expressions", true),
			c("source_ip_cidr", "strings", "source CIDRs, e.g. 10.0.0.0/8", true),
			c("source_ip_is_private", "bool", "source address is non-public", true),
			c("ip_cidr", "strings", "destination CIDRs", true),
			c("ip_is_private", "bool", "destination address is non-public", true),
			c("source_port", "ports", "source ports, e.g. 12345", true),
			c("source_port_range", "strings", "ranges, e.g. 1000:2000", true),
			c("port", "ports", "destination ports, e.g. 80 443", true),
			c("port_range", "strings", "ranges, e.g. 8000:8080", true),
			c("clash_mode", "string", "rule/global/direct mode tag", true),
			c("rule_set", "strings", "rule-set tags defined in this config", true),
			c("rule_set_ip_cidr_match_source", "bool", "match rule-set IPs against the SOURCE address", true),
			c("invert", "bool", "negate this rule's conditions", true),
			c("process_name", "strings", "process executable name, e.g. firefox", true),
			c("process_path", "strings", "full executable path", true),
			c("process_path_regex", "strings", "path regular expressions", true),
			c("user", "strings", "OS user name", true),
			c("user_id", "numbers", "OS numeric uid", true),
			c("package_name", "strings", "Android package, e.g. org.mozilla.firefox", false),
			c("wifi_ssid", "strings", "current Wi-Fi SSID", false),
			c("wifi_bssid", "strings", "current Wi-Fi BSSID", false),
			c("network_type", "strings", "wifi / cellular / ethernet / other", false),
			c("network_is_expensive", "bool", "metered/expensive network", false),
			c("network_is_constrained", "bool", "constrained network (iOS)", false),
		},
		Targets:        []string{"DefaultProxy", "Direct", "Block", "Node"},
		RuleSetFormats: []string{"binary", "source"},
	}
}

// sharedResponses is what the Go daemon emits. The fixture must equal
// json.Marshal of the value EXACTLY (modulo key order) — these bytes are
// what the Dart client will parse.
var sharedResponses = map[string]Response{
	"started":      {Status: StatusStarted, Message: "cores started", Role: ptr(RoleTun)},
	"stopped_bare": {Status: StatusStopped},
	"running_full": {
		Status: StatusRunning,
		Entries: []CoreEntry{
			{Role: RoleTun, State: StateRunning, UptimeSecs: 42},
			{Role: RoleProxy, State: StateRunning, UptimeSecs: 42},
		},
		Active:         &PersistedEntry{Profile: ptr("main"), Node: ptr("Grimnir [VLESS - tcp]"), Routing: ptr("basic")},
		ActiveNodeLive: ptr("Grimnir [VLESS - tcp]"),
	},
	"running_no_context": {
		Status:  StatusRunning,
		Entries: []CoreEntry{{Role: RoleProxy, State: StateRunning, UptimeSecs: 5}},
	},
	"activated": {
		Status: StatusActivated,
		Entries: []CoreEntry{
			{Role: RoleTun, State: StateRunning, UptimeSecs: 0},
			{Role: RoleProxy, State: StateRunning, UptimeSecs: 0},
		},
	},
	"idle":     {Status: StatusIdle},
	"switched": {Status: StatusSwitched, Node: "osaka"},
	"latencies": {
		Status: StatusLatencies,
		Latencies: []LatencyResult{
			{Node: "Grimnir [VLESS - tcp]", LatencyMs: ptr(uint16(58))},
			{Node: "osaka"},
		},
	},
	"ok":          {Status: StatusOk, Message: "removed"},
	"ok_add_node": {Status: StatusOk, Message: "node added", Node: "node-3f2a"},
	"error":       {Status: StatusError, Message: "no such node: osaka"},

	"profiles": {Status: StatusProfiles, Profiles: []string{"main", "work"}, ActiveProfile: ptr("main")},
	"nodes": {Status: StatusNodes, Nodes: []NodeInfo{
		{ID: "3f2a", Name: "Grimnir [VLESS - tcp]", SubID: ptr("s1"), CoreOverride: ptr(CoreXray), Protocol: "vless", Transport: "tcp", Security: "reality", Active: true, EligibleCores: []CoreType{CoreSingBox, CoreXray}},
		{ID: "9b1c", Name: "osaka", Protocol: "vless", Transport: "xhttp", Security: "reality", Active: false, EligibleCores: []CoreType{CoreXray}},
	}},
	"subscriptions": {Status: StatusSubscriptions, Subscriptions: []SubscriptionInfo{
		{ID: "s1", Name: "main", URL: "https://example.com/sub", Enabled: true, AllowInvalidCerts: false, LastUpdated: "2026-06-10T12:00:00Z", NodeCount: 42},
	}},
	"refreshed": {Status: StatusRefreshed, Refreshed: []RefreshInfo{
		{Name: "main", Count: 42, Added: 2, Removed: 1},
		{Name: "backup", Count: 17, Skipped: true},
		{Name: "dead", Count: 0, Error: "fetch: status 502"},
	}},
	"routing": {Status: StatusRouting, Routing: []RoutingInfo{
		{ID: "r1", Name: "basic", Rules: 4, RuleSets: 2, Active: true},
		{ID: "r2", Name: "(default)", Rules: 0, RuleSets: 0, Active: false},
	}},
	// The RawMessage must byte-match the fixture's payload (the round-trip
	// check compares raw bytes; the daemon emits whatever json.Marshal of the
	// stored config produced).
	"routing_config": {Status: StatusRoutingConfig, RoutingConfig: json.RawMessage(`{"id": "r1", "name": "basic", "rule_sets": [], "rules": [{"target": "DefaultProxy", "domain_keyword": ["example.com"]}], "default_target": "Direct"}`)},
	"routing_schema": {Status: StatusRoutingSchema, RoutingSchema: routingSchemaLinuxDoc()},
	"diagnosis": {Status: StatusDiagnosis, Diagnosis: &DiagnosisInfo{
		Node: "tokyo", OK: false, FailedStage: "tls",
		Stages: []StageInfo{
			{Stage: "tcp", OK: true, DurationMs: 23},
			{Stage: "tls", OK: false, DurationMs: 1500, Error: "handshake timeout"},
		},
	}},
	"app_traffic": {Status: StatusAppTraffic, Apps: []AppTraffic{
		{Path: "/usr/bin/firefox", Up: 524288, Down: 8388608, ByRoute: map[string]RouteBytes{
			"direct": {Up: 32768, Down: 262144},
			"proxy":  {Up: 491520, Down: 8126464},
		}},
		{Path: "/usr/lib/telegram/telegram", Up: 16384, Down: 131072, ByRoute: map[string]RouteBytes{
			"proxy": {Up: 16384, Down: 131072},
		}},
	}},
	"doctor_report": {Status: StatusDoctorReport, Checks: []DoctorCheck{
		{
			ID:      "internet",
			Title:   "Internet connectivity",
			Status:  "warn",
			Summary: "Online over IPv6 only.",
			Details: []string{
				"IPv4: unreachable (dial tcp4 1.1.1.1:443: connect: network is unreachable)",
				"IPv6: reachable",
				"DNS resolver: answers A and AAAA",
				`DNS strategy is "prefer_ipv4" but only IPv6 is reachable.`,
			},
			Remedy: &DoctorRemedy{Summary: "Switch DNS strategy to prefer IPv6", DNSStrategy: "prefer_ipv6"},
		},
		{
			ID:      "resolvers",
			Title:   "DNS resolvers",
			Status:  "warn",
			Summary: "Resolver DoT 8.8.8.8 is unreachable.",
			Details: []string{
				"DoT 8.8.8.8: blocked (dial tcp 8.8.8.8:853: i/o timeout)",
				"DoT 1.1.1.1: reachable — verified cloudflare-dns.com",
			},
			Remedy: &DoctorRemedy{
				Summary: "Make 1.1.1.1 the primary resolver",
				DNSServers: []DNSServer{
					{Type: "tls", Address: "1.1.1.1"},
					{Type: "tls", Address: "8.8.8.8"},
				},
			},
		},
	}},
	"settings": {Status: StatusSettings, Settings: defaultSettingsDoc()},
	"settings_needs_reactivation": {Status: StatusSettings, Settings: func() *Settings {
		s := defaultSettingsDoc()
		s.SocksPort = 1080
		return s
	}(), NeedsReactivation: true},
}

var sharedEvents = map[string]Event{
	"subscription_updated":      {Event: EventSubscriptionUpdated, SubID: "s1", Added: 3, Removed: 1, Total: 42},
	"subscription_updated_zero": {Event: EventSubscriptionUpdated, SubID: "s1", Total: 42},
	"core_error":                {Event: EventCoreError, Role: ptr(RoleProxy), Stage: "start", Message: "xray: invalid config"},

	"state_idle": {Event: EventState},
	"state_running": {
		Event: EventState,
		Entries: []CoreEntry{
			{Role: RoleTun, State: StateRunning, UptimeSecs: 0},
			{Role: RoleProxy, State: StateRunning, UptimeSecs: 0},
		},
		Active: &PersistedEntry{Profile: ptr("main"), Node: ptr("Grimnir [VLESS - tcp]"), Routing: ptr("basic")},
	},
	"traffic":      {Event: EventTraffic, Up: ptr(uint64(4096)), Down: ptr(uint64(1048576))},
	"traffic_zero": {Event: EventTraffic, Up: ptr(uint64(0)), Down: ptr(uint64(0))},
	"log":          {Event: EventLog, Level: "warning", Message: "outbound timeout"},
}

func testDecodeRequests(t *testing.T, rel string, table map[string]Request) {
	fixtures := loadFixtures(t, rel)
	assertCoverage(t, rel, fixtures, table)
	for name, raw := range fixtures {
		want, ok := table[name]
		if !ok {
			continue
		}
		var got Request
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Errorf("%s/%s: decode failed: %v", rel, name, err)
			continue
		}
		if !requestsEqual(t, got, want) {
			t.Errorf("%s/%s: decoded %+v, want %+v", rel, name, got, want)
		}
	}
}

func testEmitExact[T any](t *testing.T, rel string, table map[string]T) {
	fixtures := loadFixtures(t, rel)
	assertCoverage(t, rel, fixtures, table)
	for name, raw := range fixtures {
		want, ok := table[name]
		if !ok {
			continue
		}
		// The daemon's emission must be exactly the fixture bytes (modulo
		// key order) — this is what the other language's decoder is fed.
		emitted, err := json.Marshal(want)
		if err != nil {
			t.Errorf("%s/%s: marshal failed: %v", rel, name, err)
			continue
		}
		if !reflect.DeepEqual(jsonValue(t, emitted), jsonValue(t, raw)) {
			t.Errorf("%s/%s: emission drifted from fixture\nemitted: %s\nfixture: %s", rel, name, emitted, strings.TrimSpace(string(raw)))
		}
		// And the daemon must decode its own emission back to the same value
		// (guards against asymmetric tags like a missing json name).
		got := new(T)
		if err := json.Unmarshal(raw, got); err != nil {
			t.Errorf("%s/%s: decode failed: %v", rel, name, err)
			continue
		}
		if !reflect.DeepEqual(*got, want) {
			t.Errorf("%s/%s: round-trip drifted: %+v, want %+v", rel, name, *got, want)
		}
	}
}

func TestContractSharedRequests(t *testing.T) { testDecodeRequests(t, "requests", sharedRequests) }

func TestContractSharedResponses(t *testing.T) { testEmitExact(t, "responses", sharedResponses) }

func TestContractSharedEvents(t *testing.T) { testEmitExact(t, "events", sharedEvents) }
