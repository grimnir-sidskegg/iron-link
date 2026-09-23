// The Profile domain model + its invariant-enforcing mutators. All mutation
// goes through methods that enforce the invariants (active selections
// reference an existing member; names are validated; duplicate subscription
// URLs rejected); persistence is the store's atomic 0600 write.
package store

import (
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"ironlink/daemon/internal/api"
	"ironlink/daemon/internal/proxy"
	"ironlink/daemon/internal/routing"
	"ironlink/daemon/internal/uuid"
)

// SchemaVersion is the on-disk schema version of a Profile (bumped on
// incompatible shape changes; absent in old files = 1):
//
//	v1 = the legacy pre-release shape (no longer decodable — the legacy
//	     path was removed after the only deployment migrated)
//	v2 = plain encoding/json over the model structs
//	v3 = same shape; subscriptions gain last_error and the default refresh
//	     interval drops from 24 h to 8 h. A v2 file decodes as-is; loading
//	     one rewrites every subscription interval that is 0 or the old
//	     24 h default to the new default (see migrateSubscriptionIntervals)
const SchemaVersion = 3

// DefaultUpdateIntervalSec is the background refresh interval a new
// subscription gets (8 h); MinUpdateIntervalSec is the smallest interval the
// wire accepts (10 min) — anything shorter would hammer the provider.
const (
	DefaultUpdateIntervalSec uint32 = 28800
	MinUpdateIntervalSec     uint32 = 600
)

// legacyUpdateIntervalSec is the pre-v3 default (24 h). It was stored on every
// subscription but never acted on — there was no scheduler — so a v2 file
// carrying it is read as "the default", not as a user choice.
const legacyUpdateIntervalSec uint32 = 86400

// Profile is a user profile: its nodes, subscriptions, routing configs, and
// the active selections among them. The on-disk JSON (schema v3) is the
// plain encoding/json shape of this struct.
type Profile struct {
	SchemaVersion   uint32           `json:"schema_version"`
	Name            string           `json:"name"`
	ActiveNodeID    *string          `json:"active_node_id"`
	Subscriptions   []Subscription   `json:"subscriptions"`
	Nodes           []Node           `json:"nodes"`
	RoutingConfigs  []routing.Config `json:"routing_configs"`
	ActiveRoutingID *string          `json:"active_routing_id"`
}

// NewProfile builds a fresh profile with one "default" routing config
// selected as active.
func NewProfile(name string) *Profile {
	defaultRouting := routing.NewDefault()
	return &Profile{
		SchemaVersion:   SchemaVersion,
		Name:            name,
		Subscriptions:   []Subscription{},
		Nodes:           []Node{},
		RoutingConfigs:  []routing.Config{defaultRouting},
		ActiveRoutingID: &defaultRouting.ID,
	}
}

// normalize repairs nil-vs-empty asymmetries (clients read `[]`, not `null`,
// for the collection fields) and stamps the current schema version: the
// in-memory model is ALWAYS current-schema, whatever version the file
// carried, so a save always writes the current version.
func (p *Profile) normalize() {
	p.SchemaVersion = SchemaVersion
	if p.Subscriptions == nil {
		p.Subscriptions = []Subscription{}
	}
	if p.Nodes == nil {
		p.Nodes = []Node{}
	}
	if p.RoutingConfigs == nil {
		p.RoutingConfigs = []routing.Config{}
	}
}

// Subscription is one subscription source. Format is the body dialect the
// refresh parses with — "auto" (or "", in pre-format files) detects; an
// explicit value forces one parser (validated at the wire boundary, values
// owned by internal/subscription).
//
// The refresh bookkeeping: LastUpdated moves only on a SUCCESSFUL refresh
// (manual or background) and is what the scheduler measures the interval
// from; Enabled is the background-refresh toggle (a disabled subscription
// keeps its nodes, it just is not swept); LastError is the most recent
// fetch/parse failure, cleared by the next success and by a URL edit.
type Subscription struct {
	ID                string    `json:"id"`
	Name              string    `json:"name"`
	URL               string    `json:"url"`
	LastUpdated       time.Time `json:"last_updated"`
	UpdateIntervalSec uint32    `json:"update_interval_sec"`
	Enabled           bool      `json:"enabled"`
	AllowInvalidCerts bool      `json:"allow_invalid_certs"`
	Format            string    `json:"format,omitempty"`
	LastError         string    `json:"last_error,omitempty"`
}

