// The connection doctor: read-only network checks, one row each (the "Doctor"
// tab). v1 is the connectivity + address-family check, which also recommends a
// dns.strategy when the current setting leans to an unreachable family — the
// dual-stack break the dns.strategy split (decoupled from ip_version) was
// groundwork for. Checks never change state; a Remedy is a suggestion the
// client offers to apply via set_settings.
package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	"ironlink/daemon/internal/api"
	"ironlink/daemon/internal/engine"
	"ironlink/daemon/internal/proxy"
	"ironlink/daemon/internal/store"
)

// doctorBudget bounds a single doctor run. The checks run concurrently; within
// a check probes are 4s each (connectivity = two families; resolvers =
// configured upstreams, then curated candidates; camouflage = tcp → tls →
// decoy-download, each ~4s).
const doctorBudget = 15 * time.Second

// doctorNodesBudget bounds the "probe all nodes" sweep: per node a connectivity
// diagnosis (every protocol) plus a camouflage probe for TLS-fronted nodes, run
// concurrency-capped. The detailed sweep dials each node, so it is roomier than
// the quick doctor battery.
const doctorNodesBudget = 45 * time.Second

// camouflageThresholdBytes is the manager-side view of "we crossed the ~16KB
// freeze window" — below it a clean transfer can't have exercised the block.
const camouflageThresholdBytes = 16 * 1024

// qualityProbeBudget bounds the connection-quality decomposition inside the
// doctor run: the staged fetch (DNS → TCP → TLS → TTFB → throughput sample) runs
// under this so even a throttled path cannot stretch the whole battery to the
// outer doctorBudget ceiling; a healthy cycle finishes in a second or two.
const qualityProbeBudget = 12 * time.Second

// doctor runs the doctor battery and returns the ordered checks. The network
// checks need no running session (the probes dial direct, TUN-exempt on Linux);
// the camouflage row probes the SELECTED node when one exists and carries a TLS
// front.
func (m *manager) doctor(_ api.Request) api.Response {
	settings, err := m.store.LoadSettings()
	if err != nil {
		return errResp(err.Error())
	}

	// The active node (best-effort) drives the per-node rows: a universal
	// reachability check for ANY protocol, plus the camouflage probe when it
	// carries a TLS front (SS/anytls-without-front/QUIC get the reachability row
	// but no camouflage row).
	var activeNode, camNode *store.Node
	if prof, _ := m.activeProfileForDoctor(); prof != nil {
		// A group has no endpoint of its own to probe (its members are diagnosed
		// individually), so it drives no per-node doctor row.
		if n := prof.ActiveNode(); n != nil && !n.IsGroup() {
			activeNode = n
			if engine.HasCamouflage(n.Profile()) {
				camNode = n
			}
		}
	}
	m.mu.Lock()
	tunActive := m.active != nil && m.active.Tun
	m.mu.Unlock()
	probeURL, _ := m.probeParams()

	ctx, cancel := context.WithTimeout(context.Background(), doctorBudget)
	defer cancel()

	// The checks are independent — run them concurrently so the report's
	// wall-clock is the slowest one, not their sum.
	var (
		internet engine.InternetReport
		resolver api.DoctorCheck
		quality  api.DoctorCheck
		health   *api.DoctorCheck
		cam      *api.DoctorCheck
		wg       sync.WaitGroup
	)
	wg.Add(3)
	go func() { defer wg.Done(); internet = engine.ProbeInternet(ctx) }()
	go func() { defer wg.Done(); resolver = resolverCheck(ctx, settings) }()
	go func() {
		defer wg.Done()
		// Sub-budget the full-cycle probe so a throttled resolver cannot push the
		// whole doctor to the outer ceiling; it stays well under it when healthy.
		qctx, qcancel := context.WithTimeout(ctx, qualityProbeBudget)
		defer qcancel()
		quality = connectionQualityCheck(engine.ProbeConnectionQuality(qctx, primaryUpstream(settings), "", ""))
	}()
	if activeNode != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if hc, ok := nodeHealth(ctx, activeNode, probeURL, tunActive); ok {
				health = &hc
			}
		}()
	}
	if camNode != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cc := camouflageCheck(camNode.DisplayName(), engine.ProbeCamouflage(ctx, camNode.Profile()))
			cam = &cc
		}()
	}
	wg.Wait()

	checks := []api.DoctorCheck{
		internetCheck(internet, settings.IPVersion, settings.DNS.Strategy),
		resolver,
		quality,
	}
	if health != nil {
		checks = append(checks, *health)
	}
	if cam != nil {
		checks = append(checks, *cam)
	}
	return api.Response{Status: api.StatusDoctorReport, Checks: checks}
}

