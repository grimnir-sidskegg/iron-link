package api

import "encoding/json"

// The control protocol. Every message is a 4-byte big-endian length prefix
// followed by a JSON body (see internal/ipc). Three message kinds, each
// internally tagged:
//
//	Request  {"command": …}   client -> daemon, one reply expected
//	Response {"status":  …}   daemon -> client, the reply
//	Event    {"event":   …}   daemon -> client, many per Subscribe connection
//
// Each kind is modelled as a FLAT struct that unions all its variants' fields,
// tagged by the discriminator. A flat struct (rather than a Go interface)
// round-trips cleanly with the Rust internally-tagged enums: absent fields are
// nil / omitempty and the handler switches on the tag. Adding a variant is
// additive.

// ---- Request ---------------------------------------------------------------

// Command tags.
const (
	CmdActivate    = "activate"
	CmdStop        = "stop"
	CmdStatus      = "status"
	CmdSwitchNode  = "switch_node"
	CmdTestLatency = "test_latency"
	CmdSubscribe   = "subscribe"

	// The store verbs (G3) — the daemon is the source of truth; front-ends
	// never touch the files. Where a verb takes `profile`, nil means the
	// store's active profile.
	CmdListProfiles     = "list_profiles"
	CmdCreateProfile    = "create_profile"
	CmdDeleteProfile    = "delete_profile"
	CmdSetActiveProfile = "set_active_profile"
	CmdListNodes        = "list_nodes"
	CmdSelectNode       = "select_node"
	CmdAddNode          = "add_node"
	CmdRemoveNode       = "remove_node"
	CmdSetNodePrefs     = "set_node_prefs"
	CmdUpsertGroup      = "upsert_group"
	CmdGetGroup         = "get_group"
	CmdGetNode          = "get_node"
	CmdListSubs         = "list_subscriptions"
	CmdAddSub           = "add_subscription"
	CmdRefreshSubs      = "refresh_subscriptions"
	CmdRemoveSub        = "remove_subscription"
	CmdUpdateSub        = "update_subscription"
	CmdListRouting      = "list_routing"
	CmdSelectRouting    = "select_routing"
	CmdGetRouting       = "get_routing"
	CmdUpsertRouting    = "upsert_routing"
	CmdRemoveRouting    = "remove_routing"
	// CmdRoutingSchema returns the routing-rule condition-field schema,
	// computed from the daemon's pinned sing-box and ITS platform — clients
	// render routing forms from this data instead of carrying typed models.
	CmdRoutingSchema = "routing_schema"

	// CmdDiagnose runs the staged diagnosis (G5) for one node.
	CmdDiagnose = "diagnose"

	// CmdTrafficApps polls the session's cumulative per-app byte counts (the
	// "by app" panel). Empty list when no session runs; the client derives
	// rates from the cumulative deltas.
	CmdTrafficApps = "traffic_apps"

	// CmdDoctor runs the connection doctor — a battery of read-only network
	// checks (the "Doctor" tab). Each check is one row; the reply is the
	// ordered list (connectivity + resolvers + the selected node's camouflage).
	CmdDoctor = "doctor"
	// CmdDoctorNodes probes the camouflage of EVERY node in the active profile
	// (the tab's "probe all nodes" button) — one camouflage check per node,
	// same doctor_report reply shape.
	CmdDoctorNodes = "doctor_nodes"
	// CmdNetworkReport runs the heavier "Network report" characterization
	// (provider reachability + DNS integrity + transport reachability) — each
	// result a DoctorCheck row, same doctor_report reply shape.
	CmdNetworkReport = "network_report"
	// CmdForwardingCheck runs the Linux-only host-firewall ↔ forwarded-traffic
	// check (docker0 / br-* bridges vs a default-deny INPUT firewall) — its own
	// button/section; same doctor_report reply shape (one row; empty off Linux).
	CmdForwardingCheck = "forwarding_check"

	// The daemon-global settings document (settings.go). get returns the
	// effective document (defaults when no file exists); set validates,
	// persists, and reports whether a running session needs re-activation
	// to pick the change up.
	CmdGetSettings = "get_settings"
	CmdSetSettings = "set_settings"
)

