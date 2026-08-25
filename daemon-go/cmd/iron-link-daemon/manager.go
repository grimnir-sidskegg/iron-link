package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"ironlink/daemon/internal/api"
	"ironlink/daemon/internal/engine"
	"ironlink/daemon/internal/ipc"
	"ironlink/daemon/internal/proxy"
	"ironlink/daemon/internal/routing"
	"ironlink/daemon/internal/store"
)

// The SOCKS port and probe budget live in the settings document
// (store.DefaultSettings); the manager fields below are TEST overrides
// only (zero = settings-driven).

// tunInterfaceName returns the fixed TUN device name, or "" on macOS where
// only kernel-assigned utunN names are allowed (sing-box auto-assigns).
// Mirrors the Rust generator's TUN_INTERFACE.
func tunInterfaceName() string {
	if runtime.GOOS == "darwin" {
		return ""
	}
	return "tun777"
}

// manager owns the daemon's data-plane state: at most one engine.Session (the
// embedded sing-box + xray pair) plus the Activate context it was started
// from. It is the G2 replacement of the G1 stub handlers.
//
// Activation is capability-driven (proxy.SelectCore per node, honoring the
// per-node core_override): every sing-box-eligible node embeds as a native
// selector member; the active node, when xray-routed (xhttp, or pinned to
// xray), joins the selector as the xray-reality outbound. SwitchNode is LIVE
// (in-process selector) for embedded members and a re-activation otherwise —
// v1 keeps one xray instance per active xray node.
type manager struct {
	store     *store.Store
	hub       *ipc.Hub // set right after construction (hub's snapshot is m.Snapshot)
	socksPort int      // TEST override of settings.socks_port (0 = settings)

	// mu guards the session state. It IS held across engine.Start/Close —
	// activations are deliberately serialized; a second Activate waits, then
	// REPLACES the session (same semantics as the Rust daemon's stop-then-start).
	mu        sync.Mutex
	sess      *engine.Session
	plan      engine.SessionPlan // the embedded node set (IsMember = live-switchable)
	startedAt time.Time
	active    *api.PersistedEntry
	// stopTraffic ends the running session's per-second Traffic emitter.
	stopTraffic chan struct{}
	// activations counts engine.Start calls — observability for "did that
	// switch re-activate or live-select" (asserted by tests, cheap to keep).
	activations int

	// probeTimeout is the TEST override of settings.latency_probe.budget_secs
	// (0 = settings; the default budget is sized for slow xhttp/xmux cold
	// starts — a too-eager cap mislabels working xhttp nodes unreachable).
	probeTimeout time.Duration
}

func newManager(st *store.Store) *manager {
	return &manager{store: st}
}

// probeParams resolves the latency-probe endpoint and budget: settings,
// unless a test injected m.probeTimeout.
func (m *manager) probeParams() (string, time.Duration) {
	url, budget := engine.ProbeURL, 45*time.Second
	if s, err := m.store.LoadSettings(); err == nil {
		url = s.LatencyProbe.URL
		budget = time.Duration(s.LatencyProbe.BudgetSecs) * time.Second
	}
	if m.probeTimeout != 0 {
		budget = m.probeTimeout
	}
	return url, budget
}

// logSink fans a sing-box log line into the event hub — Broadcast drops
// for lagging subscribers, so the logging path never stalls.
func (m *manager) logSink(level, message string) {
	if m.hub != nil {
		m.hub.Broadcast(api.Event{Event: api.EventLog, Level: level, Message: message})
	}
}