// nodeHealth runs the universal staged diagnosis for the active node through the
// core the live session would use, and turns it into a doctor row — the per-node
// "does it actually work" check for EVERY protocol. The camouflage row only
// covers TLS-fronted nodes, so without this SS/anytls/QUIC would show no node
// check at all. ok=false when no installed core can dial the node.
func nodeHealth(ctx context.Context, n *store.Node, url string, tunExempt bool) (api.DoctorCheck, bool) {
	core, err := proxy.SelectCore(n.Profile(), api.CoreSingBox, n.Preferences.CoreOverride)
	if err != nil {
		return api.DoctorCheck{}, false
	}
	d := engine.DiagnoseNode(ctx, n.Profile(), core, n.ID, url, tunExempt)
	return nodeHealthCheck(n.DisplayName(), d), true
}

// nodeHealthCheck classifies a node's staged diagnosis into one doctor row: ok
// when every stage passed (with the through-tunnel round trip as the headline
// latency), fail naming whether the endpoint was unreachable (tcp) or the tunnel
// could not open (proxy).
func nodeHealthCheck(nodeName string, d engine.Diagnosis) api.DoctorCheck {
	c := api.DoctorCheck{ID: "node_health", Title: "Node connectivity — " + nodeName}
	for _, s := range d.Stages {
		if s.OK {
			c.Details = append(c.Details, fmt.Sprintf("%s: ok (%d ms)", s.Stage, s.DurationMs))
		} else {
			c.Details = append(c.Details, fmt.Sprintf("%s: %s", s.Stage, s.Err))
		}
	}
	if d.OK {
		c.Status = "ok"
		var ms int64
		if k := len(d.Stages); k > 0 {
			ms = d.Stages[k-1].DurationMs
		}
		c.Summary = fmt.Sprintf("Node reachable and carrying traffic (%d ms through the tunnel).", ms)
		return c
	}
	c.Status = "fail"
	switch d.FailedStage {
	case engine.StageConfig:
		c.Summary = "Invalid node configuration — the core rejected it (wrong cipher/key size, missing Reality keys); see details."
	case engine.StageTCP:
		c.Summary = "Server unreachable — the endpoint is down or its IP/port is blocked."
	default:
		c.Summary = "Tunnel did not open — handshake/auth failed or the node is misconfigured."
	}
	return c
}

