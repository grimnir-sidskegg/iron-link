// Routing-compilation verification (no root, no tag: the fixture members are
// xray-routed, so no native uTLS is involved). The real cores accept the
// compiled route block; the shape assertions lock target resolution, null
// stripping, logical nesting, and the rule-set references.
package engine

import (
	"encoding/json"
	"strings"
	"testing"

	ilproxy "ironlink/daemon/internal/proxy"
	"ironlink/daemon/internal/routing"
)

// xrayOnlyPlan: one xhttp member (xray-routed) so tests run untagged. The
// member tag is the node UUID, so ActiveTag is the id (not the display name).
func xrayOnlyPlan() SessionPlan {
	node := namedNode("node-x", "203.0.113.9", testReality(),
		ilproxy.Transport{Kind: ilproxy.TransportXhttp, Xhttp: &ilproxy.XhttpParams{}})
	node.ID = "9e000000-0000-4000-8000-000000000009"
	return SessionPlan{XrayNodes: []NamedNode{node}, ActiveTag: node.ID}
}

// realShapedRouting mirrors the live "basic" config: legacy null conditions,
// a 1.12 condition, a Node target, a remote rule set, Direct default.
func realShapedRouting(t *testing.T) *routing.Config {
	t.Helper()
	src := `{
	  "id": "03c9d0ff-c200-4990-8edf-621572573db6",
	  "name": "basic",
	  "rule_sets": [
	    {"tag": "refilter", "url": "https://example.com/r.srs", "format": "binary", "download_detour": "proxy"}
	  ],
	  "rules": [
	    {"target": "DefaultProxy", "process_name": ["chrome"], "domain": null,
	     "domain_keyword": null, "port": null, "rule_set": null},
	    {"target": {"Node": "9e000000-0000-4000-8000-000000000009"}, "process_name": null,
	     "domain": null, "domain_keyword": ["example.com"], "port": null, "rule_set": null},
	    {"target": "Block", "process_name": null, "domain": null, "domain_keyword": null,
	     "port": null, "rule_set": ["refilter"], "ip_cidr": ["198.51.100.20"]}
	  ],
	  "default_target": "Direct"
	}`
	var c routing.Config
	if err := json.Unmarshal([]byte(src), &c); err != nil {
		t.Fatal(err)
	}
	return &c
}

func TestRouteCompileRealShape(t *testing.T) {
	p := xrayOnlyPlan()
	p.Routing = realShapedRouting(t)

	sbCfg, _, err := PlanSocksConfigs(p, "127.0.0.1", 1080)
	if err != nil {
		t.Fatalf("PlanSocksConfigs: %v", err)
	}
	var cfg any
	if err := json.Unmarshal(sbCfg, &cfg); err != nil {
		t.Fatal(err)
	}

	if got := dig(t, cfg, "route", "final"); got != "direct" {
		t.Errorf("final = %v, want direct (the routing's default target)", got)
	}
	rules := dig(t, cfg, "route", "rules").([]any)
	if len(rules) != 4 { // sniff + 3 user rules (no hijack-dns in socks mode)
		t.Fatalf("rules = %d, want 4: %v", len(rules), rules)
	}
	if got := dig(t, rules[0], "action"); got != "sniff" {
		t.Errorf("system sniff rule must come first, got %v", rules[0])
	}

	proc := rules[1].(map[string]any)
	if got := proc["outbound"]; got != SelectorTag {
		t.Errorf("DefaultProxy target → %v, want %q", got, SelectorTag)
	}
	if _, hasNull := proc["domain"]; hasNull {
		t.Error("null stored conditions must be stripped from the compiled rule")
	}

	if got := dig(t, rules[2], "outbound"); got != "9e000000-0000-4000-8000-000000000009" {
		t.Errorf("Node target must resolve to the member's UUID tag, got %v", got)
	}
	if got := dig(t, rules[3], "outbound"); got != "block" {
		t.Errorf("Block target → %v, want block", got)
	}
	if got := dig(t, rules[3], "ip_cidr", 0); got != "198.51.100.20" {
		t.Errorf("1.12 condition must pass through: %v", got)
	}

	if got := dig(t, cfg, "route", "rule_set", 0, "type"); got != "remote" {
		t.Errorf("rule_set type = %v", got)
	}
	if got := dig(t, cfg, "route", "rule_set", 0, "http_client", "detour"); got != "proxy" {
		t.Errorf("http_client.detour = %v", got)
	}

	// The block outbound exists as the Block target's sink.
	if !strings.Contains(string(sbCfg), `"type":"block"`) {
		t.Error("plan outbounds must include the block outbound")
	}
}

// TestRouteCompileStarts: the compiled route block is ACCEPTED BY THE REAL
// CORE — Start a session with rules (no remote rule_set: nothing fetches in
// tests).
func TestRouteCompileStarts(t *testing.T) {
	p := xrayOnlyPlan()
	p.Routing = realShapedRouting(t)
	p.Routing.RuleSets = nil
	// Drop the rule referencing the remote rule_set ("refilter").
	p.Routing.Rules = p.Routing.Rules[:2]

	sbCfg, xrayCfg, err := PlanSocksConfigs(p, "127.0.0.1", freePort(t))
	if err != nil {
		t.Fatal(err)
	}
	sess, err := Start(sbCfg, xrayCfg)
	if err != nil {
		t.Fatalf("the real core rejected the compiled route: %v\n%s", err, sbCfg)
	}
	sess.Close()
}