// Handle dispatches one control verb (ipc.HandlerFunc).
func (m *manager) Handle(req api.Request) api.Response {
	switch req.Command {
	case api.CmdStatus:
		return m.status()
	case api.CmdActivate:
		return m.activate(req)
	case api.CmdStop:
		return m.stop(req)
	case api.CmdSwitchNode:
		return m.switchNode(req)
	case api.CmdTestLatency:
		return m.testLatency(req)
	case api.CmdDiagnose:
		return m.diagnose(req)
	case api.CmdTrafficApps:
		return m.trafficApps(req)
	case api.CmdDoctor:
		return m.doctor(req)
	case api.CmdDoctorNodes:
		return m.doctorNodes()
	case api.CmdNetworkReport:
		return m.networkReport()
	case api.CmdForwardingCheck:
		return m.forwardingReport()

	// The G3 store verbs (manager_store.go).
	case api.CmdListProfiles:
		return m.listProfiles()
	case api.CmdCreateProfile:
		return m.createProfile(req)
	case api.CmdDeleteProfile:
		return m.deleteProfile(req)
	case api.CmdSetActiveProfile:
		return m.setActiveProfile(req)
	case api.CmdListNodes:
		return m.listNodes(req)
	case api.CmdSelectNode:
		return m.selectNode(req)
	case api.CmdAddNode:
		return m.addNode(req)
	case api.CmdRemoveNode:
		return m.removeNode(req)
	case api.CmdSetNodePrefs:
		return m.setNodePrefs(req)
	case api.CmdListSubs:
		return m.listSubscriptions(req)
	case api.CmdAddSub:
		return m.addSubscription(req)
	case api.CmdRefreshSubs:
		return m.refreshSubscriptions(req)
	case api.CmdRemoveSub:
		return m.removeSubscription(req)
	case api.CmdUpdateSub:
		return m.updateSubscription(req)
	case api.CmdListRouting:
		return m.listRouting(req)
	case api.CmdSelectRouting:
		return m.selectRouting(req)
	case api.CmdGetRouting:
		return m.getRouting(req)
	case api.CmdUpsertRouting:
		return m.upsertRouting(req)
	case api.CmdRemoveRouting:
		return m.removeRouting(req)
	case api.CmdRoutingSchema:
		// Pure data (no lock): the condition table for THIS daemon's
		// platform and pinned sing-box.
		return api.Response{Status: api.StatusRoutingSchema, RoutingSchema: routing.SchemaFor(runtime.GOOS)}

	// The settings verbs (manager_settings.go).
	case api.CmdGetSettings:
		return m.getSettings()
	case api.CmdSetSettings:
		return m.setSettings(req)

	default:
		return errResp("unimplemented verb: " + req.Command)
	}
}

func errResp(msg string) api.Response {
	return api.Response{Status: api.StatusError, Message: msg}
}

