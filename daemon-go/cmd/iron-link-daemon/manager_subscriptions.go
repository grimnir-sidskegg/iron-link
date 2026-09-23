// The background subscription refresh: a short jittered delay after start
// (so session restore settles first), then a sweep every minute over the
// ACTIVE profile's subscriptions. A subscription is due when its auto-update
// is on and its interval has elapsed since the last SUCCESSFUL refresh. The
// fetch runs with no lock held and lands on a re-loaded profile under the
// manager mutex (internal/subscription's fetch/apply seam), so a slow
// provider never stalls the control plane. A failure records last_error and
// backs the next attempt off in memory only — a restart forgets the backoff
// on purpose: one attempt shortly after start is the catch-up. When a
// refresh changes the node set a running session was compiled from, that
// session is re-activated with the same context (the "reconnect").
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"slices"
	"time"

	"ironlink/daemon/internal/api"
	"ironlink/daemon/internal/engine"
	"ironlink/daemon/internal/store"
	"ironlink/daemon/internal/subscription"
)

const (
	// subscriptionInitialDelay (plus up to the same again of jitter) holds
	// the first sweep back until session restore has settled.
	subscriptionInitialDelay = 30 * time.Second
	// subscriptionTickInterval is how often the loop re-evaluates what is
	// due — intervals, the auto-update flags and the active profile all
	// change between ticks.
	subscriptionTickInterval = time.Minute
	// The failure backoff: the first retry waits subscriptionBackoffMin and
	// every further consecutive failure doubles the wait, up to
	// subscriptionBackoffMax.
	subscriptionBackoffMin = 10 * time.Minute
	subscriptionBackoffMax = time.Hour
)

// subscriptionRetry is one subscription's in-memory failure backoff:
// failedAt is when the last attempt failed, delay the wait that failure
// imposed (doubling per consecutive failure), next = failedAt + delay. A
// success from anywhere — the loop or a manual refresh — moves the
// subscription's LastUpdated past failedAt, which supersedes the entry (see
// supersededBy); the sweep prunes such entries.
type subscriptionRetry struct {
	failedAt time.Time
	delay    time.Duration
	next     time.Time
}

// supersededBy reports whether sub was refreshed successfully after the
// failure this entry records — the backoff then no longer applies. A stamp
// ahead of now (the clock was set back) is not evidence of a later success
// and does not supersede; subscriptionDue still counts it as due, so the
// backoff is what holds such a subscription back after a failure.
func (r subscriptionRetry) supersededBy(sub store.Subscription, now time.Time) bool {
	return sub.LastUpdated.After(r.failedAt) && !sub.LastUpdated.After(now)
}

// nextSubscriptionRetry is the entry a failure at now leaves behind: the
// minimum wait after a first failure, double the previous wait (capped)
// after a consecutive one. prev is the entry this failure follows, nil when
// there was none.
func nextSubscriptionRetry(prev *subscriptionRetry, now time.Time) subscriptionRetry {
	delay := subscriptionBackoffMin
	if prev != nil {
		delay = min(prev.delay*2, subscriptionBackoffMax)
	}
	return subscriptionRetry{failedAt: now, delay: delay, next: now.Add(delay)}
}

// subscriptionDue reports whether sub's automatic refresh is due at now:
// auto-update on, a positive interval, and the interval elapsed since the
// last SUCCESSFUL refresh. A LastUpdated in the future (the clock was set
// back) counts as due — waiting it out could silence the refresh for as
// long as the clock had been ahead. The failure backoff is a separate gate
// (dueSubscriptionsLocked).
func subscriptionDue(sub store.Subscription, now time.Time) bool {
	if !sub.Enabled || sub.UpdateIntervalSec == 0 {
		return false
	}
	if sub.LastUpdated.After(now) {
		return true
	}
	return now.Sub(sub.LastUpdated) >= time.Duration(sub.UpdateIntervalSec)*time.Second
}

// subscriptionLoop runs for the daemon's lifetime: the jittered initial
// delay, one sweep, then a sweep per tick. m.subInitialDelay and
// m.subTickInterval are the TEST overrides of the two timings.
func (m *manager) subscriptionLoop(ctx context.Context) {
	delay, tick := subscriptionInitialDelay, subscriptionTickInterval
	if m.subInitialDelay != 0 {
		delay = m.subInitialDelay
	}
	if m.subTickInterval != 0 {
		tick = m.subTickInterval
	}
	select {
	case <-ctx.Done():
		return
	case <-time.After(delay + rand.N(delay)):
	}
	m.sweepSubscriptions(ctx)
	ticker := time.NewTicker(tick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.sweepSubscriptions(ctx)
		}
	}
}