// doctorNodes probes the camouflage of every TLS-bearing node in the active
// profile (the "probe all nodes" button) — one check per node, concurrency-
// capped. Plain (no-TLS) nodes have no front and are skipped.
func (m *manager) doctorNodes() api.Response {
	prof, err := m.activeProfileForDoctor()
	if err != nil {
		return errResp(err.Error())
	}
	empty := api.Response{Status: api.StatusDoctorReport, Checks: []api.DoctorCheck{}}
	if prof == nil || len(prof.Nodes) == 0 {
		return empty
	}

	m.mu.Lock()
	tunActive := m.active != nil && m.active.Tun
	m.mu.Unlock()
	probeURL, _ := m.probeParams()

	ctx, cancel := context.WithTimeout(context.Background(), doctorNodesBudget)
	defer cancel()

	// Each node yields a connectivity row (ANY protocol — config → reach → tunnel)
	// and, when it carries a TLS front, a camouflage row. Rows are computed
	// concurrently (capped) and emitted in node order: each node's
	// [connectivity, camouflage?] group, so the report reads node-by-node.
	perNode := make([][]api.DoctorCheck, len(prof.Nodes))
	sem := make(chan struct{}, 8) // bound concurrent ephemeral cores / direct dials
	var wg sync.WaitGroup
	for i := range prof.Nodes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			n := &prof.Nodes[i]
			if n.IsGroup() {
				return // a group has no endpoint to probe; its members get their own rows
			}
			var rows []api.DoctorCheck
			if hc, ok := nodeHealth(ctx, n, probeURL, tunActive); ok {
				rows = append(rows, hc)
			}
			if engine.HasCamouflage(n.Profile()) {
				rows = append(rows, camouflageCheck(n.DisplayName(), engine.ProbeCamouflage(ctx, n.Profile())))
			}
			perNode[i] = rows
		}(i)
	}
	wg.Wait()

	checks := []api.DoctorCheck{}
	for _, rows := range perNode {
		checks = append(checks, rows...)
	}
	return api.Response{Status: api.StatusDoctorReport, Checks: checks}
}

// activeProfileForDoctor resolves the profile to probe nodes from: the running
// session's profile when one is up, else the store's active profile. Returns
// (nil, nil) when there is no active profile (the camouflage row is simply
// omitted rather than failing the whole report).
func (m *manager) activeProfileForDoctor() (*store.Profile, error) {
	m.mu.Lock()
	name := ""
	if m.active != nil && m.active.Profile != nil {
		name = *m.active.Profile
	}
	m.mu.Unlock()
	if name == "" {
		n, err := m.store.ActiveProfileName()
		if err != nil {
			return nil, err
		}
		if n == "" {
			return nil, nil
		}
		name = n
	}
	return m.store.LoadProfile(name)
}

// camouflageCheck is the PURE verdict for one node's camouflage probe: fail when
// the server is unreachable or the front is broken, warn when the ~16KB freeze
// is detected, ok otherwise (with an honest "freeze not exercised" note for a
// decoy too small to cross the threshold).
func camouflageCheck(nodeName string, r engine.CamouflageReport) api.DoctorCheck {
	c := api.DoctorCheck{ID: "camouflage", Title: "Node camouflage — " + nodeName}
	if r.SNI != "" {
		c.Details = append(c.Details, "Camouflage SNI: "+r.SNI)
	}

	if !r.TCPOK {
		c.Status = "fail"
		c.Summary = "Server unreachable — the node is down or its IP is blocked."
		c.Details = append(c.Details, "TCP "+r.Address+": "+r.Err)
		return c
	}
	c.Details = append(c.Details, "TCP "+r.Address+": connected")

	if !r.TLSOK {
		c.Status = "fail"
		c.Summary = "Camouflage front broken — TLS/cert failed (mis-fronted, wrong SNI, or DPI on the handshake)."
		c.Details = append(c.Details, "TLS: "+r.Err)
		return c
	}
	c.Details = append(c.Details, certLine(r.SNI))

	if r.Frozen {
		kb := r.BytesRead / 1024
		c.Status = "warn"
		c.Summary = fmt.Sprintf("Connection froze after ~%d KB — Russia's ~16 KB block is active on this node's path.", kb)
		c.Details = append(c.Details,
			fmt.Sprintf("The decoy transfer stalled at ~%d KB with no reset — the signature of the IP-keyed ~16 KB freeze.", kb),
			"Real traffic dies at the same threshold even though the front looks intact. Try another node/IP, a relay on a whitelisted IP, or DPI-bypass (byedpi, planned).")
		return c
	}

	c.Status = "ok"
	kb := r.BytesRead / 1024
	if r.HTTPStatus > 0 {
		c.Details = append(c.Details, fmt.Sprintf("Decoy responded HTTP %d", r.HTTPStatus))
	}
	if r.BytesRead >= camouflageThresholdBytes {
		c.Summary = fmt.Sprintf("Healthy — front intact and %d KB transferred past the 16 KB threshold with no freeze.", kb)
	} else {
		c.Summary = fmt.Sprintf("Front intact; pulled %d KB with no freeze.", kb)
		c.Details = append(c.Details,
			"The decoy is small (<16 KB), so the ~16 KB freeze could not be exercised here — real traffic may still hit it.")
	}
	return c
}

