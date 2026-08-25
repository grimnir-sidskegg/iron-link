// Package subscription is the daemon-side subscription pipeline (G3): Fetch →
// decode → split → parse → merge → (the caller persists). Ported from the
// former Rust subscription pipeline; fetch lives in the DAEMON only — front-ends never
// fetch (G3). Fetch routing is direct for now
// (through-tunnel / bootstrap is a later seam).
package subscription

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"ironlink/daemon/internal/api"
	"ironlink/daemon/internal/proxy"
	"ironlink/daemon/internal/store"
)

// fetchTimeout is unchanged from the Rust implementation. DefaultUserAgent
// is the fallback UA (providers gate the body format on the UA); the
// effective value is the settings document's subscription_user_agent,
// passed in by the caller.
const (
	fetchTimeout     = 30 * time.Second
	DefaultUserAgent = "v2rayN/6.23"
)

// Fetch GETs a subscription document over HTTP(S). TLS verification is on by
// default; allowInvalidCerts is the per-source opt-in for a trusted source
// whose certificate cannot be verified (self-signed / private CA). An empty
// ua falls back to DefaultUserAgent.
func Fetch(ctx context.Context, subURL string, allowInvalidCerts bool, ua string) (string, error) {
	parsed, err := url.Parse(subURL)
	if err != nil {
		return "", err
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return "", fmt.Errorf("subscription must be a HTTP(s) link")
	}

	transport := http.DefaultTransport
	if allowInvalidCerts {
		transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	}
	client := &http.Client{Timeout: fetchTimeout, Transport: transport}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, subURL, nil)
	if err != nil {
		return "", err
	}
	if ua == "" {
		ua = DefaultUserAgent
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "*/*")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", fmt.Errorf("subscription fetch: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// Result is one subscription's refresh outcome (name + what happened).
type Result struct {
	SubID string
	Name  string
	// Count is the new node count; Added/Removed the diff vs the previous set
	// (matched by prefsKey) — all valid when the refresh succeeded.
	Count   int
	Added   int
	Removed int
	// Skipped: the subscription is disabled and the refresh was a sweep.
	Skipped bool
	// Err is the fetch/parse failure, "" on success. The old nodes are kept.
	Err string
	// The parse accounting on success (see Outcome): the dialect that
	// matched and the entry/duplicate/unrecognized counts.
	Format       string
	Entries      int
	Duplicates   int
	Unrecognized int
}

// Refresh re-fetches subscriptions in p and replaces their nodes, carrying
// each surviving node's STABLE UUID and its per-node core override across the
// refresh (matched by prefsKey — protocol+credential+endpoint+stream). Mutates
// p in place; THE CALLER PERSISTS. One subscription's failure is reported in
// its Result and does not abort the others.
//
// only == "" sweeps every ENABLED subscription; a non-empty only (id or name)
// refreshes exactly that subscription even when disabled — an explicit
// request beats the enabled flag.
func Refresh(ctx context.Context, p *store.Profile, only, ua string) []Result {
	var results []Result
	for i := range p.Subscriptions {
		sub := &p.Subscriptions[i]
		if only != "" {
			if sub.ID != only && sub.Name != only {
				continue
			}
		} else if !sub.Enabled {
			results = append(results, Result{SubID: sub.ID, Name: sub.Name, Skipped: true})
			continue
		}

		body, err := Fetch(ctx, sub.URL, sub.AllowInvalidCerts, ua)
		if err != nil {
			results = append(results, Result{SubID: sub.ID, Name: sub.Name, Err: err.Error()})
			continue
		}
		// A stored format is validated at the wire boundary; a value this
		// build no longer knows (downgrade) falls back to detection.
		format, err := ParseFormat(sub.Format)
		if err != nil {
			format = FormatAuto
		}
		// A parse failure — unrecognized dialect OR zero usable nodes — is a
		// failed refresh: old nodes stay, last_updated stays. An empty body
		// must never masquerade as a successful empty subscription.
		outcome, err := Parse(body, sub.ID, format)
		if err != nil {
			results = append(results, Result{SubID: sub.ID, Name: sub.Name, Err: err.Error()})
			continue
		}
		newNodes := outcome.Nodes
		reconcile(p, sub.ID, newNodes)
		added, removed := diffNodes(p, sub.ID, newNodes)

		p.ReplaceSubscriptionNodes(sub.ID, newNodes)
		sub.LastUpdated = time.Now().UTC()
		results = append(results, Result{
			SubID: sub.ID, Name: sub.Name,
			Count: len(newNodes), Added: added, Removed: removed,
			Format:       string(outcome.Format),
			Entries:      outcome.Entries,
			Duplicates:   outcome.Duplicates,
			Unrecognized: outcome.Unrecognized,
		})
	}
	return results
}

