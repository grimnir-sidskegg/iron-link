//go:build with_utls

// Construct + live verification of the multi-node session plan (no root, no
// real traffic): the compiled selector config starts with the real cores and
// the in-process live select actually moves the selector. Native REALITY
// outbounds need sing-box's uTLS (`-tags with_utls` — part of the canonical
// daemon tag set; xray brings its own uTLS unconditionally).
package engine

import (
	"strings"
	"testing"
	"time"

	ilproxy "ironlink/daemon/internal/proxy"
)

func twoNativePlan() SessionPlan {
	return SessionPlan{
		Natives: []NamedNode{
			namedNode("node-a", "203.0.113.1", testReality(), ilproxy.Transport{Kind: ilproxy.TransportTCP}),
			namedNode("node-b", "203.0.113.2",
				ilproxy.Security{Kind: ilproxy.SecurityTLS, TLS: &ilproxy.TLSParams{SNI: "cdn.example.com", Fp: "chrome"}},
				ilproxy.Transport{Kind: ilproxy.TransportWs, Ws: &ilproxy.WsParams{Path: "/ws", Host: "cdn.example.com"}}),
		},
		ActiveTag: "node-a",
	}
}

// TestPlanSocksLiveSelect starts a two-native-node session (real cores, SOCKS
// inbound, nothing dialed) and drives the in-process selector: the default is
// the active tag, SelectOutbound moves it, an unknown member is rejected.
func TestPlanSocksLiveSelect(t *testing.T) {
	sbCfg, xrayCfg, err := PlanSocksConfigs(twoNativePlan(), "127.0.0.1", freePort(t))
	if err != nil {
		t.Fatalf("PlanSocksConfigs: %v", err)
	}
	sess, err := Start(sbCfg, xrayCfg)
	if err != nil {
		t.Fatalf("Start (two native members): %v", err)
	}
	defer sess.Close()

	if now, ok := sess.SelectedOutbound(); !ok || now != "node-a" {
		t.Errorf("SelectedOutbound = %q, %v; want node-a, true", now, ok)
	}
	if err := sess.SelectOutbound("node-b"); err != nil {
		t.Fatalf("SelectOutbound(node-b): %v", err)
	}
	if now, _ := sess.SelectedOutbound(); now != "node-b" {
		t.Errorf("after select: SelectedOutbound = %q, want node-b", now)
	}
	if err := sess.SelectOutbound("nope"); err == nil || !strings.Contains(err.Error(), "not a member") {
		t.Errorf("SelectOutbound(nope) = %v, want 'not a member'", err)
	}
}

// TestPlanWithXrayMember: the active xray-routed node joins the selector as
// the xray-reality outbound under ITS OWN name, and live select onto it works.
func TestPlanWithXrayMember(t *testing.T) {
	p := twoNativePlan()
	xrayNode := namedNode("node-x", "203.0.113.3", testReality(),
		ilproxy.Transport{Kind: ilproxy.TransportXhttp, Xhttp: &ilproxy.XhttpParams{}})
	p.XrayNodes = []NamedNode{xrayNode}
	p.ActiveTag = "node-x"

	if !p.IsMember("node-x") || !p.IsMember("node-a") || p.IsMember("ghost") {
		t.Fatalf("IsMember wrong: %+v", p.memberTags())
	}

	sbCfg, xrayCfg, err := PlanSocksConfigs(p, "127.0.0.1", freePort(t))
	if err != nil {
		t.Fatalf("PlanSocksConfigs: %v", err)
	}
	sess, err := Start(sbCfg, xrayCfg)
	if err != nil {
		t.Fatalf("Start (natives + xray member): %v", err)
	}
	defer sess.Close()

	if now, _ := sess.SelectedOutbound(); now != "node-x" {
		t.Errorf("default member = %q, want node-x", now)
	}
	if err := sess.SelectOutbound("node-a"); err != nil {
		t.Fatalf("live select away from the xray member: %v", err)
	}
	if err := sess.SelectOutbound("node-x"); err != nil {
		t.Fatalf("live select back onto the xray member: %v", err)
	}
}