func certLine(sni string) string {
	if sni == "" {
		return "TLS handshake ok (no SNI to verify)"
	}
	return "TLS + cert verified for " + sni
}

// resolverCheck probes the configured DNS upstreams and — when the primary is
// blocked and no direct alternate answers — falls back to the curated
// candidates. The probing is here (it needs the network); the verdict is the
// pure resolverCheckResult so it stays unit-testable.
func resolverCheck(ctx context.Context, s api.Settings) api.DoctorCheck {
	servers := s.DNS.Servers
	ups := make([]engine.DNSUpstream, len(servers))
	for i, srv := range servers {
		ups[i] = engine.DNSUpstream{Type: srv.Type, Address: srv.Address}
	}
	probes := engine.ProbeResolvers(ctx, ups)

	// Only spend the candidate budget when the primary is down and no
	// configured secondary already answers (and DNS isn't tunnelled).
	var candidate *engine.DNSUpstream
	if !s.DNS.ViaTunnel && len(probes) > 0 && !probes[0].Reachable && !anyReachable(probes[1:]) {
		candidate = engine.FirstReachableCandidate(ctx, addrSet(servers))
	}
	return resolverCheckResult(servers, probes, s.DNS.ViaTunnel, candidate)
}

// resolverCheckResult is the PURE verdict: ok when the primary answers (or DNS
// is tunnelled), warn otherwise with a remedy — promote a reachable configured
// secondary, else switch to a reachable curated candidate, else route DNS
// through the tunnel.
func resolverCheckResult(servers []api.DNSServer, probes []engine.DNSUpstreamProbe, viaTunnel bool, candidate *engine.DNSUpstream) api.DoctorCheck {
	c := api.DoctorCheck{ID: "resolvers", Title: "DNS resolvers"}

	if len(servers) == 0 {
		c.Status = "warn"
		c.Summary = "No DNS upstream is configured."
		c.Remedy = &api.DoctorRemedy{
			Summary:    "Use 1.1.1.1 (DoT) as the resolver",
			DNSServers: []api.DNSServer{{Type: "tls", Address: "1.1.1.1"}},
		}
		return c
	}

	for _, p := range probes {
		c.Details = append(c.Details, upstreamLine(p))
	}

	if viaTunnel {
		c.Status = "ok"
		c.Summary = "DNS is routed through the tunnel."
		c.Details = append(c.Details,
			"via_tunnel is on — upstream IPs are reached through the proxy, so direct ISP blocking does not apply.")
		return c
	}

	if probes[0].Reachable {
		c.Status = "ok"
		c.Summary = fmt.Sprintf("Resolver %s is reachable.", resolverLabel(probes[0].Type, probes[0].Address))
		return c
	}

	c.Status = "warn"
	c.Summary = fmt.Sprintf("Resolver %s is unreachable.", resolverLabel(probes[0].Type, probes[0].Address))

	// A reachable configured secondary → promote it to primary.
	for i := 1; i < len(probes); i++ {
		if probes[i].Reachable {
			c.Remedy = &api.DoctorRemedy{
				Summary:    fmt.Sprintf("Make %s the primary resolver", probes[i].Address),
				DNSServers: promoteResolver(servers, probes[i].Address),
			}
			return c
		}
	}
	// A reachable curated candidate → prepend it, keep the rest.
	if candidate != nil {
		c.Remedy = &api.DoctorRemedy{
			Summary: fmt.Sprintf("Switch to %s (%s)", candidate.Address, resolverKindLabel(candidate.Type)),
			DNSServers: append([]api.DNSServer{{Type: candidate.Type, Address: candidate.Address}},
				servers...),
		}
		return c
	}
	// Nothing direct answers → route DNS through the proxy.
	via := true
	c.Remedy = &api.DoctorRemedy{
		Summary:      "Route DNS through the tunnel",
		DNSViaTunnel: &via,
	}
	return c
}

