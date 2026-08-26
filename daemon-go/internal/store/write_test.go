package store

import (
	"encoding/json"

	"ironlink/daemon/internal/api"
	"ironlink/daemon/internal/routing"
	"ironlink/daemon/internal/uuid"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"ironlink/daemon/internal/proxy"
)

// mustCompactJSON marshals v compactly — the semantic-equality form for
// models carrying raw JSON blocks (routing rule conditions, xhttp extra)
// whose whitespace the pretty-printed save normalizes by design
// (encoding/json compacts RawMessage contents on marshal).
func mustCompactJSON(t *testing.T, v any) string {
	t.Helper()
	j, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(j)
}

// TestSaveLoadRoundTrip: load the v2 fixture (incl. a NON-TRIVIAL
// routing_configs block the Go side does not interpret), save it back, load
// again — the reloaded model must equal the loaded one, and the saved file
// must keep the schema-v2 Go-native shape (no Rust enum tags creep back).
func TestSaveLoadRoundTrip(t *testing.T) {
	s := fixtureStore(t)
	p1, err := s.LoadProfile("main")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := s.SaveProfile("copy", p1); err != nil {
		t.Fatalf("save: %v", err)
	}
	p2, err := s.LoadProfile("copy")
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	// The nodes (the part the v1→v2 migration rewrites) deep-equal; the whole
	// model compares via compact re-marshal because the routing raw blocks are
	// whitespace-normalized by the pretty-printed save.
	if !reflect.DeepEqual(p1.Nodes, p2.Nodes) {
		t.Errorf("nodes drifted:\n p1: %+v\n p2: %+v", p1.Nodes, p2.Nodes)
	}
	if j1, j2 := mustCompactJSON(t, p1), mustCompactJSON(t, p2); j1 != j2 {
		t.Errorf("round trip drifted:\n p1: %s\n p2: %s", j1, j2)
	}
	// The typed routing block must keep the rule content verbatim.
	rj, _ := json.Marshal(p2.RoutingConfigs)
	if !strings.Contains(string(rj), "ads.example.com") {
		t.Errorf("routing rule content lost: %s", rj)
	}

	// The saved file is Go-native v2, not the Rust serde shape.
	raw, err := os.ReadFile(filepath.Join(s.baseDir, "profiles", "copy.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		SchemaVersion uint32 `json:"schema_version"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.SchemaVersion != 2 {
		t.Errorf("saved schema_version = %d, want 2", doc.SchemaVersion)
	}
	if !strings.Contains(string(raw), `"vless"`) {
		t.Errorf("saved file lacks the Go-native \"vless\" key:\n%s", raw)
	}
	for _, rustTag := range []string{`"Vless"`, `"Tcp"`, `"None"`, `"Reality"`, `"Xhttp"`} {
		if strings.Contains(string(raw), rustTag) {
			t.Errorf("saved file still carries the Rust enum tag %s:\n%s", rustTag, raw)
		}
	}
}

// TestNativeRoundTripEveryVariant locks the Go-native encode/decode: a
// profile built in code with EVERY union variant (tcp/ws/grpc/xhttp,
// none/tls/reality, flow set and empty, xhttp extra set and nil) must
// survive save → reload, and the file is 0600.
func TestNativeRoundTripEveryVariant(t *testing.T) {
	dir := t.TempDir()
	s := OpenAt(dir)

	nodes := []*proxy.VlessConfig{
		{ServerName: "tcp none flow", UUID: "u1", Address: "198.51.100.1", Port: 443,
			Encryption: "none", Flow: "xtls-rprx-vision",
			Security:  proxy.Security{Kind: proxy.SecurityNone},
			Transport: proxy.Transport{Kind: proxy.TransportTCP}},
		{ServerName: "ws tls", UUID: "u2", Address: "cdn.example.com", Port: 443,
			Encryption: "none",
			Security:   proxy.Security{Kind: proxy.SecurityTLS, TLS: &proxy.TLSParams{SNI: "cdn.example.com", Fp: "chrome"}},
			Transport:  proxy.Transport{Kind: proxy.TransportWs, Ws: &proxy.WsParams{Path: "/ws", Host: "cdn.example.com"}}},
		{ServerName: "grpc reality", UUID: "u3", Address: "198.51.100.3", Port: 8443,
			Encryption: "none",
			Security:   proxy.Security{Kind: proxy.SecurityReality, Reality: &proxy.RealityParams{SNI: "g.com", Fp: "chrome", Pbk: "PBK", Sid: "01ab"}},
			Transport:  proxy.Transport{Kind: proxy.TransportGrpc, Grpc: &proxy.GrpcParams{ServiceName: "svc"}}},
		{ServerName: "xhttp extra", UUID: "u4", Address: "198.51.100.4", Port: 443,
			Encryption: "none",
			Security:   proxy.Security{Kind: proxy.SecurityTLS, TLS: &proxy.TLSParams{}},
			Transport: proxy.Transport{Kind: proxy.TransportXhttp, Xhttp: &proxy.XhttpParams{
				Path: "/x", Host: "h.example.com", Mode: "auto",
				Extra: json.RawMessage(`{"xmux":{"maxConcurrency":16}}`)}}},
		{ServerName: "xhttp no extra", UUID: "u5", Address: "198.51.100.5", Port: 443,
			Encryption: "none",
			Security:   proxy.Security{Kind: proxy.SecurityReality, Reality: &proxy.RealityParams{SNI: "g.com", Fp: "chrome", Pbk: "PBK", Sid: "01ab"}},
			Transport:  proxy.Transport{Kind: proxy.TransportXhttp, Xhttp: &proxy.XhttpParams{Path: "/y", Mode: "auto"}}},
	}
	p := NewProfile("fresh")
	for _, v := range nodes {
		p.AddNode(NewNode(v, nil))
	}
	if _, err := p.AddSubscription(NewSubscription("https://s.example.com/a", "s1")); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveProfile("fresh", p); err != nil {
		t.Fatalf("save: %v", err)
	}

	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(dir, "profiles", "fresh.json"))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("profile mode = %o, want 0600", info.Mode().Perm())
		}
	}

	p2, err := s.LoadProfile("fresh")
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if j1, j2 := mustCompactJSON(t, p), mustCompactJSON(t, p2); j1 != j2 {
		t.Errorf("native round trip drifted:\n saved:    %s\n reloaded: %s", j1, j2)
	}
	// Spot-check the variants the compact comparison could mask.
	if got := p2.Nodes[0].Vless(); got.Flow != "xtls-rprx-vision" || got.Security.Kind != proxy.SecurityNone || got.Transport.Kind != proxy.TransportTCP {
		t.Errorf("tcp/none/flow node drifted: %+v", got)
	}
	if got := p2.Nodes[1].Vless(); got.Flow != "" || got.Security.TLS == nil || got.Transport.Ws == nil {
		t.Errorf("ws/tls node drifted: %+v", got)
	}
	if got := p2.Nodes[2].Vless(); got.Security.Reality == nil || got.Transport.Grpc == nil || got.Transport.Grpc.ServiceName != "svc" {
		t.Errorf("grpc/reality node drifted: %+v", got)
	}
	if got := p2.Nodes[3].Vless().Transport.Xhttp; got == nil || !strings.Contains(string(got.Extra), "maxConcurrency") {
		t.Errorf("xhttp extra lost: %+v", got)
	}
	if got := p2.Nodes[4].Vless().Transport.Xhttp; got == nil || got.Extra != nil {
		t.Errorf("absent xhttp extra must reload as nil: %+v", got)
	}
}