// TestTUNRouteImmutableOrder locks the immutable system-rule ORDER of a TUN
// plan: sniff → dns-hijack → private-direct → (user rules) → final. The
// dns-hijack MUST precede private-direct so that DNS aimed at a LAN resolver
// (e.g. 192.168.1.1:53) is captured by the hijack first and never falls through
// to the direct route — the whole point of moving RFC1918 out of the editable
// route_exclude set into an in-TUN direct rule.
func TestTUNRouteImmutableOrder(t *testing.T) {
	plan := tunablesPlan()
	// Attach a user rule so we also prove the immutable rules come BEFORE it.
	plan.Routing = &routing.Config{
		Name:          "u",
		DefaultTarget: routing.RuleTarget{Kind: routing.TargetDefaultProxy},
		Rules: []routing.Rule{{
			Target:     routing.RuleTarget{Kind: routing.TargetBlock},
			Conditions: map[string]json.RawMessage{"domain": json.RawMessage(`["ads.example.com"]`)},
		}},
	}
	sbCfg, _, err := PlanTUNConfigs(plan, "tun-test")
	if err != nil {
		t.Fatalf("PlanTUNConfigs: %v", err)
	}
	rules := dig(t, decodeCfg(t, sbCfg), "route", "rules").([]any)
	if len(rules) < 4 {
		t.Fatalf("TUN route must carry sniff + dns-hijack + private-direct + user rule, got %d: %v", len(rules), rules)
	}

	hijackIdx, privateIdx, userIdx := -1, -1, -1
	for i, r := range rules {
		m := r.(map[string]any)
		if m["action"] == "hijack-dns" {
			hijackIdx = i
		}
		if m["outbound"] == "direct" {
			if _, hasCIDR := m["ip_cidr"]; hasCIDR {
				privateIdx = i
			}
		}
		if m["outbound"] == "block" {
			userIdx = i
		}
	}
	if hijackIdx < 0 || privateIdx < 0 || userIdx < 0 {
		t.Fatalf("missing a rule: hijack=%d private=%d user=%d in %v", hijackIdx, privateIdx, userIdx, rules)
	}
	if !(rules[0].(map[string]any)["action"] == "sniff") {
		t.Errorf("sniff must be the first rule, got %v", rules[0])
	}
	if !(hijackIdx < privateIdx) {
		t.Errorf("dns-hijack (%d) MUST precede private-direct (%d) so LAN-resolver DNS is hijacked, not routed direct", hijackIdx, privateIdx)
	}
	if !(privateIdx < userIdx) {
		t.Errorf("private-direct (%d) must precede user rules (%d)", privateIdx, userIdx)
	}

	// The private-direct rule steers exactly the RFC1918 IPv4 set to direct.
	pd := rules[privateIdx].(map[string]any)
	got := pd["ip_cidr"].([]any)
	want := []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"}
	if len(got) != len(want) {
		t.Fatalf("private-direct ip_cidr = %v, want %v", got, want)
	}
	for i := range want {
		if got[i].(string) != want[i] {
			t.Errorf("private-direct ip_cidr[%d] = %v, want %s", i, got[i], want[i])
		}
	}
}

// TestSocksHasNoDNSHijackOrPrivateDirect: outside the TUN the daemon does not
// own DNS and there is no route_exclude to compensate for, so neither the
// dns-hijack nor the private-direct immutable rule is emitted.
func TestSocksHasNoDNSHijackOrPrivateDirect(t *testing.T) {
	sbCfg, _, err := PlanSocksConfigs(xrayOnlyPlan(), "127.0.0.1", 1080)
	if err != nil {
		t.Fatalf("PlanSocksConfigs: %v", err)
	}
	rules := dig(t, decodeCfg(t, sbCfg), "route", "rules").([]any)
	for _, r := range rules {
		m := r.(map[string]any)
		if m["action"] == "hijack-dns" {
			t.Errorf("socks mode must not emit dns-hijack: %v", rules)
		}
		if _, hasCIDR := m["ip_cidr"]; hasCIDR && m["outbound"] == "direct" {
			t.Errorf("socks mode must not emit the private-direct rule: %v", rules)
		}
	}
}

