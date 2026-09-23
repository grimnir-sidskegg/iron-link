package subscription

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// TestApplyDiscardsOnDrift: a FetchResult fetched for one URL lands on
// nothing when the subscription was removed or its URL edited in between —
// no nodes replaced, no last_error pinned on the new URL, Discarded set.
func TestApplyDiscardsOnDrift(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, linkA)
	}))
	defer srv.Close()

	p, subID := refreshFixture(t, srv.URL)
	fr := FetchSubscription(context.Background(), p.Subscriptions[0], "")
	if fr.Err != nil || fr.SubID != subID || fr.URL != srv.URL {
		t.Fatalf("fetch = %+v", fr)
	}

	// URL edited meanwhile.
	p.Subscriptions[0].URL = "https://moved.example.com/sub"
	stamp := p.Subscriptions[0].LastUpdated
	res := Apply(p, fr)
	if !res.Discarded || res.Err != "" || res.Count != 0 {
		t.Fatalf("apply after a URL edit must discard: %+v", res)
	}
	if p.FindNodeByName("old-name") == nil || p.FindNodeByName("node-a") != nil {
		t.Error("a discarded result must not touch the nodes")
	}
	if !p.Subscriptions[0].LastUpdated.Equal(stamp) || p.Subscriptions[0].LastError != "" {
		t.Errorf("a discarded result must not touch the bookkeeping: %+v", p.Subscriptions[0])
	}

	// A failed fetch for the old URL is discarded the same way — the error
	// belongs to the old URL, not the new one.
	failed := FetchResult{SubID: subID, Name: "live", URL: srv.URL, Format: FormatAuto, Err: fmt.Errorf("boom")}
	if res := Apply(p, failed); !res.Discarded || p.Subscriptions[0].LastError != "" {
		t.Errorf("a stale failure must be discarded, not recorded: %+v / %q", res, p.Subscriptions[0].LastError)
	}

	// Subscription removed meanwhile.
	p.Subscriptions[0].URL = srv.URL
	p.RemoveSubscription(subID)
	res = Apply(p, fr)
	if !res.Discarded || res.SubID != subID {
		t.Fatalf("apply after a removal must discard: %+v", res)
	}
	if len(p.Subscriptions) != 1 || p.FindNodeByName("node-a") != nil {
		t.Error("a discarded result must not resurrect the subscription or its nodes")
	}
}

// TestApplyRecordsAndClearsLastError: a failing fetch (and a body that parses
// to nothing) sets last_error and leaves last_updated; the next success clears
// it and moves last_updated. The same holds through Refresh, which is built
// on the seam.
func TestApplyRecordsAndClearsLastError(t *testing.T) {
	var fail atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			http.Error(w, "boom", http.StatusServiceUnavailable)
			return
		}
		fmt.Fprint(w, linkA)
	}))
	defer srv.Close()

	p, _ := refreshFixture(t, srv.URL)
	sub := &p.Subscriptions[0]
	stamp := time.Now().UTC().Add(-time.Hour)
	sub.LastUpdated = stamp

	fail.Store(true)
	res := Apply(p, FetchSubscription(context.Background(), *sub, ""))
	if res.Err == "" || res.Discarded {
		t.Fatalf("failed fetch must be reported: %+v", res)
	}
	if sub.LastError != res.Err || sub.LastError == "" {
		t.Errorf("last_error = %q, want the result error %q", sub.LastError, res.Err)
	}
	if !sub.LastUpdated.Equal(stamp) {
		t.Error("a failure must not move last_updated")
	}
	if p.FindNodeByName("old-name") == nil {
		t.Error("a failure must keep the old nodes")
	}

	// A 200 with an unusable body is a parse failure with its own message.
	fail.Store(false)
	res = Apply(p, FetchResult{SubID: sub.ID, Name: sub.Name, URL: sub.URL, Format: FormatAuto, Body: "<html>sign in</html>"})
	if res.Err == "" || sub.LastError != res.Err {
		t.Fatalf("parse failure must set last_error: %+v / %q", res, sub.LastError)
	}

	res = Apply(p, FetchSubscription(context.Background(), *sub, ""))
	if res.Err != "" || res.Discarded || res.Count != 1 {
		t.Fatalf("success = %+v", res)
	}
	if sub.LastError != "" {
		t.Errorf("success must clear last_error, got %q", sub.LastError)
	}
	if !sub.LastUpdated.After(stamp) {
		t.Error("success must move last_updated")
	}
	if p.FindNodeByName("node-a") == nil || p.FindNodeByName("old-name") != nil {
		t.Error("success must replace the nodes")
	}

	// The verb path (Refresh) carries the same bookkeeping.
	fail.Store(true)
	results := Refresh(context.Background(), p, "", "")
	if results[0].Err == "" || p.Subscriptions[0].LastError == "" {
		t.Errorf("Refresh must record last_error: %+v / %q", results[0], p.Subscriptions[0].LastError)
	}
	if results[1].Skipped != true {
		t.Errorf("Refresh sweep must still skip the disabled subscription: %+v", results[1])
	}
	fail.Store(false)
	results = Refresh(context.Background(), p, "live", "")
	if len(results) != 1 || results[0].Err != "" || p.Subscriptions[0].LastError != "" {
		t.Errorf("Refresh success must clear last_error: %+v / %q", results, p.Subscriptions[0].LastError)
	}
}

// TestFetchSubscriptionDoesNotTouchProfile: the fetch step works on a value
// copy — the stored subscription is unchanged whatever the outcome.
func TestFetchSubscriptionDoesNotTouchProfile(t *testing.T) {
	p, _ := refreshFixture(t, "http://127.0.0.1:1/unreachable")
	before := p.Subscriptions[0]
	fr := FetchSubscription(context.Background(), before, "")
	if fr.Err == nil {
		t.Fatal("an unreachable URL must fail the fetch")
	}
	if p.Subscriptions[0] != before {
		t.Errorf("fetch mutated the stored subscription: %+v -> %+v", before, p.Subscriptions[0])
	}
}