// sweepSubscriptions runs one pass: pick the due subscriptions of the
// active profile under mu, fetch each with mu released, land each result
// under mu again. Fetches run one at a time — a sweep is not latency
// sensitive, and a provider is not hammered. The subscriptions are
// snapshotted once; one edited or removed during the sweep is caught by
// Apply's drift check, not here. A fetch the shutdown cuts short is dropped,
// not landed.
func (m *manager) sweepSubscriptions(ctx context.Context) {
	m.mu.Lock()
	profileName, due, ua := m.dueSubscriptionsLocked(time.Now())
	m.mu.Unlock()
	for _, sub := range due {
		if ctx.Err() != nil {
			return
		}
		fr := subscription.FetchSubscription(ctx, sub, ua)
		if ctx.Err() != nil {
			// The daemon is stopping: a fetch cut short by the shutdown is
			// not a provider failure and is not recorded. The subscription
			// stays due; the first sweep after the next start is the catch-up.
			return
		}
		m.landSubscriptionFetch(profileName, fr)
	}
}

// dueSubscriptionsLocked loads the active profile and returns its name, the
// subscriptions due at now that no failure backoff holds back, and the
// User-Agent to fetch with. It also prunes the backoff map: an entry a
// later success superseded, or whose subscription is gone. No active
// profile means nothing to do; a profile that does not load is logged (any
// verb fails on it the same way). Callers hold mu.
func (m *manager) dueSubscriptionsLocked(now time.Time) (string, []store.Subscription, string) {
	name, err := m.store.ActiveProfileName()
	if err != nil || name == "" {
		return "", nil, ""
	}
	p, err := m.store.LoadProfile(name)
	if err != nil {
		m.logSink("warn", "subscription refresh: "+err.Error())
		return "", nil, ""
	}
	present := make(map[string]bool, len(p.Subscriptions))
	var due []store.Subscription
	for _, sub := range p.Subscriptions {
		present[sub.ID] = true
		retry, held := m.subRetry[sub.ID]
		if held && retry.supersededBy(sub, now) {
			delete(m.subRetry, sub.ID)
			held = false
		}
		if !subscriptionDue(sub, now) || (held && now.Before(retry.next)) {
			continue
		}
		due = append(due, sub)
	}
	for id := range m.subRetry {
		if !present[id] {
			delete(m.subRetry, id)
		}
	}
	return name, due, m.subscriptionUA()
}

// landSubscriptionFetch is the store half of one automatic refresh: apply
// the fetch outcome to a freshly loaded profile under mu, persist, publish
// subscription_updated, settle the backoff and log the outcome. A result
// the profile no longer matches (the subscription removed or re-pointed
// meanwhile) is dropped without a word — Apply wrote nothing. The backoff is
// settled after the save: a fetch failure backs off whether or not the
// profile could be saved (the endpoint failed either way), and a success the
// store could not persist backs off too — its last_updated never landed, so
// without a hold the provider would be re-fetched every sweep while the
// store is unwritable. A success that changed the node set of a running
// session on this profile re-activates that session, with mu released.
func (m *manager) landSubscriptionFetch(profileName string, fr subscription.FetchResult) {
	now := time.Now()
	m.mu.Lock()
	p, err := m.store.LoadProfile(profileName)
	if err != nil {
		m.mu.Unlock()
		m.logSink("warn", fmt.Sprintf("subscription %q: refresh not applied: %v", fr.Name, err))
		return
	}
	res := subscription.Apply(p, fr)
	if res.Discarded {
		delete(m.subRetry, fr.SubID)
		m.mu.Unlock()
		return
	}
	saveErr := m.store.SaveProfile(profileName, p)
	var retry subscriptionRetry
	if res.Err != "" || saveErr != nil {
		var prev *subscriptionRetry
		if r, ok := m.subRetry[fr.SubID]; ok {
			prev = &r
		}
		retry = nextSubscriptionRetry(prev, now)
		m.subRetry[fr.SubID] = retry
	} else {
		delete(m.subRetry, fr.SubID)
	}
	var reconnect *sessionReconnect
	if saveErr == nil {
		m.publishRefreshLocked([]subscription.Result{res})
		if res.Err == "" {
			reconnect = m.sessionReconnectLocked(profileName, p)
		}
	}
	m.mu.Unlock()

	switch {
	case saveErr != nil:
		m.logSink("warn", fmt.Sprintf("subscription %q: refresh not saved: %v; next attempt in %s", res.Name, saveErr, retry.delay))
	case res.Err != "":
		m.logSink("warn", fmt.Sprintf("subscription %q: refresh failed: %s; next attempt in %s", res.Name, res.Err, retry.delay))
	default:
		m.logSink("info", fmt.Sprintf("subscription %q: refreshed, %d nodes (+%d -%d)", res.Name, res.Count, res.Added, res.Removed))
	}
	if reconnect != nil {
		m.reconnectSession(*reconnect)
	}
}