// Request is the flat union of all control verbs, tagged by Command.
type Request struct {
	Command string `json:"command"`

	// activate + every store verb: the profile NAME (nil = active profile;
	// REQUIRED for create/delete/set_active_profile)
	Profile *string `json:"profile,omitempty"`
	// activate (nil = the profile's active routing) AND
	// select_routing / remove_routing (required): routing name or id
	Routing *string `json:"routing,omitempty"`
	Tun     *bool   `json:"tun,omitempty"`
	// upsert_routing: the full routing config JSON (the store shape —
	// rule_sets / rules / default_target; a missing id gets a fresh one,
	// an existing id replaces that config in place)
	RoutingConfig json.RawMessage `json:"routing_config,omitempty"`

	// activate (optional), switch_node / select_node / remove_node /
	// set_node_prefs / get_group (required): the node NAME (or id for the store
	// verbs; get_group's ref must resolve to a group)
	Node *string `json:"node,omitempty"`

	// stop (optional): which role to stop; nil = stop all
	Role *CoreRole `json:"role,omitempty"`

	// test_latency (optional): node NAMES; nil = all current selector members
	Nodes *[]string `json:"nodes,omitempty"`

	// add_node / add_subscription: the share link / subscription URL
	URL *string `json:"url,omitempty"`
	// add_subscription: the display name (nil = derived from the URL host)
	Name *string `json:"name,omitempty"`
	// add_subscription: skip TLS verification for this source (default false)
	AllowInvalidCerts *bool `json:"allow_invalid_certs,omitempty"`
	// add_subscription / update_subscription (optional): the body dialect —
	// "auto" (default: detect), "links", "xray", "sing-box", "clash",
	// "sip008". Auto fits nearly every provider; an explicit value is the
	// escape hatch for a body the detection misreads.
	Format *string `json:"format,omitempty"`
	// refresh_subscriptions (optional; nil = all enabled),
	// remove_subscription (required) AND update_subscription (required):
	// subscription id or name
	Sub *string `json:"sub,omitempty"`
	// update_subscription (all optional — a nil field is left unchanged): the
	// new enabled state and auto-refresh interval in seconds. The editable
	// text fields reuse Name / URL / AllowInvalidCerts above.
	Enabled           *bool   `json:"enabled,omitempty"`
	UpdateIntervalSec *uint32 `json:"update_interval_sec,omitempty"`
	// set_node_prefs: the core pin; nil/absent CLEARS the override
	CoreOverride *CoreType `json:"core_override,omitempty"`

	// set_settings: the FULL settings document (no patch semantics — the
	// client edits what get_settings returned and sends it back)
	Settings *Settings `json:"settings,omitempty"`

	// upsert_group: the user-group spec JSON — {id?, name, members?/all_of_sub?,
	// probe?}. A missing/empty id creates a new user group; an existing user
	// group id replaces that group's spec in place. Removal reuses remove_node.
	Group json.RawMessage `json:"group,omitempty"`
}

// ---- Response --------------------------------------------------------------

// Status tags.
const (
	StatusStarted   = "started"
	StatusStopped   = "stopped"
	StatusRunning   = "running"
	StatusActivated = "activated"
	StatusIdle      = "idle"
	StatusSwitched  = "switched"
	StatusLatencies = "latencies"
	StatusOk        = "ok"
	StatusError     = "error"

	// G3 store-verb replies.
	StatusProfiles      = "profiles"
	StatusNodes         = "nodes"
	StatusSubscriptions = "subscriptions"
	StatusRefreshed     = "refreshed"
	StatusRouting       = "routing"
	StatusRoutingConfig = "routing_config"
	StatusGroupConfig   = "group_config"
	StatusNodeConfig    = "node_config"
	StatusRoutingSchema = "routing_schema"
	StatusDiagnosis     = "diagnosis"
	StatusSettings      = "settings"
	StatusAppTraffic    = "app_traffic"
	StatusDoctorReport  = "doctor_report"
)

