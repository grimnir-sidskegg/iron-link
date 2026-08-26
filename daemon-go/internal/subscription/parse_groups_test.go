package subscription

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"ironlink/daemon/internal/store"
)

// findGroup returns the extracted group with the given name, failing if absent.
func findGroup(t *testing.T, o *Outcome, name string) GroupOut {
	t.Helper()
	for _, g := range o.Groups {
		if g.Name == name {
			return g
		}
	}
	t.Fatalf("group %q not extracted; groups = %+v", name, o.Groups)
	return GroupOut{}
}

func nodeByAddr(o *Outcome, addr string) bool {
	for i := range o.Nodes {
		if o.Nodes[i].Profile().ServerAddress() == addr {
			return true
		}
	}
	return false
}

// TestParseXrayBalancerGroups: a balancer entry becomes a group whose members
// are the outbounds its selector prefix-matches; the balancer entry STILL emits
// its member nodes (a balancer-only server, present in no dedicated entry, must
// exist as a node); the probe interval is decoded from the duration string.
func TestParseXrayBalancerGroups(t *testing.T) {
	o, err := Parse(readFixture(t, "xray.json"), "s", FormatAuto)
	if err != nil {
		t.Fatal(err)
	}

	auto := findGroup(t, o, "Auto")
	if len(auto.MemberKeys) != 3 {
		t.Errorf("Auto members = %d, want 3 (proxy-ams/fra/nyc)", len(auto.MemberKeys))
	}
	if auto.Probe.URL != "https://example.com/generate_204" || auto.Probe.IntervalSec != 90 {
		t.Errorf("Auto probe = %+v, want example.com/generate_204 @ 90s", auto.Probe)
	}
	if eu := findGroup(t, o, "Europe"); len(eu.MemberKeys) != 3 {
		t.Errorf("Europe members = %d, want 3", len(eu.MemberKeys))
	}
	// proxy-nyc (198.51.100.12) and proxy-vie (198.51.100.13) appear only inside
	// a balancer entry — they must still be collected as nodes.
	if !nodeByAddr(o, "198.51.100.12") || !nodeByAddr(o, "198.51.100.13") {
		t.Error("a balancer-only member must also be collected as a dialable node")
	}
}

// TestParseSingBoxUrltestGroup: a sing-box urltest outbound becomes a group over
// its named members, with the url/interval carried across.
func TestParseSingBoxUrltestGroup(t *testing.T) {
	o, err := Parse(readFixture(t, "singbox.json"), "s", FormatAuto)
	if err != nil {
		t.Fatal(err)
	}
	g := findGroup(t, o, "Fastest")
	if len(g.MemberKeys) != 2 {
		t.Errorf("Fastest members = %d, want 2 (Stockholm Reality, Oslo SS)", len(g.MemberKeys))
	}
	if g.Probe.URL != "https://www.example.com/generate_204" || g.Probe.IntervalSec != 300 {
		t.Errorf("Fastest probe = %+v, want www.example.com/generate_204 @ 300s (5m)", g.Probe)
	}
}

// TestParseClashUrltestGroup: a clash url-test proxy-group becomes a group over
// its explicit members, interval in seconds.
func TestParseClashUrltestGroup(t *testing.T) {
	o, err := Parse(readFixture(t, "clash.yaml"), "s", FormatAuto)
	if err != nil {
		t.Fatal(err)
	}
	g := findGroup(t, o, "auto")
	if len(g.MemberKeys) != 2 {
		t.Errorf("auto members = %d, want 2 (US Basic, WS VMess)", len(g.MemberKeys))
	}
	if g.Probe.IntervalSec != 300 {
		t.Errorf("auto interval = %d, want 300", g.Probe.IntervalSec)
	}
}