// sessionReconnect is a decided re-activation: the request that re-runs the
// running session's activation, the session it was decided against and the
// selection the decision was made against (m.active.Node as read then).
type sessionReconnect struct {
	req       api.Request
	sess      *engine.Session
	selection string
	name      string // the active selection, for the log lines
}

// sessionReconnectLocked decides whether the refresh just applied to p
// changed what a running session on that profile embeds. Nothing running,
// or a session on another profile: nil. The plan is rebuilt around the node
// the running session was compiled around (m.plan.ActiveTag — so the group
// lowering and the xray ordering line up with m.plan; a live switch since
// then moved the selection, not the embedded set) and compared member-wise
// (planMembersEqual). The current selection (m.active.Node, a display name)
// is resolved in the new profile by name, then through the running plan's
// tag for that name (a provider rename keeps the stable id): missing, or
// the plan no longer buildable, keeps the running session as it is with a
// "warn". Callers hold mu.
func (m *manager) sessionReconnectLocked(profileName string, p *store.Profile) *sessionReconnect {
	if m.sess == nil || m.active == nil || m.active.Profile == nil || m.active.Node == nil || *m.active.Profile != profileName {
		return nil
	}
	selection := *m.active.Node
	current := p.FindNodeByName(selection)
	if current == nil {
		if tag, ok := m.plan.MemberTagForName(selection); ok {
			current = p.FindNodeByID(tag)
		}
	}
	if current == nil {
		m.logSink("warn", fmt.Sprintf("subscription refresh removed the active node %q; the running session is kept as it is", selection))
		return nil
	}
	base := p.FindNodeByID(m.plan.ActiveTag)
	if base == nil {
		base = current
	}
	fresh, err := buildPlan(p, base)
	if err != nil {
		m.logSink("warn", fmt.Sprintf("subscription refresh: %v; the running session is kept as it is", err))
		return nil
	}
	if planMembersEqual(m.plan, fresh) {
		m.relabelPlanLocked(fresh, current)
		return nil
	}
	name := current.DisplayName()
	profile, tun := *m.active.Profile, m.active.Tun
	return &sessionReconnect{
		sess:      m.sess,
		selection: selection,
		name:      name,
		req: api.Request{
			Command: api.CmdActivate,
			Profile: &profile,
			Node:    &name,
			Routing: m.active.Routing,
			Tun:     &tun,
		},
	}
}

// reconnectSession re-runs the activation for a decided reconnect with mu
// released: the activation is prepared like any other (prepareActivation,
// no lock), then started only while the session it was decided against is
// still the running one AND the selection is still the one the decision was
// made against — a stop, another activation or a live switch_node that
// slipped in meanwhile wins, and the reconnect is dropped (a live switch
// replaces the selection, not the session, so the session identity alone
// would not catch it).
func (m *manager) reconnectSession(rc sessionReconnect) {
	prep, err := m.prepareActivation(rc.req)
	if err != nil {
		m.logSink("error", fmt.Sprintf("subscription refresh changed the active selection %q, but the reconnect failed: %v", rc.name, err))
		return
	}
	m.mu.Lock()
	if m.sess != rc.sess || m.active == nil || m.active.Node == nil || *m.active.Node != rc.selection {
		m.mu.Unlock()
		m.logSink("info", fmt.Sprintf("subscription refresh changed the active selection %q, but the session or selection changed meanwhile; reconnect skipped", rc.name))
		return
	}
	resp := m.startActivationLocked(prep)
	m.mu.Unlock()
	if resp.Status != api.StatusActivated {
		m.logSink("error", fmt.Sprintf("subscription refresh changed the active selection %q, but the reconnect failed: %s", rc.name, resp.Message))
		return
	}
	m.logSink("info", fmt.Sprintf("subscription refresh changed the active selection %q; session reconnected", rc.name))
}