// NodeKindGroup is the NodeInfo.Kind value for a member group (an "Auto" node);
// a dialable node leaves Kind "".
const NodeKindGroup = "group"

// ConditionSpec describes one routing-rule condition field. Kind is a closed
// enum of input shapes: "strings" (JSON array of strings), "ports" (array of
// 1..65535 ints), "numbers" (array of ints), "bool", "string" (single
// string), "number" (single int). Supported reports whether the condition can
// match on the DAEMON's platform.
type ConditionSpec struct {
	Key       string `json:"key"`
	Kind      string `json:"kind"`
	Hint      string `json:"hint"`
	Supported bool   `json:"supported"`
}

// RoutingSchema is the routing_schema reply: the condition table, the valid
// rule targets, and the rule-set formats — everything a client needs to
// render a routing form.
type RoutingSchema struct {
	Conditions     []ConditionSpec `json:"conditions"`
	Targets        []string        `json:"targets"`
	RuleSetFormats []string        `json:"rule_set_formats"`
}

// RoutingInfo is one routing config, as listed by `list_routing`.
type RoutingInfo struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Rules    int    `json:"rules"`
	RuleSets int    `json:"rule_sets"`
	Active   bool   `json:"active"`
}

// StageInfo is one diagnosis stage's outcome (G5).
type StageInfo struct {
	Stage      string `json:"stage"`
	OK         bool   `json:"ok"`
	DurationMs int64  `json:"duration_ms"`
	Error      string `json:"error,omitempty"`
}

// DiagnosisInfo is the staged-diagnosis verdict for one node: the ordered
// stage results, whether ALL passed, and the first failing stage ("" when
// OK).
type DiagnosisInfo struct {
	Node        string      `json:"node"`
	OK          bool        `json:"ok"`
	FailedStage string      `json:"failed_stage,omitempty"`
	Stages      []StageInfo `json:"stages"`
}

// AppTraffic is one app's CUMULATIVE routed bytes since session start, keyed
// by the owning process's executable path (an empty Path is the unattributed
// bucket). Up/Down are the totals; ByRoute splits them per route outcome
// ("direct"/"proxy"/"blocked"/"dpi") — only outcomes with nonzero bytes appear
// (so "dpi" stays absent until byedpi exists), and Up/Down == the sum over
// ByRoute. The client derives per-app rates from the cumulative deltas.
type AppTraffic struct {
	Path    string                `json:"path"`
	Up      uint64                `json:"up"`
	Down    uint64                `json:"down"`
	ByRoute map[string]RouteBytes `json:"by_route,omitempty"`
}

// RouteBytes is one route outcome's cumulative up/down bytes within an
// AppTraffic.
type RouteBytes struct {
	Up   uint64 `json:"up"`
	Down uint64 `json:"down"`
}

// DoctorCheck is one connection-doctor check's outcome — one row on the Doctor
// tab. Status is a closed enum: "ok" (healthy), "warn" (works but a setting is
// suboptimal — see Remedy), "fail" (broken). Summary is the one-line verdict;
// Details are the supporting facts (e.g. per-family reachability). Remedy, when
// present, is a setting change the client offers to apply.
type DoctorCheck struct {
	ID      string        `json:"id"`
	Title   string        `json:"title"`
	Status  string        `json:"status"`
	Summary string        `json:"summary"`
	Details []string      `json:"details,omitempty"`
	Remedy  *DoctorRemedy `json:"remedy,omitempty"`
}

// DoctorRemedy is a suggested settings change for a warn/fail check — the
// client applies whichever fields are set via set_settings. A given remedy
// sets ONE lever; the union grows additively as checks are added.
type DoctorRemedy struct {
	Summary string `json:"summary"`
	// DNSStrategy, when non-empty, is the value to write to settings.dns.strategy
	// ("prefer_ipv4" / "prefer_ipv6" / "ipv4_only" / "ipv6_only").
	DNSStrategy string `json:"dns_strategy,omitempty"`
	// DNSServers, when non-nil, REPLACES settings.dns.servers (the resolver
	// check's "switch upstream" fix — a full intended list, already reordered).
	DNSServers []DNSServer `json:"dns_servers,omitempty"`
	// DNSViaTunnel, when non-nil, sets settings.dns.via_tunnel (the resolver
	// check's "route DNS through the tunnel" fix when nothing direct answers).
	DNSViaTunnel *bool `json:"dns_via_tunnel,omitempty"`
}

