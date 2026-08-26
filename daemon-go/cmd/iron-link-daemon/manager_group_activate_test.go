//go:build with_utls

// Activating a group against the REAL cores (SOCKS mode): the group lowers to a
// urltest over its members, status descends to the live member, and a switch to
// a member is a live selector move. Uses groupFixtureManager (Auto over node-x).
package main

import (
	"testing"

	"ironlink/daemon/internal/api"
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