// TestClashPlaceholderProxyDropped: a proxy whose name equals a url-test group's
// name is the group's carrier host — dropped from the node stream, excluded from
// the group's own membership, while the real members survive.
func TestClashPlaceholderProxyDropped(t *testing.T) {
	body := `proxies:
  - {name: auto, type: vless, server: 203.0.113.99, port: 443, uuid: 00000000-0000-4000-8000-0000000000f0, network: tcp, tls: true, servername: carrier.example.com}
  - {name: Alpha, type: vless, server: 203.0.113.1, port: 443, uuid: 00000000-0000-4000-8000-0000000000a1, network: tcp, tls: true, servername: alpha.example.com}
  - {name: Beta, type: vless, server: 203.0.113.2, port: 443, uuid: 00000000-0000-4000-8000-0000000000b2, network: tcp, tls: true, servername: beta.example.com}
proxy-groups:
  - name: auto
    type: url-test
    include-all: true
    url: http://example.com/generate_204
    interval: 300`
	o, err := Parse(body, "s", FormatAuto)
	if err != nil {
		t.Fatal(err)
	}
	if len(o.Nodes) != 2 {
		t.Fatalf("nodes = %d, want 2 (carrier dropped)", len(o.Nodes))
	}
	if nodeByAddr(o, "203.0.113.99") {
		t.Error("the carrier host (named like the group) must be dropped from nodes")
	}
	g := findGroup(t, o, "auto")
	if len(g.MemberKeys) != 2 {
		t.Errorf("auto members = %d, want 2 (carrier excluded)", len(g.MemberKeys))
	}
}

// TestClashQuotedGroupScalarsDoNotRejectBody: a provider that quotes group
// scalars (interval: "300", include-all: "true"), and carries a group type we
// never extract (select), must NOT make the whole clash body unparseable — the
// nodes still import. (Regression: a typed proxy-groups decode failed the whole
// yaml.Unmarshal on the first such mismatch, silently freezing the sub.)
func TestClashQuotedGroupScalarsDoNotRejectBody(t *testing.T) {
	body := `proxies:
  - {name: Alpha, type: vless, server: 203.0.113.1, port: 443, uuid: 00000000-0000-4000-8000-0000000000a1, network: tcp, tls: true, servername: alpha.example.com}
proxy-groups:
  - name: Select
    type: select
    proxies: ["Alpha"]
  - name: auto
    type: url-test
    include-all: "true"
    interval: "300"
    tolerance: "50"
    url: http://example.com/generate_204`
	o, err := Parse(body, "s", FormatAuto)
	if err != nil {
		t.Fatalf("quoted group scalars must not reject the body: %v", err)
	}
	if len(o.Nodes) != 1 {
		t.Errorf("nodes = %d, want 1 (Alpha)", len(o.Nodes))
	}
	g := findGroup(t, o, "auto")
	if len(g.MemberKeys) != 1 || g.Probe.IntervalSec != 300 || g.Probe.Tolerance != 50 {
		t.Errorf("auto group = %+v, want 1 member @ interval 300 tolerance 50", g)
	}
}

// TestClashFilterKeepsExplicitProxies: filter/exclude-filter apply only to the
// include-all set; an explicitly-listed proxy is kept even if its name fails the
// filter (mihomo semantics).
func TestClashFilterKeepsExplicitProxies(t *testing.T) {
	body := `proxies:
  - {name: KeepMe, type: vless, server: 203.0.113.1, port: 443, uuid: 00000000-0000-4000-8000-0000000000a1, network: tcp, tls: true, servername: a.example.com}
  - {name: US-Fast, type: vless, server: 203.0.113.2, port: 443, uuid: 00000000-0000-4000-8000-0000000000a2, network: tcp, tls: true, servername: b.example.com}
  - {name: EU-Slow, type: vless, server: 203.0.113.3, port: 443, uuid: 00000000-0000-4000-8000-0000000000a3, network: tcp, tls: true, servername: c.example.com}
proxy-groups:
  - name: auto
    type: url-test
    proxies: ["KeepMe"]
    include-all: true
    filter: "^US"
    url: http://example.com/generate_204
    interval: 300`
	o, err := Parse(body, "s", FormatAuto)
	if err != nil {
		t.Fatal(err)
	}
	// KeepMe (explicit, unfiltered) + US-Fast (include-all, matches ^US); EU-Slow filtered out.
	if g := findGroup(t, o, "auto"); len(g.MemberKeys) != 2 {
		t.Errorf("auto members = %d, want 2 (explicit KeepMe + filtered US-Fast)", len(g.MemberKeys))
	}
}

