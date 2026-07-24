// Package subscription is the daemon-side subscription pipeline (G3): Fetch →
// decode → split → parse → merge → (the caller persists). Ported from the
// former Rust subscription pipeline; fetch lives in the DAEMON only — front-ends never
// fetch (G3). Fetch routing is direct for now
// (through-tunnel / bootstrap is a later seam).
package subscription

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
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

// ParseBody decodes a subscription document into nodes owned by subID. The
// body is either standard-base64 of share links or the share links plainly;
// the base64 branch is taken only when it decodes to UTF-8 that LOOKS like
// links (contains "://" — mirrors the Rust heuristic). Unparseable lines are
// skipped, not fatal: providers mix link schemes and we take what we speak.
func ParseBody(body, subID string) []store.Node {
	text := body
	if decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(body)); err == nil {
		if s := string(decoded); strings.Contains(s, "://") {
			text = s
		}
	}

	var nodes []store.Node
	for line := range strings.SplitSeq(text, "\n") {
		p, err := proxy.ParseURL(strings.TrimSpace(line))
		if err != nil {
			continue
		}
		nodes = append(nodes, store.NewNode(p, &subID))
	}
	return nodes
}

// Result is one subscription's refresh outcome (name + what happened).
type Result struct {
	SubID string
	Name  string
	// Count is the new node count; Added/Removed the diff vs the previous set
	// (matched by uuid+address+port) — all valid when the refresh succeeded.
	Count   int
	Added   int
	Removed int
	// Skipped: the subscription is disabled and the refresh was a sweep.
	Skipped bool
	// Err is the fetch/parse failure, "" on success. The old nodes are kept.
	Err string
}

// Refresh re-fetches subscriptions in p and replaces their nodes, carrying
// each node's per-node core override across the refresh (matched by
// uuid+address+port — the Rust `preserve-node-prefs` port). Mutates p in
// place; THE CALLER PERSISTS. One subscription's failure is reported in its
// Result and does not abort the others.
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
		newNodes := ParseBody(body, sub.ID)
		carryPrefs(p, sub.ID, newNodes)
		added, removed := diffNodes(p, sub.ID, newNodes)

		p.ReplaceSubscriptionNodes(sub.ID, newNodes)
		sub.LastUpdated = time.Now().UTC()
		results = append(results, Result{
			SubID: sub.ID, Name: sub.Name,
			Count: len(newNodes), Added: added, Removed: removed,
		})
	}
	return results
}

// diffNodes counts the identity changes a replace will make (matched by
// uuid+address+port), for the SubscriptionUpdated event.
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
// credential (uuid/password), and the endpoint. Protocol-agnostic so a
// subscription mixing schemes diffs correctly.
type prefsKey struct {
	kind     proxy.Protocol
	identity string
	address  string
	port     uint16
}

// profileKey builds the refresh-diff key for a node's profile.
func profileKey(p proxy.Profile) prefsKey {
	return prefsKey{p.Kind(), p.Identity(), p.ServerAddress(), p.ServerPort()}
}

// carryPrefs copies each existing node's core override onto its replacement.
// Nodes that let selection decide stay on auto.
func carryPrefs(p *store.Profile, subID string, newNodes []store.Node) {
	saved := map[prefsKey]api.CoreType{}
	for i := range p.Nodes {
		n := &p.Nodes[i]
		if n.SubID == nil || *n.SubID != subID || n.Preferences.CoreOverride == nil {
			continue
		}
		saved[profileKey(n.Profile())] = *n.Preferences.CoreOverride
	}
	for i := range newNodes {
		if core, ok := saved[profileKey(newNodes[i].Profile())]; ok {
			c := core
			newNodes[i].Preferences.CoreOverride = &c
		}
	}
}
