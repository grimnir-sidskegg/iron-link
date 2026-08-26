//go:build with_utls

// The full manager lifecycle against the REAL embedded cores in SOCKS mode
// (no root, nothing dialed). Native REALITY members need sing-box's uTLS, so
// this file runs under the canonical tag set (`-tags "with_gvisor,with_utls,with_clash_api"`).
//
// Fixture shape: node-a = xhttp (xray-routed, the store's active node),
// node-b = tcp (sing-box-native member), node-c = xhttp (xray-routed). Since
// M1 every xray-eligible node embeds as a live member of the one xray instance,
// so ALL of a↔b↔c are LIVE selector moves (no re-activation) — asserted via
// m.activations staying 1. An explicit activate is the re-activation path.
package main

import (
	"encoding/json"
	"testing"

	"ironlink/daemon/internal/api"
)

func TestManagerLifecycleLiveAndReactivateSwitch(t *testing.T) {
	m := fixtureManager(t)

	// Activate with no names: the store's active profile + active node.
	resp := m.Handle(api.Request{Command: api.CmdActivate})
	if resp.Status != api.StatusActivated || len(resp.Entries) != 2 {
		t.Fatalf("activate: %+v", resp)
	}
	if m.activations != 1 {
		t.Fatalf("activations = %d, want 1", m.activations)
	}

	st := m.Handle(api.Request{Command: api.CmdStatus})
	if st.Status != api.StatusRunning || st.Active == nil || *st.Active.Node != "node-a" {
		t.Fatalf("status after activate: %+v", st)
	}
	if st.ActiveNodeLive == nil || *st.ActiveNodeLive != "node-a" {
		t.Errorf("active_node_live = %v, want node-a (the selector default)", st.ActiveNodeLive)
	}
	if ev := m.Snapshot(); len(ev.Entries) != 2 || ev.Active == nil {
		t.Errorf("running snapshot: %+v", ev)
	}

	// node-b is a native member → LIVE switch, no re-activation.
	nodeB := "node-b"
	if sw := m.Handle(api.Request{Command: api.CmdSwitchNode, Node: &nodeB}); sw.Status != api.StatusSwitched {
		t.Fatalf("switch to native member: %+v", sw)
	}
	if m.activations != 1 {
		t.Errorf("switch to a member must be live, got %d activations", m.activations)
	}
	st = m.Handle(api.Request{Command: api.CmdStatus})
	if st.ActiveNodeLive == nil || *st.ActiveNodeLive != "node-b" || *st.Active.Node != "node-b" {
		t.Fatalf("status after live switch: %+v", st)
	}

	// node-a is the EMBEDDED xray member → also a live switch.
	nodeA := "node-a"
	if sw := m.Handle(api.Request{Command: api.CmdSwitchNode, Node: &nodeA}); sw.Status != api.StatusSwitched {
		t.Fatalf("switch back to the xray member: %+v", sw)
	}
	if m.activations != 1 {
		t.Errorf("switch to the embedded xray member must be live, got %d activations", m.activations)
	}

	// node-c is a second xray member of the same instance → also a LIVE switch.
	nodeC := "node-c"
	if sw := m.Handle(api.Request{Command: api.CmdSwitchNode, Node: &nodeC}); sw.Status != api.StatusSwitched {
		t.Fatalf("switch to the second xray member: %+v", sw)
	}
	if m.activations != 1 {
		t.Errorf("switch among embedded xray members must be live, got %d activations", m.activations)
	}
	st = m.Handle(api.Request{Command: api.CmdStatus})
	if st.ActiveNodeLive == nil || *st.ActiveNodeLive != "node-c" || *st.Active.Node != "node-c" {
		t.Fatalf("status after live xray→xray switch: %+v", st)
	}

	// An EXPLICIT activate is the re-activation path (always stops + starts).
	if sw := m.Handle(api.Request{Command: api.CmdActivate, Node: &nodeA}); sw.Status != api.StatusActivated {
		t.Fatalf("explicit re-activate to node-a: %+v", sw)
	}
	if m.activations != 2 {
		t.Errorf("an explicit activate must re-activate, got %d activations", m.activations)
	}

	missing := "nope"
	if got := m.Handle(api.Request{Command: api.CmdSwitchNode, Node: &missing}); got.Status != api.StatusError {
		t.Errorf("switch to unknown node should error: %+v", got)
	}

	if got := m.Handle(api.Request{Command: api.CmdStop}); got.Status != api.StatusStopped {
		t.Fatalf("stop: %+v", got)
	}
	if got := m.Handle(api.Request{Command: api.CmdStatus}); got.Status != api.StatusIdle {
		t.Errorf("status after stop: %+v", got)
	}
}