func anyReachable(probes []engine.DNSUpstreamProbe) bool {
	for _, p := range probes {
		if p.Reachable {
			return true
		}
	}
	return false
}

// addrSet is the set of configured upstream IPs, so the candidate probe skips
// resolvers already known blocked.
func addrSet(servers []api.DNSServer) map[string]bool {
	m := make(map[string]bool, len(servers))
	for _, s := range servers {
		m[s.Address] = true
	}
	return m
}

// promoteResolver returns servers with the upstream at addr moved to the front
// (the daemon-computed "intended list" the remedy carries verbatim).
func promoteResolver(servers []api.DNSServer, addr string) []api.DNSServer {
	out := make([]api.DNSServer, 0, len(servers))
	var promoted *api.DNSServer
	for i := range servers {
		if promoted == nil && servers[i].Address == addr {
			s := servers[i]
			promoted = &s
			continue
		}
		out = append(out, servers[i])
	}
	if promoted != nil {
		out = append([]api.DNSServer{*promoted}, out...)
	}
	return out
}

func resolverKindLabel(typ string) string {
	switch typ {
	case "tls":
		return "DoT"
	case "https":
		return "DoH"
	case "udp":
		return "plain DNS"
	default:
		return typ
	}
}

func resolverLabel(typ, addr string) string {
	return resolverKindLabel(typ) + " " + addr
}

func upstreamLine(p engine.DNSUpstreamProbe) string {
	label := resolverLabel(p.Type, p.Address)
	if p.Reachable {
		if p.Detail != "" {
			return label + ": reachable — " + p.Detail
		}
		return label + ": reachable"
	}
	return label + ": blocked (" + p.Detail + ")"
}