// relabelPlanLocked carries the display names of a refresh that changed no
// member over to the running plan (the tag -> name map status and switch_node
// resolve through) and to the persisted selection, so a provider rotating
// text in node names (remaining traffic, expiry) costs a label update, not
// a reconnect. current is the active selection as found in the refreshed
// profile. Callers hold mu.
func (m *manager) relabelPlanLocked(fresh engine.SessionPlan, current *store.Node) {
	names := make(map[string]string, len(fresh.Natives)+len(fresh.XrayNodes))
	for i := range fresh.Natives {
		names[fresh.Natives[i].ID] = fresh.Natives[i].Name
	}
	for i := range fresh.XrayNodes {
		names[fresh.XrayNodes[i].ID] = fresh.XrayNodes[i].Name
	}
	changed := false
	relabel := func(members []engine.NamedNode) {
		for i := range members {
			if n, ok := names[members[i].ID]; ok && n != members[i].Name {
				members[i].Name = n
				changed = true
			}
		}
	}
	relabel(m.plan.Natives)
	relabel(m.plan.XrayNodes)
	if m.plan.Group != nil && fresh.Group != nil && m.plan.Group.Name != fresh.Group.Name {
		m.plan.Group.Name = fresh.Group.Name
		changed = true
	}
	if name := current.DisplayName(); m.active.Node == nil || *m.active.Node != name {
		m.active.Node = &name
		changed = true
	}
	if changed {
		m.saveLastSessionLocked()
		m.broadcastStateLocked()
	}
}

// planMembersEqual reports whether two session plans embed the same node
// set: the same native and xray members — by id AND document, since a
// provider can move a node's endpoint or credentials under the stable id a
// refresh carries over — and the same group lowering (id and member set).
// Member order is ignored: a provider reordering its list changes nothing
// the session dials, and so is a member's display name: a label, carried
// over by relabelPlanLocked. Routing, cache path and tunables are settings,
// not subscription content, and are ignored too.
func planMembersEqual(a, b engine.SessionPlan) bool {
	if !memberSetsEqual(a.Natives, b.Natives) || !memberSetsEqual(a.XrayNodes, b.XrayNodes) {
		return false
	}
	switch {
	case a.Group == nil && b.Group == nil:
		return true
	case a.Group == nil || b.Group == nil:
		return false
	}
	return a.Group.ID == b.Group.ID && stringSetsEqual(a.Group.Members, b.Group.Members)
}

// memberSetsEqual compares two member lists as id → digest maps.
func memberSetsEqual(a, b []engine.NamedNode) bool {
	if len(a) != len(b) {
		return false
	}
	index := make(map[string]string, len(a))
	for i := range a {
		index[a[i].ID] = memberDigest(&a[i])
	}
	for i := range b {
		if d, ok := index[b[i].ID]; !ok || d != memberDigest(&b[i]) {
			return false
		}
	}
	return true
}

// memberDigest is the comparable form of one member: the canonical
// {"<kind>": body} document the store writes for it, minus the body's
// server_name — that key is the display name (the share-link fragment),
// a label rather than a dial parameter, carried over separately
// (relabelPlanLocked). A document that cannot be re-encoded digests as the
// error text, which never equals a real document.
func memberDigest(n *engine.NamedNode) string {
	doc, err := json.Marshal(store.ProfileDoc{P: n.Profile})
	if err != nil {
		return err.Error()
	}
	var union map[string]map[string]json.RawMessage
	if err := json.Unmarshal(doc, &union); err != nil {
		return err.Error()
	}
	for _, body := range union {
		delete(body, "server_name")
	}
	stripped, err := json.Marshal(union)
	if err != nil {
		return err.Error()
	}
	return string(stripped)
}

// stringSetsEqual compares two id lists regardless of order.
func stringSetsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	as, bs := slices.Clone(a), slices.Clone(b)
	slices.Sort(as)
	slices.Sort(bs)
	return slices.Equal(as, bs)
}
