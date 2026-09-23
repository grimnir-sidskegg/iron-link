// The background subscription refresh, offline: the due rule, the failure
// backoff, one sweep end to end against an httptest provider (the profile
// updated, last_updated moved, subscription_updated published), the loop's
// timing overrides, and the reconnect decision — both the pure plan
// comparison and a REAL session (SOCKS mode, nothing dialed) re-activated
// when a refresh changes its node set. The provider serves xhttp-only nodes,
// which route to xray, so no native uTLS member is involved.
package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ironlink/daemon/internal/api"
	"ironlink/daemon/internal/engine"
	"ironlink/daemon/internal/proxy"
	"ironlink/daemon/internal/routing"
	"ironlink/daemon/internal/store"
)

// Share links for the provider stub: valid REALITY parameters (xray's client
// build rejects garbage at engine.Start) at TEST-NET addresses that are
// never dialed. subLinkAMoved is sub-a at another server — a different node
// identity under the same name; subLinkARenamed is the same node under
// another name (a provider rotating informational text in the label).
const (
	subLinkA        = "vless://88f2c0dc-a8e3-49f4-89b9-b3b54f1cad3a@203.0.113.1:443?security=reality&sni=example.com&fp=chrome&pbk=MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY&sid=01ab&type=xhttp&mode=auto#sub-a"
	subLinkAMoved   = "vless://88f2c0dc-a8e3-49f4-89b9-b3b54f1cad3a@203.0.113.9:443?security=reality&sni=example.com&fp=chrome&pbk=MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY&sid=01ab&type=xhttp&mode=auto#sub-a"
	subLinkARenamed = "vless://88f2c0dc-a8e3-49f4-89b9-b3b54f1cad3a@203.0.113.1:443?security=reality&sni=example.com&fp=chrome&pbk=MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY&sid=01ab&type=xhttp&mode=auto#sub-a%20(122%20GB%20left)"
	subLinkB        = "vless://99f2c0dc-a8e3-49f4-89b9-b3b54f1cad3a@203.0.113.2:443?security=reality&sni=example.com&fp=chrome&pbk=MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY&sid=01ab&type=xhttp#sub-b"
	subNameARenamed = "sub-a (122 GB left)"
)

// subProvider is an httptest provider: counts hits, serves the current body,
// or a 503 while failing is set. The "/json" probe an auto-format fetch
// tries first is answered 404 and not counted, so hits count refreshes.
type subProvider struct {
	srv     *httptest.Server
	hits    atomic.Int32
	failing atomic.Bool
	mu      sync.Mutex
	body    string
}

func newSubProvider(t *testing.T, body string) *subProvider {
	t.Helper()
	p := &subProvider{body: body}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/json" {
			http.NotFound(w, r)
			return
		}
		p.hits.Add(1)
		if p.failing.Load() {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		fmt.Fprint(w, p.body)
	}))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *subProvider) serve(body string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.body = body
}

// testSubscription is a stored subscription in the loop's terms: id doubles
// as the name, format auto-detected.
func testSubscription(id, url string, lastUpdated time.Time, intervalSec uint32, enabled bool) store.Subscription {
	return store.Subscription{
		ID: id, Name: id, URL: url,
		LastUpdated: lastUpdated, UpdateIntervalSec: intervalSec,
		Enabled: enabled, Format: "auto",
	}
}

// subLoopManager is a manager whose active profile "main" carries the given
// subscriptions and no nodes.
func subLoopManager(t *testing.T, subs ...store.Subscription) *manager {
	t.Helper()
	m := emptyManager(t)
	if err := m.store.CreateProfile("main"); err != nil {
		t.Fatal(err)
	}
	if err := m.store.SetActiveProfile("main"); err != nil {
		t.Fatal(err)
	}
	editSubscriptions(t, m, func(p *store.Profile) { p.Subscriptions = append(p.Subscriptions, subs...) })
	return m
}

// editSubscriptions rewrites profile "main" through fn — the test's stand-in
// for time passing (rewinding last_updated) or for a client edit.
func editSubscriptions(t *testing.T, m *manager, fn func(p *store.Profile)) {
	t.Helper()
	p, err := m.store.LoadProfile("main")
	if err != nil {
		t.Fatal(err)
	}
	fn(p)
	if err := m.store.SaveProfile("main", p); err != nil {
		t.Fatal(err)
	}
}

func loadSubscription(t *testing.T, m *manager, id string) (store.Subscription, *store.Profile) {
	t.Helper()
	p, err := m.store.LoadProfile("main")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range p.Subscriptions {
		if s.ID == id {
			return s, p
		}
	}
	t.Fatalf("subscription %q not in profile", id)
	return store.Subscription{}, nil
}