// internetCheck turns the connectivity probe into one doctor row: fail when no
// family reaches the internet, warn when the settings lean to a dead family
// (with a dns.strategy remedy), ok otherwise.
func internetCheck(r engine.InternetReport, ipVersion, strategy string) api.DoctorCheck {
	c := api.DoctorCheck{ID: "internet", Title: "Internet connectivity"}
	cur := normalizeStrategy(strategy)
	c.Details = []string{
		familyLine("IPv4", r.V4, r.V4Detail, familyDisabled("v4", ipVersion, cur)),
		familyLine("IPv6", r.V6, r.V6Detail, familyDisabled("v6", ipVersion, cur)),
		resolverLine(r),
	}

	switch {
	case !r.V4 && !r.V6:
		c.Status = "fail"
		c.Summary = "No internet — neither IPv4 nor IPv6 reached a public endpoint."
		return c
	case r.V4 && r.V6:
		c.Summary = "Online over IPv4 and IPv6."
	case r.V4:
		c.Summary = "Online over IPv4 only."
	default:
		c.Summary = "Online over IPv6 only."
	}

	if remedy := strategyRemedy(r.V4, r.V6, cur); remedy != nil {
		c.Status = "warn"
		c.Remedy = remedy
		c.Details = append(c.Details, fmt.Sprintf(
			"DNS strategy is %q but only %s is reachable.", cur, reachableFamily(r.V4, r.V6)))
	} else {
		c.Status = "ok"
	}

	// A TUN pinned to a single, unreachable family is a deeper mismatch; surface
	// it as a detail (changing ip_version is a heavier remedy, deferred to a
	// later doctor iteration).
	if (ipVersion == "v4" && !r.V4 && r.V6) || (ipVersion == "v6" && !r.V6 && r.V4) {
		c.Details = append(c.Details, fmt.Sprintf(
			"TUN ip_version is %q but that family is unreachable — consider ip_version=both.", ipVersion))
		if c.Status == "ok" {
			c.Status = "warn"
		}
	}

	// ip_version pins the TUN to ONE family while this network actually HAS the
	// other — the user is giving up working connectivity, and any destination
	// reachable ONLY over the disabled family fails. Worth a warn even when the
	// strategy is pinned consistently: it's a silent capability loss on a network
	// that supports both families. (Gated on the tunnel's own family working —
	// when it does NOT, the dead-family pin above already spoke.) No one-click
	// remedy: the fix is ip_version=both, not a DNS lever, so it's named in text.
	switch {
	case ipVersion == "v4" && r.V4 && r.V6:
		c.Details = append(c.Details, "This network has working IPv6, but ip_version=v4 disables it "+
			"end-to-end (DNS is pinned to IPv4) — IPv6-only destinations are unreachable. Set ip_version=both to use it.")
		if c.Status == "ok" {
			c.Status = "warn"
		}
	case ipVersion == "v6" && r.V6 && r.V4:
		c.Details = append(c.Details, "This network has working IPv4, but ip_version=v6 disables it "+
			"end-to-end (DNS is pinned to IPv6) — IPv4-only destinations are unreachable. Set ip_version=both to use it.")
		if c.Status == "ok" {
			c.Status = "warn"
		}
	}

	// Contradiction: the stored dns.strategy leans to the family ip_version
	// FORBIDS. The engine pins DNS to the matching *_only regardless (tunables.go
	// dnsStrategy), so the preference is silently ignored — warn, and when the
	// tunnel's OWN family reaches the internet offer to align the stored setting.
	// Honoring the preference instead means ip_version=both, named in the text as
	// the manual alternative (ip_version is not a one-click remedy lever).
	leansV4 := cur == "prefer_ipv4" || cur == "ipv4_only"
	leansV6 := cur == "prefer_ipv6" || cur == "ipv6_only"
	switch {
	case ipVersion == "v4" && leansV6:
		c.Status = "warn"
		c.Details = append(c.Details, fmt.Sprintf(
			"dns.strategy %q wants IPv6, but ip_version=v4 forbids it — DNS is pinned to ipv4_only and "+
				"the preference is ignored. Set ip_version=both to use IPv6, or dns.strategy=ipv4_only to match.", cur))
		if r.V4 && c.Remedy == nil {
			c.Remedy = &api.DoctorRemedy{Summary: "Pin DNS to IPv4 to match ip_version=v4", DNSStrategy: "ipv4_only"}
		}
	case ipVersion == "v6" && leansV4:
		c.Status = "warn"
		c.Details = append(c.Details, fmt.Sprintf(
			"dns.strategy %q wants IPv4, but ip_version=v6 forbids it — DNS is pinned to ipv6_only and "+
				"the preference is ignored. Set ip_version=both to use IPv4, or dns.strategy=ipv6_only to match.", cur))
		if r.V6 && c.Remedy == nil {
			c.Remedy = &api.DoctorRemedy{Summary: "Pin DNS to IPv6 to match ip_version=v6", DNSStrategy: "ipv6_only"}
		}
	}
	return c
}

func familyLine(label string, ok bool, detail string, disabled bool) string {
	if ok {
		return label + ": reachable"
	}
	if disabled {
		// The user turned this family off (ip_version pins the OTHER family, or
		// an *_only DNS strategy forces it); its being unreachable is the
		// intended state, not a fault — annotate rather than alarm.
		return label + ": unreachable — expected (disabled by config)"
	}
	if detail != "" {
		return label + ": unreachable (" + detail + ")"
	}
	return label + ": unreachable"
}

