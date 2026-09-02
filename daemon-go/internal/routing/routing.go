// Package routing is the user routing model. A stored rule is DELIBERATELY
// thin: its match conditions are already the sing-box 1.12 condition fields
// ("full 1.12 parity" by design), so the model keeps them as a PASSTHROUGH
// map and only interprets what it must: the `target` (which outbound/action
// the rule selects) and the nested `rules` of a logical rule. The engine
// compiles target → outbound tag; everything else flows to sing-box
// verbatim.
package routing

import (
	"bytes"
	"encoding/json"
	"fmt"

	"ironlink/daemon/internal/uuid"
)

// Config is one named routing configuration. On decode, a config without an
// id gets a fresh one (defaulted, never rejected).
type Config struct {
	ID            string
	Name          string
	RuleSets      []RuleSetSource
	Rules         []Rule
	DefaultTarget RuleTarget
}

type configJSON struct {
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	RuleSets      []RuleSetSource `json:"rule_sets"`
	Rules         []Rule          `json:"rules"`
	DefaultTarget RuleTarget      `json:"default_target"`
}

func (c *Config) UnmarshalJSON(data []byte) error {
	var a configJSON
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	if a.ID == "" {
		a.ID = uuid.New()
	}
	*c = Config{ID: a.ID, Name: a.Name, RuleSets: a.RuleSets, Rules: a.Rules, DefaultTarget: a.DefaultTarget}
	return nil
}

func (c Config) MarshalJSON() ([]byte, error) {
	a := configJSON{ID: c.ID, Name: c.Name, RuleSets: c.RuleSets, Rules: c.Rules, DefaultTarget: c.DefaultTarget}
	// The stored shape spells the collections `[]`, never `null`.
	if a.RuleSets == nil {
		a.RuleSets = []RuleSetSource{}
	}
	if a.Rules == nil {
		a.Rules = []Rule{}
	}
	return json.Marshal(a)
}

// NewDefault is the "default" config a fresh profile carries: no rules,
// everything to the default proxy.
func NewDefault() Config {
	return Config{
		ID:            uuid.New(),
		Name:          "default",
		RuleSets:      []RuleSetSource{},
		Rules:         []Rule{},
		DefaultTarget: RuleTarget{Kind: TargetDefaultProxy},
	}
}

// RuleSetSource is one remote sing-box rule-set reference.
type RuleSetSource struct {
	Tag            string  `json:"tag"`
	URL            string  `json:"url"`
	Format         string  `json:"format"`
	DownloadDetour *string `json:"download_detour"`
}

// TargetKind discriminates RuleTarget.
type TargetKind string

const (
	// Route targets — name an outbound the matched traffic dispatches to.
	TargetDirect       TargetKind = "Direct"
	TargetBlock        TargetKind = "Block"
	TargetDefaultProxy TargetKind = "DefaultProxy"
	TargetDpiBypass    TargetKind = "DpiBypass"
	TargetNode         TargetKind = "Node"
	// Non-route actions — mutate the in-flight connection, evaluation
	// continues (sing-box 1.12 actions).
	TargetHijackDns TargetKind = "HijackDns"
	TargetSniff     TargetKind = "Sniff"
	TargetResolve   TargetKind = "Resolve"
)

// SniffParams carries the sniff action's options.
type SniffParams struct {
	Sniffer []string `json:"sniffer,omitempty"`
	Timeout string   `json:"timeout,omitempty"`
}

// ResolveParams carries the resolve action's options.
type ResolveParams struct {
	Server   string `json:"server,omitempty"`
	Strategy string `json:"strategy,omitempty"`
}

// RuleTarget is an externally-tagged union: unit variants are bare strings,
// Node is {"Node":"<uuid>"}, Sniff/Resolve carry their params.
type RuleTarget struct {
	Kind    TargetKind
	Node    string // uuid, when Kind == TargetNode
	Sniff   *SniffParams
	Resolve *ResolveParams
}