func TestRouteCompileLogicalRule(t *testing.T) {
	var rule routing.Rule
	src := `{
	  "target": "Block", "type": "logical", "mode": "and", "invert": true,
	  "rules": [
	    {"target": "Direct", "domain_suffix": [".ads.example.com"]},
	    {"target": "Direct", "network": ["udp"]}
	  ]
	}`
	if err := json.Unmarshal([]byte(src), &rule); err != nil {
		t.Fatal(err)
	}
	p := xrayOnlyPlan()
	compiled, err := p.compileRule(rule)
	if err != nil {
		t.Fatal(err)
	}
	if compiled["outbound"] != "block" {
		t.Errorf("logical rule target: %v", compiled["outbound"])
	}
	subs := compiled["rules"].([]any)
	if len(subs) != 2 {
		t.Fatalf("nested rules = %d", len(subs))
	}
	for _, sub := range subs {
		m := sub.(map[string]any)
		if _, has := m["outbound"]; has {
			t.Error("nested sub-rules must carry NO outbound (conditions only)")
		}
		if _, has := m["action"]; has {
			t.Error("nested sub-rules must carry NO action")
		}
	}
}

func TestRouteCompileErrors(t *testing.T) {
	p := xrayOnlyPlan()

	// DpiBypass: not yet available.
	p.Routing = &routing.Config{Name: "dpi", DefaultTarget: routing.RuleTarget{Kind: routing.TargetDpiBypass}}
	if _, _, err := PlanSocksConfigs(p, "127.0.0.1", 1080); err == nil || !strings.Contains(err.Error(), "byedpi") {
		t.Errorf("DpiBypass must be rejected for now: %v", err)
	}

	// Node target outside the embedded member set.
	p.Routing = &routing.Config{
		Name:          "ghost",
		DefaultTarget: routing.RuleTarget{Kind: routing.TargetDirect},
		Rules: []routing.Rule{{
			Target:     routing.RuleTarget{Kind: routing.TargetNode, Node: "not-embedded"},
			Conditions: map[string]json.RawMessage{"domain": json.RawMessage(`["x.com"]`)},
		}},
	}
	if _, _, err := PlanSocksConfigs(p, "127.0.0.1", 1080); err == nil || !strings.Contains(err.Error(), "not embedded") {
		t.Errorf("non-member node target must be rejected: %v", err)
	}

	// An action as the default target is meaningless.
	p.Routing = &routing.Config{Name: "act", DefaultTarget: routing.RuleTarget{Kind: routing.TargetHijackDns}}
	if _, _, err := PlanSocksConfigs(p, "127.0.0.1", 1080); err == nil || !strings.Contains(err.Error(), "action") {
		t.Errorf("action default target must be rejected: %v", err)
	}
}

// TestRouteTargetGroup: a routing rule can target an active group node (its
// urltest tag resolves like any member).
func TestRouteTargetGroup(t *testing.T) {
	p := xrayOnlyPlan() // one xray member "9e00...0009"
	p.Group = &GroupPlan{ID: "grp-1", Name: "Auto", Members: []string{p.XrayNodes[0].ID}}
	tag, err := p.resolveTarget(routing.RuleTarget{Kind: routing.TargetNode, Node: "grp-1"})
	if err != nil || tag != "grp-1" {
		t.Errorf("group node target = %q, %v; want grp-1, nil", tag, err)
	}
}

// TestRouteRuleSetDecodes keeps the remote rule set in the compiled route and
// hands it to the pinned core: sing-box 1.14 replaced the rule-set
// `download_detour` key with an inline `http_client`, and only the core's own
// decoder can confirm the shape we emit (New builds the rule set without
// fetching it, so no network is touched).
func TestRouteRuleSetDecodes(t *testing.T) {
	p := xrayOnlyPlan()
	p.Routing = realShapedRouting(t)
	sbCfg, xrayCfg, err := PlanSocksConfigs(p, "127.0.0.1", freePort(t))
	if err != nil {
		t.Fatal(err)
	}
	xinst, err := BuildXray(xrayCfg)
	if err != nil {
		t.Fatal(err)
	}
	backends := []Backend{newXrayBackend(xinst)}
	defer closeBackends(backends)
	b, err := BuildBox(sbCfg, backends, nil)
	if err != nil {
		t.Fatalf("the real core rejected the compiled rule set: %v\n%s", err, sbCfg)
	}
	b.Close()
}

// TestTUNPlanDecodes hands the compiled TUN plan — the shape the daemon runs
// as root — to the pinned core's decoder without starting it (opening the TUN
// needs root; New only constructs). Deprecation notes surface here at decode
// time, so a core bump that deprecates a TUN/DNS key we emit fails loudly in
// the log of this test rather than on every daemon start.
func TestTUNPlanDecodes(t *testing.T) {
	p := tunablesPlan()
	p.Routing = realShapedRouting(t)
	// The tunables plan embeds no extra node; drop the Node-target rule.
	p.Routing.Rules = append(p.Routing.Rules[:1], p.Routing.Rules[2:]...)
	sbCfg, xrayCfg, err := PlanTUNConfigs(p, "tun-test")
	if err != nil {
		t.Fatal(err)
	}
	xinst, err := BuildXray(xrayCfg)
	if err != nil {
		t.Fatal(err)
	}
	backends := []Backend{newXrayBackend(xinst)}
	defer closeBackends(backends)
	b, err := BuildBox(sbCfg, backends, nil)
	if err != nil {
		t.Fatalf("the real core rejected the compiled TUN plan: %v\n%s", err, sbCfg)
	}
	b.Close()
}