// TestPlanTwoXrayMembersLiveSelect is the M1 payoff: TWO xray-routed nodes are
// simultaneous members of the one xray instance and BOTH are live-switchable
// with no re-activation. The active xray node is XrayNodes[0] and is the
// selector default; a live select moves to the second xray member and back.
func TestPlanTwoXrayMembersLiveSelect(t *testing.T) {
	p := twoNativePlan()
	xa := namedNode("xray-a", "203.0.113.7", testReality(),
		ilproxy.Transport{Kind: ilproxy.TransportXhttp, Xhttp: &ilproxy.XhttpParams{}})
	xb := namedNode("xray-b", "203.0.113.8", testReality(),
		ilproxy.Transport{Kind: ilproxy.TransportXhttp, Xhttp: &ilproxy.XhttpParams{}})
	p.XrayNodes = []NamedNode{xa, xb}
	p.ActiveTag = "xray-a"

	for _, tag := range []string{"xray-a", "xray-b", "node-a"} {
		if !p.IsMember(tag) {
			t.Fatalf("IsMember(%q) = false; members = %v", tag, p.memberTags())
		}
	}

	sbCfg, xrayCfg, err := PlanSocksConfigs(p, "127.0.0.1", freePort(t))
	if err != nil {
		t.Fatalf("PlanSocksConfigs: %v", err)
	}
	sess, err := Start(sbCfg, xrayCfg)
	if err != nil {
		t.Fatalf("Start (two xray members): %v", err)
	}
	defer sess.Close()

	if now, _ := sess.SelectedOutbound(); now != "xray-a" {
		t.Errorf("default member = %q, want xray-a", now)
	}
	if err := sess.SelectOutbound("xray-b"); err != nil {
		t.Fatalf("live select to the second xray member: %v", err)
	}
	if now, _ := sess.SelectedOutbound(); now != "xray-b" {
		t.Errorf("after select: SelectedOutbound = %q, want xray-b", now)
	}
	if err := sess.SelectOutbound("xray-a"); err != nil {
		t.Fatalf("live select back to the first xray member: %v", err)
	}
}

// TestPlanGroupUrltestLiveSelect: an active group lowers to a urltest over its
// members, becomes the selector default, and the session live-switches onto a
// concrete member and back. LiveOutbound descends into the urltest.
func TestPlanGroupUrltestLiveSelect(t *testing.T) {
	p := twoNativePlan() // node-a, node-b
	p.Group = &GroupPlan{
		ID:       "auto-group",
		Name:     "Auto",
		Members:  []string{"node-a", "node-b"},
		ProbeURL: "https://example.com/generate_204",
		Interval: 3 * time.Minute,
	}
	p.ActiveTag = "auto-group"

	if !p.IsMember("auto-group") {
		t.Fatalf("the group must be a selector member: %v", p.memberTags())
	}

	sbCfg, xrayCfg, err := PlanSocksConfigs(p, "127.0.0.1", freePort(t))
	if err != nil {
		t.Fatalf("PlanSocksConfigs: %v", err)
	}
	sess, err := Start(sbCfg, xrayCfg)
	if err != nil {
		t.Fatalf("Start (group urltest): %v", err)
	}
	defer sess.Close()

	// The selector defaults to the group.
	if now, _ := sess.SelectedOutbound(); now != "auto-group" {
		t.Errorf("selector default = %q, want auto-group", now)
	}
	// LiveOutbound descends into the urltest: its pick is a member, or the group
	// tag itself in the pre-first-sweep window (probes to doc IPs fail).
	if live, ok := sess.LiveOutbound(); !ok || (live != "auto-group" && live != "node-a" && live != "node-b") {
		t.Errorf("LiveOutbound = %q, want a member or the group tag", live)
	}
	// A live switch to a concrete member takes LiveOutbound off the group.
	if err := sess.SelectOutbound("node-a"); err != nil {
		t.Fatalf("select member node-a: %v", err)
	}
	if live, _ := sess.LiveOutbound(); live != "node-a" {
		t.Errorf("after selecting node-a, LiveOutbound = %q, want node-a", live)
	}
	if err := sess.SelectOutbound("auto-group"); err != nil {
		t.Fatalf("select back onto the group: %v", err)
	}
}

// TestPlanGroupValidation: a group with no members, or a member that is not an
// embedded node, is rejected at compile time.
func TestPlanGroupValidation(t *testing.T) {
	p := twoNativePlan()
	p.Group = &GroupPlan{ID: "g", Name: "Auto", Members: nil}
	p.ActiveTag = "g"
	if _, _, err := PlanSocksConfigs(p, "127.0.0.1", 1080); err == nil {
		t.Error("a group with no members must be rejected")
	}

	p = twoNativePlan()
	p.Group = &GroupPlan{ID: "g", Name: "Auto", Members: []string{"ghost"}}
	p.ActiveTag = "g"
	if _, _, err := PlanSocksConfigs(p, "127.0.0.1", 1080); err == nil {
		t.Error("a group member that is not an embedded node must be rejected")
	}
}

// TestPlanGroupIdleTimeoutClamp: idle_timeout is emitted only when the probe
// interval exceeds the sing-box default idle_timeout (30m), so NewURLTestGroup's
// interval <= idle_timeout constraint always holds.
func TestPlanGroupIdleTimeoutClamp(t *testing.T) {
	p := twoNativePlan()
	p.Group = &GroupPlan{ID: "g", Name: "Auto", Members: []string{"node-a"}, Interval: 3 * time.Minute}
	p.ActiveTag = "g"
	sb, _, err := PlanSocksConfigs(p, "127.0.0.1", 1080)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(sb), "idle_timeout") {
		t.Error("interval <= 30m must NOT emit idle_timeout")
	}

	p.Group.Interval = 45 * time.Minute
	sb, _, err = PlanSocksConfigs(p, "127.0.0.1", 1080)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sb), "idle_timeout") {
		t.Error("interval > 30m must emit idle_timeout")
	}
}

