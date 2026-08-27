package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"ironlink/daemon/internal/api"
	"ironlink/daemon/internal/engine"
	"ironlink/daemon/internal/ipc"
	"ironlink/daemon/internal/routing"
	"ironlink/daemon/internal/store"
)

// fixtureProfile (schema v2, Go-native shape) holds three SYNTACTICALLY
// VALID reality nodes (xray's client REALITY build rejects garbage
// pbk/sid/fp at engine.Start, so the fixtures must pass it) pointing at
// TEST-NET addresses that are never dialed.
const fixtureProfile = `{
  "schema_version": 2,
  "name": "main",
  "active_node_id": "11111111-1111-1111-1111-111111111111",
  "subscriptions": [],
  "nodes": [
    {
      "id": "11111111-1111-1111-1111-111111111111",
      "sub_id": null,
      "profile": {"vless": {
        "server_name": "node-a",
        "uuid": "88f2c0dc-a8e3-49f4-89b9-b3b54f1cad3a",
        "address": "203.0.113.1", "port": 443, "encryption": "none",
        "security": {"kind": "reality", "reality": {"sni": "google.com", "fp": "chrome",
          "pbk": "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY", "sid": "01ab"}},
        "transport": {"kind": "xhttp", "xhttp": {"path": "/x", "mode": "auto"}}
      }},
      "preferences": {"core_override": null}
    },
    {
      "id": "22222222-2222-2222-2222-222222222222",
      "sub_id": null,
      "profile": {"vless": {
        "server_name": "node-b",
        "uuid": "99f2c0dc-a8e3-49f4-89b9-b3b54f1cad3a",
        "address": "203.0.113.2", "port": 443, "encryption": "none",
        "security": {"kind": "reality", "reality": {"sni": "google.com", "fp": "chrome",
          "pbk": "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY", "sid": "01ab"}},
        "transport": {"kind": "tcp"}
      }},
      "preferences": {"core_override": null}
    },
    {
      "id": "33333333-3333-3333-3333-333333333333",
      "sub_id": null,
      "profile": {"vless": {
        "server_name": "node-c",
        "uuid": "aaf2c0dc-a8e3-49f4-89b9-b3b54f1cad3a",
        "address": "203.0.113.3", "port": 443, "encryption": "none",
        "security": {"kind": "reality", "reality": {"sni": "google.com", "fp": "chrome",
          "pbk": "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY", "sid": "01ab"}},
        "transport": {"kind": "xhttp", "xhttp": {"path": "/c", "mode": "auto"}}
      }},
      "preferences": {"core_override": null}
    }
  ],
  "routing_configs": [],
  "active_routing_id": null
}`