func TestCreateListSetActiveDelete(t *testing.T) {
	s := OpenAt(t.TempDir())

	if err := s.CreateProfile("alpha"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.CreateProfile("alpha"); err == nil {
		t.Error("duplicate create must be rejected")
	}
	if err := s.CreateProfile("beta"); err != nil {
		t.Fatal(err)
	}

	names, err := s.ListProfiles()
	if err != nil || !slices.Equal(names, []string{"alpha", "beta"}) {
		t.Errorf("ListProfiles = %v, %v", names, err)
	}

	if err := s.SetActiveProfile("ghost"); err == nil {
		t.Error("set_active of a missing profile must be rejected")
	}
	if err := s.SetActiveProfile("beta"); err != nil {
		t.Fatal(err)
	}
	if active, _ := s.ActiveProfileName(); active != "beta" {
		t.Errorf("active = %q, want beta", active)
	}

	if err := s.DeleteProfile("beta"); err != nil {
		t.Fatal(err)
	}
	if active, _ := s.ActiveProfileName(); active != "" {
		t.Errorf("deleting the active profile must clear the selection, got %q", active)
	}
	if names, _ := s.ListProfiles(); !slices.Equal(names, []string{"alpha"}) {
		t.Errorf("ListProfiles after delete = %v", names)
	}
}

func TestProfileMutators(t *testing.T) {
	p := NewProfile("m")
	v1 := &proxy.VlessConfig{ServerName: "n1", UUID: "u1", Address: "1.1.1.1", Port: 443,
		Encryption: "none", Security: proxy.Security{Kind: proxy.SecurityNone},
		Transport: proxy.Transport{Kind: proxy.TransportTCP}}
	v2 := &proxy.VlessConfig{ServerName: "n2", UUID: "u2", Address: "2.2.2.2", Port: 443,
		Encryption: "none", Security: proxy.Security{Kind: proxy.SecurityNone},
		Transport: proxy.Transport{Kind: proxy.TransportTCP}}

	// First node becomes active.
	id1 := p.AddNode(NewNode(v1, nil))
	if p.ActiveNodeID == nil || *p.ActiveNodeID != id1 {
		t.Errorf("first node must become active")
	}
	p.AddNode(NewNode(v2, nil))

	// Removing the active node clears the selection.
	if !p.RemoveNodeByID(id1) {
		t.Fatal("remove must report true")
	}
	if p.ActiveNodeID != nil {
		t.Error("removing the active node must clear the selection")
	}

	// Subscriptions: duplicate URL rejected.
	sub := NewSubscription("https://x.example.com", "x")
	if _, err := p.AddSubscription(sub); err != nil {
		t.Fatal(err)
	}
	if _, err := p.AddSubscription(NewSubscription("https://x.example.com", "other")); err == nil {
		t.Error("duplicate subscription URL must be rejected")
	}

	// Replace: drops only the subscription's nodes, elects a default active.
	subNode := NewNode(v1, &sub.ID)
	p.ReplaceSubscriptionNodes(sub.ID, []Node{subNode})
	if len(p.Nodes) != 2 {
		t.Fatalf("nodes = %d, want manual n2 + sub n1", len(p.Nodes))
	}
	if p.ActiveNodeID == nil {
		t.Error("replace must elect an active node when none is set")
	}
	replacement := NewNode(v2, &sub.ID)
	p.ReplaceSubscriptionNodes(sub.ID, []Node{replacement})
	if p.FindNodeByID(subNode.ID) != nil || p.FindNodeByID(replacement.ID) == nil {
		t.Error("replace must swap the subscription's nodes")
	}

	// RemoveSubscription drops the sub and its nodes.
	if !p.RemoveSubscription(sub.ID) {
		t.Fatal("remove subscription must report true")
	}
	if len(p.Subscriptions) != 0 || p.FindNodeByID(replacement.ID) != nil {
		t.Error("removing a subscription must drop its nodes")
	}
}

// TestGroupNodeRoundTrip: a group node serializes with a null profile + a group
// block, survives a JSON round-trip with its spec intact, and reports IsGroup /
// DisplayName / nil Profile() correctly (no panic on the missing proxy).
func TestGroupNodeRoundTrip(t *testing.T) {
	sub := "s1"
	g := NewGroupNode(GroupSpec{
		Name:    "Auto",
		Members: []string{"m1", "m2"},
		Probe:   GroupProbe{URL: "https://example.com/204", IntervalSec: 180, Tolerance: 150},
	}, &sub)

	if !g.IsGroup() {
		t.Fatal("NewGroupNode must be a group")
	}
	if g.DisplayName() != "Auto" {
		t.Errorf("DisplayName = %q, want Auto", g.DisplayName())
	}
	if g.Profile() != nil {
		t.Errorf("a group node has no proxy profile, got %+v", g.Profile())
	}

	raw, err := json.Marshal(g)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"profile":null`) {
		t.Errorf("group node must serialize a null profile: %s", raw)
	}
	if !strings.Contains(string(raw), `"group":`) {
		t.Errorf("group node must serialize a group block: %s", raw)
	}

	var back Node
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if !back.IsGroup() || back.DisplayName() != "Auto" {
		t.Fatalf("round-trip lost the group: %+v", back)
	}
	if !reflect.DeepEqual(back.Group.Members, []string{"m1", "m2"}) {
		t.Errorf("members = %v, want [m1 m2]", back.Group.Members)
	}
	if back.Group.Probe.IntervalSec != 180 || back.Group.Probe.Tolerance != 150 {
		t.Errorf("probe = %+v", back.Group.Probe)
	}
	if back.SubID == nil || *back.SubID != "s1" {
		t.Errorf("sub id lost: %+v", back.SubID)
	}
}

// TestFindNodeByNameNodeBeatsGroup: a dialable node wins a name tie with a group
// STRUCTURALLY (two-pass), independent of store order — the group is stored
// FIRST here, yet the dialable node still wins.
func TestFindNodeByNameNodeBeatsGroup(t *testing.T) {
	p := NewProfile("m")
	p.AddNode(NewGroupNode(GroupSpec{Name: "Auto", Members: []string{"x"}}, nil))
	v := &proxy.VlessConfig{ServerName: "Auto", UUID: "u", Address: "1.1.1.1", Port: 443,
		Encryption: "none", Security: proxy.Security{Kind: proxy.SecurityNone},
		Transport: proxy.Transport{Kind: proxy.TransportTCP}}
	nodeID := p.AddNode(NewNode(v, nil))

	got := p.FindNodeByName("Auto")
	if got == nil || got.IsGroup() || got.ID != nodeID {
		t.Fatalf("FindNodeByName(Auto) = %+v, want the dialable node %s", got, nodeID)
	}
}

// TestGroupCRUD: a user group is added (SubID nil), edited in place (id kept),
// and a provider group / dialable node cannot be edited as a user group.
func TestGroupCRUD(t *testing.T) {
	p := NewProfile("m")
	id, err := p.AddGroup(GroupSpec{Name: "Auto", Members: []string{"a", "b"}})
	if err != nil {
		t.Fatal(err)
	}
	if g := p.FindNodeByID(id); g == nil || !g.IsGroup() || g.SubID != nil {
		t.Fatalf("user group not added as SubID-nil group: %+v", g)
	}

	if err := p.ReplaceGroupByID(id, GroupSpec{Name: "Auto2", Members: []string{"c"}}); err != nil {
		t.Fatal(err)
	}
	if g := p.FindNodeByID(id); g == nil || g.DisplayName() != "Auto2" || len(g.Group.Members) != 1 {
		t.Errorf("edit lost or churned the id: %+v", g)
	}

	// A provider group (SubID set) cannot be edited.
	sub := "s1"
	pg := NewGroupNode(GroupSpec{Name: "Prov"}, &sub)
	p.Nodes = append(p.Nodes, pg)
	if err := p.ReplaceGroupByID(pg.ID, GroupSpec{Name: "X", Members: []string{"a"}}); err == nil {
		t.Error("editing a provider group must be refused")
	}

	// A dialable node id is not a group.
	v := &proxy.VlessConfig{ServerName: "n", UUID: "u", Address: "1.1.1.1", Port: 443,
		Encryption: "none", Security: proxy.Security{Kind: proxy.SecurityNone},
		Transport: proxy.Transport{Kind: proxy.TransportTCP}}
	nid := p.AddNode(NewNode(v, nil))
	if err := p.ReplaceGroupByID(nid, GroupSpec{Name: "Y", Members: []string{"a"}}); err == nil {
		t.Error("editing a dialable node as a group must be refused")
	}

	if _, err := p.AddGroup(GroupSpec{Name: "", Members: []string{"a"}}); err == nil {
		t.Error("an empty group name must be refused")
	}
}

// TestSetActiveNodeIfUnsetSkipsGroups: a group added first must NOT become the
// silent auto-default; the first DIALABLE node does.
func TestSetActiveNodeIfUnsetSkipsGroups(t *testing.T) {
	p := NewProfile("m")
	if _, err := p.AddGroup(GroupSpec{Name: "Auto", Members: []string{"x"}}); err != nil {
		t.Fatal(err)
	}
	if p.ActiveNodeID != nil {
		t.Fatalf("adding a group must not elect it active: %v", p.ActiveNodeID)
	}
	v := &proxy.VlessConfig{ServerName: "n1", UUID: "u1", Address: "1.1.1.1", Port: 443,
		Encryption: "none", Security: proxy.Security{Kind: proxy.SecurityNone},
		Transport: proxy.Transport{Kind: proxy.TransportTCP}}
	nid := p.AddNode(NewNode(v, nil))
	if p.ActiveNodeID == nil || *p.ActiveNodeID != nid {
		t.Errorf("active = %v, want the dialable node %s (never the group)", p.ActiveNodeID, nid)
	}
}

func TestRoutingMutators(t *testing.T) {
	p := NewProfile("m")
	if p.ActiveRouting() == nil || p.ActiveRouting().Name != "default" {
		t.Fatalf("fresh profile must select the default routing: %+v", p.ActiveRoutingID)
	}

	rc := routing.Config{Name: "basic", DefaultTarget: routing.RuleTarget{Kind: routing.TargetDirect}}
	id, err := p.AddRouting(rc)
	if err != nil || id == "" {
		t.Fatalf("AddRouting: %v", err)
	}
	if _, err := p.AddRouting(routing.Config{Name: "basic", DefaultTarget: routing.RuleTarget{Kind: routing.TargetDirect}}); err == nil {
		t.Error("duplicate routing name must be rejected")
	}
	if _, err := p.AddRouting(routing.Config{Name: "bad name!", DefaultTarget: routing.RuleTarget{Kind: routing.TargetDirect}}); err == nil {
		t.Error("invalid routing name must be rejected")
	}

	// Replace keeps the slot id (active selection stays valid).
	if err := p.SetActiveRouting(id); err != nil {
		t.Fatal(err)
	}
	edited := routing.Config{ID: "ignored", Name: "basic", DefaultTarget: routing.RuleTarget{Kind: routing.TargetBlock}}
	if err := p.ReplaceRoutingByID(id, edited); err != nil {
		t.Fatalf("ReplaceRoutingByID: %v", err)
	}
	if got := p.ActiveRouting(); got == nil || got.ID != id || got.DefaultTarget.Kind != routing.TargetBlock {
		t.Errorf("replace must keep the id and apply the edit: %+v", got)
	}
	if err := p.ReplaceRoutingByID(id, routing.Config{Name: "default", DefaultTarget: routing.RuleTarget{Kind: routing.TargetDirect}}); err == nil {
		t.Error("replace must reject a name collision with a DIFFERENT config")
	}

	// FindRouting by id and by name.
	if p.FindRouting("basic") == nil || p.FindRouting(id) == nil || p.FindRouting("ghost") != nil {
		t.Error("FindRouting by id/name wrong")
	}

	// Remove clears the active selection.
	if !p.RemoveRouting(id) {
		t.Fatal("remove must report true")
	}
	if p.ActiveRoutingID != nil {
		t.Error("removing the active routing must clear the selection")
	}
}

func TestLastSessionRoundTripAndClear(t *testing.T) {
	s := OpenAt(t.TempDir())

	// No record yet → nil, no error.
	if ctx, err := s.LoadLastSession(); ctx != nil || err != nil {
		t.Fatalf("empty load = %+v, %v", ctx, err)
	}

	profile, node, routing := "main", "Grimnir", "basic"
	want := api.PersistedEntry{Profile: &profile, Node: &node, Routing: &routing, Tun: true}
	if err := s.SaveLastSession(want); err != nil {
		t.Fatal(err)
	}

	// The on-disk shape matches the Rust LastSession ({"context": {...}}).
	raw, err := os.ReadFile(filepath.Join(s.baseDir, "last_session.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	ctxDoc, ok := doc["context"].(map[string]any)
	if !ok || ctxDoc["profile"] != "main" || ctxDoc["tun"] != true {
		t.Errorf("last_session shape wrong: %s", raw)
	}

	got, err := s.LoadLastSession()
	if err != nil || got == nil || *got.Profile != profile || *got.Node != node || !got.Tun {
		t.Fatalf("load = %+v, %v", got, err)
	}

	if err := s.ClearLastSession(); err != nil {
		t.Fatal(err)
	}
	if ctx, _ := s.LoadLastSession(); ctx != nil {
		t.Error("clear must remove the record")
	}
	if err := s.ClearLastSession(); err != nil {
		t.Errorf("double clear must be a no-op, got %v", err)
	}
}

func TestNewUUIDShape(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		id := uuid.New()
		if len(id) != 36 || id[14] != '4' {
			t.Fatalf("bad uuid v4: %q", id)
		}
		if seen[id] {
			t.Fatalf("uuid collision: %q", id)
		}
		seen[id] = true
	}
}
