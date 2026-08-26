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
	return managerWithProfile(t, groupFixtureProfile)
}

func managerWithProfile(t *testing.T, profileJSON string) *manager {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "profiles"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "profiles", "main.json"), []byte(profileJSON), 0o600); err != nil {
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

// subGroupProfile has a subscription "MySub" (id sub-1) with one node owned by
// it — the fixture for the all_of_sub happy path.
const subGroupProfile = `{
  "schema_version": 2,
  "name": "main",
  "active_node_id": null,
  "subscriptions": [
    {"id": "sub-1", "name": "MySub", "url": "https://example.com/sub", "last_updated": "2026-08-26T00:00:00Z", "update_interval_sec": 86400, "enabled": true, "format": "auto"}
  ],
  "nodes": [
    {
      "id": "22222222-2222-2222-2222-222222222222",
      "sub_id": "sub-1",
      "profile": {"vless": {
        "server_name": "sub-node",
        "uuid": "88f2c0dc-a8e3-49f4-89b9-b3b54f1cad3a",
        "address": "198.51.100.5", "port": 443, "encryption": "none",
        "security": {"kind": "reality", "reality": {"sni": "example.com", "fp": "chrome",
          "pbk": "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY", "sid": "01ab"}},
        "transport": {"kind": "tcp"}
      }},
      "preferences": {"core_override": null}
    }
  ],
  "routing_configs": [],
  "active_routing_id": null
}`

// TestUpsertGroupAllOfSubNormalizesToID: an all_of_sub group created by the
// subscription NAME stores its ID, so membership resolves to the sub's nodes
// (a name stored verbatim would match nothing).
func TestUpsertGroupAllOfSubNormalizesToID(t *testing.T) {
	m := managerWithProfile(t, subGroupProfile)

	if r := m.Handle(api.Request{Command: api.CmdUpsertGroup,
		Group: json.RawMessage(`{"name":"Auto","all_of_sub":"MySub"}`)}); r.Status != api.StatusOk {
		t.Fatalf("create all_of_sub group by name: %+v", r)
	}

	resp := m.Handle(api.Request{Command: api.CmdListNodes})
	var group *api.NodeInfo
	for i := range resp.Nodes {
		if resp.Nodes[i].Kind == api.NodeKindGroup {
			group = &resp.Nodes[i]
		}
	}
	if group == nil {
		t.Fatal("the all_of_sub group was not listed")
	}
	if len(group.Members) != 1 || group.Members[0] != "22222222-2222-2222-2222-222222222222" {
		t.Errorf("all_of_sub group resolved members = %v, want the sub's node", group.Members)
	}
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

// TestUpsertGroupCreateEditValidate exercises the user-group CRUD verb: create
// over explicit members, edit in place, and the validation rejections.
func TestUpsertGroupCreateEditValidate(t *testing.T) {
	m := groupFixtureManager(t)
	up := func(payload string) api.Response {
		return m.Handle(api.Request{Command: api.CmdUpsertGroup, Group: json.RawMessage(payload)})
	}

	if r := up(`{"name":"My Auto","members":["11111111-1111-1111-1111-111111111111"],"probe":{"interval_sec":120}}`); r.Status != api.StatusOk {
		t.Fatalf("create group: %+v", r)
	}
	// Neither / both membership modes.
	if r := up(`{"name":"Empty"}`); r.Status != api.StatusError {
		t.Errorf("no membership must error: %+v", r)
	}
	if r := up(`{"name":"Both","members":["11111111-1111-1111-1111-111111111111"],"all_of_sub":"s1"}`); r.Status != api.StatusError {
		t.Errorf("both membership modes must error: %+v", r)
	}
	// Unknown member, a group as a member, missing subscription.
	if r := up(`{"name":"Bad","members":["deadbeef-0000-0000-0000-000000000000"]}`); r.Status != api.StatusError {
		t.Errorf("unknown member must error: %+v", r)
	}
	if r := up(`{"name":"Nested","members":["99999999-9999-9999-9999-999999999999"]}`); r.Status != api.StatusError {
		t.Errorf("a group as a member must error: %+v", r)
	}
	if r := up(`{"name":"Sub","all_of_sub":"nope"}`); r.Status != api.StatusError {
		t.Errorf("missing subscription must error: %+v", r)
	}

	// Edit the existing user group Auto in place (id preserved).
	if r := up(`{"id":"99999999-9999-9999-9999-999999999999","name":"Auto Renamed","members":["11111111-1111-1111-1111-111111111111"]}`); r.Status != api.StatusOk {
		t.Fatalf("edit group: %+v", r)
	}
	prof, err := m.store.LoadProfile("main")
	if err != nil {
		t.Fatal(err)
	}
	if g := prof.FindNodeByID("99999999-9999-9999-9999-999999999999"); g == nil || g.DisplayName() != "Auto Renamed" {
		t.Errorf("edit did not persist: %+v", g)
	}
}

// TestGetGroupReturnsStoredSpec: get_group returns a group's TRUE stored spec
// (membership mode + probe), not the resolved list_nodes row — and rejects a
// dialable node or an unknown ref.
func TestGetGroupReturnsStoredSpec(t *testing.T) {
	m := groupFixtureManager(t)

	resp := m.Handle(api.Request{Command: api.CmdGetGroup, Node: strPtr("Auto")})
	if resp.Status != api.StatusGroupConfig {
		t.Fatalf("get_group: %+v", resp)
	}
	var spec store.GroupSpec
	if err := json.Unmarshal(resp.GroupConfig, &spec); err != nil {
		t.Fatalf("decode group_config: %v", err)
	}
	if spec.Name != "Auto" ||
		len(spec.Members) != 1 || spec.Members[0] != "11111111-1111-1111-1111-111111111111" ||
		spec.AllOfSub != nil ||
		spec.Probe.IntervalSec != 180 || spec.Probe.Tolerance != 150 {
		t.Errorf("group_config spec = %+v", spec)
	}

	// A dialable node is not a group.
	if r := m.Handle(api.Request{Command: api.CmdGetGroup, Node: strPtr("node-x")}); r.Status != api.StatusError {
		t.Errorf("get_group on a dialable node must error: %+v", r)
	}
	// An unknown ref errors.
	if r := m.Handle(api.Request{Command: api.CmdGetGroup, Node: strPtr("nope")}); r.Status != api.StatusError {
		t.Errorf("get_group on an unknown ref must error: %+v", r)
	}
}

// TestGetNodeReturnsConfig: get_node returns a dialable node's full stored
// config ({name, protocol, profile:{…}}) — the endpoint and the nested security
// front the list_nodes row drops — and rejects a group or an unknown ref.
func TestGetNodeReturnsConfig(t *testing.T) {
	m := groupFixtureManager(t)

	resp := m.Handle(api.Request{Command: api.CmdGetNode, Node: strPtr("node-x")})
	if resp.Status != api.StatusNodeConfig {
		t.Fatalf("get_node: %+v", resp)
	}
	var env struct {
		Name     string                 `json:"name"`
		Protocol string                 `json:"protocol"`
		Profile  map[string]interface{} `json:"profile"`
	}
	if err := json.Unmarshal(resp.NodeConfig, &env); err != nil {
		t.Fatalf("decode node_config: %v", err)
	}
	if env.Name != "node-x" || env.Protocol != "vless" {
		t.Errorf("node_config envelope = %+v", env)
	}
	if env.Profile["address"] != "203.0.113.1" {
		t.Errorf("profile.address = %v, want the stored endpoint", env.Profile["address"])
	}
	security, _ := env.Profile["security"].(map[string]interface{})
	reality, _ := security["reality"].(map[string]interface{})
	if reality["sni"] != "google.com" {
		t.Errorf("profile.security.reality.sni = %v, want the stored front", reality["sni"])
	}

	// A group has no endpoint config — rejected (its shape is get_group's).
	if r := m.Handle(api.Request{Command: api.CmdGetNode, Node: strPtr("Auto")}); r.Status != api.StatusError {
		t.Errorf("get_node on a group must error: %+v", r)
	}
	// An unknown ref errors.
	if r := m.Handle(api.Request{Command: api.CmdGetNode, Node: strPtr("nope")}); r.Status != api.StatusError {
		t.Errorf("get_node on an unknown ref must error: %+v", r)
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
