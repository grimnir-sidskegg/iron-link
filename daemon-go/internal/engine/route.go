// Routing compilation: a user routing.Config becomes the sing-box `route`
// block of the session plan. Conditions pass through VERBATIM (the stored
// shape is the sing-box 1.12 shape — internal/routing); the compiler's work
// is the target: route targets resolve to THIS plan's outbound tags, actions
// become sing-box rule actions, null-valued stored conditions are stripped
// (the Rust generator never emitted them).
//
// The old system rule `process_name [xray, sing-box] → direct` is
// DELIBERATELY dropped: in the embedded model there are no core child
// processes — own-traffic exemption is the auto_redirect fwmark
// (AutoRedirectOutputMark).

package engine

import (
	"bytes"
	"fmt"

	"ironlink/daemon/internal/routing"
)

// resolveTarget maps a ROUTE target to an outbound tag in this plan. Actions
// (hijack-dns/sniff/resolve) are not outbounds and are rejected here — they
// are applied by compileRule; a DpiBypass target is rejected until the byedpi
// integration lands (GO_DAEMON_PLAN.md §9).
func (p *SessionPlan) resolveTarget(t routing.RuleTarget) (string, error) {
	switch t.Kind {
	case routing.TargetDirect:
		return "direct", nil
	case routing.TargetBlock:
		return "block", nil
	case routing.TargetDefaultProxy:
		return SelectorTag, nil
	case routing.TargetDpiBypass:
		return "", fmt.Errorf("DpiBypass routing target: the byedpi integration has not landed in the Go daemon yet")
	case routing.TargetNode:
		// The selector member / outbound tag IS the node UUID, and a rule's
		// Node target already carries that UUID — return it as the tag once we
		// confirm the node is embedded.
		for i := range p.Natives {
			if p.Natives[i].ID == t.Node {
				return p.Natives[i].ID, nil
			}
		}
		for i := range p.XrayNodes {
			if p.XrayNodes[i].ID == t.Node {
				return p.XrayNodes[i].ID, nil
			}
		}
		if p.Group != nil && p.Group.ID == t.Node {
			return p.Group.ID, nil
		}
		return "", fmt.Errorf("routing target node %s is not embedded in this session (only selector members can be rule targets)", t.Node)
	default:
		return "", fmt.Errorf("%s is an action, not a route target", t.Kind)
	}
}

// conditionsOf copies a rule's stored conditions, stripping null values (the
// Rust store serializes the legacy condition fields as null when unset; the
// dispatcher must not see them).
func conditionsOf(r routing.Rule) map[string]any {
	out := make(map[string]any, len(r.Conditions)+2)
	for k, v := range r.Conditions {
		if bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			continue
		}
		out[k] = v
	}
	return out
}

// compileMatchRule builds a nested logical sub-rule: CONDITIONS ONLY, no
// action/outbound — a sub-rule's own target is ignored, as in the Rust
// build_match_rule_no_action. Recurses for logical sub-rules.
func compileMatchRule(r routing.Rule) map[string]any {
	out := conditionsOf(r)
	if r.IsLogical() {
		subs := make([]any, 0, len(r.Rules))
		for _, sub := range r.Rules {
			subs = append(subs, compileMatchRule(sub))
		}
		out["rules"] = subs
	}
	return out
}

// compileRule builds one full route rule (conditions + action) from a user
// rule.
func (p *SessionPlan) compileRule(r routing.Rule) (map[string]any, error) {
	out := conditionsOf(r)
	if r.IsLogical() {
		subs := make([]any, 0, len(r.Rules))
		for _, sub := range r.Rules {
			subs = append(subs, compileMatchRule(sub))
		}
		out["rules"] = subs
	}

	switch r.Target.Kind {
	case routing.TargetHijackDns:
		out["action"] = "hijack-dns"
	case routing.TargetSniff:
		out["action"] = "sniff"
		if s := r.Target.Sniff; s != nil {
			if len(s.Sniffer) > 0 {
				out["sniffer"] = s.Sniffer
			}
			if s.Timeout != "" {
				out["timeout"] = s.Timeout
			}
		}
	case routing.TargetResolve:
		out["action"] = "resolve"
		if rv := r.Target.Resolve; rv != nil {
			if rv.Server != "" {
				out["server"] = rv.Server
			}
			if rv.Strategy != "" {
				out["strategy"] = rv.Strategy
			}
		}
	default:
		tag, err := p.resolveTarget(r.Target)
		if err != nil {
			return nil, err
		}
		out["outbound"] = tag
	}
	return out, nil
}

