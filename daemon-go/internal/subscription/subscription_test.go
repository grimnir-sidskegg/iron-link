package subscription

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ironlink/daemon/internal/api"
	"ironlink/daemon/internal/proxy"
	"ironlink/daemon/internal/store"
)

const (
	linkA = "vless://88f2c0dc-a8e3-49f4-89b9-b3b54f1cad3a@203.0.113.1:443?security=reality&sni=g.com&fp=chrome&pbk=PBK&sid=01ab&type=xhttp&mode=auto#node-a"
	linkB = "vless://99f2c0dc-a8e3-49f4-89b9-b3b54f1cad3a@203.0.113.2:443#node-b"
)

func TestParseBodyPlainAndGarbage(t *testing.T) {
	body := linkA + "\n# comment\nssr://nope@h:443\n\n" + linkB + "\n"
	nodes := ParseBody(body, "sub-1")
	if len(nodes) != 2 {
		t.Fatalf("nodes = %d, want 2 (garbage skipped)", len(nodes))
	}
	if nodes[0].DisplayName() != "node-a" || nodes[1].DisplayName() != "node-b" {
		t.Errorf("names: %q, %q", nodes[0].DisplayName(), nodes[1].DisplayName())
	}
	if nodes[0].SubID == nil || *nodes[0].SubID != "sub-1" {
		t.Errorf("sub id not attached: %v", nodes[0].SubID)
	}
}

func TestParseBodyBase64(t *testing.T) {
	body := base64.StdEncoding.EncodeToString([]byte(linkA + "\n" + linkB))
	if nodes := ParseBody(body, "s"); len(nodes) != 2 {
		t.Errorf("base64 body: nodes = %d, want 2", len(nodes))
	}
	// Valid base64 that does NOT decode to links stays raw (and yields none).
	if nodes := ParseBody("aGVsbG8=", "s"); len(nodes) != 0 {
		t.Errorf("non-link base64 must yield no nodes, got %d", len(nodes))
	}
}

func TestFetchHeadersAndErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != "v2rayN/6.23" {
			t.Errorf("UA = %q", r.Header.Get("User-Agent"))
		}
		fmt.Fprint(w, "BODY")
	}))
	defer srv.Close()

	body, err := Fetch(context.Background(), srv.URL, false, "")
	if err != nil || body != "BODY" {
		t.Errorf("Fetch = %q, %v", body, err)
	}

	if _, err := Fetch(context.Background(), "ftp://x", false, ""); err == nil {
		t.Error("non-http scheme must be rejected")
	}

	srv404 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer srv404.Close()
	if _, err := Fetch(context.Background(), srv404.URL, false, ""); err == nil {
		t.Error("HTTP 404 must be an error")
	}
}

func TestFetchInvalidCertOptIn(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "TLSBODY")
	}))
	defer srv.Close()

	if _, err := Fetch(context.Background(), srv.URL, false, ""); err == nil {
		t.Error("self-signed cert must fail with verification on")
	}
	body, err := Fetch(context.Background(), srv.URL, true, "")
	if err != nil || body != "TLSBODY" {
		t.Errorf("allow_invalid_certs fetch = %q, %v", body, err)
	}
}

// refreshFixture: a profile with one enabled subscription pointing at srvURL,
// one disabled subscription, and existing sub nodes (one with a core
// override) plus a manual node.
func refreshFixture(t *testing.T, srvURL string) (*store.Profile, string) {
	t.Helper()
	p := store.NewProfile("t")
	sub := store.NewSubscription(srvURL, "live")
	subID, err := p.AddSubscription(sub)
	if err != nil {
		t.Fatal(err)
	}
	disabled := store.NewSubscription("http://127.0.0.1:1/unused", "off")
	disabled.Enabled = false
	if _, err := p.AddSubscription(disabled); err != nil {
		t.Fatal(err)
	}

	manual, _ := proxy.ParseURL(linkB)
	p.AddNode(store.NewNode(manual.(*proxy.VlessConfig), nil))

	// Existing subscription node matching linkA's (uuid, address, port), with
	// a pinned core — the override must survive the refresh.
	old, _ := proxy.ParseURL(strings.Replace(linkA, "#node-a", "#old-name", 1))
	oldNode := store.NewNode(old.(*proxy.VlessConfig), &subID)
	pin := api.CoreXray
	oldNode.Preferences.CoreOverride = &pin
	p.Nodes = append(p.Nodes, oldNode)
	return p, subID
}

func TestRefreshCarriesPrefsAndSkipsDisabled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, linkA+"\n"+strings.Replace(linkB, "#node-b", "#sub-b", 1))
	}))
	defer srv.Close()

	p, subID := refreshFixture(t, srv.URL)
	before := time.Now().UTC().Add(-time.Minute)
	p.Subscriptions[0].LastUpdated = before

	results := Refresh(context.Background(), p, "", "")
	if len(results) != 2 {
		t.Fatalf("results = %+v", results)
	}
	if results[0].Err != "" || results[0].Count != 2 {
		t.Errorf("live sub result = %+v", results[0])
	}
	if !results[1].Skipped {
		t.Errorf("disabled sub must be skipped in a sweep: %+v", results[1])
	}

	// 1 manual + 2 fresh sub nodes; the old sub node replaced.
	if len(p.Nodes) != 3 {
		t.Fatalf("nodes = %d, want 3", len(p.Nodes))
	}
	if p.FindNodeByName("old-name") != nil {
		t.Error("stale subscription node must be gone")
	}
	refreshed := p.FindNodeByName("node-a")
	if refreshed == nil || refreshed.SubID == nil || *refreshed.SubID != subID {
		t.Fatalf("refreshed node wrong: %+v", refreshed)
	}
	if refreshed.Preferences.CoreOverride == nil || *refreshed.Preferences.CoreOverride != api.CoreXray {
		t.Error("core override must survive the refresh (uuid+address+port match)")
	}
	if fresh := p.FindNodeByName("sub-b"); fresh == nil || fresh.Preferences.CoreOverride != nil {
		t.Error("a node without a saved pref must stay on auto")
	}
	if !p.Subscriptions[0].LastUpdated.After(before) {
		t.Error("successful refresh must touch last_updated")
	}
}

func TestRefreshFailureKeepsOldNodes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	p, _ := refreshFixture(t, srv.URL)
	stamp := p.Subscriptions[0].LastUpdated

	results := Refresh(context.Background(), p, "", "")
	if results[0].Err == "" {
		t.Fatal("fetch failure must be reported")
	}
	if p.FindNodeByName("old-name") == nil {
		t.Error("failed refresh must keep the old nodes")
	}
	if !p.Subscriptions[0].LastUpdated.Equal(stamp) {
		t.Error("failed refresh must not touch last_updated")
	}
}

func TestRefreshOnlyTargetsOneEvenDisabled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, linkA)
	}))
	defer srv.Close()

	p := store.NewProfile("t")
	sub := store.NewSubscription(srv.URL, "solo")
	sub.Enabled = false
	if _, err := p.AddSubscription(sub); err != nil {
		t.Fatal(err)
	}

	results := Refresh(context.Background(), p, "solo", "")
	if len(results) != 1 || results[0].Err != "" || results[0].Count != 1 {
		t.Fatalf("explicit refresh of a disabled sub must run: %+v", results)
	}
}