// familyDisabled reports whether the user's config deliberately turns OFF the
// given address family ("v4"/"v6"): the TUN is pinned to the OTHER family
// (ip_version), or the DNS strategy forces the OTHER family (`ipv4_only` /
// `ipv6_only`). A family the user disabled being unreachable is EXPECTED, so a
// check must never warn about it — the same rule the strategy/ip_version remedy
// logic already follows for the verdict, made explicit for the detail lines.
func familyDisabled(family, ipVersion, strategy string) bool {
	switch family {
	case "v4":
		return ipVersion == "v6" || strategy == "ipv6_only"
	case "v6":
		return ipVersion == "v4" || strategy == "ipv4_only"
	}
	return false
}

func resolverLine(r engine.InternetReport) string {
	if r.ResolveErr != "" {
		return "DNS resolver: failed (" + r.ResolveErr + ")"
	}
	switch {
	case r.ResolverV4 && r.ResolverV6:
		return "DNS resolver: answers A and AAAA"
	case r.ResolverV4:
		return "DNS resolver: answers A only"
	case r.ResolverV6:
		return "DNS resolver: answers AAAA only"
	default:
		return "DNS resolver: no records"
	}
}

func reachableFamily(v4, v6 bool) string {
	switch {
	case v4 && v6:
		return "IPv4 and IPv6"
	case v4:
		return "IPv4"
	case v6:
		return "IPv6"
	default:
		return "neither family"
	}
}

// normalizeStrategy resolves the stored value (incl. "") to the effective
// sing-box strategy, mirroring engine.dnsStrategy.
func normalizeStrategy(s string) string {
	switch s {
	case "prefer_ipv4", "prefer_ipv6", "ipv4_only", "ipv6_only":
		return s
	default:
		return "prefer_ipv4"
	}
}

// strategyRemedy proposes a dns.strategy change when the current strategy
// prefers or forces a family that is NOT reachable; nil when it is consistent
// with reachability. Only meaningful when at least one family works. Recommends
// the "prefer_*" form (keeps fallback) rather than the "*_only" form.
func strategyRemedy(v4, v6 bool, cur string) *api.DoctorRemedy {
	leansV4 := cur == "prefer_ipv4" || cur == "ipv4_only"
	leansV6 := cur == "prefer_ipv6" || cur == "ipv6_only"
	switch {
	case v6 && !v4 && leansV4:
		return &api.DoctorRemedy{Summary: "Switch DNS strategy to prefer IPv6", DNSStrategy: "prefer_ipv6"}
	case v4 && !v6 && leansV6:
		return &api.DoctorRemedy{Summary: "Switch DNS strategy to prefer IPv4", DNSStrategy: "prefer_ipv4"}
	default:
		return nil
	}
}

// ---- connection-quality decomposition (Task 5) -----------------------------

// primaryUpstream is the configured primary resolver as an engine upstream — the
// resolver the connection-quality DNS stage times, so the row reflects the
// channel the session actually uses. A store with no servers (validation forbids
// it) falls back to the DoT default rather than probing nothing.
func primaryUpstream(s api.Settings) engine.DNSUpstream {
	if len(s.DNS.Servers) > 0 {
		return engine.DNSUpstream{Type: s.DNS.Servers[0].Type, Address: s.DNS.Servers[0].Address}
	}
	return engine.DNSUpstream{Type: "tls", Address: "8.8.8.8"}
}