// TestClashReservedCaseSensitive: a user proxy named "Global" is a real member,
// not the reserved GLOBAL policy (reserved names match case-sensitively).
func TestClashReservedCaseSensitive(t *testing.T) {
	body := `proxies:
  - {name: Global, type: vless, server: 203.0.113.1, port: 443, uuid: 00000000-0000-4000-8000-0000000000a1, network: tcp, tls: true, servername: a.example.com}
proxy-groups:
  - name: auto
    type: url-test
    include-all: true
    url: http://example.com/generate_204
    interval: 300`
	o, err := Parse(body, "s", FormatAuto)
	if err != nil {
		t.Fatal(err)
	}
	if g := findGroup(t, o, "auto"); len(g.MemberKeys) != 1 {
		t.Errorf("a proxy named 'Global' must be a member, got %d", len(g.MemberKeys))
	}
}

// TestXraySingleMemberBalancer: a balancer entry with exactly ONE selected
// outbound keeps both its node and its group (the sole member is suffixed with
// its address so it never collides with the group name).
func TestXraySingleMemberBalancer(t *testing.T) {
	body := `[
	  {"remarks":"Auto","outbounds":[
	    {"tag":"proxy","protocol":"vless","settings":{"vnext":[{"address":"203.0.113.7","port":443,"users":[{"id":"00000000-0000-4000-8000-000000000077","encryption":"none"}]}]},"streamSettings":{"network":"tcp","security":"tls","tlsSettings":{"serverName":"solo.example.com"}}},
	    {"tag":"direct","protocol":"freedom","settings":{}}
	  ],"routing":{"balancers":[{"tag":"b","selector":["proxy"]}]}}
	]`
	o, err := Parse(body, "s", FormatAuto)
	if err != nil {
		t.Fatalf("a single-member balancer must not vanish: %v", err)
	}
	if len(o.Nodes) != 1 || !nodeByAddr(o, "203.0.113.7") {
		t.Errorf("the sole balancer member must survive as a node: %d nodes", len(o.Nodes))
	}
	if g := findGroup(t, o, "Auto"); len(g.MemberKeys) != 1 {
		t.Errorf("the group must resolve its sole member, got %d", len(g.MemberKeys))
	}
}

// TestXraySingleMemberBalancerDoesNotStealName: a single-member balancer entry
// that precedes a dedicated entry for the SAME server must not steal the node's
// display name — the dedicated entry's clean name wins the dedup regardless of
// order (balancer members always dedup in the second pass).
func TestXraySingleMemberBalancerDoesNotStealName(t *testing.T) {
	body := `[
	  {"remarks":"Auto","outbounds":[
	    {"tag":"proxy","protocol":"vless","settings":{"vnext":[{"address":"203.0.113.7","port":443,"users":[{"id":"00000000-0000-4000-8000-000000000077","encryption":"none"}]}]},"streamSettings":{"network":"tcp","security":"tls","tlsSettings":{"serverName":"solo.example.com"}}},
	    {"tag":"direct","protocol":"freedom","settings":{}}
	  ],"routing":{"balancers":[{"tag":"b","selector":["proxy"]}]}},
	  {"remarks":"Tokyo","outbounds":[
	    {"tag":"proxy","protocol":"vless","settings":{"vnext":[{"address":"203.0.113.7","port":443,"users":[{"id":"00000000-0000-4000-8000-000000000077","encryption":"none"}]}]},"streamSettings":{"network":"tcp","security":"tls","tlsSettings":{"serverName":"solo.example.com"}}}
	  ]}
	]`
	o, err := Parse(body, "s", FormatAuto)
	if err != nil {
		t.Fatal(err)
	}
	if len(o.Nodes) != 1 {
		t.Fatalf("nodes = %d, want 1 (deduped)", len(o.Nodes))
	}
	if got := o.Nodes[0].DisplayName(); got != "Tokyo" {
		t.Errorf("node name = %q, want Tokyo (the dedicated entry wins, not the balancer)", got)
	}
	if g := findGroup(t, o, "Auto"); len(g.MemberKeys) != 1 {
		t.Errorf("group must still resolve its member, got %d", len(g.MemberKeys))
	}
}

