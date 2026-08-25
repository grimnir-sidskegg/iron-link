// Multi-node session compilation (GO_DAEMON_PLAN.md §5): ALL of a profile's
// sing-box-eligible nodes become native selector members (live switch); when
// the ACTIVE node is xray-routed it joins the selector as the xray-reality
// outbound backed by the one xray instance compiled for it.
//
// Members are tagged with the stable node UUID (NamedNode.ID) — NOT the display
// name, which may collide. The wire keeps addressing nodes by display name; the
// manager resolves that to the UUID tag (MemberTagForName) on the way in and
// maps the selector's live tag back to a display name (DisplayNameForTag) on
// the way out, so the client-facing contract is unchanged.

package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"runtime"
	"slices"
	"time"

	"github.com/sagernet/sing-box/protocol/group"

	"ironlink/daemon/internal/proxy"
	"ironlink/daemon/internal/routing"
)

// SelectorTag is the selector outbound's tag — the route final.
const SelectorTag = "proxy"

// NamedNode pairs a node's stable identity with its profile. ID is the store
// node UUID and IS the selector member / outbound tag — routing rules address
// nodes by id (RuleTarget::Node), and the tag must be unique. Name is the
// display label reported to the client (it MAY collide across nodes).
type NamedNode struct {
	ID      string
	Name    string
	Profile proxy.Profile
}

// SessionPlan is one activation's node set.
//
// v1 (plan §5): one xray instance per active xray-routed node — only the
// ACTIVE xray node is embedded (XrayNode); other xray-only nodes need a
// re-activation. Native nodes are all embedded and live-switchable.
type SessionPlan struct {
	// Natives are the sing-box-native selector members.
	Natives []NamedNode
	// XrayNode is the active node when it is xray-routed, else nil.
	XrayNode *NamedNode
	// ActiveTag is the selector default; it must name a member (a native, or
	// XrayNode).
	ActiveTag string
	// Routing is the user routing to compile into the route block (nil =
	// everything through the selector).
	Routing *routing.Config
	// CachePath is the sing-box cache-file location (rule-set cache +
	// selector persistence). MUST be set when the session uses a log sink:
	// the platform-writer hook forces the cache-file service, whose default
	// path is "cache.db" IN THE WORKING DIRECTORY. "" = no cache_file block.
	CachePath string
	// Tunables are the engine-relevant user settings; nil means
	// DefaultTunables() (the pre-settings hardcoded shapes).
	Tunables *Tunables
}

// memberTags lists the selector members by their UUID tag: every native node,
// plus the active xray node (its member IS the xray-reality outbound).
func (p *SessionPlan) memberTags() []string {
	tags := make([]string, 0, len(p.Natives)+1)
	for _, n := range p.Natives {
		tags = append(tags, n.ID)
	}
	if p.XrayNode != nil {
		tags = append(tags, p.XrayNode.ID)
	}
	return tags
}

// IsMember reports whether tag (a node UUID) names an embedded selector member
// (i.e. a SwitchNode target reachable WITHOUT re-activation).
func (p *SessionPlan) IsMember(tag string) bool {
	return slices.Contains(p.memberTags(), tag)
}

// MemberTagForName resolves a client-facing display name to the embedded
// member's UUID tag (the wire addresses nodes by display name). The first
// member with that name wins when names collide; ok=false when none matches.
func (p *SessionPlan) MemberTagForName(name string) (tag string, ok bool) {
	for i := range p.Natives {
		if p.Natives[i].Name == name {
			return p.Natives[i].ID, true
		}
	}
	if p.XrayNode != nil && p.XrayNode.Name == name {
		return p.XrayNode.ID, true
	}
	return "", false
}

// DisplayNameForTag maps a selector member's UUID tag back to its display name
// — the reverse of MemberTagForName, so a live tag read from the selector is
// reported to the client as the same name it was activated under.
func (p *SessionPlan) DisplayNameForTag(tag string) (name string, ok bool) {
	for i := range p.Natives {
		if p.Natives[i].ID == tag {
			return p.Natives[i].Name, true
		}
	}
	if p.XrayNode != nil && p.XrayNode.ID == tag {
		return p.XrayNode.Name, true
	}
	return "", false
}

func (p *SessionPlan) validate() error {
	members := p.memberTags()
	if len(members) == 0 {
		return errors.New("session plan has no nodes")
	}
	if !slices.Contains(members, p.ActiveTag) {
		return fmt.Errorf("active node %q is not in the plan", p.ActiveTag)
	}
	seen := map[string]bool{}
	for _, tag := range members {
		if seen[tag] {
			return fmt.Errorf("duplicate node id %q — selector members must be unique", tag)
		}
		seen[tag] = true
	}
	return nil
}