// NodeInfo is one stored node, as listed by `list_nodes`.
type NodeInfo struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	SubID        *string   `json:"sub_id,omitempty"`
	CoreOverride *CoreType `json:"core_override,omitempty"`
	// Protocol is the node's protocol kind ("vless"/"shadowsocks"/"vmess"/…) —
	// the primary badge.
	Protocol string `json:"protocol"`
	// Transport / Security are the variant kinds ("tcp"/"ws"/"grpc"/"xhttp",
	// "none"/"tls"/"reality") — enough for a front-end to render badges. Either
	// may be "" for a protocol that has no such dimension (e.g. hysteria2).
	Transport string `json:"transport"`
	Security  string `json:"security"`
	Active    bool   `json:"active"`
	// EligibleCores is which installed cores can dial this node, in priority
	// order — the daemon-computed capability verdict, so clients never carry
	// capability tables of their own.
	EligibleCores []CoreType `json:"eligible_cores"`
	// Kind discriminates the row: "" (absent) = a dialable node, "group"
	// (NodeKindGroup) = a member group (an "Auto" node). A group row leaves
	// Protocol/Transport/Security "" and EligibleCores empty.
	Kind string `json:"kind,omitempty"`
	// Members is the RESOLVED member node ids of a group row (AllOfSub already
	// expanded by the daemon); absent for a dialable node.
	Members []string `json:"members,omitempty"`
}

// SubscriptionInfo is one subscription, as listed by `list_subscriptions`.
type SubscriptionInfo struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	URL               string `json:"url"`
	Enabled           bool   `json:"enabled"`
	AllowInvalidCerts bool   `json:"allow_invalid_certs"`
	// LastUpdated is RFC 3339.
	LastUpdated string `json:"last_updated"`
	NodeCount   int    `json:"node_count"`
	// Format is the stored body dialect ("auto" unless the user pinned one).
	Format string `json:"format"`
}

// RefreshInfo is one subscription's refresh outcome.
type RefreshInfo struct {
	Name    string `json:"name"`
	Count   int    `json:"count"`
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
	Skipped bool   `json:"skipped,omitempty"`
	Error   string `json:"error,omitempty"`
	// The parse accounting of a successful refresh: the dialect that matched,
	// how many entries the document held, how many parsed links were dropped
	// as duplicates, and how many entries no parser understood. All zero on
	// skip/error.
	Format       string `json:"format,omitempty"`
	Entries      int    `json:"entries,omitempty"`
	Duplicates   int    `json:"duplicates,omitempty"`
	Unrecognized int    `json:"unrecognized,omitempty"`
}

// LatencyResult is one node's probe outcome.
//
// Intentional API cleanup vs the Rust `(String, Option<u16>)` tuple: an object
// with named fields, not a 2-element array — both mirrors stay readable and the
// Rust side is updated to match at G1 step 5. LatencyMs == nil means a
// timed-out / unreachable probe (the request as a whole still succeeds).
type LatencyResult struct {
	Node      string  `json:"node"`
	LatencyMs *uint16 `json:"latency_ms"`
}