// NewSubscription builds a subscription with the defaults: enabled, the
// default refresh interval, verification on, format auto-detected.
func NewSubscription(url, name string) Subscription {
	return Subscription{
		ID:                uuid.New(),
		Name:              name,
		URL:               url,
		LastUpdated:       time.Now().UTC(),
		UpdateIntervalSec: DefaultUpdateIntervalSec,
		Enabled:           true,
		Format:            "auto",
	}
}

// migrateSubscriptionIntervals is the v2 → v3 step: a pre-v3 file's interval
// of 0 or the old 24 h default is rewritten to DefaultUpdateIntervalSec. Both
// values are "never chosen" — 86400 was stamped on every subscription by the
// old NewSubscription and nothing ever acted on it — so only a value that
// differs from both survives as a user setting. A v3 file is never touched
// (a deliberate 24 h must stay 24 h).
func (p *Profile) migrateSubscriptionIntervals() {
	for i := range p.Subscriptions {
		switch p.Subscriptions[i].UpdateIntervalSec {
		case 0, legacyUpdateIntervalSec:
			p.Subscriptions[i].UpdateIntervalSec = DefaultUpdateIntervalSec
		}
	}
}

// ProfileDoc is the per-node protocol union. It serializes as a single-key
// object — {"<kind>": <body>}, e.g. {"vless": {...}} — and the concrete
// protocol is any proxy.Profile. (De)serialization dispatches through the proxy
// registry (EncodeProfileDoc / DecodeProfile), so adding a protocol never edits
// the store. The on-disk shape is unchanged from the old one-pointer struct.
type ProfileDoc struct {
	P proxy.Profile
}

// MarshalJSON writes the {"<kind>": <body>} tagged union (null for a zero doc).
func (d ProfileDoc) MarshalJSON() ([]byte, error) {
	if d.P == nil {
		return []byte("null"), nil
	}
	return proxy.EncodeProfileDoc(d.P)
}

// UnmarshalJSON reads the {"<kind>": <body>} tagged union, dispatching the body
// to the registered decoder for that kind.
func (d *ProfileDoc) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		return nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	for k, raw := range m {
		p, err := proxy.DecodeProfile(proxy.Protocol(k), raw)
		if err != nil {
			return err
		}
		d.P = p
		return nil
	}
	return fmt.Errorf("node profile is empty")
}

// Node is one stored node: identity, its proxy profile OR a group spec, and the
// user prefs. Exactly one of Doc / Group is populated: a dialable node carries
// Doc (Group nil); a member group carries Group (Doc.P nil). SubID set = the
// group/node came from a subscription (a provider group); nil = user-created.
type Node struct {
	ID          string     `json:"id"`
	SubID       *string    `json:"sub_id"`
	Doc         ProfileDoc `json:"profile"`
	Group       *GroupSpec `json:"group,omitempty"`
	Preferences NodePrefs  `json:"preferences"`
}

// GroupSpec is a member group: a named "Auto" node that lowers to a sing-box
// urltest (fastest-by-ping) over its members. Membership is EITHER an explicit
// list of member node UUIDs (Members) OR every non-group node of a subscription
// (AllOfSub) — exactly one is set. Probe carries the health-check tuning; zero
// fields fall back to the sing-box urltest defaults.
type GroupSpec struct {
	Name     string     `json:"name"`
	Members  []string   `json:"members,omitempty"`
	AllOfSub *string    `json:"all_of_sub,omitempty"`
	Probe    GroupProbe `json:"probe"`
}