func fixtureManager(t *testing.T) *manager {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "profiles"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "profiles", "main.json"), []byte(fixtureProfile), 0o600); err != nil {
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

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// TestBuildPlanEmbedsAllXrayNodes pins the M1 invariant: EVERY xray-eligible
// node embeds as a member (not just the active one), the active xray node is
// XrayNodes[0] (xray's default outbound), and native nodes stay native. The
// fixture is node-a (xhttp/xray), node-b (tcp/native), node-c (xhttp/xray).
func TestBuildPlanEmbedsAllXrayNodes(t *testing.T) {
	m := fixtureManager(t)
	prof, err := m.store.LoadProfile("main")
	if err != nil {
		t.Fatal(err)
	}
	active := prof.FindNodeByName("node-c")
	if active == nil {
		t.Fatal("fixture missing node-c")
	}

	plan, err := buildPlan(prof, active)
	if err != nil {
		t.Fatalf("buildPlan: %v", err)
	}
	if plan.ActiveTag != active.ID {
		t.Errorf("ActiveTag = %q, want %q", plan.ActiveTag, active.ID)
	}
	if len(plan.Natives) != 1 || plan.Natives[0].Name != "node-b" {
		t.Errorf("Natives = %+v, want [node-b]", plan.Natives)
	}
	if len(plan.XrayNodes) != 2 {
		t.Fatalf("XrayNodes count = %d, want 2 (both xray nodes embedded)", len(plan.XrayNodes))
	}
	if plan.XrayNodes[0].Name != "node-c" {
		t.Errorf("XrayNodes[0] = %q, want node-c (the active xray node leads)", plan.XrayNodes[0].Name)
	}
	if plan.XrayNodes[1].Name != "node-a" {
		t.Errorf("XrayNodes[1] = %q, want node-a", plan.XrayNodes[1].Name)
	}
}

func TestManagerIdleVerbs(t *testing.T) {
	m := fixtureManager(t)

	if got := m.Handle(api.Request{Command: api.CmdStatus}); got.Status != api.StatusIdle || got.DaemonVersion != "dev" {
		t.Errorf("status while idle: %+v", got)
	}
	node := "node-b"
	if got := m.Handle(api.Request{Command: api.CmdSwitchNode, Node: &node}); got.Status != api.StatusError {
		t.Errorf("switch while idle should error: %+v", got)
	}
	if got := m.Handle(api.Request{Command: api.CmdStop}); got.Status != api.StatusIdle {
		t.Errorf("stop while idle: %+v", got)
	}
	role := api.RoleProxy
	if got := m.Handle(api.Request{Command: api.CmdStop, Role: &role}); got.Status != api.StatusError {
		t.Errorf("role-scoped stop should error: %+v", got)
	}
	if got := m.Handle(api.Request{Command: "bogus"}); got.Status != api.StatusError {
		t.Errorf("unknown verb should error: %+v", got)
	}
	if ev := m.Snapshot(); ev.Event != api.EventState || ev.Entries != nil || ev.Active != nil {
		t.Errorf("idle snapshot: %+v", ev)
	}
}

// emptyManager is a manager over a FRESH store dir (no profiles yet).
func emptyManager(t *testing.T) *manager {
	t.Helper()
	m := newManager(store.OpenAt(t.TempDir()))
	m.hub = ipc.NewHub(m.Snapshot)
	m.socksPort = freePort(t)
	return m
}

func strPtr(s string) *string { return &s }

// TestManagerSubscriptionGate is the G3 gate, daemon-side: add a subscription
// → list its nodes → activate a node from it → refresh preserves prefs. The
// subscription serves xhttp-only nodes so the session needs no native uTLS
// (runs untagged); REAL cores start in SOCKS mode, nothing is dialed.
func TestManagerSubscriptionGate(t *testing.T) {
	const (
		linkA  = "vless://88f2c0dc-a8e3-49f4-89b9-b3b54f1cad3a@203.0.113.1:443?security=reality&sni=g.com&fp=chrome&pbk=MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY&sid=01ab&type=xhttp&mode=auto#sub-a"
		linkA2 = "vless://88f2c0dc-a8e3-49f4-89b9-b3b54f1cad3a@203.0.113.1:443?security=reality&sni=g.com&fp=chrome&pbk=MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY&sid=01ab&type=xhttp&mode=auto#sub-a-renamed"
		linkB  = "vless://99f2c0dc-a8e3-49f4-89b9-b3b54f1cad3a@203.0.113.2:443?security=reality&sni=g.com&fp=chrome&pbk=MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY&sid=01ab&type=xhttp#sub-b"
		linkC  = "vless://aaf2c0dc-a8e3-49f4-89b9-b3b54f1cad3a@203.0.113.3:443?security=reality&sni=g.com&fp=chrome&pbk=MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY&sid=01ab&type=xhttp#sub-c"
	)
	body := linkA + "\n" + linkB
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, body)
	}))
	defer srv.Close()

	m := emptyManager(t)
	if got := m.Handle(api.Request{Command: api.CmdCreateProfile, Profile: strPtr("main")}); got.Status != api.StatusOk {
		t.Fatalf("create_profile: %+v", got)
	}
	if got := m.Handle(api.Request{Command: api.CmdSetActiveProfile, Profile: strPtr("main")}); got.Status != api.StatusOk {
		t.Fatalf("set_active_profile: %+v", got)
	}

	// Add: stores the subscription AND runs the initial fetch.
	add := m.Handle(api.Request{Command: api.CmdAddSub, URL: strPtr(srv.URL), Name: strPtr("prov")})
	if add.Status != api.StatusRefreshed || len(add.Refreshed) != 1 || add.Refreshed[0].Count != 2 {
		t.Fatalf("add_subscription: %+v", add)
	}

	subs := m.Handle(api.Request{Command: api.CmdListSubs})
	if subs.Status != api.StatusSubscriptions || len(subs.Subscriptions) != 1 || subs.Subscriptions[0].NodeCount != 2 {
		t.Fatalf("list_subscriptions: %+v", subs)
	}

	nodes := m.Handle(api.Request{Command: api.CmdListNodes})
	if nodes.Status != api.StatusNodes || len(nodes.Nodes) != 2 {
		t.Fatalf("list_nodes: %+v", nodes)
	}
	if !nodes.Nodes[0].Active {
		t.Error("the subscription's first node must be elected active")
	}

	// Pin a core on sub-a, then activate the profile (node from the sub).
	pin := api.CoreXray
	if got := m.Handle(api.Request{Command: api.CmdSetNodePrefs, Node: strPtr("sub-a"), CoreOverride: &pin}); got.Status != api.StatusOk {
		t.Fatalf("set_node_prefs: %+v", got)
	}
	if got := m.Handle(api.Request{Command: api.CmdActivate}); got.Status != api.StatusActivated {
		t.Fatalf("activate from subscription: %+v", got)
	}
	defer m.Handle(api.Request{Command: api.CmdStop})

	// Provider mutates: sub-a renamed (same identity), sub-b dropped, sub-c new.
	body = linkA2 + "\n" + linkC
	ref := m.Handle(api.Request{Command: api.CmdRefreshSubs})
	if ref.Status != api.StatusRefreshed || len(ref.Refreshed) != 1 {
		t.Fatalf("refresh: %+v", ref)
	}
	if r := ref.Refreshed[0]; r.Count != 2 || r.Added != 1 || r.Removed != 1 || r.Error != "" {
		t.Fatalf("refresh outcome: %+v", r)
	}

	nodes = m.Handle(api.Request{Command: api.CmdListNodes})
	byName := map[string]api.NodeInfo{}
	for _, n := range nodes.Nodes {
		byName[n.Name] = n
	}
	if _, gone := byName["sub-b"]; gone {
		t.Error("dropped node must be gone after refresh")
	}
	renamed, ok := byName["sub-a-renamed"]
	if !ok {
		t.Fatalf("renamed node missing: %v", byName)
	}
	if renamed.CoreOverride == nil || *renamed.CoreOverride != api.CoreXray {
		t.Error("core override must survive the refresh (G3 gate: prefs preserved)")
	}
	if fresh, ok := byName["sub-c"]; !ok || fresh.CoreOverride != nil {
		t.Error("new node must exist with no override")
	}

	// remove_subscription drops the sub and its nodes.
	if got := m.Handle(api.Request{Command: api.CmdRemoveSub, Sub: strPtr("prov")}); got.Status != api.StatusOk {
		t.Fatalf("remove_subscription: %+v", got)
	}
	nodes = m.Handle(api.Request{Command: api.CmdListNodes})
	if len(nodes.Nodes) != 0 {
		t.Errorf("nodes after sub removal = %d, want 0", len(nodes.Nodes))
	}
}

