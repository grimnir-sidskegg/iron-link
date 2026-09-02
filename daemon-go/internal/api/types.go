// Package api defines the iron-link control-plane wire types: the framed-JSON
// request / response / event protocol spoken between the Go daemon and the
// Flutter client. It is the canonical (emitter) side of the wire and MUST stay
// byte-compatible with the Dart mirror (client-flutter/lib/src/wire/) — a
// shared JSON fixture test enforces this.
//
// stdlib-only by design (encoding/json): the contract pulls in no core deps, so
// the client speaks it without the sing-box/xray module graph.
package api

// CoreRole is the part a core plays in the active session. JSON: bare PascalCase
// strings ("Proxy"/"Tun"/"Dpi").
//
//	Proxy = the SOCKS/HTTP outbound core (xray)
//	Tun   = the privileged tunnel / dispatcher core (sing-box)
//	Dpi   = the local DPI-bypass SOCKS outbound (byedpi) the proxy/TUN core
//	        chains to
type CoreRole string

const (
	RoleProxy CoreRole = "Proxy"
	RoleTun   CoreRole = "Tun"
	RoleDpi   CoreRole = "Dpi"
)

// CoreType is which proxy core a node runs with. JSON: "Xray"/"SingBox".
type CoreType string

const (
	CoreXray    CoreType = "Xray"
	CoreSingBox CoreType = "SingBox"
)

// CoreState is whether an embedded core is currently running.
type CoreState string

const (
	StateRunning CoreState = "running"
	StateStopped CoreState = "stopped"
)

// CoreEntry describes one embedded core in the session.
//
// The cores are in-process (one daemon pid), so there is no per-core pid —
// identity is the logical Role + State.
type CoreEntry struct {
	Role       CoreRole  `json:"role"`
	State      CoreState `json:"state"`
	UptimeSecs uint64    `json:"uptime_secs"`
}

// PersistedEntry is the activation INTENT — the high-level NAMES + the tun flag,
// never paths/argv. A poisoned record can at worst name a different
// profile/node; the daemon re-plans on restore.
//
// Profile/Node/Routing are nullable and serialize as JSON null when absent
// (no omitempty — the field is always present). Tun is always a bool.
type PersistedEntry struct {
	Profile *string `json:"profile"`
	Node    *string `json:"node"`
	Routing *string `json:"routing"`
	Tun     bool    `json:"tun"`
}