// TestManagerReactivationHonoursActivatedNode guards the selector-cache
// determinism fix. A live switch persists its selection to cache.db, and a
// selector RESTORES that cached tag ahead of the configured `default` on the
// next Start (sing-box group.Selector.Start). So a re-activation onto a
// DIFFERENT node would silently come up live on the stale cached member unless
// the manager pins the selector to the activated node. Sequence: activate
// (node-a) → live-switch to the native node-b (caches "node-b") → EXPLICIT
// activate of node-c, which re-activates. node-b (native) is still a member of
// the node-c plan, so the cached selection would win — the live node MUST be
// node-c regardless. (Since M1 every node is an embedded member, a switch never
// re-activates; the explicit activate is the re-activation trigger.)
func TestManagerReactivationHonoursActivatedNode(t *testing.T) {
	m := fixtureManager(t)

	if resp := m.Handle(api.Request{Command: api.CmdActivate}); resp.Status != api.StatusActivated {
		t.Fatalf("activate: %+v", resp)
	}
	defer m.Handle(api.Request{Command: api.CmdStop})

	// Live-switch to the native member node-b — this StoreSelected("node-b")s
	// into cache.db (a member of the next plan too, so its restore would win).
	nodeB := "node-b"
	if sw := m.Handle(api.Request{Command: api.CmdSwitchNode, Node: &nodeB}); sw.Status != api.StatusSwitched {
		t.Fatalf("live switch to node-b: %+v", sw)
	}
	if m.activations != 1 {
		t.Fatalf("switch to a native member must be live, got %d activations", m.activations)
	}

	// Explicit activate of node-c → re-activation with node-b still cached.
	nodeC := "node-c"
	if sw := m.Handle(api.Request{Command: api.CmdActivate, Node: &nodeC}); sw.Status != api.StatusActivated {
		t.Fatalf("re-activate node-c: %+v", sw)
	}
	if m.activations != 2 {
		t.Fatalf("an explicit activate must re-activate, got %d activations", m.activations)
	}
	st := m.Handle(api.Request{Command: api.CmdStatus})
	if st.ActiveNodeLive == nil || *st.ActiveNodeLive != "node-c" {
		t.Fatalf("live node after re-activation = %v, want node-c (not the cached node-b)", st.ActiveNodeLive)
	}
}

// TestManagerLastSessionRestore: a persisted context restores on start; an
// explicit stop clears the record; a shutdown keeps it (so a daemon restart
// restores again). Tagged: restore builds the FULL plan, including the
// fixture's native REALITY member (uTLS).
func TestManagerLastSessionRestore(t *testing.T) {
	m := fixtureManager(t)

	// restore-on-start defaults OFF on Windows (store.defaultRestoreOnStart);
	// enable it explicitly so this test exercises the restore path on every OS.
	s, err := m.store.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	s.RestoreOnStart = true
	if err := m.store.SaveSettings(s); err != nil {
		t.Fatal(err)
	}

	// Simulate a previous run's record, then a daemon start.
	profile, node := "main", "node-a"
	if err := m.store.SaveLastSession(api.PersistedEntry{Profile: &profile, Node: &node}); err != nil {
		t.Fatal(err)
	}
	m.restoreLastSession()
	st := m.Handle(api.Request{Command: api.CmdStatus})
	if st.Status != api.StatusRunning || st.Active == nil || *st.Active.Node != "node-a" {
		t.Fatalf("status after restore: %+v", st)
	}

	// Shutdown (process exit): session closed, record SURVIVES.
	m.shutdown()
	if got := m.Handle(api.Request{Command: api.CmdStatus}); got.Status != api.StatusIdle {
		t.Fatalf("status after shutdown: %+v", got)
	}
	if ctx, _ := m.store.LoadLastSession(); ctx == nil {
		t.Fatal("shutdown must keep the last-session record")
	}

	// Restore again, then an EXPLICIT stop: record cleared.
	m.restoreLastSession()
	if got := m.Handle(api.Request{Command: api.CmdStop}); got.Status != api.StatusStopped {
		t.Fatalf("stop: %+v", got)
	}
	if ctx, _ := m.store.LoadLastSession(); ctx != nil {
		t.Error("explicit stop must clear the last-session record")
	}
	m.restoreLastSession() // no record -> stays idle
	if got := m.Handle(api.Request{Command: api.CmdStatus}); got.Status != api.StatusIdle {
		t.Errorf("restore without a record must stay idle: %+v", got)
	}
}

// TestManagerActivateWithRouting: activate compiles the named routing into
// the plan; the persisted context carries the CANONICAL routing name.
func TestManagerActivateWithRouting(t *testing.T) {
	m := fixtureManager(t)
	basic := json.RawMessage(`{
	  "name": "basic",
	  "rule_sets": [],
	  "rules": [{"target": "DefaultProxy", "domain_keyword": ["example.com"]}],
	  "default_target": "Direct"
	}`)
	if got := m.Handle(api.Request{Command: api.CmdUpsertRouting, RoutingConfig: basic}); got.Status != api.StatusOk {
		t.Fatalf("upsert: %+v", got)
	}

	routingName := "basic"
	if got := m.Handle(api.Request{Command: api.CmdActivate, Routing: &routingName}); got.Status != api.StatusActivated {
		t.Fatalf("activate with routing: %+v", got)
	}
	defer m.Handle(api.Request{Command: api.CmdStop})

	st := m.Handle(api.Request{Command: api.CmdStatus})
	if st.Active == nil || st.Active.Routing == nil || *st.Active.Routing != "basic" {
		t.Fatalf("status must carry the routing name: %+v", st.Active)
	}

	// An unknown routing name fails the activation.
	ghost := "ghost"
	if got := m.Handle(api.Request{Command: api.CmdActivate, Routing: &ghost}); got.Status != api.StatusError {
		t.Errorf("activate with unknown routing must error: %+v", got)
	}
}