// activate resolves NAMES from the wire to a stored node (the wire never
// carries paths or config — same trust boundary as the Rust daemon), compiles
// the node to native core configs, and starts the Session, REPLACING any
// running one.
func (m *manager) activate(req api.Request) api.Response {
	profileName := ""
	if req.Profile != nil {
		profileName = *req.Profile
	}
	if profileName == "" {
		name, err := m.store.ActiveProfileName()
		if err != nil {
			return errResp(err.Error())
		}
		if name == "" {
			return errResp("no profile named and no active profile set")
		}
		profileName = name
	}

	prof, err := m.store.LoadProfile(profileName)
	if err != nil {
		return errResp(err.Error())
	}

	var node *store.Node
	if req.Node != nil && *req.Node != "" {
		if node = prof.FindNodeByName(*req.Node); node == nil {
			return errResp(fmt.Sprintf("node %q not found in profile %q", *req.Node, profileName))
		}
	} else if node = prof.ActiveNode(); node == nil {
		return errResp(fmt.Sprintf("profile %q has no active node; name one in the request", profileName))
	}

	// Routing: an explicit name must exist; nil falls back to the profile's
	// active routing (nil = no routing, everything through the selector).
	var routingCfg *routing.Config
	if req.Routing != nil && *req.Routing != "" {
		if routingCfg = prof.FindRouting(*req.Routing); routingCfg == nil {
			return errResp(fmt.Sprintf("routing %q not found in profile %q", *req.Routing, profileName))
		}
	} else {
		routingCfg = prof.ActiveRouting()
	}

	plan, err := buildPlan(prof, node)
	if err != nil {
		return errResp(err.Error())
	}
	plan.Routing = routingCfg
	// Rule-set cache + selector persistence live next to the profiles (the
	// platform log writer forces the cache-file service; an explicit path
	// keeps it out of the daemon's cwd).
	plan.CachePath = filepath.Join(m.store.Dir(), "cache.db")

	settings, err := m.store.LoadSettings()
	if err != nil {
		return errResp(err.Error())
	}
	tunables := tunablesFromSettings(settings)
	plan.Tunables = &tunables
	socksPort := settings.SocksPort
	if m.socksPort != 0 {
		socksPort = m.socksPort
	}

	tun := req.Tun != nil && *req.Tun
	var sbCfg, xrayCfg []byte
	if tun {
		// Fail fast with a clear message if we can't open a TUN (not elevated)
		// instead of surfacing a buried access-denied from sing-box.
		if err := engine.EnsureTUNPrivilege(); err != nil {
			return errResp(err.Error())
		}
		sbCfg, xrayCfg, err = engine.PlanTUNConfigs(plan, tunInterfaceName())
	} else {
		sbCfg, xrayCfg, err = engine.PlanSocksConfigs(plan, "127.0.0.1", socksPort)
	}
	if err != nil {
		return errResp(err.Error())
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Replace semantics: a running session is stopped first, so Activate is
	// also "switch profile/node/mode".
	if m.sess != nil {
		m.stopTrafficLocked()
		if err := m.sess.Close(); err != nil {
			m.sess = nil
			m.active = nil
			return errResp("stopping the previous session: " + err.Error())
		}
		m.sess = nil
		m.active = nil
	}

	sess, err := engine.StartWithOptions(sbCfg, xrayCfg, engine.StartOptions{LogSink: m.logSink})
	if err != nil {
		m.broadcastStateLocked()
		return errResp("start session: " + err.Error())
	}

	// A selector PERSISTS its live selection to cache.db and RESTORES it ahead
	// of the configured `default` on the next Start (sing-box
	// group.Selector.Start), so a re-activation — or a fresh start with a
	// populated cache — can silently come up live on a STALE cached node
	// instead of the one just activated (a native member left in the cache from
	// an earlier live switch is a member of the new plan, so its restore wins).
	// That makes the very first status readback report the wrong live node,
	// which the client cannot correct. Pin the selector to the activation's node
	// so the live node is ALWAYS the activation intent. A no-op when the cache
	// already agrees (early return, no connection interrupt) and harmless for a
	// selector-less single-node session (the error is ignored).
	_ = sess.SelectOutbound(plan.ActiveTag)

	m.sess = sess
	m.plan = plan
	m.activations++
	m.startedAt = time.Now()
	nodeName := node.DisplayName()
	m.active = &api.PersistedEntry{
		Profile: &profileName,
		Node:    &nodeName,
		Tun:     tun,
	}
	// Persist the CANONICAL routing name (the request may have passed an id,
	// or nil resolved to the active routing).
	if routingCfg != nil {
		m.active.Routing = &routingCfg.Name
	}
	m.saveLastSessionLocked()
	m.startTrafficEmitterLocked()
	m.broadcastStateLocked()
	return api.Response{Status: api.StatusActivated, Entries: m.entriesLocked()}
}

// startTrafficEmitterLocked spawns the per-second Traffic{up,down} event
// emitter for the session just started (the in-process replacement of
// the Clash /traffic WS). Deltas, not totals; emitted only while someone
// subscribes. Callers hold mu and a non-nil sess.
func (m *manager) startTrafficEmitterLocked() {
	stop := make(chan struct{})
	m.stopTraffic = stop
	sess := m.sess
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		var lastUp, lastDown int64
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				up, down := sess.Traffic()
				deltaUp, deltaDown := uint64(up-lastUp), uint64(down-lastDown)
				lastUp, lastDown = up, down
				if m.hub != nil && m.hub.SubscriberCount() > 0 {
					m.hub.Broadcast(api.Event{Event: api.EventTraffic, Up: &deltaUp, Down: &deltaDown})
				}
			}
		}
	}()
}