// TestClashGroupLookaroundFilterDropped: a filter Go's RE2 cannot compile (a
// lookaround) drops the whole group rather than importing wrong membership.
func TestClashGroupLookaroundFilterDropped(t *testing.T) {
	body := `proxies:
  - {name: Alpha, type: vless, server: 203.0.113.1, port: 443, uuid: 00000000-0000-4000-8000-0000000000a1, network: tcp, tls: true, servername: alpha.example.com}
proxy-groups:
  - name: auto
    type: url-test
    include-all: true
    exclude-filter: "(?!Alpha)"
    url: http://example.com/generate_204
    interval: 300`
	entries, groups, ok := parseClash(body)
	if !ok || len(entries) == 0 {
		t.Fatalf("body must parse as clash: ok=%v entries=%d", ok, len(entries))
	}
	if len(groups) != 0 {
		t.Errorf("a lookaround exclude-filter must drop the group, got %+v", groups)
	}
}

// TestDurationSeconds pins the duration-string decode the group probes rely on.
func TestDurationSeconds(t *testing.T) {
	cases := map[string]uint32{"90s": 90, "3m": 180, "1h30m": 5400, "5m": 300, "": 0, "garbage": 0, "-5s": 0}
	for in, want := range cases {
		if got := durationSeconds(in); got != want {
			t.Errorf("durationSeconds(%q) = %d, want %d", in, got, want)
		}
	}
}

// TestRefreshMaterializesAndReconcilesGroups: a refresh turns extracted groups
// into stored group nodes whose members are the dialable nodes' UUIDs; a second
// refresh keeps the group's own UUID and its members stable (matched by name /
// prefsKey); and the reported Count is the DIALABLE node count, excluding groups.
func TestRefreshMaterializesAndReconcilesGroups(t *testing.T) {
	body := readFixture(t, "xray.json")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, body)
	}))
	defer srv.Close()

	p := store.NewProfile("t")
	sub := store.NewSubscription(srv.URL, "live")
	subID, err := p.AddSubscription(sub)
	if err != nil {
		t.Fatal(err)
	}

	findAuto := func() *store.Node {
		for i := range p.Nodes {
			if p.Nodes[i].IsGroup() && p.Nodes[i].DisplayName() == "Auto" {
				return &p.Nodes[i]
			}
		}
		return nil
	}

	Refresh(context.Background(), p, "", "")
	g1 := findAuto()
	if g1 == nil {
		t.Fatal("the Auto group was not materialized")
	}
	if len(g1.Group.Members) != 3 {
		t.Fatalf("Auto members = %d, want 3", len(g1.Group.Members))
	}
	for _, id := range g1.Group.Members {
		if n := p.FindNodeByID(id); n == nil || n.IsGroup() {
			t.Errorf("member %q does not resolve to a dialable node", id)
		}
	}
	groupID := g1.ID
	members1 := append([]string(nil), g1.Group.Members...)

	// Second refresh: same body → group and member UUIDs must be stable.
	Refresh(context.Background(), p, "", "")
	g2 := findAuto()
	if g2 == nil || g2.ID != groupID {
		t.Fatalf("group UUID churned across refresh: %v -> %v", groupID, g2)
	}
	if !sameStringSet(g2.Group.Members, members1) {
		t.Errorf("member UUIDs churned: %v -> %v", members1, g2.Group.Members)
	}

	// Count coherence: the reported Count equals the sub's DIALABLE node count.
	results := Refresh(context.Background(), p, "", "")
	var reported int
	for _, r := range results {
		if r.SubID == subID {
			reported = r.Count
		}
	}
	dialable := 0
	for i := range p.Nodes {
		n := &p.Nodes[i]
		if n.SubID != nil && *n.SubID == subID && !n.IsGroup() {
			dialable++
		}
	}
	if reported != dialable {
		t.Errorf("Result.Count = %d, want the dialable node count %d (groups excluded)", reported, dialable)
	}
}

func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]int{}
	for _, s := range a {
		seen[s]++
	}
	for _, s := range b {
		seen[s]--
	}
	for _, v := range seen {
		if v != 0 {
			return false
		}
	}
	return true
}
