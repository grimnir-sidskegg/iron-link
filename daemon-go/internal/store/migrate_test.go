package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// profileWithSubs renders a minimal profile file at the given schema version
// carrying one subscription per interval, and returns a store rooted over it.
func profileWithSubs(t *testing.T, schema uint32, intervals ...uint32) *Store {
	t.Helper()
	subs := make([]map[string]any, 0, len(intervals))
	for i, iv := range intervals {
		subs = append(subs, map[string]any{
			"id":                  "00000000-0000-0000-0000-00000000000" + string(rune('1'+i)),
			"name":                "sub" + string(rune('1'+i)),
			"url":                 "https://sub.example.com/" + string(rune('1'+i)),
			"last_updated":        "2026-06-09T07:18:45Z",
			"update_interval_sec": iv,
			"enabled":             true,
			"allow_invalid_certs": false,
		})
	}
	doc := map[string]any{
		"schema_version":    schema,
		"name":              "main",
		"active_node_id":    nil,
		"subscriptions":     subs,
		"nodes":             []any{},
		"routing_configs":   []any{},
		"active_routing_id": nil,
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "profiles"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "profiles", "main.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return OpenAt(dir)
}

// TestLoadProfileMigratesV2Intervals: a v2 file's 0 / 86400 interval (the
// old never-acting default) becomes the v3 default on load; a value the user
// chose (neither) survives; the reloaded file is stamped v3.
func TestLoadProfileMigratesV2Intervals(t *testing.T) {
	s := profileWithSubs(t, 2, 86400, 0, 3600)
	p, err := s.LoadProfile("main")
	if err != nil {
		t.Fatalf("LoadProfile: %v", err)
	}
	want := []uint32{DefaultUpdateIntervalSec, DefaultUpdateIntervalSec, 3600}
	for i, w := range want {
		if got := p.Subscriptions[i].UpdateIntervalSec; got != w {
			t.Errorf("v2 sub[%d] interval = %d, want %d", i, got, w)
		}
	}
	if p.SchemaVersion != SchemaVersion {
		t.Errorf("in-memory schema = %d, want %d", p.SchemaVersion, SchemaVersion)
	}
	if err := s.SaveProfile("main", p); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(s.baseDir, "profiles", "main.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		SchemaVersion uint32 `json:"schema_version"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.SchemaVersion != 3 {
		t.Errorf("saved schema_version = %d, want 3", doc.SchemaVersion)
	}
}

// TestLoadProfileKeepsV3Intervals: a v3 file is never rewritten — a stored
// 24 h there is a user choice, and even a 0 is left as the file says.
func TestLoadProfileKeepsV3Intervals(t *testing.T) {
	s := profileWithSubs(t, 3, 86400, 0)
	p, err := s.LoadProfile("main")
	if err != nil {
		t.Fatalf("LoadProfile: %v", err)
	}
	if got := p.Subscriptions[0].UpdateIntervalSec; got != 86400 {
		t.Errorf("v3 86400 interval = %d, want 86400 (untouched)", got)
	}
	if got := p.Subscriptions[1].UpdateIntervalSec; got != 0 {
		t.Errorf("v3 zero interval = %d, want 0 (untouched)", got)
	}
}

// TestNewSubscriptionDefaultsAndLastErrorRoundTrip: a fresh subscription gets
// the 8 h default, and last_error survives save → load (and is omitted from
// the file when empty).
func TestNewSubscriptionDefaultsAndLastErrorRoundTrip(t *testing.T) {
	if got := NewSubscription("https://sub.example.com/a", "a").UpdateIntervalSec; got != 28800 {
		t.Fatalf("NewSubscription interval = %d, want 28800", got)
	}
	s := profileWithSubs(t, 3, 3600, 7200)
	p, err := s.LoadProfile("main")
	if err != nil {
		t.Fatal(err)
	}
	p.Subscriptions[0].LastError = "subscription fetch: HTTP 503"
	if err := s.SaveProfile("main", p); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(s.baseDir, "profiles", "main.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Subscriptions []map[string]any `json:"subscriptions"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if _, present := doc.Subscriptions[1]["last_error"]; present {
		t.Errorf("empty last_error must be omitted from the file: %v", doc.Subscriptions[1])
	}
	p2, err := s.LoadProfile("main")
	if err != nil {
		t.Fatal(err)
	}
	if p2.Subscriptions[0].LastError != "subscription fetch: HTTP 503" || p2.Subscriptions[1].LastError != "" {
		t.Errorf("last_error round trip: %+v", p2.Subscriptions)
	}
}