// stopTrafficLocked ends the emitter of the session being torn down. Callers
// hold mu.
func (m *manager) stopTrafficLocked() {
	if m.stopTraffic != nil {
		close(m.stopTraffic)
		m.stopTraffic = nil
	}
}

// saveLastSessionLocked persists the activation INTENT for restore-on-start.
// Best-effort: a write failure must not fail the activation that already
// succeeded. Callers hold mu and a non-nil active.
func (m *manager) saveLastSessionLocked() {
	if err := m.store.SaveLastSession(*m.active); err != nil {
		fmt.Fprintln(os.Stderr, "iron-link-daemon: persist last session:", err)
	}
}

// buildPlan runs capability selection over EVERY node of the profile
// (tunEngine = sing-box, the per-node core_override honored): sing-box-eligible
// nodes embed as native selector members; the ACTIVE node, when selection
// sends it to xray, becomes the plan's one xray-routed member. Other
// xray-routed nodes stay out (v1: switching to them re-activates). A non-active
// node that no core can dial is skipped, not fatal; the ACTIVE node failing
// selection fails the activation.
func buildPlan(prof *store.Profile, active *store.Node) (engine.SessionPlan, error) {
	var plan engine.SessionPlan
	// Selector member tags are the stable node UUIDs (DisplayName may collide),
	// so the active default is addressed by id too.
	plan.ActiveTag = active.ID

	for i := range prof.Nodes {
		n := &prof.Nodes[i]
		core, err := proxy.SelectCore(n.Profile(), api.CoreSingBox, n.Preferences.CoreOverride)
		isActive := n.ID == active.ID
		if err != nil {
			if isActive {
				return plan, err
			}
			continue
		}
		named := engine.NamedNode{ID: n.ID, Name: n.DisplayName(), Profile: n.Profile()}
		switch {
		case core == api.CoreSingBox:
			plan.Natives = append(plan.Natives, named)
		case isActive: // xray-routed AND active
			plan.XrayNode = &named
		}
	}
	return plan, nil
}

// stop closes the running session. Role-scoped stop died with the pid model:
// the embedded cores live and die as ONE session (closing only xray would
// sever the bridge), so a role argument is rejected rather than half-honored.
func (m *manager) stop(req api.Request) api.Response {
	if req.Role != nil {
		return errResp("role-scoped stop is not supported in the embedded model; stop the whole session")
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sess == nil {
		return api.Response{Status: api.StatusIdle, Message: "nothing is running"}
	}
	m.stopTrafficLocked()
	err := m.sess.Close()
	m.sess = nil
	m.plan = engine.SessionPlan{}
	m.active = nil
	// An EXPLICIT stop is user intent — the next daemon start must not
	// restore (unlike shutdown, which keeps the record).
	if err := m.store.ClearLastSession(); err != nil {
		fmt.Fprintln(os.Stderr, "iron-link-daemon: clear last session:", err)
	}
	m.broadcastStateLocked()
	if err != nil {
		return errResp("stop session: " + err.Error())
	}
	return api.Response{Status: api.StatusStopped}
}

// shutdown closes a running session WITHOUT clearing the last-session record:
// a daemon restart restores it (the Rust restore semantics). Called on
// process exit only.
func (m *manager) shutdown() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sess != nil {
		m.stopTrafficLocked()
		m.sess.Close()
		m.sess = nil
	}
}

