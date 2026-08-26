// The G3 store verbs: the daemon owns profile/node/subscription state and
// front-ends speak NAMES over the wire — no client ever touches the files.
// Every handler loads → mutates → SAVES under m.mu (one writer by design);
// `profile` nil resolves to the store's active profile.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"time"

	"ironlink/daemon/internal/api"
	"ironlink/daemon/internal/proxy"
	"ironlink/daemon/internal/routing"
	"ironlink/daemon/internal/store"
	"ironlink/daemon/internal/subscription"
)

// resolveProfileName maps the request's optional profile to a concrete name.
func (m *manager) resolveProfileName(req api.Request) (string, error) {
	if req.Profile != nil && *req.Profile != "" {
		return *req.Profile, nil
	}
	name, err := m.store.ActiveProfileName()
	if err != nil {
		return "", err
	}
	if name == "" {
		return "", fmt.Errorf("no profile named and no active profile set")
	}
	return name, nil
}

// withProfile loads the resolved profile, runs fn, and persists the profile
// when fn succeeds. Callers hold NO locks; this takes m.mu.
func (m *manager) withProfile(req api.Request, fn func(name string, p *store.Profile) (api.Response, error)) api.Response {
	m.mu.Lock()
	defer m.mu.Unlock()

	name, err := m.resolveProfileName(req)
	if err != nil {
		return errResp(err.Error())
	}
	p, err := m.store.LoadProfile(name)
	if err != nil {
		return errResp(err.Error())
	}
	resp, err := fn(name, p)
	if err != nil {
		return errResp(err.Error())
	}
	if err := m.store.SaveProfile(name, p); err != nil {
		return errResp(err.Error())
	}
	return resp
}

func (m *manager) listProfiles() api.Response {
	m.mu.Lock()
	defer m.mu.Unlock()
	names, err := m.store.ListProfiles()
	if err != nil {
		return errResp(err.Error())
	}
	resp := api.Response{Status: api.StatusProfiles, Profiles: names}
	if active, err := m.store.ActiveProfileName(); err == nil && active != "" {
		resp.ActiveProfile = &active
	}
	return resp
}