// GroupProbe is the urltest health-check tuning. Zero values mean "use the
// sing-box default": URL "" = the built-in 204 endpoint, IntervalSec 0 = 3m,
// Tolerance 0 = 50ms.
type GroupProbe struct {
	URL         string `json:"url,omitempty"`
	IntervalSec uint32 `json:"interval_sec,omitempty"`
	Tolerance   uint16 `json:"tolerance,omitempty"`
}

// NewNode wraps a parsed profile into a stored node with a fresh id.
func NewNode(p proxy.Profile, subID *string) Node {
	return Node{ID: uuid.New(), SubID: subID, Doc: ProfileDoc{P: p}}
}

// NewGroupNode wraps a group spec into a stored node with a fresh id.
func NewGroupNode(g GroupSpec, subID *string) Node {
	return Node{ID: uuid.New(), SubID: subID, Group: &g}
}

// IsGroup reports whether this node is a member group (vs a dialable node).
func (n *Node) IsGroup() bool { return n.Group != nil }

// Profile returns the node's proxy profile (nil for a group node or a zero node).
func (n *Node) Profile() proxy.Profile { return n.Doc.P }

// Vless returns the node's VLESS config, or nil when the node is another
// protocol — convenience for the VLESS-specific call sites that remain.
func (n *Node) Vless() *proxy.VlessConfig {
	v, _ := n.Doc.P.(*proxy.VlessConfig)
	return v
}

// DisplayName is the group name for a group node, else the proxy profile's
// display name ("" for a zero node).
func (n *Node) DisplayName() string {
	if n.Group != nil {
		return n.Group.Name
	}
	if n.Doc.P == nil {
		return ""
	}
	return n.Doc.P.DisplayName()
}

// NodePrefs holds per-node preferences: CoreOverride nil = "let selection
// decide".
type NodePrefs struct {
	CoreOverride *api.CoreType `json:"core_override"`
}

// -- read accessors ----------------------------------------------------------

// FindNodeByName returns the node whose display name equals name, or nil. A
// dialable node WINS a name tie with a group structurally (two passes: dialable
// nodes first, groups second) — not by store position, which
// ReplaceSubscriptionNodes reorders. This mirrors the placeholder guard that
// keeps a provider's carrier host out of the node stream; the two-pass rule is
// the backstop if a name still collides.
func (p *Profile) FindNodeByName(name string) *Node {
	for i := range p.Nodes {
		if !p.Nodes[i].IsGroup() && p.Nodes[i].DisplayName() == name {
			return &p.Nodes[i]
		}
	}
	for i := range p.Nodes {
		if p.Nodes[i].IsGroup() && p.Nodes[i].DisplayName() == name {
			return &p.Nodes[i]
		}
	}
	return nil
}

// FindNodeByID returns the node with the given id, or nil.
func (p *Profile) FindNodeByID(id string) *Node {
	for i := range p.Nodes {
		if p.Nodes[i].ID == id {
			return &p.Nodes[i]
		}
	}
	return nil
}

// ActiveNode returns the node selected by active_node_id, or nil.
func (p *Profile) ActiveNode() *Node {
	if p.ActiveNodeID == nil {
		return nil
	}
	return p.FindNodeByID(*p.ActiveNodeID)
}

// FindSubscription returns the subscription whose id OR name equals ref, or
// nil (the wire identifies subscriptions by either).
func (p *Profile) FindSubscription(ref string) *Subscription {
	for i := range p.Subscriptions {
		if p.Subscriptions[i].ID == ref || p.Subscriptions[i].Name == ref {
			return &p.Subscriptions[i]
		}
	}
	return nil
}

// -- mutators ----------------------------------------------------------------

// AddNode appends a node and returns its id. The first node added to a
// profile with no active node becomes the default active node (a front-end
// never lands in "no default node" right after adding one).
func (p *Profile) AddNode(n Node) string {
	p.Nodes = append(p.Nodes, n)
	p.SetActiveNodeIfUnset()
	return n.ID
}

