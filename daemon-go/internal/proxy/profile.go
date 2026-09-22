// The protocol-agnostic node model: the Profile interface every proxy protocol
// implements, and the registry that wires each protocol's share-link parser and
// store decoder into ParseURL / the store's tagged-union loader.
//
// Adding a protocol is purely additive — a new file `proxy/<name>.go` that
// implements Profile and registers itself from an init(); nothing here (or in
// the store/engine/subscription seams) changes. vless.go is the worked example.

package proxy

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"ironlink/daemon/internal/api"
)

// Protocol is the application protocol kind of a node. Each protocol declares
// its own constant in its own file (additive — no shared block to edit).
type Protocol string

const ProtocolVless Protocol = "vless"

// Profile is one proxy endpoint the app can dial. VLESS was the first; every
// other protocol (shadowsocks/vmess/trojan/hysteria2/tuic/…) is a peer
// implementation registered through registerProtocol.
//
// The interface carries everything the daemon needs polymorphically: identity
// for the subscription refresh diff, UI badge labels, the doctor's camouflage
// front, and the two core-config emitters. The own-traffic plumbing (the
// auto_redirect fwmark for native sing-box; the sockopt mark for xray) is added
// by the engine, NOT here — emitters return the pure protocol-shaped object.
type Profile interface {
	// DisplayName is the human-facing node name (the share-link fragment).
	DisplayName() string
	// Kind is the protocol tag — the store union key and the NodeInfo badge.
	Kind() Protocol
	// ServerAddress / ServerPort are the node's endpoint (for the latency
	// probe, the off-Linux route_exclude, and the camouflage dial).
	ServerAddress() string
	ServerPort() uint16
	// Identity distinguishes two nodes at the same server:port across a
	// subscription refresh (uuid / password). It keys the refresh diff and the
	// pref-carry — never logged, never compared for authentication.
	Identity() string
	// TransportLabel / SecurityLabel are short UI badge strings, "" when the
	// protocol has no such dimension (e.g. hysteria2 has no stream transport).
	TransportLabel() string
	SecurityLabel() string
	// ServerNetwork is the L4 protocol the node's server speaks — "tcp" for the
	// V2Ray/stream family (vless/vmess/trojan/ss/anytls), "udp" for the QUIC
	// family (hysteria2/tuic/hysteria). The doctor's direct camouflage probe is
	// TCP+TLS, so it only applies to "tcp" nodes; a "udp" node has no TCP front
	// to replicate the censor's TLS-over-TCP view against.
	ServerNetwork() string
	// CamouflageSNI returns the TLS front domain to probe and ok=true when the
	// node bears a probeable TLS/Reality layer; ok=false for a plain node with
	// no front (the doctor camouflage check is then skipped).
	CamouflageSNI() (sni string, ok bool)
	// SingBoxOutbound builds the native sing-box outbound object tagged tag, or
	// errors when this node has no native sing-box outbound (selection then
	// routes it to xray). sing-box marks its own sockets, so no fwmark here.
	SingBoxOutbound(tag string) (map[string]any, error)
	// XrayOutbound builds the xray outbound object (protocol/settings/
	// streamSettings) WITHOUT the own-traffic sockopt — the engine injects
	// sockopt.mark + domainStrategy into streamSettings. ok=false when xray
	// cannot dial this node (tuic/hysteria/anytls; a hysteria2 node that is
	// insecure without a certificate pin; a shadowsocks node with a plugin).
	// The returned map MUST carry a "streamSettings" map (possibly empty) for
	// the engine to inject into.
	XrayOutbound() (outbound map[string]any, ok bool, err error)
	// dialableBy reports whether the given core can dial this node — the
	// per-protocol half of the capability model (selection.go drives it). It is
	// unexported so Profile can only be implemented inside this package.
	dialableBy(core api.CoreType) bool
	isProfile()
}

// --- the protocol registry --------------------------------------------------

// codec is a protocol's (parse share-link, decode stored JSON) pair.
type codec struct {
	parse  func(*url.URL) (Profile, error)
	decode func(json.RawMessage) (Profile, error)
}

var (
	byScheme = map[string]codec{}   // share-link scheme → codec (ParseURL)
	byKind   = map[Protocol]codec{} // store union key → codec (DecodeProfile)
)

// registerProtocol wires a protocol's parser/decoder into the dispatch tables.
// Each protocol file calls this from an init(); schemes are the URL schemes the
// parser claims (lowercase — url.Parse lowercases the scheme).
func registerProtocol(kind Protocol, schemes []string, c codec) {
	byKind[kind] = c
	for _, s := range schemes {
		byScheme[s] = c
	}
}

// ParseURL parses a share link into a Profile, dispatching on the URL scheme.
func ParseURL(rawURL string) (Profile, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return nil, err
	}
	c, ok := byScheme[u.Scheme]
	if !ok {
		return nil, fmt.Errorf("unsupported protocol: %s", u.Scheme)
	}
	return c.parse(u)
}

// DecodeProfile reconstructs a stored Profile of kind from its raw JSON body —
// the store's tagged-union loader (ProfileDoc.UnmarshalJSON).
func DecodeProfile(kind Protocol, raw json.RawMessage) (Profile, error) {
	c, ok := byKind[kind]
	if !ok {
		return nil, fmt.Errorf("unknown protocol kind: %s", kind)
	}
	return c.decode(raw)
}

// EncodeProfileDoc returns the single-key tagged-union JSON for p
// ({"<kind>": <body>}) — the store's on-disk node profile shape.
func EncodeProfileDoc(p Profile) ([]byte, error) {
	return json.Marshal(map[Protocol]Profile{p.Kind(): p})
}