// testLatency probes nodes IN-PROCESS (the Observatory generalization,
// no /debug/vars port): an ephemeral xray instance per node dials the 204
// endpoint via core.Dial and times the response. The live activation's
// profile wins; otherwise the store's active profile. Explicit names resolve
// to nodes (unknown names are skipped); nil means every node. Probes run
// concurrently; a failed/timed-out probe yields a nil latency, never an
// error for the whole request.
func (m *manager) testLatency(req api.Request) api.Response {
	m.mu.Lock()
	profileName := ""
	tunActive := false
	if m.active != nil && m.active.Profile != nil {
		profileName = *m.active.Profile
		tunActive = m.active.Tun // a live auto_redirect TUN → the probe must fwmark-exempt its dials
	}
	m.mu.Unlock()
	if profileName == "" {
		name, err := m.store.ActiveProfileName()
		if err != nil {
			return errResp(err.Error())
		}
		if name == "" {
			return errResp("no active profile to ping")
		}
		profileName = name
	}
	prof, err := m.store.LoadProfile(profileName)
	if err != nil {
		return errResp(err.Error())
	}

	var nodes []*store.Node
	if req.Nodes != nil {
		for _, name := range *req.Nodes {
			if n := prof.FindNodeByName(name); n != nil {
				nodes = append(nodes, n)
			}
		}
	} else {
		for i := range prof.Nodes {
			nodes = append(nodes, &prof.Nodes[i])
		}
	}
	if len(nodes) == 0 {
		return errResp("no nodes to ping")
	}

	probeURL, probeBudget := m.probeParams()
	ctx, cancel := context.WithTimeout(context.Background(), probeBudget)
	defer cancel()

	results := make([]api.LatencyResult, len(nodes))
	sem := make(chan struct{}, latencyProbeConcurrency) // each probe spins an ephemeral core
	var wg sync.WaitGroup
	for i, n := range nodes {
		results[i] = api.LatencyResult{Node: n.DisplayName()}
		wg.Add(1)
		go func(i int, n *store.Node) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			d, err := probeNodeLatency(ctx, n, fmt.Sprintf("%d-%s", i, n.ID), probeURL, tunActive)
			if err != nil {
				return // unreachable probe: latency stays nil
			}
			ms := min(d.Milliseconds(), 65535)
			latency := uint16(ms)
			results[i].LatencyMs = &latency
		}(i, n)
	}
	wg.Wait()
	return api.Response{Status: api.StatusLatencies, Latencies: results}
}

// latencyProbeConcurrency caps how many ephemeral probe cores run at once: each
// node's probe spins a full in-process core (a sing-box box or an xray
// instance), so an unbounded sweep over a large profile would thrash. 8 keeps
// the sweep quick without a fork bomb.
const latencyProbeConcurrency = 8

// probeNodeLatency measures one node's latency through the SAME core the live
// session would dial it with — the universal probe: the ephemeral sing-box
// probe for native nodes (incl. anytls + the QUIC family, which xray cannot
// dial), the ephemeral xray probe for the xhttp nodes xray is the specialist
// for. tunExempt stamps the own-traffic fwmark on the sing-box probe when a live
// TUN is up (see engine.ProbeLatencySingBox).
func probeNodeLatency(ctx context.Context, n *store.Node, stubSuffix, url string, tunExempt bool) (time.Duration, error) {
	core, err := proxy.SelectCore(n.Profile(), api.CoreSingBox, n.Preferences.CoreOverride)
	if err != nil {
		return 0, err
	}
	if core == api.CoreXray {
		return engine.ProbeLatency(ctx, n.Profile(), stubSuffix, url)
	}
	return engine.ProbeLatencySingBox(ctx, n.Profile(), url, tunExempt)
}