// AddGroup appends a USER group (SubID nil) built from spec, returning its id.
// The name is validated; a group never becomes the auto-default, so the active
// node is left untouched.
func (p *Profile) AddGroup(g GroupSpec) (string, error) {
	if g.Name == "" {
		return "", fmt.Errorf("a group needs a name")
	}
	n := NewGroupNode(g, nil)
	p.Nodes = append(p.Nodes, n)
	return n.ID, nil
}

// ReplaceGroupByID replaces a USER group's spec in place, keeping its id (so an
// active-node or membership reference to it survives the edit). Errors when the
// id is unknown or names a dialable node or a PROVIDER group (a provider group
// is regenerated from its subscription on every refresh, so editing it is
// futile).
func (p *Profile) ReplaceGroupByID(id string, g GroupSpec) error {
	if g.Name == "" {
		return fmt.Errorf("a group needs a name")
	}
	for i := range p.Nodes {
		if p.Nodes[i].ID != id {
			continue
		}
		if !p.Nodes[i].IsGroup() {
			return fmt.Errorf("node %s is not a group", id)
		}
		if p.Nodes[i].SubID != nil {
			return fmt.Errorf("group %s belongs to a subscription and cannot be edited", id)
		}
		spec := g
		p.Nodes[i].Group = &spec
		return nil
	}
	return fmt.Errorf("group %s does not exist in this profile", id)
}

// RemoveNodeByID removes a node by id, clearing the active-node selection if
// it pointed at the removed node. Reports whether a node was removed.
func (p *Profile) RemoveNodeByID(id string) bool {
	before := len(p.Nodes)
	p.Nodes = slices.DeleteFunc(p.Nodes, func(n Node) bool { return n.ID == id })
	removed := len(p.Nodes) != before
	if removed && p.ActiveNodeID != nil && *p.ActiveNodeID == id {
		p.ActiveNodeID = nil
	}
	return removed
}

// SetActiveNode selects an existing node as active.
func (p *Profile) SetActiveNode(id string) error {
	if p.FindNodeByID(id) == nil {
		return fmt.Errorf("node %s does not exist in this profile", id)
	}
	p.ActiveNodeID = &id
	return nil
}

// SetActiveNodeIfUnset selects the first DIALABLE node when none is active yet,
// returning the chosen id ("" if none). Groups are skipped: a group has no
// endpoint until it is explicitly activated (and may have no dialable members),
// so it must never become the silent default just by being added first.
func (p *Profile) SetActiveNodeIfUnset() string {
	if p.ActiveNodeID != nil {
		return ""
	}
	for i := range p.Nodes {
		if p.Nodes[i].IsGroup() {
			continue
		}
		id := p.Nodes[i].ID
		p.ActiveNodeID = &id
		return id
	}
	return ""
}

// AddSubscription appends a subscription, rejecting a duplicate URL (the same
// source added twice would only confuse refresh). Returns the new id.
func (p *Profile) AddSubscription(sub Subscription) (string, error) {
	for i := range p.Subscriptions {
		if p.Subscriptions[i].URL == sub.URL {
			return "", fmt.Errorf("a subscription with URL '%s' already exists", sub.URL)
		}
	}
	p.Subscriptions = append(p.Subscriptions, sub)
	return sub.ID, nil
}

// RemoveSubscription drops the subscription AND its nodes (a removed source's
// nodes are unrefreshable orphans). Reports whether one was removed.
func (p *Profile) RemoveSubscription(id string) bool {
	before := len(p.Subscriptions)
	p.Subscriptions = slices.DeleteFunc(p.Subscriptions, func(s Subscription) bool { return s.ID == id })
	if len(p.Subscriptions) == before {
		return false
	}
	p.ReplaceSubscriptionNodes(id, nil)
	return true
}

