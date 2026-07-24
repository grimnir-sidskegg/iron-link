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
	p.XrayNode = &xrayNode
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
	p = twoNativePlan()
	p.Natives[1].Name = "node-a"
	if _, _, err := PlanSocksConfigs(p, "127.0.0.1", 1080); err == nil {
		t.Error("duplicate member names must be rejected")
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