// diagnose runs the staged diagnosis (G5) for ONE node — the named node, or
// the resolved profile's active node when none is named. Heavier than
// test_latency (it stages tcp → proxy), so it is single-node by design.
func (m *manager) diagnose(req api.Request) api.Response {
	m.mu.Lock()
	profileName := ""
	tunActive := false
	if m.active != nil && m.active.Profile != nil {
		profileName = *m.active.Profile
		tunActive = m.active.Tun // a live auto_redirect TUN → fwmark-exempt the sing-box proxy stage
	}
	m.mu.Unlock()
	if profileName == "" {
		name, err := m.store.ActiveProfileName()
		if err != nil {
			return errResp(err.Error())
		}
		if name == "" {
			return errResp("no active profile to diagnose")
		}
		profileName = name
	}
	prof, err := m.store.LoadProfile(profileName)
	if err != nil {
		return errResp(err.Error())
	}

	var node *store.Node
	if req.Node != nil && *req.Node != "" {
		if node = prof.FindNodeByName(*req.Node); node == nil {
			return errResp(fmt.Sprintf("node %q not found in profile %q", *req.Node, profileName))
		}
	} else if node = prof.ActiveNode(); node == nil {
		return errResp("no node named and the profile has no active node")
	}

	core, err := proxy.SelectCore(node.Profile(), api.CoreSingBox, node.Preferences.CoreOverride)
	if err != nil {
		return errResp(err.Error())
	}
	probeURL, probeBudget := m.probeParams()
	ctx, cancel := context.WithTimeout(context.Background(), probeBudget+tcpDiagnoseSlack)
	defer cancel()
	d := engine.DiagnoseNode(ctx, node.Profile(), core, node.ID, probeURL, tunActive)

	info := &api.DiagnosisInfo{Node: node.DisplayName(), OK: d.OK, FailedStage: string(d.FailedStage)}
	for _, s := range d.Stages {
		info.Stages = append(info.Stages, api.StageInfo{
			Stage: string(s.Stage), OK: s.OK, DurationMs: s.DurationMs, Error: s.Err,
		})
	}
	return api.Response{Status: api.StatusDiagnosis, Diagnosis: info}
}

// trafficApps reports the running session's CUMULATIVE per-app byte counts
// (the "by app" panel polls it). Counters are session-scoped — a new session
// is a new TrafficCounter, so the totals reset implicitly. No session = an
// empty list, not an error.
func (m *manager) trafficApps(req api.Request) api.Response {
	m.mu.Lock()
	defer m.mu.Unlock()
	resp := api.Response{Status: api.StatusAppTraffic, Apps: []api.AppTraffic{}}
	if m.sess == nil {
		return resp
	}
	for _, a := range m.sess.TrafficApps() {
		var byRoute map[string]api.RouteBytes
		if len(a.ByRoute) > 0 {
			byRoute = make(map[string]api.RouteBytes, len(a.ByRoute))
			for outcome, rb := range a.ByRoute {
				byRoute[outcome] = api.RouteBytes{Up: uint64(rb.Up), Down: uint64(rb.Down)}
			}
		}
		resp.Apps = append(resp.Apps, api.AppTraffic{
			Path:    a.Path,
			Up:      uint64(a.Up),
			Down:    uint64(a.Down),
			ByRoute: byRoute,
		})
	}
	return resp
}

// tcpDiagnoseSlack extends the diagnosis deadline beyond the proxy-stage
// budget to leave room for the tcp stage that runs first.
const tcpDiagnoseSlack = 6 * time.Second

// restoreLastSession re-issues the persisted Activate context (names + tun
// flag — the daemon re-plans from the store, nothing else is trusted).
// Best-effort: a missing record or a failed activation leaves the daemon
// idle.
func (m *manager) restoreLastSession() {
	if s, err := m.store.LoadSettings(); err == nil && !s.RestoreOnStart {
		fmt.Fprintln(os.Stderr, "iron-link-daemon: session restore disabled by settings")
		return
	}
	context, err := m.store.LoadLastSession()
	if err != nil {
		fmt.Fprintln(os.Stderr, "iron-link-daemon: load last session:", err)
		return
	}
	if context == nil {
		return
	}
	resp := m.activate(api.Request{
		Command: api.CmdActivate,
		Profile: context.Profile,
		Node:    context.Node,
		Routing: context.Routing,
		Tun:     &context.Tun,
	})
	if resp.Status != api.StatusActivated {
		fmt.Fprintln(os.Stderr, "iron-link-daemon: session restore failed:", resp.Message)
		return
	}
	fmt.Fprintln(os.Stderr, "iron-link-daemon: previous session restored")
}