// ReplaceSubscriptionNodes replaces the nodes belonging to subID with a fresh
// set (after a refresh), clearing the active node if it was among the dropped
// ones and electing a default when the profile gained its first nodes.
func (p *Profile) ReplaceSubscriptionNodes(subID string, newNodes []Node) {
	p.Nodes = slices.DeleteFunc(p.Nodes, func(n Node) bool { return n.SubID != nil && *n.SubID == subID })
	p.Nodes = append(p.Nodes, newNodes...)
	if p.ActiveNodeID != nil && p.FindNodeByID(*p.ActiveNodeID) == nil {
		p.ActiveNodeID = nil
	}
	p.SetActiveNodeIfUnset()
}

// -- routing mutators ---------------------------------------------------------

// FindRouting returns the routing config whose id OR name equals ref, or nil.
func (p *Profile) FindRouting(ref string) *routing.Config {
	for i := range p.RoutingConfigs {
		if p.RoutingConfigs[i].ID == ref || p.RoutingConfigs[i].Name == ref {
			return &p.RoutingConfigs[i]
		}
	}
	return nil
}

// ActiveRouting returns the config selected by active_routing_id, or nil.
func (p *Profile) ActiveRouting() *routing.Config {
	if p.ActiveRoutingID == nil {
		return nil
	}
	for i := range p.RoutingConfigs {
		if p.RoutingConfigs[i].ID == *p.ActiveRoutingID {
			return &p.RoutingConfigs[i]
		}
	}
	return nil
}

// AddRouting appends a routing config, rejecting an invalid or duplicate NAME
// (the BUGREPORT-2 invariant). Returns the config's id.
func (p *Profile) AddRouting(rc routing.Config) (string, error) {
	if err := ValidateName(rc.Name); err != nil {
		return "", err
	}
	for i := range p.RoutingConfigs {
		if p.RoutingConfigs[i].Name == rc.Name {
			return "", fmt.Errorf("a routing config named '%s' already exists", rc.Name)
		}
	}
	if rc.ID == "" {
		rc.ID = uuid.New()
	}
	p.RoutingConfigs = append(p.RoutingConfigs, rc)
	return rc.ID, nil
}

// ReplaceRoutingByID replaces an existing routing config in place. The
// replacement's name must be valid and must not collide with a DIFFERENT
// existing config; the slot keeps its id, so active_routing_id stays valid
// across an edit.
func (p *Profile) ReplaceRoutingByID(id string, rc routing.Config) error {
	if err := ValidateName(rc.Name); err != nil {
		return err
	}
	for i := range p.RoutingConfigs {
		if p.RoutingConfigs[i].ID != id && p.RoutingConfigs[i].Name == rc.Name {
			return fmt.Errorf("a routing config named '%s' already exists", rc.Name)
		}
	}
	for i := range p.RoutingConfigs {
		if p.RoutingConfigs[i].ID == id {
			rc.ID = id
			p.RoutingConfigs[i] = rc
			return nil
		}
	}
	return fmt.Errorf("routing config %s does not exist", id)
}

// RemoveRouting removes a routing config by id, clearing the active-routing
// selection if it pointed at the removed config. Reports whether one was
// removed.
func (p *Profile) RemoveRouting(id string) bool {
	before := len(p.RoutingConfigs)
	p.RoutingConfigs = slices.DeleteFunc(p.RoutingConfigs, func(r routing.Config) bool { return r.ID == id })
	removed := len(p.RoutingConfigs) != before
	if removed && p.ActiveRoutingID != nil && *p.ActiveRoutingID == id {
		p.ActiveRoutingID = nil
	}
	return removed
}

// SetActiveRouting selects an existing routing config as active.
func (p *Profile) SetActiveRouting(id string) error {
	for i := range p.RoutingConfigs {
		if p.RoutingConfigs[i].ID == id {
			p.ActiveRoutingID = &id
			return nil
		}
	}
	return fmt.Errorf("routing config %s does not exist in this profile", id)
}