// TestRoutingVerbsCRUD: upsert (insert + in-place edit by id) / list / select
// / remove through the wire verbs — no session, store only.
func TestRoutingVerbsCRUD(t *testing.T) {
	m := emptyManager(t)
	if got := m.Handle(api.Request{Command: api.CmdCreateProfile, Profile: strPtr("main")}); got.Status != api.StatusOk {
		t.Fatalf("create_profile: %+v", got)
	}
	if got := m.Handle(api.Request{Command: api.CmdSetActiveProfile, Profile: strPtr("main")}); got.Status != api.StatusOk {
		t.Fatalf("set_active_profile: %+v", got)
	}

	// A fresh profile carries the active "default" routing.
	lr := m.Handle(api.Request{Command: api.CmdListRouting})
	if lr.Status != api.StatusRouting || len(lr.Routing) != 1 || lr.Routing[0].Name != "default" || !lr.Routing[0].Active {
		t.Fatalf("list_routing on a fresh profile: %+v", lr)
	}

	// Insert "basic" (no id -> fresh one).
	basic := json.RawMessage(`{
	  "name": "basic",
	  "rule_sets": [],
	  "rules": [{"target": "DefaultProxy", "domain_keyword": ["example.com"]}],
	  "default_target": "Direct"
	}`)
	if got := m.Handle(api.Request{Command: api.CmdUpsertRouting, RoutingConfig: basic}); got.Status != api.StatusOk {
		t.Fatalf("upsert insert: %+v", got)
	}
	lr = m.Handle(api.Request{Command: api.CmdListRouting})
	if len(lr.Routing) != 2 || lr.Routing[1].Name != "basic" || lr.Routing[1].Rules != 1 {
		t.Fatalf("list after insert: %+v", lr.Routing)
	}
	basicID := lr.Routing[1].ID

	// Duplicate NAME with a different id is rejected.
	if got := m.Handle(api.Request{Command: api.CmdUpsertRouting, RoutingConfig: basic}); got.Status != api.StatusError {
		t.Errorf("duplicate-name insert must be rejected: %+v", got)
	}

	// get_routing returns the full document (by name or id) — the read half
	// of upsert; an unknown ref is an error.
	gr := m.Handle(api.Request{Command: api.CmdGetRouting, Routing: strPtr("basic")})
	if gr.Status != api.StatusRoutingConfig {
		t.Fatalf("get_routing: %+v", gr)
	}
	var fetched routing.Config
	if err := json.Unmarshal(gr.RoutingConfig, &fetched); err != nil {
		t.Fatalf("get_routing payload does not decode: %v", err)
	}
	if fetched.ID != basicID || fetched.Name != "basic" || len(fetched.Rules) != 1 {
		t.Fatalf("get_routing returned the wrong config: %+v", fetched)
	}
	if got := m.Handle(api.Request{Command: api.CmdGetRouting, Routing: strPtr("ghost")}); got.Status != api.StatusError {
		t.Errorf("get_routing of unknown routing must error: %+v", got)
	}

	// Select, then edit IN PLACE by id — the active selection must survive.
	if got := m.Handle(api.Request{Command: api.CmdSelectRouting, Routing: strPtr("basic")}); got.Status != api.StatusOk {
		t.Fatalf("select_routing: %+v", got)
	}
	edited := json.RawMessage(`{
	  "id": "` + basicID + `",
	  "name": "basic",
	  "rule_sets": [],
	  "rules": [],
	  "default_target": "DefaultProxy"
	}`)
	if got := m.Handle(api.Request{Command: api.CmdUpsertRouting, RoutingConfig: edited}); got.Status != api.StatusOk {
		t.Fatalf("upsert edit: %+v", got)
	}
	lr = m.Handle(api.Request{Command: api.CmdListRouting})
	if len(lr.Routing) != 2 || lr.Routing[1].Rules != 0 || !lr.Routing[1].Active || lr.Routing[1].ID != basicID {
		t.Fatalf("edit must keep id + active selection: %+v", lr.Routing)
	}

	// Remove clears the active selection.
	if got := m.Handle(api.Request{Command: api.CmdRemoveRouting, Routing: strPtr(basicID)}); got.Status != api.StatusOk {
		t.Fatalf("remove_routing: %+v", got)
	}
	lr = m.Handle(api.Request{Command: api.CmdListRouting})
	if len(lr.Routing) != 1 || lr.Routing[0].Active {
		t.Fatalf("list after remove: %+v", lr.Routing)
	}
	if got := m.Handle(api.Request{Command: api.CmdRemoveRouting, Routing: strPtr("ghost")}); got.Status != api.StatusError {
		t.Errorf("remove of unknown routing must error: %+v", got)
	}
}

