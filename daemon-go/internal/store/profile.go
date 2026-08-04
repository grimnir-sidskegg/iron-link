// The Profile domain model + its invariant-enforcing mutators — the Go port
// of the former Rust profile model. All mutation goes through methods that enforce
// the invariants (active selections reference an existing member; names are
// validated; duplicate subscription URLs rejected); persistence is the
// store's atomic 0600 write.
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
//	v1 = the Rust serde shape (no longer decodable — the legacy path was
//	     removed 2026-06-12 after the only deployment migrated)
//	v2 = Go-native (plain encoding/json over the model structs)
const SchemaVersion = 2

// Profile is a user profile: its nodes, subscriptions, routing configs, and
// the active selections among them. The on-disk JSON (schema v2) is the
// plain encoding/json shape of this struct — no Rust byte-compat anymore.
type Profile struct {
	SchemaVersion   uint32           `json:"schema_version"`
	Name            string           `json:"name"`
	ActiveNodeID    *string          `json:"active_node_id"`
	Subscriptions   []Subscription   `json:"subscriptions"`
	Nodes           []Node           `json:"nodes"`
	RoutingConfigs  []routing.Config `json:"routing_configs"`
	ActiveRoutingID *string          `json:"active_routing_id"`
}

// NewProfile mirrors Rust Profile::new: a fresh profile with one "default"
// routing config selected as active.
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
// carried, so a save always writes v2.
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

// Subscription mirrors the Rust Subscription. Format is the body dialect the
// refresh parses with — "auto" (or "", in pre-format files) detects; an
// explicit value forces one parser (validated at the wire boundary, values
// owned by internal/subscription).
type Subscription struct {
	ID                string    `json:"id"`
	Name              string    `json:"name"`
	URL               string    `json:"url"`
	LastUpdated       time.Time `json:"last_updated"`
	UpdateIntervalSec uint32    `json:"update_interval_sec"`
	Enabled           bool      `json:"enabled"`
	AllowInvalidCerts bool      `json:"allow_invalid_certs"`
	Format            string    `json:"format,omitempty"`
}

// NewSubscription mirrors Rust Subscription::new: enabled, daily interval,
// verification on, format auto-detected.
func NewSubscription(url, name string) Subscription {
	return Subscription{
		ID:                uuid.New(),
		Name:              name,
		URL:               url,
		LastUpdated:       time.Now().UTC(),
		UpdateIntervalSec: 86400,
		Enabled:           true,
		Format:            "auto",
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

// Node is one stored node: identity, its proxy profile, and the user prefs.
type Node struct {
	ID          string     `json:"id"`
	SubID       *string    `json:"sub_id"`
	Doc         ProfileDoc `json:"profile"`
	Preferences NodePrefs  `json:"preferences"`
}

// NewNode wraps a parsed profile into a stored node with a fresh id.
func NewNode(p proxy.Profile, subID *string) Node {
	return Node{ID: uuid.New(), SubID: subID, Doc: ProfileDoc{P: p}}
}

// Profile returns the node's proxy profile (nil only for a zero node).
func (n *Node) Profile() proxy.Profile { return n.Doc.P }

// Vless returns the node's VLESS config, or nil when the node is another
// protocol — convenience for the VLESS-specific call sites that remain.
func (n *Node) Vless() *proxy.VlessConfig {
	v, _ := n.Doc.P.(*proxy.VlessConfig)
	return v
}

// DisplayName forwards to the underlying profile's display name.
func (n *Node) DisplayName() string {
	if n.Doc.P == nil {
		return ""
	}
	return n.Doc.P.DisplayName()
}

// NodePrefs mirrors the Rust NodePrefs: CoreOverride nil = "let selection
// decide".
type NodePrefs struct {
	CoreOverride *api.CoreType `json:"core_override"`
}

// -- read accessors ----------------------------------------------------------

// FindNodeByName returns the node whose display name equals name (mirrors the
// Rust Profile::find_node_by_name), or nil.
func (p *Profile) FindNodeByName(name string) *Node {
	for i := range p.Nodes {
		if p.Nodes[i].DisplayName() == name {
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

// -- mutators (the Rust owner methods) ---------------------------------------

// AddNode appends a node and returns its id. The first node added to a
// profile with no active node becomes the default active node (a front-end
// never lands in "no default node" right after adding one).
func (p *Profile) AddNode(n Node) string {
	p.Nodes = append(p.Nodes, n)
	p.SetActiveNodeIfUnset()
	return n.ID
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

// SetActiveNodeIfUnset selects the first node when none is active yet,
// returning the chosen id ("" if none).
func (p *Profile) SetActiveNodeIfUnset() string {
	if p.ActiveNodeID == nil && len(p.Nodes) > 0 {
		id := p.Nodes[0].ID
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

// -- routing (the former Rust profile routing owner methods) ------------------

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
