package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ironlink/daemon/internal/api"
	"ironlink/daemon/internal/ipc"
	"ironlink/daemon/internal/store"
)

// groupFixtureProfile is a profile with one dialable node (node-x) plus a user
// group "Auto" whose sole member is node-x. It exercises every group-handling
// branch M2a added WITHOUT spinning a core (each verb short-circuits before the
// data plane).
const groupFixtureProfile = `{
  "schema_version": 2,
  "name": "main",
  "active_node_id": "11111111-1111-1111-1111-111111111111",
  "subscriptions": [],
  "nodes": [
    {
      "id": "11111111-1111-1111-1111-111111111111",
      "sub_id": null,
      "profile": {"vless": {
        "server_name": "node-x",
        "uuid": "88f2c0dc-a8e3-49f4-89b9-b3b54f1cad3a",
        "address": "203.0.113.1", "port": 443, "encryption": "none",
        "security": {"kind": "reality", "reality": {"sni": "google.com", "fp": "chrome",
          "pbk": "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY", "sid": "01ab"}},
        "transport": {"kind": "tcp"}
      }},
      "preferences": {"core_override": null}
    },
    {
      "id": "99999999-9999-9999-9999-999999999999",
      "sub_id": null,
      "profile": null,
      "group": {
        "name": "Auto",
        "members": ["11111111-1111-1111-1111-111111111111"],
        "probe": {"url": "https://example.com/204", "interval_sec": 180, "tolerance": 150}
      },
      "preferences": {"core_override": null}
    }
  ],
  "routing_configs": [],
  "active_routing_id": null
}`

func groupFixtureManager(t *testing.T) *manager {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "profiles"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "profiles", "main.json"), []byte(groupFixtureProfile), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(`{"active":"main"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	m := newManager(store.OpenAt(dir))
	m.hub = ipc.NewHub(m.Snapshot)
	m.socksPort = freePort(t)
	return m
}

// TestGroupVerbsDoNotPanic sweeps the verbs a stored group flows through. None
// may panic on the group's nil proxy profile; each must treat the group per its
// policy (listed as a group row; rejected for prefs/diagnose/activate; skipped
// for latency).
func TestGroupVerbsDoNotPanic(t *testing.T) {
	m := groupFixtureManager(t)

	// list_nodes: the group is a row with kind=group and resolved members; the
	// dialable node is a normal row.
	nodesResp := m.Handle(api.Request{Command: api.CmdListNodes})
	if nodesResp.Status != api.StatusNodes || len(nodesResp.Nodes) != 2 {
		t.Fatalf("list_nodes: %+v", nodesResp)
	}
	var group *api.NodeInfo
	for i := range nodesResp.Nodes {
		if nodesResp.Nodes[i].Kind == api.NodeKindGroup {
			group = &nodesResp.Nodes[i]
		}
	}
	if group == nil {
		t.Fatal("list_nodes did not surface the group row")
	}
	if group.Name != "Auto" || len(group.Members) != 1 || group.Members[0] != "11111111-1111-1111-1111-111111111111" {
		t.Errorf("group row = %+v", group)
	}
	if group.Protocol != "" || len(group.EligibleCores) != 0 {
		t.Errorf("group row must have empty protocol/eligible_cores: %+v", group)
	}

	// set_node_prefs on the group: rejected, not a nil-panic.
	groupName := "Auto"
	xray := api.CoreXray
	if r := m.Handle(api.Request{Command: api.CmdSetNodePrefs, Node: &groupName, CoreOverride: &xray}); r.Status != api.StatusError {
		t.Errorf("set_node_prefs on a group must error: %+v", r)
	}

	// diagnose the group: rejected before any probe.
	if r := m.Handle(api.Request{Command: api.CmdDiagnose, Node: &groupName}); r.Status != api.StatusError {
		t.Errorf("diagnose on a group must error: %+v", r)
	}

	// buildPlan: activating the group lowers it to a urltest over its members;
	// activating the dialable node embeds only that node (group absent).
	prof, err := m.store.LoadProfile("main")
	if err != nil {
		t.Fatal(err)
	}
	gplan, err := buildPlan(prof, prof.FindNodeByName("Auto"))
	if err != nil {
		t.Fatalf("buildPlan for the group must succeed (lowered): %v", err)
	}
	if gplan.Group == nil || gplan.Group.Name != "Auto" || len(gplan.Group.Members) != 1 {
		t.Errorf("group not lowered to a urltest plan: %+v", gplan.Group)
	}
	plan, err := buildPlan(prof, prof.FindNodeByName("node-x"))
	if err != nil {
		t.Fatalf("buildPlan(node-x): %v", err)
	}
	if len(plan.Natives) != 1 || len(plan.XrayNodes) != 0 || plan.Group != nil {
		t.Errorf("plan must embed only the dialable node, not the group: %+v", plan)
	}
}

// TestUpsertRoutingRejectsGroupTarget: a routing rule cannot target a group —
// the group's urltest exists only while it is the active node, so such a rule
// would fail an unrelated activation. It is rejected at authoring time.
func TestUpsertRoutingRejectsGroupTarget(t *testing.T) {
	m := groupFixtureManager(t)

	groupTarget := `{"name":"g","rule_sets":[],"rules":[{"target":{"Node":"99999999-9999-9999-9999-999999999999"},"domain_keyword":["x.com"]}],"default_target":"Direct"}`
	resp := m.Handle(api.Request{Command: api.CmdUpsertRouting, RoutingConfig: json.RawMessage(groupTarget)})
	if resp.Status != api.StatusError || !strings.Contains(resp.Message, "group") {
		t.Errorf("a rule targeting a group must be rejected: %+v", resp)
	}

	nodeTarget := `{"name":"ok","rule_sets":[],"rules":[{"target":{"Node":"11111111-1111-1111-1111-111111111111"},"domain_keyword":["x.com"]}],"default_target":"Direct"}`
	if resp := m.Handle(api.Request{Command: api.CmdUpsertRouting, RoutingConfig: json.RawMessage(nodeTarget)}); resp.Status != api.StatusOk {
		t.Errorf("a rule targeting a dialable node must be accepted: %+v", resp)
	}
}