func (m *manager) createProfile(req api.Request) api.Response {
	if req.Profile == nil || *req.Profile == "" {
		return errResp("create_profile requires a profile name")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.store.CreateProfile(*req.Profile); err != nil {
		return errResp(err.Error())
	}
	return api.Response{Status: api.StatusOk, Message: "profile created: " + *req.Profile}
}

func (m *manager) deleteProfile(req api.Request) api.Response {
	if req.Profile == nil || *req.Profile == "" {
		return errResp("delete_profile requires a profile name")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.store.DeleteProfile(*req.Profile); err != nil {
		return errResp(err.Error())
	}
	return api.Response{Status: api.StatusOk, Message: "profile deleted: " + *req.Profile}
}

func (m *manager) setActiveProfile(req api.Request) api.Response {
	if req.Profile == nil || *req.Profile == "" {
		return errResp("set_active_profile requires a profile name")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.store.SetActiveProfile(*req.Profile); err != nil {
		return errResp(err.Error())
	}
	return api.Response{Status: api.StatusOk, Message: "active profile: " + *req.Profile}
}

func (m *manager) listNodes(req api.Request) api.Response {
	m.mu.Lock()
	defer m.mu.Unlock()
	name, err := m.resolveProfileName(req)
	if err != nil {
		return errResp(err.Error())
	}
	p, err := m.store.LoadProfile(name)
	if err != nil {
		return errResp(err.Error())
	}
	infos := make([]api.NodeInfo, 0, len(p.Nodes))
	for i := range p.Nodes {
		n := &p.Nodes[i]
		active := p.ActiveNodeID != nil && *p.ActiveNodeID == n.ID
		if n.IsGroup() {
			infos = append(infos, api.NodeInfo{
				ID:            n.ID,
				Name:          n.DisplayName(),
				SubID:         n.SubID,
				Active:        active,
				EligibleCores: []api.CoreType{}, // wire shape: always an array
				Kind:          api.NodeKindGroup,
				Members:       resolveGroupMemberIDs(p, n),
			})
			continue
		}
		prof := n.Profile()
		eligible := proxy.EligibleCores(prof)
		if eligible == nil {
			eligible = []api.CoreType{} // wire shape: always an array, never null
		}
		infos = append(infos, api.NodeInfo{
			ID:            n.ID,
			Name:          n.DisplayName(),
			SubID:         n.SubID,
			CoreOverride:  n.Preferences.CoreOverride,
			Protocol:      string(prof.Kind()),
			Transport:     prof.TransportLabel(),
			Security:      prof.SecurityLabel(),
			Active:        active,
			EligibleCores: eligible,
		})
	}
	return api.Response{Status: api.StatusNodes, Nodes: infos}
}

// resolveGroupMemberIDs returns a group's member node ids, resolved against the
// profile's current nodes: an explicit Members list filtered to ids that still
// exist and are not themselves groups; an AllOfSub group expands to every
// dialable node of that subscription. Members that no longer resolve (a churned
// or removed node) are dropped, with the count logged — an explicit user list
// is the only membership that can silently shed this way (AllOfSub is derived
// fresh each call). Never returns nil (wire shape: an array).
func resolveGroupMemberIDs(p *store.Profile, g *store.Node) []string {
	ids := []string{}
	if g.Group.AllOfSub != nil {
		sub := *g.Group.AllOfSub
		for i := range p.Nodes {
			n := &p.Nodes[i]
			if !n.IsGroup() && n.SubID != nil && *n.SubID == sub {
				ids = append(ids, n.ID)
			}
		}
		return ids
	}
	dropped := 0
	for _, id := range g.Group.Members {
		if n := p.FindNodeByID(id); n != nil && !n.IsGroup() {
			ids = append(ids, id)
		} else {
			dropped++
		}
	}
	if dropped > 0 {
		fmt.Fprintf(os.Stderr, "iron-link-daemon: group %q dropped %d unresolved member(s)\n", g.Group.Name, dropped)
	}
	return ids
}

// resolveNode finds a node by display name OR id.
func resolveNode(p *store.Profile, ref string) (*store.Node, error) {
	if n := p.FindNodeByName(ref); n != nil {
		return n, nil
	}
	if n := p.FindNodeByID(ref); n != nil {
		return n, nil
	}
	return nil, fmt.Errorf("node %q not found", ref)
}

func (m *manager) selectNode(req api.Request) api.Response {
	if req.Node == nil || *req.Node == "" {
		return errResp("select_node requires a node")
	}
	return m.withProfile(req, func(name string, p *store.Profile) (api.Response, error) {
		n, err := resolveNode(p, *req.Node)
		if err != nil {
			return api.Response{}, err
		}
		if err := p.SetActiveNode(n.ID); err != nil {
			return api.Response{}, err
		}
		return api.Response{Status: api.StatusOk, Node: n.DisplayName()}, nil
	})
}

func (m *manager) addNode(req api.Request) api.Response {
	if req.URL == nil || *req.URL == "" {
		return errResp("add_node requires a share link url")
	}
	return m.withProfile(req, func(name string, p *store.Profile) (api.Response, error) {
		parsed, err := proxy.ParseURL(*req.URL)
		if err != nil {
			return api.Response{}, err
		}
		node := store.NewNode(parsed, nil)
		p.AddNode(node)
		return api.Response{Status: api.StatusOk, Node: node.DisplayName(),
			Message: "node added: " + node.DisplayName()}, nil
	})
}

func (m *manager) removeNode(req api.Request) api.Response {
	if req.Node == nil || *req.Node == "" {
		return errResp("remove_node requires a node")
	}
	return m.withProfile(req, func(name string, p *store.Profile) (api.Response, error) {
		n, err := resolveNode(p, *req.Node)
		if err != nil {
			return api.Response{}, err
		}
		display := n.DisplayName()
		p.RemoveNodeByID(n.ID)
		return api.Response{Status: api.StatusOk, Message: "node removed: " + display}, nil
	})
}

func (m *manager) setNodePrefs(req api.Request) api.Response {
	if req.Node == nil || *req.Node == "" {
		return errResp("set_node_prefs requires a node")
	}
	return m.withProfile(req, func(name string, p *store.Profile) (api.Response, error) {
		n, err := resolveNode(p, *req.Node)
		if err != nil {
			return api.Response{}, err
		}
		if n.IsGroup() {
			return api.Response{}, fmt.Errorf("a group has no core override; pin a member instead")
		}
		// nil core_override CLEARS the pin (back to "selection decides").
		n.Preferences.CoreOverride = req.CoreOverride
		msg := "core override cleared"
		if req.CoreOverride != nil {
			msg = "core override: " + string(*req.CoreOverride)
		}
		return api.Response{Status: api.StatusOk, Node: n.DisplayName(), Message: msg}, nil
	})
}

// groupUpsert is the wire shape of an upsert_group payload: a user group's id
// (empty = create), name, membership (EXACTLY one of Members / AllOfSub), and
// probe tuning.
type groupUpsert struct {
	ID       string           `json:"id"`
	Name     string           `json:"name"`
	Members  []string         `json:"members"`
	AllOfSub *string          `json:"all_of_sub"`
	Probe    store.GroupProbe `json:"probe"`
}

// upsertGroup creates or edits a USER group (SubID nil). Membership is EITHER an
// explicit list of existing dialable node ids OR every dialable node of a
// subscription (all_of_sub) — never both, never neither. A missing id creates;
// an existing user-group id replaces its spec in place.
func (m *manager) upsertGroup(req api.Request) api.Response {
	if len(req.Group) == 0 {
		return errResp("upsert_group requires a group payload")
	}
	var g groupUpsert
	if err := json.Unmarshal(req.Group, &g); err != nil {
		return errResp("decode group: " + err.Error())
	}
	hasMembers := len(g.Members) > 0
	hasSub := g.AllOfSub != nil && *g.AllOfSub != ""
	if hasMembers == hasSub {
		return errResp("a group needs exactly one of members or all_of_sub")
	}
	return m.withProfile(req, func(name string, p *store.Profile) (api.Response, error) {
		spec := store.GroupSpec{Name: g.Name, Probe: g.Probe}
		if hasSub {
			// FindSubscription accepts an id OR a name; membership resolution
			// (resolveGroupMemberIDs) matches a node's SubID, which is ALWAYS the
			// subscription UUID — so normalize the stored ref to the id, else a
			// name-based group would silently resolve to zero members.
			sub := p.FindSubscription(*g.AllOfSub)
			if sub == nil {
				return api.Response{}, fmt.Errorf("subscription %q not found", *g.AllOfSub)
			}
			id := sub.ID
			spec.AllOfSub = &id
		} else {
			for _, id := range g.Members {
				member := p.FindNodeByID(id)
				if member == nil || member.IsGroup() {
					return api.Response{}, fmt.Errorf("group member %q is not a dialable node", id)
				}
			}
			spec.Members = g.Members
		}
		if g.ID == "" {
			id, err := p.AddGroup(spec)
			if err != nil {
				return api.Response{}, err
			}
			return api.Response{Status: api.StatusOk, Node: spec.Name, Message: "group created: " + spec.Name + " (" + id + ")"}, nil
		}
		if err := p.ReplaceGroupByID(g.ID, spec); err != nil {
			return api.Response{}, err
		}
		return api.Response{Status: api.StatusOk, Node: spec.Name, Message: "group updated: " + spec.Name}, nil
	})
}

// getGroup returns ONE group's stored spec as its full JSON document — the read
// half of upsert_group (list_nodes carries only the RESOLVED member ids, losing
// the membership mode and the probe tuning). An editor fetches this to round-trip
// the true spec. The ref (req.Node) must resolve to a group.
func (m *manager) getGroup(req api.Request) api.Response {
	if req.Node == nil || *req.Node == "" {
		return errResp("get_group requires a group name or id")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	name, err := m.resolveProfileName(req)
	if err != nil {
		return errResp(err.Error())
	}
	p, err := m.store.LoadProfile(name)
	if err != nil {
		return errResp(err.Error())
	}
	n, err := resolveNode(p, *req.Node)
	if err != nil {
		return errResp(err.Error())
	}
	if !n.IsGroup() {
		return errResp(fmt.Sprintf("node %q is not a group", *req.Node))
	}
	raw, err := json.Marshal(n.Group)
	if err != nil {
		return errResp("encode group: " + err.Error())
	}
	return api.Response{Status: api.StatusGroupConfig, GroupConfig: raw}
}

func (m *manager) listSubscriptions(req api.Request) api.Response {
	m.mu.Lock()
	defer m.mu.Unlock()
	name, err := m.resolveProfileName(req)
	if err != nil {
		return errResp(err.Error())
	}
	p, err := m.store.LoadProfile(name)
	if err != nil {
		return errResp(err.Error())
	}
	infos := make([]api.SubscriptionInfo, 0, len(p.Subscriptions))
	for i := range p.Subscriptions {
		s := &p.Subscriptions[i]
		count := 0
		for j := range p.Nodes {
			// node_count means dialable nodes; a provider group is not one.
			if p.Nodes[j].IsGroup() {
				continue
			}
			if p.Nodes[j].SubID != nil && *p.Nodes[j].SubID == s.ID {
				count++
			}
		}
		format, err := subscription.ParseFormat(s.Format)
		if err != nil {
			format = subscription.FormatAuto
		}
		infos = append(infos, api.SubscriptionInfo{
			ID:                s.ID,
			Name:              s.Name,
			URL:               s.URL,
			Enabled:           s.Enabled,
			AllowInvalidCerts: s.AllowInvalidCerts,
			LastUpdated:       s.LastUpdated.Format(time.RFC3339),
			NodeCount:         count,
			Format:            string(format),
		})
	}
	return api.Response{Status: api.StatusSubscriptions, Subscriptions: infos}
}

func (m *manager) addSubscription(req api.Request) api.Response {
	if req.URL == nil || *req.URL == "" {
		return errResp("add_subscription requires a url")
	}
	name := ""
	if req.Name != nil {
		name = *req.Name
	}
	if name == "" {
		if u, err := url.Parse(*req.URL); err == nil {
			name = u.Hostname()
		}
		if name == "" {
			return errResp("add_subscription: cannot derive a name from the url; pass one")
		}
	}

	var format subscription.Format
	if req.Format != nil {
		f, err := subscription.ParseFormat(*req.Format)
		if err != nil {
			return errResp(err.Error())
		}
		format = f
	}

	return m.withProfile(req, func(profName string, p *store.Profile) (api.Response, error) {
		sub := store.NewSubscription(*req.URL, name)
		if req.AllowInvalidCerts != nil {
			sub.AllowInvalidCerts = *req.AllowInvalidCerts
		}
		if format != "" {
			sub.Format = string(format)
		}
		subID, err := p.AddSubscription(sub)
		if err != nil {
			return api.Response{}, err
		}
		// The initial fetch: its failure does NOT undo the add — the
		// subscription stays stored (same split semantics as the Rust client
		// blocks) and the outcome is reported.
		results := subscription.Refresh(context.Background(), p, subID, m.subscriptionUA())
		return api.Response{
			Status:    api.StatusRefreshed,
			Message:   "subscription added: " + name,
			Refreshed: m.publishRefreshLocked(results),
		}, nil
	})
}

func (m *manager) refreshSubscriptions(req api.Request) api.Response {
	only := ""
	if req.Sub != nil {
		only = *req.Sub
	}
	return m.withProfile(req, func(name string, p *store.Profile) (api.Response, error) {
		results := subscription.Refresh(context.Background(), p, only, m.subscriptionUA())
		if only != "" && len(results) == 0 {
			return api.Response{}, fmt.Errorf("subscription %q not found", only)
		}
		return api.Response{Status: api.StatusRefreshed, Refreshed: m.publishRefreshLocked(results)}, nil
	})
}

func (m *manager) removeSubscription(req api.Request) api.Response {
	if req.Sub == nil || *req.Sub == "" {
		return errResp("remove_subscription requires a subscription id or name")
	}
	return m.withProfile(req, func(name string, p *store.Profile) (api.Response, error) {
		sub := p.FindSubscription(*req.Sub)
		if sub == nil {
			return api.Response{}, fmt.Errorf("subscription %q not found", *req.Sub)
		}
		display := sub.Name
		p.RemoveSubscription(sub.ID)
		return api.Response{Status: api.StatusOk,
			Message: "subscription removed (with its nodes): " + display}, nil
	})
}

// updateSubscription edits a stored subscription's metadata in place — a nil
// field is left unchanged (the client sends only what it edited). It does NOT
// re-fetch; a URL change takes effect on the next refresh.
func (m *manager) updateSubscription(req api.Request) api.Response {
	if req.Sub == nil || *req.Sub == "" {
		return errResp("update_subscription requires a subscription id or name")
	}
	return m.withProfile(req, func(name string, p *store.Profile) (api.Response, error) {
		sub := p.FindSubscription(*req.Sub)
		if sub == nil {
			return api.Response{}, fmt.Errorf("subscription %q not found", *req.Sub)
		}
		if req.Name != nil && *req.Name != "" {
			sub.Name = *req.Name
		}
		if req.URL != nil && *req.URL != "" {
			sub.URL = *req.URL
		}
		if req.AllowInvalidCerts != nil {
			sub.AllowInvalidCerts = *req.AllowInvalidCerts
		}
		if req.Enabled != nil {
			sub.Enabled = *req.Enabled
		}
		if req.UpdateIntervalSec != nil {
			sub.UpdateIntervalSec = *req.UpdateIntervalSec
		}
		if req.Format != nil {
			f, err := subscription.ParseFormat(*req.Format)
			if err != nil {
				return api.Response{}, err
			}
			sub.Format = string(f)
		}
		return api.Response{Status: api.StatusOk,
			Message: "subscription updated: " + sub.Name}, nil
	})
}

func (m *manager) listRouting(req api.Request) api.Response {
	m.mu.Lock()
	defer m.mu.Unlock()
	name, err := m.resolveProfileName(req)
	if err != nil {
		return errResp(err.Error())
	}
	p, err := m.store.LoadProfile(name)
	if err != nil {
		return errResp(err.Error())
	}
	infos := make([]api.RoutingInfo, 0, len(p.RoutingConfigs))
	for i := range p.RoutingConfigs {
		rc := &p.RoutingConfigs[i]
		infos = append(infos, api.RoutingInfo{
			ID:       rc.ID,
			Name:     rc.Name,
			Rules:    len(rc.Rules),
			RuleSets: len(rc.RuleSets),
			Active:   p.ActiveRoutingID != nil && *p.ActiveRoutingID == rc.ID,
		})
	}
	return api.Response{Status: api.StatusRouting, Routing: infos}
}

func (m *manager) selectRouting(req api.Request) api.Response {
	if req.Routing == nil || *req.Routing == "" {
		return errResp("select_routing requires a routing name or id")
	}
	return m.withProfile(req, func(name string, p *store.Profile) (api.Response, error) {
		rc := p.FindRouting(*req.Routing)
		if rc == nil {
			return api.Response{}, fmt.Errorf("routing %q not found", *req.Routing)
		}
		if err := p.SetActiveRouting(rc.ID); err != nil {
			return api.Response{}, err
		}
		return api.Response{Status: api.StatusOk, Message: "active routing: " + rc.Name}, nil
	})
}

// getRouting returns ONE routing config as its full JSON document — the read
// half of upsert_routing (list_routing carries only summaries). This is what
// lets a client edit routing without ever reading the store files.
func (m *manager) getRouting(req api.Request) api.Response {
	if req.Routing == nil || *req.Routing == "" {
		return errResp("get_routing requires a routing name or id")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	name, err := m.resolveProfileName(req)
	if err != nil {
		return errResp(err.Error())
	}
	p, err := m.store.LoadProfile(name)
	if err != nil {
		return errResp(err.Error())
	}
	rc := p.FindRouting(*req.Routing)
	if rc == nil {
		return errResp(fmt.Sprintf("routing %q not found", *req.Routing))
	}
	raw, err := json.Marshal(rc)
	if err != nil {
		return errResp("encode routing config: " + err.Error())
	}
	return api.Response{Status: api.StatusRoutingConfig, RoutingConfig: raw}
}

// upsertRouting mirrors the Rust client block: keyed on the config's id — an
// existing id replaces that config IN PLACE (the slot keeps its id, so the
// active selection stays valid across an edit), a new/absent id adds.
func (m *manager) upsertRouting(req api.Request) api.Response {
	if len(req.RoutingConfig) == 0 {
		return errResp("upsert_routing requires a routing_config payload")
	}
	var rc routing.Config
	if err := json.Unmarshal(req.RoutingConfig, &rc); err != nil {
		return errResp("decode routing_config: " + err.Error())
	}
	return m.withProfile(req, func(name string, p *store.Profile) (api.Response, error) {
		// A group cannot yet be a routing rule target: its urltest exists in the
		// plan only while the group is the ACTIVE node, so a rule targeting it
		// would fail the activation of any OTHER node. Reject at authoring time
		// with a readable message instead of a raw-UUID activation failure later.
		if g := groupRoutingTarget(p, &rc); g != "" {
			return api.Response{}, fmt.Errorf("routing rule targets group %q; a group is not a routing target — target one of its members instead", g)
		}
		exists := false
		for i := range p.RoutingConfigs {
			if p.RoutingConfigs[i].ID == rc.ID {
				exists = true
				break
			}
		}
		if exists {
			if err := p.ReplaceRoutingByID(rc.ID, rc); err != nil {
				return api.Response{}, err
			}
		} else if _, err := p.AddRouting(rc); err != nil {
			return api.Response{}, err
		}
		return api.Response{Status: api.StatusOk, Message: "routing upserted: " + rc.Name}, nil
	})
}

// groupRoutingTarget returns the display name of a group a routing config
// targets (its default target or any rule), or "" when no target is a group —
// the guard that keeps a group out of routing rules (see upsertRouting).
func groupRoutingTarget(p *store.Profile, rc *routing.Config) string {
	isGroupTarget := func(t routing.RuleTarget) string {
		if t.Kind == routing.TargetNode {
			if n := p.FindNodeByID(t.Node); n != nil && n.IsGroup() {
				return n.DisplayName()
			}
		}
		return ""
	}
	if g := isGroupTarget(rc.DefaultTarget); g != "" {
		return g
	}
	for i := range rc.Rules {
		if g := isGroupTarget(rc.Rules[i].Target); g != "" {
			return g
		}
	}
	return ""
}

func (m *manager) removeRouting(req api.Request) api.Response {
	if req.Routing == nil || *req.Routing == "" {
		return errResp("remove_routing requires a routing name or id")
	}
	return m.withProfile(req, func(name string, p *store.Profile) (api.Response, error) {
		rc := p.FindRouting(*req.Routing)
		if rc == nil {
			return api.Response{}, fmt.Errorf("routing %q not found", *req.Routing)
		}
		display := rc.Name
		p.RemoveRouting(rc.ID)
		return api.Response{Status: api.StatusOk, Message: "routing removed: " + display}, nil
	})
}

// publishRefreshLocked converts pipeline results to the wire shape and emits
// a SubscriptionUpdated event per successful refresh. Callers hold m.mu.
func (m *manager) publishRefreshLocked(results []subscription.Result) []api.RefreshInfo {
	infos := make([]api.RefreshInfo, 0, len(results))
	for _, r := range results {
		infos = append(infos, api.RefreshInfo{
			Name: r.Name, Count: r.Count, Added: r.Added, Removed: r.Removed,
			Skipped: r.Skipped, Error: r.Err,
			Format: r.Format, Entries: r.Entries,
			Duplicates: r.Duplicates, Unrecognized: r.Unrecognized,
		})
		if r.Err == "" && !r.Skipped && m.hub != nil {
			m.hub.Broadcast(api.Event{
				Event: api.EventSubscriptionUpdated,
				SubID: r.SubID, Added: r.Added, Removed: r.Removed, Total: r.Count,
				Format: r.Format,
			})
		}
	}
	return infos
}
