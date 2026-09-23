package main

import (
	"strings"
	"testing"

	"ironlink/daemon/internal/api"
)

// subVerbsProfile: one subscription with a recorded last_error, no nodes.
const subVerbsProfile = `{
  "schema_version": 3,
  "name": "main",
  "active_node_id": null,
  "subscriptions": [
    {
      "id": "sub-1",
      "name": "MySub",
      "url": "https://sub.example.com/a",
      "last_updated": "2026-06-09T07:18:45Z",
      "update_interval_sec": 28800,
      "enabled": true,
      "allow_invalid_certs": false,
      "format": "links",
      "last_error": "subscription fetch: HTTP 503"
    }
  ],
  "nodes": [],
  "routing_configs": [],
  "active_routing_id": null
}`

func listOneSub(t *testing.T, m *manager) api.SubscriptionInfo {
	t.Helper()
	resp := m.listSubscriptions(api.Request{Command: api.CmdListSubs})
	if resp.Status != api.StatusSubscriptions || len(resp.Subscriptions) != 1 {
		t.Fatalf("list_subscriptions = %+v", resp)
	}
	return resp.Subscriptions[0]
}

// TestListSubscriptionsEmitsIntervalAndLastError: the two v3 fields reach the
// wire as stored.
func TestListSubscriptionsEmitsIntervalAndLastError(t *testing.T) {
	m := managerWithProfile(t, subVerbsProfile)
	info := listOneSub(t, m)
	if info.UpdateIntervalSec != 28800 || info.LastError != "subscription fetch: HTTP 503" {
		t.Errorf("subscription info = %+v", info)
	}
}

// TestListSubscriptionsZeroLastUpdatedIsEmpty: a subscription that never
// refreshed (the zero time on disk, or no last_updated at all) lists with an
// empty stamp, not year 1 — clients read an empty stamp as "never".
func TestListSubscriptionsZeroLastUpdatedIsEmpty(t *testing.T) {
	m := managerWithProfile(t, strings.Replace(subVerbsProfile, `"last_updated": "2026-06-09T07:18:45Z",`, "", 1))
	if got := listOneSub(t, m).LastUpdated; got != "" {
		t.Errorf("last_updated = %q, want empty", got)
	}
}

// TestUpdateSubscriptionLeavesStoredIntervalBelowFloor: an update that does
// not carry update_interval_sec succeeds on a subscription stored below the
// floor (a pre-floor value) — the floor gates a NEW value, not the other
// edits.
func TestUpdateSubscriptionLeavesStoredIntervalBelowFloor(t *testing.T) {
	m := managerWithProfile(t, strings.Replace(subVerbsProfile, `"update_interval_sec": 28800,`, `"update_interval_sec": 0,`, 1))
	sub, renamed := "MySub", "Renamed"
	resp := m.updateSubscription(api.Request{Command: api.CmdUpdateSub, Sub: &sub, Name: &renamed})
	if resp.Status != api.StatusOk {
		t.Fatalf("a name-only update must not trip the interval floor: %+v", resp)
	}
	if info := listOneSub(t, m); info.Name != renamed || info.UpdateIntervalSec != 0 {
		t.Errorf("subscription after the rename = %+v, want the new name and the interval untouched", info)
	}
}

// TestUpdateSubscriptionIntervalFloor: 599 s is rejected (nothing stored),
// 600 s is accepted and persisted.
func TestUpdateSubscriptionIntervalFloor(t *testing.T) {
	m := managerWithProfile(t, subVerbsProfile)
	sub := "MySub"

	tooShort := uint32(599)
	resp := m.updateSubscription(api.Request{Command: api.CmdUpdateSub, Sub: &sub, UpdateIntervalSec: &tooShort})
	if resp.Status != api.StatusError {
		t.Fatalf("599 s must be rejected: %+v", resp)
	}
	if got := listOneSub(t, m).UpdateIntervalSec; got != 28800 {
		t.Errorf("a rejected update must leave the interval alone, got %d", got)
	}

	floor := uint32(600)
	resp = m.updateSubscription(api.Request{Command: api.CmdUpdateSub, Sub: &sub, UpdateIntervalSec: &floor})
	if resp.Status != api.StatusOk {
		t.Fatalf("600 s must be accepted: %+v", resp)
	}
	if got := listOneSub(t, m).UpdateIntervalSec; got != 600 {
		t.Errorf("interval = %d, want 600", got)
	}
}

// TestUpdateSubscriptionURLChangeClearsLastError: only a URL that differs
// clears the stored failure; re-sending the same URL or editing other fields
// keeps it.
func TestUpdateSubscriptionURLChangeClearsLastError(t *testing.T) {
	m := managerWithProfile(t, subVerbsProfile)
	sub := "MySub"

	sameURL := "https://sub.example.com/a"
	off := false
	resp := m.updateSubscription(api.Request{Command: api.CmdUpdateSub, Sub: &sub, URL: &sameURL, Enabled: &off})
	if resp.Status != api.StatusOk {
		t.Fatalf("update: %+v", resp)
	}
	if info := listOneSub(t, m); info.LastError == "" || info.Enabled {
		t.Errorf("an unchanged URL must keep last_error: %+v", info)
	}

	newURL := "https://sub.example.com/b"
	resp = m.updateSubscription(api.Request{Command: api.CmdUpdateSub, Sub: &sub, URL: &newURL})
	if resp.Status != api.StatusOk {
		t.Fatalf("update: %+v", resp)
	}
	if info := listOneSub(t, m); info.LastError != "" || info.URL != newURL {
		t.Errorf("a URL change must clear last_error: %+v", info)
	}
}