func (m *manager) status() api.Response {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sess == nil {
		return api.Response{Status: api.StatusIdle}
	}
	resp := api.Response{
		Status:  api.StatusRunning,
		Entries: m.entriesLocked(),
		Active:  m.active,
	}
	// The live node comes from the IN-PROCESS selector (no Clash
	// API); single-node sessions have no selector, so the activated node is
	// the live node by construction. The selector tag is the node UUID; map it
	// BACK to the client-facing display name so the wire behaves as before.
	if live, ok := m.sess.SelectedOutbound(); ok {
		if name, ok := m.plan.DisplayNameForTag(live); ok {
			resp.ActiveNodeLive = &name
		} else {
			resp.ActiveNodeLive = &live
		}
	} else if m.active != nil {
		resp.ActiveNodeLive = m.active.Node
	}
	return resp
}

// switchNode moves traffic to the named node: LIVE via the in-process
// selector when the node is an embedded member, otherwise a re-activation
// with the stored context (xray-routed non-active nodes — v1 keeps one xray
// instance per active xray node).
func (m *manager) switchNode(req api.Request) api.Response {
	if req.Node == nil || *req.Node == "" {
		return errResp("switch_node requires a node name")
	}
	target := *req.Node

	m.mu.Lock()
	if m.sess == nil {
		m.mu.Unlock()
		return errResp("nothing is running; use activate")
	}

	// The wire addresses nodes by display name; resolve it to the member's UUID
	// tag (the selector's internal identifier) before the live select.
	if tag, ok := m.plan.MemberTagForName(target); ok {
		if err := m.sess.SelectOutbound(tag); err != nil {
			m.mu.Unlock()
			return errResp("live select: " + err.Error())
		}
		// The switch is part of the activation intent: a restore must come
		// back to the node the user switched to.
		m.active.Node = &target
		m.saveLastSessionLocked()
		m.broadcastStateLocked()
		m.mu.Unlock()
		return api.Response{Status: api.StatusSwitched, Node: target}
	}

	activateReq := api.Request{
		Command: api.CmdActivate,
		Profile: m.active.Profile,
		Node:    req.Node,
		Routing: m.active.Routing,
		Tun:     &m.active.Tun,
	}
	m.mu.Unlock()

	resp := m.activate(activateReq)
	if resp.Status != api.StatusActivated {
		return resp
	}
	return api.Response{Status: api.StatusSwitched, Node: target}
}

// Snapshot is the hub's first-frame source: the current session state.
func (m *manager) Snapshot() api.Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stateEventLocked()
}

func (m *manager) stateEventLocked() api.Event {
	ev := api.Event{Event: api.EventState, Active: m.active}
	if m.sess != nil {
		ev.Entries = m.entriesLocked()
	}
	return ev
}

// broadcastStateLocked pushes the new State to every subscriber after a
// transition. Callers hold mu.
func (m *manager) broadcastStateLocked() {
	if m.hub != nil {
		m.hub.Broadcast(m.stateEventLocked())
	}
}

// entriesLocked describes the embedded cores: sing-box is the dispatcher
// (role Tun), xray the proxy. One Session, one uptime. Callers hold mu and a
// non-nil sess.
func (m *manager) entriesLocked() []api.CoreEntry {
	uptime := uint64(time.Since(m.startedAt).Seconds())
	return []api.CoreEntry{
		{Role: api.RoleTun, State: api.StateRunning, UptimeSecs: uptime},
		{Role: api.RoleProxy, State: api.StateRunning, UptimeSecs: uptime},
	}
}