// TestManagerTestLatency: probes run in-process per node (ephemeral xray) —
// the fixture's TEST-NET nodes are unreachable, so every latency is nil but
// the request succeeds, order and names preserved; unknown names are skipped
// and an empty resolved set errors. probeTimeout is shortened for the test.
func TestManagerTestLatency(t *testing.T) {
	m := fixtureManager(t)
	m.probeTimeout = 400 * time.Millisecond

	resp := m.Handle(api.Request{Command: api.CmdTestLatency})
	if resp.Status != api.StatusLatencies || len(resp.Latencies) != 3 {
		t.Fatalf("test_latency all: %+v", resp)
	}
	for i, want := range []string{"node-a", "node-b", "node-c"} {
		if resp.Latencies[i].Node != want {
			t.Errorf("latencies[%d].Node = %q, want %q", i, resp.Latencies[i].Node, want)
		}
		if resp.Latencies[i].LatencyMs != nil {
			t.Errorf("TEST-NET probe must yield nil latency, got %v", *resp.Latencies[i].LatencyMs)
		}
	}

	names := []string{"node-b", "ghost"}
	resp = m.Handle(api.Request{Command: api.CmdTestLatency, Nodes: &names})
	if resp.Status != api.StatusLatencies || len(resp.Latencies) != 1 || resp.Latencies[0].Node != "node-b" {
		t.Fatalf("explicit names (unknown skipped): %+v", resp)
	}

	empty := []string{"ghost"}
	if got := m.Handle(api.Request{Command: api.CmdTestLatency, Nodes: &empty}); got.Status != api.StatusError {
		t.Errorf("no resolvable nodes must error: %+v", got)
	}
}