// logLines returns the log events at level whose message contains want.
func logLines(events []api.Event, level, want string) []api.Event {
	var out []api.Event
	for _, ev := range events {
		if ev.Level == level && strings.Contains(ev.Message, want) {
			out = append(out, ev)
		}
	}
	return out
}

// TestSubscriptionDue pins the due rule: auto-update on with the interval
// elapsed is due; a fresh one, a disabled one and a zero interval are not;
// a last_updated in the future counts as due.
func TestSubscriptionDue(t *testing.T) {
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		sub  store.Subscription
		want bool
	}{
		{"elapsed", testSubscription("s", "u", now.Add(-2*time.Hour), 3600, true), true},
		{"exactly the interval", testSubscription("s", "u", now.Add(-time.Hour), 3600, true), true},
		{"fresh", testSubscription("s", "u", now.Add(-time.Minute), 3600, true), false},
		{"disabled", testSubscription("s", "u", now.Add(-2*time.Hour), 3600, false), false},
		{"zero interval", testSubscription("s", "u", now.Add(-48*time.Hour), 0, true), false},
		{"future stamp", testSubscription("s", "u", now.Add(time.Hour), 28800, true), true},
	}
	for _, c := range cases {
		if got := subscriptionDue(c.sub, now); got != c.want {
			t.Errorf("%s: due = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestSubscriptionRetryBackoff: 10 min, doubling per consecutive failure,
// capped at 1 h; a success stamped after the failure supersedes the entry.
func TestSubscriptionRetryBackoff(t *testing.T) {
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	var prev *subscriptionRetry
	for i, want := range []time.Duration{10 * time.Minute, 20 * time.Minute, 40 * time.Minute, time.Hour, time.Hour} {
		r := nextSubscriptionRetry(prev, now)
		if r.delay != want || !r.next.Equal(now.Add(want)) || !r.failedAt.Equal(now) {
			t.Fatalf("failure %d: %+v, want delay %s", i+1, r, want)
		}
		prev = &r
	}
	r := nextSubscriptionRetry(nil, now)
	later := now.Add(time.Minute)
	if r.supersededBy(testSubscription("s", "u", now.Add(-time.Second), 600, true), later) {
		t.Error("a stamp before the failure must not supersede it")
	}
	if !r.supersededBy(testSubscription("s", "u", now.Add(time.Second), 600, true), later) {
		t.Error("a stamp after the failure must supersede it")
	}
	if r.supersededBy(testSubscription("s", "u", later.Add(2*time.Hour), 600, true), later) {
		t.Error("a stamp ahead of the clock is not a later success and must not supersede")
	}
}

// TestSubscriptionSweepRefreshesDue: one sweep fetches a due subscription,
// lands its nodes, moves last_updated, publishes subscription_updated and
// logs at info; the next sweep leaves it alone (no longer due).
func TestSubscriptionSweepRefreshesDue(t *testing.T) {
	prov := newSubProvider(t, subLinkA+"\n"+subLinkB)
	stale := time.Now().UTC().Add(-2 * time.Hour)
	m := subLoopManager(t, testSubscription("sub-1", prov.srv.URL, stale, 3600, true))
	events := subscribeEvents(t, m)

	m.sweepSubscriptions(context.Background())
	if n := prov.hits.Load(); n != 1 {
		t.Fatalf("provider hits = %d, want 1", n)
	}
	sub, p := loadSubscription(t, m, "sub-1")
	if len(p.Nodes) != 2 {
		t.Fatalf("nodes after the sweep = %d, want 2", len(p.Nodes))
	}
	if !sub.LastUpdated.After(stale) || time.Since(sub.LastUpdated) > time.Minute {
		t.Errorf("last_updated = %v, want moved to now", sub.LastUpdated)
	}
	if sub.LastError != "" {
		t.Errorf("last_error = %q, want empty", sub.LastError)
	}
	updated := collectEvents(events, api.EventSubscriptionUpdated, 300*time.Millisecond)
	if len(updated) != 1 || updated[0].SubID != "sub-1" || updated[0].Total != 2 || updated[0].Added != 2 {
		t.Fatalf("subscription_updated events = %+v, want one for sub-1 with 2 nodes", updated)
	}

	m.sweepSubscriptions(context.Background())
	if n := prov.hits.Load(); n != 1 {
		t.Errorf("a freshly refreshed subscription must not be fetched again, hits = %d", n)
	}
}

// TestSubscriptionSweepInfoLog: the success line reaches the log feed at
// "info" with the node accounting.
func TestSubscriptionSweepInfoLog(t *testing.T) {
	prov := newSubProvider(t, subLinkA)
	m := subLoopManager(t, testSubscription("sub-1", prov.srv.URL, time.Now().UTC().Add(-2*time.Hour), 3600, true))
	events := subscribeEvents(t, m)
	m.sweepSubscriptions(context.Background())
	logs := collectEvents(events, api.EventLog, 300*time.Millisecond)
	if got := logLines(logs, "info", `subscription "sub-1": refreshed, 1 nodes (+1 -0)`); len(got) != 1 {
		t.Errorf("info log lines = %+v, want the refreshed line", logs)
	}
}

// TestSubscriptionSweepSkipsNotDue: a disabled subscription (however
// stale), a fresh one and a zero-interval one are not fetched.
func TestSubscriptionSweepSkipsNotDue(t *testing.T) {
	prov := newSubProvider(t, subLinkA)
	now := time.Now().UTC()
	m := subLoopManager(t,
		testSubscription("disabled", prov.srv.URL, now.Add(-48*time.Hour), 3600, false),
		testSubscription("fresh", prov.srv.URL, now, 3600, true),
		testSubscription("never", prov.srv.URL, now.Add(-48*time.Hour), 0, true),
	)
	m.sweepSubscriptions(context.Background())
	if n := prov.hits.Load(); n != 0 {
		t.Errorf("provider hits = %d, want 0", n)
	}
	for _, id := range []string{"disabled", "fresh", "never"} {
		if sub, _ := loadSubscription(t, m, id); sub.LastError != "" {
			t.Errorf("%s: last_error = %q, want untouched", id, sub.LastError)
		}
	}
}

// TestSubscriptionSweepFutureStampIsDue: a last_updated ahead of the clock
// is refreshed rather than waited out.
func TestSubscriptionSweepFutureStampIsDue(t *testing.T) {
	prov := newSubProvider(t, subLinkA)
	m := subLoopManager(t, testSubscription("sub-1", prov.srv.URL, time.Now().UTC().Add(2*time.Hour), 28800, true))
	m.sweepSubscriptions(context.Background())
	if n := prov.hits.Load(); n != 1 {
		t.Fatalf("provider hits = %d, want 1", n)
	}
	if sub, _ := loadSubscription(t, m, "sub-1"); time.Since(sub.LastUpdated) > time.Minute || time.Since(sub.LastUpdated) < 0 {
		t.Errorf("last_updated = %v, want re-stamped to now", sub.LastUpdated)
	}
}

// TestSubscriptionSweepFutureStampKeepsBackoff: a last_updated ahead of
// the clock is due (above), but once the fetch fails the backoff must hold —
// the future stamp is not a later success that retires the hold, so the
// provider is not hit again every sweep until the clock catches up.
func TestSubscriptionSweepFutureStampKeepsBackoff(t *testing.T) {
	prov := newSubProvider(t, subLinkA)
	prov.failing.Store(true)
	m := subLoopManager(t, testSubscription("sub-1", prov.srv.URL, time.Now().UTC().Add(2*time.Hour), 28800, true))
	for range 3 {
		m.sweepSubscriptions(context.Background())
	}
	if n := prov.hits.Load(); n != 1 {
		t.Errorf("provider hits over three sweeps = %d, want 1 (the backoff must hold)", n)
	}
	m.mu.Lock()
	_, held := m.subRetry["sub-1"]
	m.mu.Unlock()
	if !held {
		t.Error("the backoff entry must survive the sweeps")
	}
}

// TestSubscriptionSweepUnsavedRefreshBacksOff: a fetch that succeeds but
// whose profile cannot be written is a failed attempt — last_updated never
// landed, so without a hold the next sweep would fetch again.
func TestSubscriptionSweepUnsavedRefreshBacksOff(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory mode bits; the unwritable profiles dir cannot be staged")
	}
	prov := newSubProvider(t, subLinkA)
	m := subLoopManager(t, testSubscription("sub-1", prov.srv.URL, time.Now().UTC().Add(-2*time.Hour), 3600, true))
	events := subscribeEvents(t, m)
	profiles := filepath.Join(m.store.Dir(), "profiles")
	if err := os.Chmod(profiles, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(profiles, 0o700) })

	m.sweepSubscriptions(context.Background())
	m.sweepSubscriptions(context.Background())
	if n := prov.hits.Load(); n != 1 {
		t.Errorf("provider hits over two sweeps = %d, want 1 (an unsaved success must back off)", n)
	}
	m.mu.Lock()
	retry, held := m.subRetry["sub-1"]
	m.mu.Unlock()
	if !held || retry.delay != subscriptionBackoffMin {
		t.Errorf("retry after the unsaved refresh = %+v (held %v), want a %s hold", retry, held, subscriptionBackoffMin)
	}
	logs := collectEvents(events, api.EventLog, 300*time.Millisecond)
	if got := logLines(logs, "warn", "refresh not saved"); len(got) != 1 || !strings.Contains(got[0].Message, "next attempt in 10m0s") {
		t.Errorf("warn log lines = %+v, want one 'refresh not saved' line with the hold", logs)
	}
	if updated := collectEvents(events, api.EventSubscriptionUpdated, 100*time.Millisecond); len(updated) != 0 {
		t.Errorf("an unsaved refresh must not publish subscription_updated: %+v", updated)
	}
}

// TestSubscriptionSweepShutdownDropsFetch: a fetch cut short by the daemon
// stopping is neither a provider failure nor a refresh — nothing is stored,
// no backoff is booked, nothing is logged as failed.
func TestSubscriptionSweepShutdownDropsFetch(t *testing.T) {
	entered := make(chan struct{}, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	stale := time.Now().UTC().Add(-2 * time.Hour)
	m := subLoopManager(t, testSubscription("sub-1", srv.URL, stale, 3600, true))
	events := subscribeEvents(t, m)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.sweepSubscriptions(ctx)
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("the provider was never contacted")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the sweep did not return after the cancel")
	}

	sub, _ := loadSubscription(t, m, "sub-1")
	if sub.LastError != "" {
		t.Errorf("last_error = %q, want empty (a shutdown is not a failure)", sub.LastError)
	}
	if !sub.LastUpdated.Equal(stale) {
		t.Errorf("last_updated = %v, want untouched", sub.LastUpdated)
	}
	m.mu.Lock()
	_, held := m.subRetry["sub-1"]
	m.mu.Unlock()
	if held {
		t.Error("a dropped fetch must not book a backoff")
	}
	logs := collectEvents(events, api.EventLog, 300*time.Millisecond)
	if got := logLines(logs, "warn", "refresh failed"); len(got) != 0 {
		t.Errorf("a dropped fetch must not be logged as failed: %+v", got)
	}
}

// TestSubscriptionSweepFailureBackoff: a failing provider sets last_error
// and leaves last_updated; the backoff holds the next sweep back; once its
// time passes a second failure doubles it; a success clears both the error
// and the entry. The retry time is rewound by hand (the test's clock).
func TestSubscriptionSweepFailureBackoff(t *testing.T) {
	prov := newSubProvider(t, subLinkA)
	prov.failing.Store(true)
	stale := time.Now().UTC().Add(-2 * time.Hour)
	m := subLoopManager(t, testSubscription("sub-1", prov.srv.URL, stale, 3600, true))
	events := subscribeEvents(t, m)

	m.sweepSubscriptions(context.Background())
	if n := prov.hits.Load(); n != 1 {
		t.Fatalf("provider hits = %d, want 1", n)
	}
	sub, p := loadSubscription(t, m, "sub-1")
	if !strings.Contains(sub.LastError, "503") {
		t.Errorf("last_error = %q, want the HTTP 503", sub.LastError)
	}
	if !sub.LastUpdated.Equal(stale) || len(p.Nodes) != 0 {
		t.Errorf("a failure must leave last_updated and the nodes: %v / %d nodes", sub.LastUpdated, len(p.Nodes))
	}
	m.mu.Lock()
	retry, held := m.subRetry["sub-1"]
	m.mu.Unlock()
	if !held || retry.delay != subscriptionBackoffMin || time.Until(retry.next) > subscriptionBackoffMin {
		t.Fatalf("retry after one failure = %+v (held %v), want a %s hold", retry, held, subscriptionBackoffMin)
	}
	logs := collectEvents(events, api.EventLog, 300*time.Millisecond)
	if got := logLines(logs, "warn", `subscription "sub-1": refresh failed:`); len(got) != 1 || !strings.Contains(got[0].Message, "next attempt in 10m0s") {
		t.Errorf("warn log lines = %+v, want the failure line with the 10 min hold", logs)
	}
	if updated := collectEvents(events, api.EventSubscriptionUpdated, 100*time.Millisecond); len(updated) != 0 {
		t.Errorf("a failure must not publish subscription_updated: %+v", updated)
	}

	// Still held back: the next sweep does not touch the provider.
	m.sweepSubscriptions(context.Background())
	if n := prov.hits.Load(); n != 1 {
		t.Fatalf("the backoff must hold the next sweep back, hits = %d", n)
	}

	// The hold expires (rewound by hand); the second failure doubles it.
	m.mu.Lock()
	retry.next = time.Now().Add(-time.Second)
	m.subRetry["sub-1"] = retry
	m.mu.Unlock()
	m.sweepSubscriptions(context.Background())
	if n := prov.hits.Load(); n != 2 {
		t.Fatalf("an expired hold must allow a retry, hits = %d", n)
	}
	m.mu.Lock()
	retry = m.subRetry["sub-1"]
	m.mu.Unlock()
	if retry.delay != 2*subscriptionBackoffMin {
		t.Fatalf("retry after two failures = %+v, want a %s hold", retry, 2*subscriptionBackoffMin)
	}

	// A success after the hold clears last_error and drops the entry.
	prov.failing.Store(false)
	m.mu.Lock()
	retry.next = time.Now().Add(-time.Second)
	m.subRetry["sub-1"] = retry
	m.mu.Unlock()
	m.sweepSubscriptions(context.Background())
	if n := prov.hits.Load(); n != 3 {
		t.Fatalf("hits = %d, want 3", n)
	}
	sub, p = loadSubscription(t, m, "sub-1")
	if sub.LastError != "" || len(p.Nodes) != 1 || !sub.LastUpdated.After(stale) {
		t.Errorf("after the success: last_error %q, %d nodes, last_updated %v", sub.LastError, len(p.Nodes), sub.LastUpdated)
	}
	m.mu.Lock()
	_, held = m.subRetry["sub-1"]
	m.mu.Unlock()
	if held {
		t.Error("a success must reset the backoff")
	}
}

// TestSubscriptionManualRefreshSupersedesBackoff: the refresh_subscriptions
// verb still works while the loop holds a subscription back, and its
// success (last_updated moved past the failure) retires the hold — once the
// subscription is due again it is fetched without waiting the backoff out.
func TestSubscriptionManualRefreshSupersedesBackoff(t *testing.T) {
	prov := newSubProvider(t, subLinkA)
	prov.failing.Store(true)
	m := subLoopManager(t, testSubscription("sub-1", prov.srv.URL, time.Now().UTC().Add(-2*time.Hour), 3600, true))
	m.sweepSubscriptions(context.Background())
	if n := prov.hits.Load(); n != 1 {
		t.Fatalf("hits = %d, want 1", n)
	}

	prov.failing.Store(false)
	resp := m.Handle(api.Request{Command: api.CmdRefreshSubs})
	if resp.Status != api.StatusRefreshed || len(resp.Refreshed) != 1 || resp.Refreshed[0].Count != 1 || resp.Refreshed[0].Error != "" {
		t.Fatalf("refresh_subscriptions: %+v", resp)
	}
	if sub, _ := loadSubscription(t, m, "sub-1"); sub.LastError != "" {
		t.Errorf("a manual success must clear last_error, got %q", sub.LastError)
	}
	// Not due now (just refreshed): the sweep leaves it alone.
	m.sweepSubscriptions(context.Background())
	if n := prov.hits.Load(); n != 2 {
		t.Fatalf("hits = %d, want 2 (the manual refresh only)", n)
	}
	// Due again (rewound by hand): the stale hold must not block the fetch.
	editSubscriptions(t, m, func(p *store.Profile) { p.Subscriptions[0].LastUpdated = time.Now().UTC().Add(-2 * time.Hour) })
	m.sweepSubscriptions(context.Background())
	if n := prov.hits.Load(); n != 3 {
		t.Fatalf("a hold superseded by a manual success must not block, hits = %d", n)
	}
	m.mu.Lock()
	_, held := m.subRetry["sub-1"]
	m.mu.Unlock()
	if held {
		t.Error("the superseded hold must be pruned")
	}
}

// TestSubscriptionSweepDropsRemovedBackoff: an entry whose subscription is
// gone is pruned by the sweep.
func TestSubscriptionSweepDropsRemovedBackoff(t *testing.T) {
	prov := newSubProvider(t, subLinkA)
	prov.failing.Store(true)
	m := subLoopManager(t, testSubscription("sub-1", prov.srv.URL, time.Now().UTC().Add(-2*time.Hour), 3600, true))
	m.sweepSubscriptions(context.Background())
	if resp := m.Handle(api.Request{Command: api.CmdRemoveSub, Sub: strPtr("sub-1")}); resp.Status != api.StatusOk {
		t.Fatalf("remove_subscription: %+v", resp)
	}
	m.sweepSubscriptions(context.Background())
	m.mu.Lock()
	n := len(m.subRetry)
	m.mu.Unlock()
	if n != 0 {
		t.Errorf("backoff entries after the removal = %d, want 0", n)
	}
}

// TestSubscriptionLoopTiming: the loop honours the initial delay and tick
// overrides — the due subscription is fetched once after the delay and not
// again on the following ticks (no longer due).
func TestSubscriptionLoopTiming(t *testing.T) {
	prov := newSubProvider(t, subLinkA)
	m := subLoopManager(t, testSubscription("sub-1", prov.srv.URL, time.Now().UTC().Add(-2*time.Hour), 3600, true))
	m.subInitialDelay = 10 * time.Millisecond
	m.subTickInterval = 20 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.subscriptionLoop(ctx)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for prov.hits.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if prov.hits.Load() == 0 {
		cancel()
		<-done
		t.Fatal("the loop never fetched the due subscription")
	}
	time.Sleep(100 * time.Millisecond) // several ticks
	cancel()
	<-done
	if n := prov.hits.Load(); n != 1 {
		t.Errorf("hits = %d, want 1 (fetched once, then not due)", n)
	}
}

// namedNode builds a plan member from a share link.
func namedNode(t *testing.T, id, link string) engine.NamedNode {
	t.Helper()
	p, err := proxy.ParseURL(link)
	if err != nil {
		t.Fatal(err)
	}
	return engine.NamedNode{ID: id, Name: p.DisplayName(), Profile: p}
}

// TestPlanMembersEqual pins the reconnect decision: the same set (in any
// order, whatever the routing, whatever the display names) is equal; a
// changed document, a member added or removed, or a changed group
// membership is not.
func TestPlanMembersEqual(t *testing.T) {
	a := namedNode(t, "id-a", subLinkA)
	b := namedNode(t, "id-b", subLinkB)
	aMoved := namedNode(t, "id-a", subLinkAMoved)
	aRenamed := namedNode(t, "id-a", subLinkARenamed) // the same node, another label
	base := engine.SessionPlan{Natives: []engine.NamedNode{a}, XrayNodes: []engine.NamedNode{b}, ActiveTag: "id-a"}

	if !planMembersEqual(base, base) {
		t.Error("a plan must equal itself")
	}
	routed := base
	routed.Routing = &routing.Config{Name: "basic"}
	routed.CachePath = "/elsewhere/cache.db"
	tunables := engine.DefaultTunables()
	routed.Tunables = &tunables
	if !planMembersEqual(base, routed) {
		t.Error("routing / cache / tunables differences must not count")
	}
	reordered := engine.SessionPlan{Natives: []engine.NamedNode{b, a}, XrayNodes: nil}
	twoNatives := engine.SessionPlan{Natives: []engine.NamedNode{a, b}}
	if !planMembersEqual(twoNatives, reordered) {
		t.Error("member order must not count")
	}
	if planMembersEqual(base, engine.SessionPlan{Natives: []engine.NamedNode{aMoved}, XrayNodes: []engine.NamedNode{b}}) {
		t.Error("a changed document under the same id must count")
	}
	if !planMembersEqual(base, engine.SessionPlan{Natives: []engine.NamedNode{aRenamed}, XrayNodes: []engine.NamedNode{b}}) {
		t.Error("a changed display name must not count")
	}
	if planMembersEqual(base, engine.SessionPlan{Natives: []engine.NamedNode{a}}) {
		t.Error("a removed member must count")
	}
	if planMembersEqual(base, engine.SessionPlan{Natives: []engine.NamedNode{a, b}, XrayNodes: []engine.NamedNode{b}}) {
		t.Error("an added member must count")
	}
	if planMembersEqual(base, engine.SessionPlan{Natives: []engine.NamedNode{b}, XrayNodes: []engine.NamedNode{a}}) {
		t.Error("a member moving between the native and xray sets must count")
	}

	grouped := base
	grouped.Group = &engine.GroupPlan{ID: "g", Name: "Auto", Members: []string{"id-a", "id-b"}}
	sameGroup := base
	sameGroup.Group = &engine.GroupPlan{ID: "g", Name: "Auto", Members: []string{"id-b", "id-a"}}
	if !planMembersEqual(grouped, sameGroup) {
		t.Error("the same group membership in another order must be equal")
	}
	fewer := base
	fewer.Group = &engine.GroupPlan{ID: "g", Name: "Auto", Members: []string{"id-a"}}
	if planMembersEqual(grouped, fewer) {
		t.Error("a group member removed must count")
	}
	more := base
	more.Group = &engine.GroupPlan{ID: "g", Name: "Auto", Members: []string{"id-a", "id-b", "id-c"}}
	if planMembersEqual(grouped, more) {
		t.Error("a group member added must count")
	}
	otherGroup := base
	otherGroup.Group = &engine.GroupPlan{ID: "g2", Name: "Auto", Members: []string{"id-a", "id-b"}}
	if planMembersEqual(grouped, otherGroup) {
		t.Error("a different group id must count")
	}
	if planMembersEqual(grouped, base) || planMembersEqual(base, grouped) {
		t.Error("a group present on one side only must count")
	}
}

// TestSubscriptionRefreshReconnectsChangedSession runs the reconnect against
// a REAL session (SOCKS mode, nothing dialed): an automatic refresh that
// changes nothing leaves the session alone; one that moves the active node
// to another server re-activates it onto the new node; one that drops the
// active node keeps the session running with a warning.
func TestSubscriptionRefreshReconnectsChangedSession(t *testing.T) {
	prov := newSubProvider(t, subLinkA+"\n"+subLinkB)
	m := subLoopManager(t)
	events := subscribeEvents(t, m)
	add := m.Handle(api.Request{Command: api.CmdAddSub, URL: strPtr(prov.srv.URL), Name: strPtr("prov")})
	if add.Status != api.StatusRefreshed || len(add.Refreshed) != 1 || add.Refreshed[0].Count != 2 {
		t.Fatalf("add_subscription: %+v", add)
	}
	// The added subscription carries the default 8 h interval; "rewind" moves
	// its last_updated a day back, the test's stand-in for time passing.
	rewind := func() {
		editSubscriptions(t, m, func(p *store.Profile) { p.Subscriptions[0].LastUpdated = time.Now().UTC().Add(-24 * time.Hour) })
	}
	if resp := m.Handle(api.Request{Command: api.CmdActivate}); resp.Status != api.StatusActivated {
		t.Fatalf("activate: %+v", resp)
	}
	defer m.Handle(api.Request{Command: api.CmdStop})
	if m.activations != 1 {
		t.Fatalf("activations = %d, want 1", m.activations)
	}
	m.mu.Lock()
	firstTag := m.plan.ActiveTag
	m.mu.Unlock()

	// The same body again: the node set is unchanged, no reconnect.
	rewind()
	m.sweepSubscriptions(context.Background())
	if n := prov.hits.Load(); n != 2 {
		t.Fatalf("hits = %d, want 2", n)
	}
	if m.activations != 1 {
		t.Errorf("an unchanged node set must not re-activate, activations = %d", m.activations)
	}

	// sub-a moved to another server: a different member under the active
	// name — the session is re-activated onto it.
	prov.serve(subLinkAMoved + "\n" + subLinkB)
	rewind()
	m.sweepSubscriptions(context.Background())
	if m.activations != 2 {
		t.Fatalf("a changed active node must re-activate, activations = %d", m.activations)
	}
	st := m.Handle(api.Request{Command: api.CmdStatus})
	if st.Status != api.StatusRunning || st.Active == nil || *st.Active.Node != "sub-a" {
		t.Fatalf("status after the reconnect: %+v", st)
	}
	m.mu.Lock()
	secondTag := m.plan.ActiveTag
	m.mu.Unlock()
	if secondTag == firstTag {
		t.Error("the reconnected session must be compiled around the new node")
	}
	logs := collectEvents(events, api.EventLog, 300*time.Millisecond)
	if got := logLines(logs, "info", `changed the active selection "sub-a"; session reconnected`); len(got) != 1 {
		t.Errorf("want one reconnect info line, got %+v", logLines(logs, "info", "subscription"))
	}

	// sub-a gone: the running session is kept, with a warning.
	prov.serve(subLinkB)
	rewind()
	m.sweepSubscriptions(context.Background())
	if m.activations != 2 {
		t.Errorf("a removed active node must not re-activate, activations = %d", m.activations)
	}
	if st := m.Handle(api.Request{Command: api.CmdStatus}); st.Status != api.StatusRunning {
		t.Errorf("the session must keep running: %+v", st)
	}
	logs = collectEvents(events, api.EventLog, 300*time.Millisecond)
	if got := logLines(logs, "warn", `removed the active node "sub-a"`); len(got) != 1 {
		t.Errorf("want one warn line about the removed node, got %+v", logLines(logs, "warn", "subscription"))
	}
}

// TestSubscriptionReconnectSkippedWhenSessionChanged: a reconnect decided
// against one session is dropped when that session is no longer the
// running one by the time it would start (here: stopped in between).
func TestSubscriptionReconnectSkippedWhenSessionChanged(t *testing.T) {
	prov := newSubProvider(t, subLinkA)
	m := subLoopManager(t)
	if add := m.Handle(api.Request{Command: api.CmdAddSub, URL: strPtr(prov.srv.URL), Name: strPtr("prov")}); add.Status != api.StatusRefreshed {
		t.Fatalf("add_subscription: %+v", add)
	}
	if resp := m.Handle(api.Request{Command: api.CmdActivate}); resp.Status != api.StatusActivated {
		t.Fatalf("activate: %+v", resp)
	}
	defer m.Handle(api.Request{Command: api.CmdStop})
	m.mu.Lock()
	sess := m.sess
	name, profile, tun := "sub-a", "main", false
	m.mu.Unlock()
	if resp := m.Handle(api.Request{Command: api.CmdStop}); resp.Status != api.StatusStopped {
		t.Fatalf("stop: %+v", resp)
	}
	m.reconnectSession(sessionReconnect{
		sess: sess, selection: name, name: name,
		req: api.Request{Command: api.CmdActivate, Profile: &profile, Node: &name, Tun: &tun},
	})
	if m.activations != 1 {
		t.Errorf("a stale reconnect must not start a session, activations = %d", m.activations)
	}
	if st := m.Handle(api.Request{Command: api.CmdStatus}); st.Status != api.StatusIdle {
		t.Errorf("the daemon must stay idle: %+v", st)
	}
}

// TestSubscriptionReconnectSkippedWhenSelectionChanged: a live switch_node
// that lands between the reconnect decision and its start replaces the
// selection, not the session — the reconnect must yield to it rather than
// revert the user's choice.
func TestSubscriptionReconnectSkippedWhenSelectionChanged(t *testing.T) {
	prov := newSubProvider(t, subLinkA+"\n"+subLinkB)
	m := subLoopManager(t)
	if add := m.Handle(api.Request{Command: api.CmdAddSub, URL: strPtr(prov.srv.URL), Name: strPtr("prov")}); add.Status != api.StatusRefreshed {
		t.Fatalf("add_subscription: %+v", add)
	}
	if resp := m.Handle(api.Request{Command: api.CmdActivate, Node: strPtr("sub-a")}); resp.Status != api.StatusActivated {
		t.Fatalf("activate: %+v", resp)
	}
	defer m.Handle(api.Request{Command: api.CmdStop})
	m.mu.Lock()
	sess := m.sess
	name, profile, tun := "sub-a", "main", false
	m.mu.Unlock()
	if resp := m.Handle(api.Request{Command: api.CmdSwitchNode, Node: strPtr("sub-b")}); resp.Status != api.StatusSwitched {
		t.Fatalf("switch_node: %+v", resp)
	}
	m.reconnectSession(sessionReconnect{
		sess: sess, selection: name, name: name,
		req: api.Request{Command: api.CmdActivate, Profile: &profile, Node: &name, Tun: &tun},
	})
	if m.activations != 1 {
		t.Errorf("a reconnect decided against a superseded selection must not start a session, activations = %d", m.activations)
	}
	m.mu.Lock()
	node := m.active.Node
	m.mu.Unlock()
	if node == nil || *node != "sub-b" {
		t.Errorf("the live switch must win: active node = %v, want sub-b", node)
	}
}

// TestSubscriptionRefreshRenameRelabelsWithoutReconnect: a refresh whose
// only change is a node's display name (same endpoint and credentials) is a
// label update on the running session — no reconnect, the same session —
// while status and the persisted selection follow the rename of the active
// node and switch_node resolves the new name.
func TestSubscriptionRefreshRenameRelabelsWithoutReconnect(t *testing.T) {
	prov := newSubProvider(t, subLinkA+"\n"+subLinkB)
	m := subLoopManager(t)
	events := subscribeEvents(t, m)
	if add := m.Handle(api.Request{Command: api.CmdAddSub, URL: strPtr(prov.srv.URL), Name: strPtr("prov")}); add.Status != api.StatusRefreshed {
		t.Fatalf("add_subscription: %+v", add)
	}
	if resp := m.Handle(api.Request{Command: api.CmdActivate, Node: strPtr("sub-a")}); resp.Status != api.StatusActivated {
		t.Fatalf("activate: %+v", resp)
	}
	defer m.Handle(api.Request{Command: api.CmdStop})
	m.mu.Lock()
	sess := m.sess
	m.mu.Unlock()

	prov.serve(subLinkARenamed + "\n" + subLinkB)
	editSubscriptions(t, m, func(p *store.Profile) { p.Subscriptions[0].LastUpdated = time.Now().UTC().Add(-24 * time.Hour) })
	m.sweepSubscriptions(context.Background())
	if n := prov.hits.Load(); n != 2 {
		t.Fatalf("hits = %d, want 2", n)
	}
	if m.activations != 1 {
		t.Errorf("a rename must not re-activate, activations = %d", m.activations)
	}
	m.mu.Lock()
	sameSess := m.sess == sess
	m.mu.Unlock()
	if !sameSess {
		t.Error("a rename must leave the running session untouched")
	}
	st := m.Handle(api.Request{Command: api.CmdStatus})
	if st.Status != api.StatusRunning || st.Active == nil || st.Active.Node == nil || *st.Active.Node != subNameARenamed {
		t.Fatalf("status after the rename: %+v", st)
	}
	if st.ActiveNodeLive == nil || *st.ActiveNodeLive != subNameARenamed {
		t.Errorf("active_node_live = %v, want the new name", st.ActiveNodeLive)
	}
	logs := collectEvents(events, api.EventLog, 300*time.Millisecond)
	if got := logLines(logs, "info", "session reconnected"); len(got) != 0 {
		t.Errorf("a rename must not log a reconnect: %+v", got)
	}
	// The relabelled plan resolves the new name for a live switch.
	if resp := m.Handle(api.Request{Command: api.CmdSwitchNode, Node: strPtr("sub-b")}); resp.Status != api.StatusSwitched {
		t.Fatalf("switch_node sub-b: %+v", resp)
	}
	if resp := m.Handle(api.Request{Command: api.CmdSwitchNode, Node: strPtr(subNameARenamed)}); resp.Status != api.StatusSwitched {
		t.Fatalf("switch_node by the new name: %+v", resp)
	}
	if m.activations != 1 {
		t.Errorf("switching among embedded members must stay live, activations = %d", m.activations)
	}
}