// diffNodes counts the identity changes a replace will make (matched by
// prefsKey), for the SubscriptionUpdated event.
func diffNodes(p *store.Profile, subID string, newNodes []store.Node) (added, removed int) {
	old := map[prefsKey]bool{}
	for i := range p.Nodes {
		n := &p.Nodes[i]
		if n.SubID != nil && *n.SubID == subID {
			old[profileKey(n.Profile())] = true
		}
	}
	fresh := map[prefsKey]bool{}
	for i := range newNodes {
		key := profileKey(newNodes[i].Profile())
		fresh[key] = true
		if !old[key] {
			added++
		}
	}
	for key := range old {
		if !fresh[key] {
			removed++
		}
	}
	return added, removed
}

// prefsKey identifies "the same node" across refreshes — the protocol, the
// credential (uuid/password), the endpoint, and the STREAM shape (transport +
// security). Protocol-agnostic so a subscription mixing schemes diffs
// correctly. The transport/security components are stable TYPE labels
// ("xhttp"/"tcp"/"grpc"/"ws", "reality"/"tls"/"none") — never the rotating
// reality keys/short-id/SNI — so a param rotation still matches the same node,
// yet two nodes sharing server:port:credential but differing in transport or
// security stay DISTINCT (they are genuinely different endpoints, not one to
// collapse).
type prefsKey struct {
	kind      proxy.Protocol
	identity  string
	address   string
	port      uint16
	transport string
	security  string
}

// profileKey builds the refresh-diff key for a node's profile.
func profileKey(p proxy.Profile) prefsKey {
	return prefsKey{
		kind:      p.Kind(),
		identity:  p.Identity(),
		address:   p.ServerAddress(),
		port:      p.ServerPort(),
		transport: p.TransportLabel(),
		security:  p.SecurityLabel(),
	}
}

// reconcile carries each surviving node's STABLE identity across a refresh: its
// UUID (so active_node_id and routing-rule targets keep resolving with no
// churn) and its per-node core override. "The same node" is a prefsKey match
// against the OLD p.Nodes, so it MUST run before ReplaceSubscriptionNodes
// swaps them out. A node still present after the refresh keeps its UUID; a
// genuinely new node keeps the fresh NewNode uuid it was minted with; a removed
// node simply is not in the new set. Nodes that let selection decide stay on
// auto.
func reconcile(p *store.Profile, subID string, newNodes []store.Node) {
	type identity struct {
		id   string
		core *api.CoreType
	}
	prev := map[prefsKey]identity{}
	for i := range p.Nodes {
		n := &p.Nodes[i]
		if n.SubID == nil || *n.SubID != subID {
			continue
		}
		prev[profileKey(n.Profile())] = identity{id: n.ID, core: n.Preferences.CoreOverride}
	}
	for i := range newNodes {
		saved, ok := prev[profileKey(newNodes[i].Profile())]
		if !ok {
			continue
		}
		newNodes[i].ID = saved.id
		if saved.core != nil {
			c := *saved.core
			newNodes[i].Preferences.CoreOverride = &c
		}
	}
}