// TestManagerDiagnose: the diagnose verb resolves a node and runs the staged
// diagnosis. The node points at a CLOSED local port (connection refused,
// deterministic), so the tcp stage owns the failure and the proxy stage does
// not run; no-node uses the active node; an unknown name errors.
func TestManagerDiagnose(t *testing.T) {
	m := emptyManager(t)
	if got := m.Handle(api.Request{Command: api.CmdCreateProfile, Profile: strPtr("main")}); got.Status != api.StatusOk {
		t.Fatalf("create_profile: %+v", got)
	}
	if got := m.Handle(api.Request{Command: api.CmdSetActiveProfile, Profile: strPtr("main")}); got.Status != api.StatusOk {
		t.Fatalf("set_active_profile: %+v", got)
	}
	closedPort := freePort(t) // freePort closed its listener → connect is refused
	url := fmt.Sprintf("vless://88f2c0dc-a8e3-49f4-89b9-b3b54f1cad3a@127.0.0.1:%d#dead-node", closedPort)
	if got := m.Handle(api.Request{Command: api.CmdAddNode, URL: &url}); got.Status != api.StatusOk {
		t.Fatalf("add_node: %+v", got)
	}

	resp := m.Handle(api.Request{Command: api.CmdDiagnose, Node: strPtr("dead-node")})
	if resp.Status != api.StatusDiagnosis || resp.Diagnosis == nil {
		t.Fatalf("diagnose: %+v", resp)
	}
	d := resp.Diagnosis
	if d.Node != "dead-node" || d.OK {
		t.Fatalf("refused endpoint must not pass: %+v", d)
	}
	if d.FailedStage != string(engine.StageTCP) {
		t.Errorf("refused endpoint must fail at tcp, got %q: %+v", d.FailedStage, d.Stages)
	}
	// config passes (the node is well-formed), then the tcp stage runs and fails.
	if len(d.Stages) != 2 || d.Stages[0].Stage != string(engine.StageConfig) || !d.Stages[0].OK {
		t.Errorf("config must pass before the tcp failure: %+v", d.Stages)
	}
	if d.Stages[1].Stage != string(engine.StageTCP) || d.Stages[1].OK || d.Stages[1].Error == "" {
		t.Errorf("the tcp stage must run and carry an error: %+v", d.Stages)
	}

	// No node named → the profile's active node (the only node).
	if got := m.Handle(api.Request{Command: api.CmdDiagnose}); got.Status != api.StatusDiagnosis || got.Diagnosis.Node != "dead-node" {
		t.Errorf("diagnose without a node must use the active node: %+v", got)
	}

	if got := m.Handle(api.Request{Command: api.CmdDiagnose, Node: strPtr("ghost")}); got.Status != api.StatusError {
		t.Errorf("unknown node must error: %+v", got)
	}
}

func TestManagerActivateUnknownNamesError(t *testing.T) {
	m := fixtureManager(t)
	profile := "ghost"
	if got := m.Handle(api.Request{Command: api.CmdActivate, Profile: &profile}); got.Status != api.StatusError {
		t.Errorf("activate unknown profile should error: %+v", got)
	}
	node := "ghost-node"
	if got := m.Handle(api.Request{Command: api.CmdActivate, Node: &node}); got.Status != api.StatusError {
		t.Errorf("activate unknown node should error: %+v", got)
	}
}