// planOutbounds builds the outbound list: the selector (route final), the
// native members, the xray-reality member (when the active node is
// xray-routed), and direct.
func (p *SessionPlan) planOutbounds() ([]any, error) {
	selector := map[string]any{
		"type":      "selector",
		"tag":       SelectorTag,
		"outbounds": p.memberTags(),
		"default":   p.ActiveTag,
		// A switch must MOVE traffic, not only new connections: existing
		// connections through the old node are interrupted.
		"interrupt_exist_connections": true,
	}
	outbounds := []any{selector}
	for _, n := range p.Natives {
		ob, err := n.Profile.SingBoxOutbound(n.ID)
		if err != nil {
			return nil, fmt.Errorf("native outbound %q: %w", n.Name, err)
		}
		outbounds = append(outbounds, ob)
	}
	if p.XrayNode != nil {
		outbounds = append(outbounds, map[string]any{"type": OutboundType, "tag": p.XrayNode.ID})
	}
	outbounds = append(outbounds,
		map[string]any{"type": "direct", "tag": "direct"},
		// The Block routing target's sink (1.12 still ships the block
		// outbound; the 1.13 reject action is out of our pin).
		map[string]any{"type": "block", "tag": "block"},
	)
	return outbounds, nil
}

// planDNS compiles the user's DNS settings into the resolver block and
// returns it with the tag the route's default_domain_resolver must use.
//
// NOTE on routing: these are sing-box 1.12 NEW-FORMAT DNS servers. Per the
// 1.12 docs they "use dialer just like an outbound, equivalent to an empty
// direct outbound by default" — a server WITHOUT a detour dials DIRECT, not
// through route.final (an explicit detour:"direct" is even rejected as
// redundant). DNSViaTunnel therefore detours query servers through the
// selector EXPLICITLY, and adds a detour-less mirror of the primary
// upstream as the bootstrap: node domains must keep resolving direct
// (resolving the proxy's own domain through the proxy is circular).
func planDNS(t Tunables) (map[string]any, string) {
	servers := make([]any, 0, len(t.DNS)+1)
	for i, upstream := range t.DNS {
		entry := map[string]any{
			"tag": dnsTag(i), "type": upstream.Type, "server": upstream.Address,
		}
		if t.DNSViaTunnel {
			entry["detour"] = SelectorTag
		}
		servers = append(servers, entry)
	}
	resolver := dnsTag(0)
	if t.DNSViaTunnel {
		servers = append(servers, map[string]any{
			"tag": "dns-bootstrap", "type": t.DNS[0].Type, "server": t.DNS[0].Address,
		})
		resolver = "dns-bootstrap"
	}
	return map[string]any{
		"independent_cache": true,
		"strategy":          dnsStrategy(t.DNSStrategy, t.IPVersion),
		"servers":           servers,
	}, resolver
}

// PlanTUNConfigs compiles the multi-node TUN session: the PROVEN auto_redirect
// TUN structure with the selector as route final. Starting it hijacks host
// traffic until the Session closes (root required). An empty tunName lets
// sing-box auto-assign (macOS).
func PlanTUNConfigs(p SessionPlan, tunName string) (singBox, xray []byte, err error) {
	if err := p.validate(); err != nil {
		return nil, nil, err
	}
	outbounds, err := p.planOutbounds()
	if err != nil {
		return nil, nil, err
	}

	t := p.tunables()
	inbound := map[string]any{
		"type": "tun", "tag": "tun-in",
		"address": tunAddresses(t.IPVersion),
		"mtu":     t.TunMTU, "auto_route": true,
		"strict_route": t.StrictRoute,
		"stack":        t.TunStack,
	}
	// auto_redirect is a Linux-only sing-box feature (nftables/eBPF) — sing-box
	// rejects it on Windows/macOS, so gate it to Linux. auto_route handles
	// routing on the other platforms; the own-output fwmark exemption it pairs
	// with is already a Linux-only no-op (see dialmark_other.go).
	if runtime.GOOS == "linux" {
		inbound["auto_redirect"] = true
	}
	lanBypass := t.LanBypass
	if lanBypass == nil {
		lanBypass = DefaultTunables().LanBypass
	}
	var excludes []any
	for _, cidr := range lanBypass {
		excludes = append(excludes, cidr)
	}
	// Off Linux there is no auto_redirect fwmark, so the daemon's OWN connection
	// to a proxy server (xray's dial, and the latency probe that rides the same
	// path) would be re-captured by auto_route and loop. Exclude the member
	// nodes' server IPs from the TUN route so those connections egress the
	// physical interface instead. (Native/sing-box dials are already exempt via
	// auto_detect_interface; this is for the in-process xray sockets.)
	if runtime.GOOS != "linux" {
		for _, cidr := range serverExcludes(p) {
			excludes = append(excludes, cidr)
		}
	}
	if len(excludes) > 0 {
		inbound["route_exclude_address"] = excludes
	}
	if tunName != "" {
		inbound["interface_name"] = tunName
	}

	dns, domainResolver := planDNS(t)
	route, err := p.routeBlock(true, domainResolver)
	if err != nil {
		return nil, nil, err
	}
	cfg := map[string]any{
		"log":       map[string]any{"level": t.LogLevel},
		"dns":       dns,
		"inbounds":  []any{inbound},
		"outbounds": outbounds,
		"route":     route,
	}
	p.applyCache(cfg)
	singBox, err = json.Marshal(cfg)
	if err != nil {
		return nil, nil, err
	}
	xray, err = p.xrayConfig("@il-xray-plan-tun")
	return singBox, xray, err
}