// Response is the flat union of all replies, tagged by Status.
type Response struct {
	Status string `json:"status"`

	// started / stopped / ok / error
	Message string `json:"message,omitempty"`
	// started / stopped: which core
	Role *CoreRole `json:"role,omitempty"`

	// running / activated
	Entries []CoreEntry `json:"entries,omitempty"`
	// running: the activation context + the live-selected node NAME (read from
	// the in-process selector, not a Clash API — GO_DAEMON_PLAN.md §6)
	Active         *PersistedEntry `json:"active,omitempty"`
	ActiveNodeLive *string         `json:"active_node_live,omitempty"`

	// switched: the node NAME now selected; ok (add_node): the added node
	Node string `json:"node,omitempty"`

	// latencies
	Latencies []LatencyResult `json:"latencies,omitempty"`

	// profiles
	Profiles      []string `json:"profiles,omitempty"`
	ActiveProfile *string  `json:"active_profile,omitempty"`
	// nodes
	Nodes []NodeInfo `json:"nodes,omitempty"`
	// subscriptions
	Subscriptions []SubscriptionInfo `json:"subscriptions,omitempty"`
	// refreshed (also the add_subscription reply: the initial fetch outcome)
	Refreshed []RefreshInfo `json:"refreshed,omitempty"`
	// routing
	Routing []RoutingInfo `json:"routing,omitempty"`
	// routing_config (get_routing): ONE config as its full JSON document —
	// the read half of upsert_routing's write
	RoutingConfig json.RawMessage `json:"routing_config,omitempty"`
	// group_config (get_group): ONE user group's stored spec as its full JSON
	// document ({name, members?/all_of_sub?, probe}) — the read half of
	// upsert_group, so an editor round-trips the true membership mode and probe
	// (neither of which the resolved list_nodes group row carries)
	GroupConfig json.RawMessage `json:"group_config,omitempty"`
	// node_config (get_node): ONE dialable node's full stored config for a
	// read-only inspector — {name, protocol, profile:{…every configured field…}}.
	// The list_nodes row carries only the badges (protocol/transport/security);
	// this exposes the endpoint (address/port), the security front (sni/pbk/…),
	// and the transport params that the row leaves out.
	NodeConfig json.RawMessage `json:"node_config,omitempty"`
	// routing_schema
	RoutingSchema *RoutingSchema `json:"routing_schema,omitempty"`
	// diagnosis
	Diagnosis *DiagnosisInfo `json:"diagnosis,omitempty"`

	// app_traffic (traffic_apps): cumulative per-app byte counts since session
	// start (empty when no session runs)
	Apps []AppTraffic `json:"apps,omitempty"`

	// doctor_report (doctor): the ordered connection-doctor checks
	Checks []DoctorCheck `json:"checks,omitempty"`

	// settings (get_settings / set_settings: the effective document)
	Settings *Settings `json:"settings,omitempty"`
	// set_settings: an engine-relevant field changed while a session runs —
	// the new value applies at the next activation
	NeedsReactivation bool `json:"needs_reactivation,omitempty"`
}

// ---- Event -----------------------------------------------------------------

// Event tags.
const (
	EventState               = "state"
	EventTraffic             = "traffic"
	EventLog                 = "log"
	EventSubscriptionUpdated = "subscription_updated"
	EventCoreError           = "core_error"
)

// Event is the flat union of all server-pushed events on a Subscribe
// connection, tagged by Event.
type Event struct {
	Event string `json:"event"`

	// state: a full session snapshot (first frame + on every transition)
	Entries []CoreEntry     `json:"entries,omitempty"`
	Active  *PersistedEntry `json:"active,omitempty"`
	// state: the in-process live node NAME (an urltest's current member pick
	// when an Auto group is active) — pushed so an auto-switch reaches clients
	// without waiting for the status poll. Absent on an idle state.
	ActiveNodeLive *string `json:"active_node_live,omitempty"`

	// traffic: bytes in the LAST SECOND (not cumulative)
	Up   *uint64 `json:"up,omitempty"`
	Down *uint64 `json:"down,omitempty"`

	// log
	Level string `json:"level,omitempty"`

	// log / core_error share Message
	Message string `json:"message,omitempty"`

	// core_error: typed per-stage failure attribution (foundation for G5)
	Role  *CoreRole `json:"role,omitempty"`
	Stage string    `json:"stage,omitempty"`

	// subscription_updated. The full parse accounting (entries/duplicates/
	// unrecognized) travels on the refresh RESPONSE (RefreshInfo) — the event
	// carries just the dialect that matched ("entries" is taken by the state
	// union above).
	SubID   string `json:"sub_id,omitempty"`
	Added   int    `json:"added,omitempty"`
	Removed int    `json:"removed,omitempty"`
	Total   int    `json:"total,omitempty"`
	Format  string `json:"format,omitempty"`
}
