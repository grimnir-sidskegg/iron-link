// Package subscription is the daemon-side subscription pipeline: Fetch →
// decode → split → parse → merge → (the caller persists). Fetch lives in
// the DAEMON only — front-ends never fetch. Fetch routing is direct for now
// (through-tunnel / bootstrap is a later seam).
package subscription

import (
	"context"
	"crypto/tls"
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

// fetchTimeout bounds one subscription fetch. DefaultUserAgent
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

// knownClientSuffixes are the Remnawave subscription client-type path segments
// (GET /api/sub/{token}/{clientType}). A URL already ending in one of these is
// left untouched — the user targeted a specific dialect on purpose.
var knownClientSuffixes = []string{
	"json", "v2ray-json", "clash", "mihomo",
	"singbox", "singbox-legacy", "sing-box", "stash", "base64",
}

// jsonCandidateURL returns "<url>/json" when the format is auto or xray and the
// URL carries no explicit client-type suffix. Remnawave serves its auto-select
// balancer group only from the structured "/json" endpoint (XRAY_JSON); the
// bare URL under a generic UA is a base64 link list with no group. ok is false
// when probing would be wrong (a pinned non-xray dialect, or an already
// suffixed URL), so the caller fetches the bare URL unchanged.
func jsonCandidateURL(rawURL string, format Format) (string, bool) {
	if format != FormatAuto && format != FormatXray {
		return "", false
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return "", false
	}
	trimmed := strings.TrimRight(u.Path, "/")
	last := trimmed
	if i := strings.LastIndex(trimmed, "/"); i >= 0 {
		last = trimmed[i+1:]
	}
	for _, s := range knownClientSuffixes {
		if strings.EqualFold(last, s) {
			return "", false
		}
	}
	u.Path = trimmed + "/json"
	return u.String(), true
}

// fetchSubscriptionBody obtains a subscription body, preferring the Remnawave
// "/json" endpoint (which carries the auto-select balancer group) when the
// format is auto or xray and the URL has no explicit client-type suffix. It
// falls back to the bare URL on any probe failure — a non-Remnawave provider, a
// front-end path that does not route the suffix, or an older panel — so every
// other provider behaves exactly as before. The probe is accepted only when it
// parses as xray with at least one node, never on a stray 200 page.
func fetchSubscriptionBody(ctx context.Context, sub *store.Subscription, format Format, ua string) (string, error) {
	if candidate, ok := jsonCandidateURL(sub.URL, format); ok {
		if body, err := Fetch(ctx, candidate, sub.AllowInvalidCerts, ua); err == nil {
			if o, perr := Parse(body, sub.ID, FormatXray); perr == nil && len(o.Nodes) > 0 {
				return body, nil
			}
		}
	}
	return Fetch(ctx, sub.URL, sub.AllowInvalidCerts, ua)
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
	// Discarded: the fetch outcome no longer applies — the subscription was
	// removed or its URL edited while the fetch was in flight (Apply). Nothing
	// was written; not an error, not a success. Never set by Refresh, which
	// fetches and applies against one profile in one go.
	Discarded bool
	// Err is the fetch/parse failure, "" on success. The old nodes are kept.
	Err string
	// The parse accounting on success (see Outcome): the dialect that
	// matched and the entry/duplicate/unrecognized counts.
	Format       string
	Entries      int
	Duplicates   int
	Unrecognized int
}

// FetchResult is the outcome of one FetchSubscription: the body a later
// Apply parses, or the fetch error. SubID/Name/URL are copied from the
// subscription the fetch was issued for — URL is what Apply compares against
// the stored one to detect an edit that happened meanwhile. Format is the
// dialect the fetch was gated on (the "/json" probe) and the one Apply parses
// with.
type FetchResult struct {
	SubID  string
	Name   string
	URL    string
	Format Format
	Body   string
	Err    error
}

// FetchSubscription is the network half of a refresh: it resolves the stored
// format and obtains the body (the "/json" probe included) for a COPY of the
// subscription, touching no profile. The caller holds no store lock while
// this runs — a fetch can take the full fetchTimeout — and hands the result
// to Apply under one. A failed fetch is reported in FetchResult.Err, never
// as a panic or a partial body.
func FetchSubscription(ctx context.Context, sub store.Subscription, ua string) FetchResult {
	// A stored format is validated at the wire boundary; a value this
	// build no longer knows (downgrade) falls back to detection. Resolve it
	// BEFORE the fetch so the panel "/json" probe is gated on it.
	format, err := ParseFormat(sub.Format)
	if err != nil {
		format = FormatAuto
	}
	fr := FetchResult{SubID: sub.ID, Name: sub.Name, URL: sub.URL, Format: format}
	fr.Body, fr.Err = fetchSubscriptionBody(ctx, &sub, format, ua)
	return fr
}

// Apply is the store half of a refresh: it lands one FetchResult on p, the
// profile as it is NOW (freshly loaded by the caller, who persists it after).
// The subscription is found by id; when it is gone, or its URL differs from
// the one fetched, the result is stale and is DISCARDED untouched
// (Result.Discarded) — an error from the old URL must not be pinned on the
// new one. Otherwise:
//
//   - a fetch error or a parse failure (unrecognized dialect OR zero usable
//     nodes) records Subscription.LastError and Result.Err; the old nodes and
//     LastUpdated stay — an empty body must never masquerade as a successful
//     empty subscription;
//   - a success replaces the subscription's nodes (see reconcile for what
//     survives), stamps LastUpdated = now and clears LastError.
func Apply(p *store.Profile, fr FetchResult) Result {
	res := Result{SubID: fr.SubID, Name: fr.Name}
	sub := findSubscriptionByID(p, fr.SubID)
	if sub == nil || sub.URL != fr.URL {
		res.Discarded = true
		return res
	}
	res.Name = sub.Name
	if fr.Err != nil {
		sub.LastError = fr.Err.Error()
		res.Err = sub.LastError
		return res
	}
	outcome, err := Parse(fr.Body, sub.ID, fr.Format)
	if err != nil {
		sub.LastError = err.Error()
		res.Err = sub.LastError
		return res
	}
	// Order matters: reconcile the dialable nodes FIRST so their UUIDs are
	// final, THEN resolve group membership to those UUIDs, THEN reconcile the
	// groups (a provider group keeps its UUID across a refresh by name). Both
	// reconciles read the OLD p.Nodes, so both must precede the replace.
	newNodes := outcome.Nodes
	reconcile(p, sub.ID, newNodes)
	groupNodes := materializeGroups(outcome.Groups, newNodes, sub.ID)
	reconcile(p, sub.ID, groupNodes)
	all := make([]store.Node, 0, len(newNodes)+len(groupNodes))
	all = append(all, newNodes...)
	all = append(all, groupNodes...)
	added, removed := diffNodes(p, sub.ID, all)

	p.ReplaceSubscriptionNodes(sub.ID, all)
	sub.LastUpdated = time.Now().UTC()
	sub.LastError = ""
	// Count is the DIALABLE node count; groups are not counted (diffNodes
	// skips them too, so Added/Removed stay dialable-only).
	res.Count, res.Added, res.Removed = len(newNodes), added, removed
	res.Format = string(outcome.Format)
	res.Entries, res.Duplicates, res.Unrecognized = outcome.Entries, outcome.Duplicates, outcome.Unrecognized
	return res
}

// findSubscriptionByID is the id-only lookup Apply needs (Profile's
// FindSubscription also matches by name, which a stale result must not).
func findSubscriptionByID(p *store.Profile, id string) *store.Subscription {
	for i := range p.Subscriptions {
		if p.Subscriptions[i].ID == id {
			return &p.Subscriptions[i]
		}
	}
	return nil
}

// Refresh re-fetches subscriptions in p and replaces their nodes, carrying
// each surviving node's STABLE UUID and its per-node core override across the
// refresh (matched by prefsKey — protocol+credential+endpoint+stream). Mutates
// p in place; THE CALLER PERSISTS. One subscription's failure is reported in
// its Result (and in the subscription's LastError) and does not abort the
// others. This is FetchSubscription + Apply per subscription, back to back on
// the one profile — the verb path, where the caller already holds the store
// lock for the duration.
//
// only == "" sweeps every ENABLED subscription; a non-empty only (id or name)
// refreshes exactly that subscription even when disabled — an explicit
// request beats the enabled flag.
func Refresh(ctx context.Context, p *store.Profile, only, ua string) []Result {
	var results []Result
	for i := range p.Subscriptions {
		sub := p.Subscriptions[i]
		if only != "" {
			if sub.ID != only && sub.Name != only {
				continue
			}
		} else if !sub.Enabled {
			results = append(results, Result{SubID: sub.ID, Name: sub.Name, Skipped: true})
			continue
		}
		results = append(results, Apply(p, FetchSubscription(ctx, sub, ua)))
	}
	return results
}

// materializeGroups turns the parse's frozen groups into stored group nodes:
// each member key is resolved to the UUID of the (already reconciled) dialable
// node that carries it, so a group references its members by their stable ids.
// A member key that resolves to nothing is dropped; a group left with no
// members is not stored. The group node's own UUID is minted fresh here and
// then reconciled by the caller (matched by name) so it survives a refresh.
func materializeGroups(groups []GroupOut, nodes []store.Node, subID string) []store.Node {
	if len(groups) == 0 {
		return nil
	}
	idByKey := make(map[prefsKey]string, len(nodes))
	for i := range nodes {
		idByKey[profileKey(nodes[i].Profile())] = nodes[i].ID
	}
	var out []store.Node
	for _, g := range groups {
		var members []string
		for _, key := range g.MemberKeys {
			if id, ok := idByKey[key]; ok {
				members = append(members, id)
			}
		}
		if len(members) == 0 {
			continue
		}
		out = append(out, store.NewGroupNode(store.GroupSpec{
			Name:    g.Name,
			Members: members,
			Probe:   g.Probe,
		}, &subID))
	}
	return out
}

// diffNodes counts the identity changes a replace will make (matched by
// nodeKey), for the SubscriptionUpdated event. GROUPS are excluded on both
// sides: the wire counts (Result.Count, list_subscriptions node_count) all mean
// "dialable nodes", so a group appearing/vanishing must not move Added/Removed.
func diffNodes(p *store.Profile, subID string, newNodes []store.Node) (added, removed int) {
	old := map[prefsKey]bool{}
	for i := range p.Nodes {
		n := &p.Nodes[i]
		if n.IsGroup() {
			continue
		}
		if n.SubID != nil && *n.SubID == subID {
			old[nodeKey(n)] = true
		}
	}
	fresh := map[prefsKey]bool{}
	for i := range newNodes {
		if newNodes[i].IsGroup() {
			continue
		}
		key := nodeKey(&newNodes[i])
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

// profileKey builds the refresh-diff key for a node's proxy profile.
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

// nodeKey is the refresh identity of ANY stored node: profileKey for a dialable
// node, a name-keyed group key for a group. The synthetic "group" kind is not a
// registered proxy.Protocol, so a group key can never collide with a real
// node's key. This is what reconcile/diffNodes must use — profileKey alone
// dereferences a group's nil profile.
func nodeKey(n *store.Node) prefsKey {
	if n.IsGroup() {
		return prefsKey{kind: proxy.Protocol("group"), identity: n.Group.Name}
	}
	return profileKey(n.Profile())
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
		prev[nodeKey(n)] = identity{id: n.ID, core: n.Preferences.CoreOverride}
	}
	for i := range newNodes {
		saved, ok := prev[nodeKey(&newNodes[i])]
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