// serverExcludes resolves every member node's server address to its IPs and
// returns them as host-mask CIDRs for route_exclude_address (the cross-platform
// stand-in for the Linux auto_redirect fwmark — see PlanTUNConfigs). Best
// effort: a literal IP is used as-is; a name that does not resolve is skipped
// (the worst case is the pre-existing loop for that one node). Resolution runs
// at activation, before the TUN is up, so the system resolver is reachable.
func serverExcludes(p SessionPlan) []string {
	hosts := map[string]struct{}{}
	add := func(n *NamedNode) {
		if n != nil && n.Profile != nil && n.Profile.ServerAddress() != "" {
			hosts[n.Profile.ServerAddress()] = struct{}{}
		}
	}
	for i := range p.Natives {
		add(&p.Natives[i])
	}
	add(p.XrayNode)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	seen := map[string]struct{}{}
	var out []string
	push := func(a netip.Addr) {
		a = a.Unmap()
		var cidr string
		switch {
		case a.Is4():
			cidr = a.String() + "/32"
		case a.Is6():
			cidr = a.String() + "/128"
		default:
			return
		}
		if _, dup := seen[cidr]; !dup {
			seen[cidr] = struct{}{}
			out = append(out, cidr)
		}
	}
	for host := range hosts {
		if a, err := netip.ParseAddr(host); err == nil {
			push(a)
			continue
		}
		ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			continue
		}
		for _, ip := range ips {
			push(ip)
		}
	}
	return out
}

// applyCache adds the experimental cache-file block (see CachePath).
func (p *SessionPlan) applyCache(cfg map[string]any) {
	if p.CachePath == "" {
		return
	}
	cfg["experimental"] = map[string]any{
		"cache_file": map[string]any{"enabled": true, "path": p.CachePath},
	}
}

// PlanSocksConfigs compiles the multi-node NO-ROOT session: a SOCKS inbound
// instead of the TUN, same selector data plane. The dns block is kept so a
// native outbound with a domain server address can resolve (route
// default_domain_resolver).
func PlanSocksConfigs(p SessionPlan, host string, port int) (singBox, xray []byte, err error) {
	if err := p.validate(); err != nil {
		return nil, nil, err
	}
	outbounds, err := p.planOutbounds()
	if err != nil {
		return nil, nil, err
	}

	t := p.tunables()
	dns, domainResolver := planDNS(t)
	route, err := p.routeBlock(false, domainResolver)
	if err != nil {
		return nil, nil, err
	}
	cfg := map[string]any{
		"log": map[string]any{"level": t.LogLevel},
		"dns": dns,
		"inbounds": []any{map[string]any{
			"type": "socks", "tag": "socks-in", "listen": host, "listen_port": port,
		}},
		"outbounds": outbounds,
		"route":     route,
	}
	p.applyCache(cfg)
	singBox, err = json.Marshal(cfg)
	if err != nil {
		return nil, nil, err
	}
	xray, err = p.xrayConfig("@il-xray-plan-socks")
	return singBox, xray, err
}

// xrayConfig returns the xray side: the real client config when the active
// node is xray-routed, else an inert freedom stub (Session.Start always
// builds an xray instance; with no xray-reality outbound in the sing-box
// config it never sees traffic).
func (p *SessionPlan) xrayConfig(stubListen string) ([]byte, error) {
	if p.XrayNode != nil {
		return CompileXrayClient(p.XrayNode.Profile, stubListen)
	}
	return []byte(fmt.Sprintf(`{
      "log": {"loglevel":"warning"},
      "inbounds": [{"listen":%q,"protocol":"socks","settings":{"udp":false}}],
      "outbounds": [{"protocol":"freedom","tag":"direct","streamSettings":{"sockopt":{"mark":%d}}}]
    }`, stubListen, AutoRedirectOutputMark)), nil
}

// SelectOutbound live-switches the running selector to the member named tag —
// the in-process replacement of the Clash-API `PUT /proxies` (plan §6). It
// fails (and the caller re-activates instead) when the session was built
// single-node (no selector) or tag is not an embedded member.
func (s *Session) SelectOutbound(tag string) error {
	if s.box == nil {
		return errors.New("session is not running")
	}
	ob, ok := s.box.Outbound().Outbound(SelectorTag)
	if !ok {
		return fmt.Errorf("no %q outbound in the running session", SelectorTag)
	}
	sel, ok := ob.(*group.Selector)
	if !ok {
		return fmt.Errorf("%q outbound is not a selector (single-node session)", SelectorTag)
	}
	if !sel.SelectOutbound(tag) {
		return fmt.Errorf("node %q is not a member of the running selector", tag)
	}
	return nil
}

// SelectedOutbound reports the selector's current member ("", false when the
// session has no selector). This is the in-process `active_node_live`.
func (s *Session) SelectedOutbound() (string, bool) {
	if s.box == nil {
		return "", false
	}
	ob, ok := s.box.Outbound().Outbound(SelectorTag)
	if !ok {
		return "", false
	}
	sel, ok := ob.(*group.Selector)
	if !ok {
		return "", false
	}
	return sel.Now(), true
}