// privateDirectCIDRs are the RFC1918 IPv4 ranges the TUN steers DIRECT through
// an IMMUTABLE route rule (see routeBlock). They were moved OUT of the editable
// LAN-bypass (TUN route_exclude_address) default so that DNS destined for a LAN
// resolver (e.g. 192.168.1.1) is captured by the DNS hijack instead of
// bypassing our stack entirely and hitting a poisoning/stale resolver. Because
// they no longer route-exclude, this traffic now enters the TUN; steering its
// NON-DNS part direct here keeps LAN devices/printers reachable.
var privateDirectCIDRs = []any{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"}

// routeBlock assembles the sing-box `route` for this plan: the system rules
// (sniff always; under the TUN, where the daemon owns DNS, the immutable
// dns-hijack then the immutable private-direct rule — in THAT order), the
// compiled user rules, the remote rule-set references, and the final from the
// routing's default target (the selector when no routing is attached).
// domainResolver names the DNS-server tag outbound domains resolve through
// (planDNS picks it: the primary upstream, or the direct bootstrap when DNS
// queries detour through the tunnel).
func (p *SessionPlan) routeBlock(tun bool, domainResolver string) (map[string]any, error) {
	final := SelectorTag
	if p.Routing != nil {
		var err error
		if final, err = p.resolveTarget(p.Routing.DefaultTarget); err != nil {
			return nil, fmt.Errorf("routing %q default target: %w", p.Routing.Name, err)
		}
	}

	rules := []any{map[string]any{"action": "sniff"}}
	if tun {
		// DNS hijack (immutable): capture ALL DNS into our resolver regardless
		// of destination, so a query aimed at a LAN resolver cannot leak. MUST
		// precede private-direct below — a query to 192.168.x.x:53 is matched
		// here first and never falls through to the direct route.
		rules = append(rules, map[string]any{"protocol": []any{"dns"}, "action": "hijack-dns"})
		// Private-direct (immutable): remaining RFC1918-bound traffic egresses
		// DIRECT so LAN devices stay reachable. Placed AFTER the DNS hijack.
		rules = append(rules, map[string]any{"ip_cidr": privateDirectCIDRs, "outbound": "direct"})
	}
	if p.Routing != nil {
		for i := range p.Routing.Rules {
			compiled, err := p.compileRule(p.Routing.Rules[i])
			if err != nil {
				return nil, fmt.Errorf("routing %q rule %d: %w", p.Routing.Name, i, err)
			}
			rules = append(rules, compiled)
		}
	}

	route := map[string]any{
		"default_domain_resolver": domainResolver,
		"final":                   final,
		"rules":                   rules,
		// Resolve the owning process for every connection so the
		// TrafficCounter can attribute bytes per app (instrument.go). The
		// router populates metadata.ProcessInfo in matchRule, BEFORE the
		// ConnectionTracker hooks fire; pure-Go on linux/windows/darwin.
		"find_process": true,
	}
	if tun {
		route["auto_detect_interface"] = true
	}
	if p.Routing != nil && len(p.Routing.RuleSets) > 0 {
		sets := make([]any, 0, len(p.Routing.RuleSets))
		for _, rs := range p.Routing.RuleSets {
			set := map[string]any{"type": "remote", "tag": rs.Tag, "format": rs.Format, "url": rs.URL}
			if rs.DownloadDetour != nil && *rs.DownloadDetour != "" {
				set["download_detour"] = *rs.DownloadDetour
			}
			sets = append(sets, set)
		}
		route["rule_set"] = sets
	}
	return route, nil
}