// TestPlanValidation: empty plans, a foreign active tag, and duplicate names
// are rejected at compile time, not at box.New.
func TestPlanValidation(t *testing.T) {
	if _, _, err := PlanSocksConfigs(SessionPlan{ActiveTag: "x"}, "127.0.0.1", 1080); err == nil {
		t.Error("empty plan must be rejected")
	}
	p := twoNativePlan()
	p.ActiveTag = "ghost"
	if _, _, err := PlanSocksConfigs(p, "127.0.0.1", 1080); err == nil {
		t.Error("active tag outside the member set must be rejected")
	}
	// Duplicate display NAMES are allowed now (a pure label); duplicate member
	// tags (UUIDs) are what must be rejected.
	p = twoNativePlan()
	p.Natives[1].ID = p.Natives[0].ID
	if _, _, err := PlanSocksConfigs(p, "127.0.0.1", 1080); err == nil {
		t.Error("duplicate member ids must be rejected")
	}
}

// TestPlanDuplicateDisplayNamesActivate: two members sharing a display name are
// DISTINCT selector members (tagged by their UUIDs), so the plan compiles, the
// real cores start, and BOTH are live-switchable — a display name is a pure
// label that MAY collide (no "duplicate node name" rejection). The name↔tag
// resolvers used at the wire boundary round-trip correctly.
func TestPlanDuplicateDisplayNamesActivate(t *testing.T) {
	p := twoNativePlan()
	p.Natives[0].Name, p.Natives[1].Name = "same", "same"
	p.Natives[0].ID = "aaaaaaaa-0000-4000-8000-000000000001"
	p.Natives[1].ID = "bbbbbbbb-0000-4000-8000-000000000002"
	p.ActiveTag = p.Natives[0].ID

	sbCfg, xrayCfg, err := PlanSocksConfigs(p, "127.0.0.1", freePort(t))
	if err != nil {
		t.Fatalf("colliding display names must still compile: %v", err)
	}
	sess, err := Start(sbCfg, xrayCfg)
	if err != nil {
		t.Fatalf("Start with colliding display names: %v", err)
	}
	defer sess.Close()

	if now, ok := sess.SelectedOutbound(); !ok || now != p.Natives[0].ID {
		t.Errorf("default member = %q,%v; want %s", now, ok, p.Natives[0].ID)
	}
	if err := sess.SelectOutbound(p.Natives[1].ID); err != nil {
		t.Fatalf("the second same-named member must be live-switchable: %v", err)
	}

	// The wire-boundary resolvers: a shared name maps to the first member's tag,
	// and each tag maps back to the shared display name.
	if tag, ok := p.MemberTagForName("same"); !ok || tag != p.Natives[0].ID {
		t.Errorf("MemberTagForName(same) = %q,%v; want %s,true", tag, ok, p.Natives[0].ID)
	}
	if name, ok := p.DisplayNameForTag(p.Natives[1].ID); !ok || name != "same" {
		t.Errorf("DisplayNameForTag = %q,%v; want same,true", name, ok)
	}
}

// TestNativeOutboundRejectsXhttp: an xhttp node must never reach the native
// compiler (capability selection routes it to xray).
func TestNativeOutboundRejectsXhttp(t *testing.T) {
	v := testVless(testReality(), ilproxy.Transport{Kind: ilproxy.TransportXhttp, Xhttp: &ilproxy.XhttpParams{}}, "")
	if _, err := v.SingBoxOutbound("x"); err == nil {
		t.Error("xhttp must have no native sing-box outbound")
	}
}

// TestSingleNodeSessionHasNoSelector: the single-node configs (NodeSocksConfigs)
// expose no selector — SelectOutbound must fail so the manager re-activates.
func TestSingleNodeSessionHasNoSelector(t *testing.T) {
	v := testVless(testReality(), ilproxy.Transport{Kind: ilproxy.TransportXhttp, Xhttp: &ilproxy.XhttpParams{}}, "")
	sbCfg, xrayCfg, err := NodeSocksConfigs(v, "127.0.0.1", freePort(t))
	if err != nil {
		t.Fatal(err)
	}
	sess, err := Start(sbCfg, xrayCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	if err := sess.SelectOutbound("anything"); err == nil {
		t.Error("single-node session must reject live select")
	}
	if _, ok := sess.SelectedOutbound(); ok {
		t.Error("single-node session must report no selector")
	}
}