// IsRoute reports whether the target names an outbound (vs a non-route
// action).
func (t RuleTarget) IsRoute() bool {
	switch t.Kind {
	case TargetDirect, TargetBlock, TargetDefaultProxy, TargetDpiBypass, TargetNode:
		return true
	default:
		return false
	}
}

func (t *RuleTarget) UnmarshalJSON(data []byte) error {
	if bytes.HasPrefix(bytes.TrimSpace(data), []byte(`"`)) {
		var variant string
		if err := json.Unmarshal(data, &variant); err != nil {
			return err
		}
		switch TargetKind(variant) {
		case TargetDirect, TargetBlock, TargetDefaultProxy, TargetDpiBypass, TargetHijackDns:
			t.Kind = TargetKind(variant)
			return nil
		default:
			return fmt.Errorf("unknown rule target %q", variant)
		}
	}

	var tagged struct {
		Node    *string        `json:"Node"`
		Sniff   *SniffParams   `json:"Sniff"`
		Resolve *ResolveParams `json:"Resolve"`
	}
	if err := json.Unmarshal(data, &tagged); err != nil {
		return err
	}
	switch {
	case tagged.Node != nil:
		*t = RuleTarget{Kind: TargetNode, Node: *tagged.Node}
	case tagged.Sniff != nil:
		*t = RuleTarget{Kind: TargetSniff, Sniff: tagged.Sniff}
	case tagged.Resolve != nil:
		*t = RuleTarget{Kind: TargetResolve, Resolve: tagged.Resolve}
	default:
		return fmt.Errorf("unknown rule target: %s", data)
	}
	return nil
}

func (t RuleTarget) MarshalJSON() ([]byte, error) {
	switch t.Kind {
	case TargetDirect, TargetBlock, TargetDefaultProxy, TargetDpiBypass, TargetHijackDns:
		return json.Marshal(string(t.Kind))
	case TargetNode:
		return json.Marshal(map[string]string{"Node": t.Node})
	case TargetSniff:
		s := t.Sniff
		if s == nil {
			s = &SniffParams{}
		}
		return json.Marshal(map[string]any{"Sniff": s})
	case TargetResolve:
		r := t.Resolve
		if r == nil {
			r = &ResolveParams{}
		}
		return json.Marshal(map[string]any{"Resolve": r})
	default:
		return nil, fmt.Errorf("unknown rule target kind %q", t.Kind)
	}
}

// Rule is one route rule: the target, the nested sub-rules of a logical rule,
// and every other key — the sing-box condition fields — passed through
// VERBATIM (never lossily rewritten; only the two interpreted parts are
// split out).
type Rule struct {
	Target RuleTarget
	// Rules are a logical rule's nested sub-rules (their own targets are
	// deliberately ignored by the compiler).
	Rules []Rule
	// Conditions holds the remaining keys verbatim (process_name, domain_*,
	// port, rule_set, type/mode/invert, ip_cidr, …).
	Conditions map[string]json.RawMessage
}

// IsLogical reports whether the rule is a sing-box logical rule
// (`"type": "logical"`).
func (r *Rule) IsLogical() bool {
	raw, ok := r.Conditions["type"]
	if !ok {
		return false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return false
	}
	return s == "logical"
}

func (r *Rule) UnmarshalJSON(data []byte) error {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	rawTarget, ok := m["target"]
	if !ok {
		return fmt.Errorf("routing rule has no target")
	}
	if err := json.Unmarshal(rawTarget, &r.Target); err != nil {
		return err
	}
	delete(m, "target")

	if rawRules, ok := m["rules"]; ok && !bytes.Equal(rawRules, []byte("null")) {
		if err := json.Unmarshal(rawRules, &r.Rules); err != nil {
			return err
		}
		delete(m, "rules")
	}
	r.Conditions = m
	return nil
}

func (r Rule) MarshalJSON() ([]byte, error) {
	out := make(map[string]any, len(r.Conditions)+2)
	for k, v := range r.Conditions {
		out[k] = v
	}
	out["target"] = r.Target
	if len(r.Rules) > 0 {
		out["rules"] = r.Rules
	}
	return json.Marshal(out)
}