// connectionQualityCheck turns the decomposed full-cycle probe into one doctor
// row: fail when a stage errored (naming which), warn when the cycle completed
// but a stage is anomalously slow (naming the bottleneck — the "works but slow"
// verdict, e.g. a throttled DoT resolver), ok when every stage is within range.
// Per-stage timings are the detail lines (mirroring nodeHealthCheck's stage
// rendering — no new wire fields).
func connectionQualityCheck(r engine.QualityReport) api.DoctorCheck {
	c := api.DoctorCheck{ID: "connection_quality", Title: "Connection quality — " + r.Target}
	for _, s := range r.Stages {
		c.Details = append(c.Details, qualityStageLine(s))
	}
	if !r.OK {
		c.Status = "fail"
		c.Summary = qualityFailSummary(r.FailedStage)
		return c
	}
	if r.Bottleneck != "" {
		c.Status = "warn"
		c.Summary = qualityBottleneckSummary(r, r.Bottleneck)
		return c
	}
	c.Status = "ok"
	c.Summary = "Full connection cycle healthy — DNS, connect, TLS, first byte, and throughput are all in range."
	return c
}

// qualityStageLine renders one stage: "dns (DoT 8.8.8.8): 45 ms", a throughput
// note verbatim, or "tls: failed — <detail>"; anomalous stages get a "— slow" tag.
func qualityStageLine(s engine.QualityStageResult) string {
	if !s.OK {
		return fmt.Sprintf("%s: failed — %s", s.Stage, s.Detail)
	}
	var line string
	switch {
	case s.Stage == engine.QStageThroughput:
		line = fmt.Sprintf("%s: %s", s.Stage, s.Detail)
	case s.Detail != "":
		line = fmt.Sprintf("%s (%s): %d ms", s.Stage, s.Detail, s.DurationMs)
	default:
		line = fmt.Sprintf("%s: %d ms", s.Stage, s.DurationMs)
	}
	if s.Anomalous {
		line += " — slow"
	}
	return line
}

// qualityBottleneckSummary names the slow stage in plain language — the doctor's
// attribution of WHERE the "works but slow" cost is.
func qualityBottleneckSummary(r engine.QualityReport, b engine.QualityStage) string {
	switch b {
	case engine.QStageDNS:
		return fmt.Sprintf("Encrypted DNS is the bottleneck (%s) — the resolver step is throttled or slow; "+
			"the connection otherwise works, which is why it feels slow rather than broken.", stageDuration(r, b))
	case engine.QStageTCP:
		return fmt.Sprintf("TCP connect is slow (%s) — high latency or shaping on the path to the server.", stageDuration(r, b))
	case engine.QStageTLS:
		return fmt.Sprintf("The TLS handshake is slow (%s) — possible DPI interference on the handshake.", stageDuration(r, b))
	case engine.QStageTTFB:
		return fmt.Sprintf("Time-to-first-byte is high (%s) — the server or path is slow to start responding.", stageDuration(r, b))
	case engine.QStageThroughput:
		return "Throughput is low — the path is degraded (deep shaping, a mid-stream freeze, or a saturated link)."
	default:
		return "A connection stage is anomalously slow."
	}
}

// qualityFailSummary explains the first hard failure.
func qualityFailSummary(stage engine.QualityStage) string {
	switch stage {
	case engine.QStageDNS:
		return "DNS resolution failed — the configured resolver did not answer (blocked, hijacked, or misconfigured)."
	case engine.QStageTCP:
		return "TCP connect failed — the reference endpoint is unreachable."
	case engine.QStageTLS:
		return "TLS handshake failed — interference on the handshake or a certificate problem."
	case engine.QStageTTFB:
		return "No response — the server returned no first byte within the budget."
	case engine.QStageThroughput:
		return "The transfer stalled — no data flowed in the sample window."
	default:
		return "The connection cycle broke."
	}
}

// stageDuration finds a stage's "N ms" for a summary; "" when absent.
func stageDuration(r engine.QualityReport, stage engine.QualityStage) string {
	for _, s := range r.Stages {
		if s.Stage == stage {
			return fmt.Sprintf("%d ms", s.DurationMs)
		}
	}
	return ""
}
