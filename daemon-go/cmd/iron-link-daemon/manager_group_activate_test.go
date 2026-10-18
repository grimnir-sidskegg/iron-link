//go:build with_utls

// Activating a group against the REAL cores (SOCKS mode): the group lowers to a
// urltest over its members, status descends to the live member, and a switch to
// a member is a live selector move. Uses groupFixtureManager (Auto over node-x).
package main

import (
	"encoding/json"
	"strings"
	"testing"

	"ironlink/daemon/internal/api"
	"ironlink/daemon/internal/store"
)

func TestManagerActivateGroup(t *testing.T) {
	m := groupFixtureManager(t)

	auto := "Auto"
	if resp := m.Handle(api.Request{Command: api.CmdActivate, Node: &auto}); resp.Status != api.StatusActivated {
		t.Fatalf("activate the group: %+v", resp)
	}
	defer m.Handle(api.Request{Command: api.CmdStop})

	st := m.Handle(api.Request{Command: api.CmdStatus})
	if st.Status != api.StatusRunning || st.Active == nil || *st.Active.Node != "Auto" {
		t.Fatalf("status after activating the group: %+v", st)
	}
	// active_node_live descends into the urltest — the member pick, or the group
	// name in the pre-first-sweep window — but never empty.
	if st.ActiveNodeLive == nil || (*st.ActiveNodeLive != "node-x" && *st.ActiveNodeLive != "Auto") {
		t.Errorf("active_node_live = %v, want node-x or Auto", st.ActiveNodeLive)
	}

	// Switching to the embedded member is a live selector move (no re-activation).
	nodeX := "node-x"
	if sw := m.Handle(api.Request{Command: api.CmdSwitchNode, Node: &nodeX}); sw.Status != api.StatusSwitched {
		t.Fatalf("switch to the member: %+v", sw)
	}
	if m.activations != 1 {
		t.Errorf("switch to an embedded member must be live, got %d activations", m.activations)
	}
	st = m.Handle(api.Request{Command: api.CmdStatus})
	if st.ActiveNodeLive == nil || *st.ActiveNodeLive != "node-x" {
		t.Errorf("after switching to node-x, active_node_live = %v, want node-x", st.ActiveNodeLive)
	}

	// Switch back onto the group — also live.
	if sw := m.Handle(api.Request{Command: api.CmdSwitchNode, Node: &auto}); sw.Status != api.StatusSwitched {
		t.Fatalf("switch back to the group: %+v", sw)
	}
	if m.activations != 1 {
		t.Errorf("switch back to the group must be live, got %d activations", m.activations)
	}
}

// TestManagerActivateRoutingFallback: removing the subscription a routing's
// targets point at does not brick the profile — the activation succeeds and
// status plus the state event list one warning per fallen-back target.
func TestManagerActivateRoutingFallback(t *testing.T) {
	m := managerWithProfile(t, fallbackProfile)
	sub := "MySub"
	if r := m.Handle(api.Request{Command: api.CmdRemoveSub, Sub: &sub}); r.Status != api.StatusOk {
		t.Fatalf("remove_subscription: %+v", r)
	}
	if resp := m.Handle(api.Request{Command: api.CmdActivate}); resp.Status != api.StatusActivated {
		t.Fatalf("activate with unresolvable routing targets: %+v", resp)
	}
	defer m.Handle(api.Request{Command: api.CmdStop})

	if st := m.Handle(api.Request{Command: api.CmdStatus}); len(st.RoutingWarnings) != 5 {
		t.Errorf("status routing_warnings = %q, want 5", st.RoutingWarnings)
	}
	if ev := m.Snapshot(); len(ev.RoutingWarnings) != 5 {
		t.Errorf("state routing_warnings = %q, want 5", ev.RoutingWarnings)
	}
}

// TestRoutingFallbackGroupRepopulatedReconnects: a routing-only group that fell
// back while empty gets its members back from the provider — nodes the session
// already embeds — and the reconnect decision must see it. It compares against
// the routing as stored; the compiled copy already points at the selector and
// would call the plans equal.
func TestRoutingFallbackGroupRepopulatedReconnects(t *testing.T) {
	m := managerWithProfile(t, fallbackProfile)
	load := func() *store.Profile {
		p, err := m.store.LoadProfile("main")
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	p := load()
	p.FindNodeByID("44444444-4444-4444-4444-444444444444").Group.Members = nil
	if err := m.store.SaveProfile("main", p); err != nil {
		t.Fatal(err)
	}
	if resp := m.Handle(api.Request{Command: api.CmdActivate}); resp.Status != api.StatusActivated {
		t.Fatalf("activate: %+v", resp)
	}
	defer m.Handle(api.Request{Command: api.CmdStop})

	m.mu.Lock()
	defer m.mu.Unlock()
	if rc := m.sessionReconnectLocked("main", load(), "test"); rc != nil {
		t.Fatalf("an unchanged profile must not reconnect: %+v", rc)
	}
	p = load()
	p.FindNodeByID("44444444-4444-4444-4444-444444444444").Group.Members = []string{"22222222-2222-2222-2222-222222222222"}
	if rc := m.sessionReconnectLocked("main", p, "test"); rc == nil {
		t.Error("a re-populated routing target group must reconnect")
	}
}

// TestManagerSwitchByIDWithDuplicateNames: two groups both named "Auto" (two
// subscriptions shipping the same label). A switch to the SECOND one by id
// moves the live selector onto it, persists its id, and a restore comes back
// to that same group — the name alone would resolve to the first one. A name
// still works for an older caller.
func TestManagerSwitchByIDWithDuplicateNames(t *testing.T) {
	const first, second = "99999999-9999-9999-9999-999999999999", "88888888-8888-8888-8888-888888888888"
	m := managerWithProfile(t, strings.Replace(groupFixtureProfile, "\n  ],\n  \"routing_configs\"", `,
    {"id": "`+second+`", "sub_id": null, "profile": null,
      "group": {"name": "Auto", "members": ["11111111-1111-1111-1111-111111111111"],
        "probe": {"url": "https://example.com/204", "interval_sec": 180, "tolerance": 150}},
      "preferences": {"core_override": null}}
  ],
  "routing_configs"`, 1))
	s, err := m.store.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	s.RestoreOnStart = true
	if err := m.store.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	// A routing targeting the second group embeds it beside the active first
	// one, so the switch between them is a live selector move.
	rc := `{"name":"g","rule_sets":[],"rules":[],"default_target":{"Node":"` + second + `"}}`
	if resp := m.Handle(api.Request{Command: api.CmdUpsertRouting, RoutingConfig: json.RawMessage(rc)}); resp.Status != api.StatusOk {
		t.Fatalf("upsert_routing: %+v", resp)
	}
	firstID, secondID, g := first, second, "g"
	if resp := m.Handle(api.Request{Command: api.CmdActivate, Node: &firstID, Routing: &g}); resp.Status != api.StatusActivated {
		t.Fatalf("activate the first Auto: %+v", resp)
	}
	defer m.Handle(api.Request{Command: api.CmdStop})

	sw := m.Handle(api.Request{Command: api.CmdSwitchNode, Node: &secondID})
	if sw.Status != api.StatusSwitched || sw.Node != "Auto" {
		t.Fatalf("switch to the second Auto by id: %+v", sw)
	}
	if m.activations != 1 {
		t.Errorf("the switch must be live, got %d activations", m.activations)
	}
	if sel, _ := m.sess.SelectedOutbound(); sel != second {
		t.Errorf("selector = %q, want the second Auto", sel)
	}
	st := m.Handle(api.Request{Command: api.CmdStatus})
	if st.Active == nil || *st.Active.Node != "Auto" || st.Active.NodeID == nil || *st.Active.NodeID != second {
		t.Fatalf("status after the switch: %+v", st.Active)
	}
	if ctx, _ := m.store.LoadLastSession(); ctx == nil || ctx.NodeID == nil || *ctx.NodeID != second {
		t.Fatalf("last session = %+v, want the second Auto's id", ctx)
	}

	m.shutdown()
	m.restoreLastSession()
	if m.plan.ActiveTag != second {
		t.Errorf("restore came back to %q, want the second Auto", m.plan.ActiveTag)
	}
	if sel, _ := m.sess.SelectedOutbound(); sel != second {
		t.Errorf("selector after restore = %q, want the second Auto", sel)
	}

	auto := "Auto"
	if sw := m.Handle(api.Request{Command: api.CmdSwitchNode, Node: &auto}); sw.Status != api.StatusSwitched {
		t.Fatalf("switch by name: %+v", sw)
	}
}
