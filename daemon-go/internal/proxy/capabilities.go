// The core-level capability facts for the V2Ray-family stream model: which
// stream transports / securities each embedded core can dial, and whether it
// supports the xtls-rprx-vision flow. These are facts about the CORE (they
// change only when a core gains a transport), consulted by the V2Ray-family
// dialableBy (vless/vmess/trojan). Protocols with their own wire shape
// (shadowsocks/hysteria2/tuic/…) declare their core support directly in
// dialableBy and do not consult this file.
//
// Selection (selection.go) drives EligibleCores off each Profile's dialableBy,
// so adding a protocol never edits this file.

package proxy

import (
	"slices"
	"strings"

	"ironlink/daemon/internal/api"
)

// CoreCapabilities statically describes a single core's V2Ray-family stream
// support. This is data: the dialableBy impls consult the lists.
type CoreCapabilities struct {
	// CanTUN: whether the core can run a TUN inbound (i.e. qualify as the TUN
	// dispatcher). sing-box: yes; xray: no (needs an external tun2socks).
	CanTUN bool
	// Transports, Securities: the stream transports / securities the core can
	// dial. Matching is by variant kind (payloads ignored).
	Transports []TransportKind
	Securities []SecurityKind
	// Vision: whether the xtls-rprx-vision flow is supported.
	Vision bool
}

// coversFlow: only xtls-rprx-vision* is gated; an absent/empty flow — or an
// unrecognized non-vision flow we do not gate on — is always fine.
func (c CoreCapabilities) coversFlow(flow string) bool {
	if isVisionFlow(flow) {
		return c.Vision
	}
	return true
}

// isVisionFlow reports whether flow is an xtls-rprx-vision flow (also matches
// the -udp443 suffix).
func isVisionFlow(flow string) bool {
	return strings.HasPrefix(flow, "xtls-rprx-vision")
}

// hasTransport / hasSecurity are the membership checks the V2Ray-family
// dialableBy use (re-exported as small helpers so a protocol file reads
// cleanly).
func (c CoreCapabilities) hasTransport(t TransportKind) bool { return slices.Contains(c.Transports, t) }
func (c CoreCapabilities) hasSecurity(s SecurityKind) bool   { return slices.Contains(c.Securities, s) }

// singBoxCaps: can_tun; transports tcp/ws/grpc but NOT xhttp (no xhttp client);
// securities none/tls/reality; vision supported.
var singBoxCaps = CoreCapabilities{
	CanTUN:     true,
	Transports: []TransportKind{TransportTCP, TransportWs, TransportGrpc},
	Securities: []SecurityKind{SecurityNone, SecurityTLS, SecurityReality},
	Vision:     true,
}

// xrayCaps: no TUN; transports tcp/ws/grpc/xhttp; securities none/tls/reality;
// vision supported.
var xrayCaps = CoreCapabilities{
	CanTUN:     false,
	Transports: []TransportKind{TransportTCP, TransportWs, TransportGrpc, TransportXhttp},
	Securities: []SecurityKind{SecurityNone, SecurityTLS, SecurityReality},
	Vision:     true,
}

// CoreCaps returns the V2Ray-family capability facts for core. An unknown core
// type covers nothing (empty lists → dialableBy false).
func CoreCaps(core api.CoreType) CoreCapabilities {
	switch core {
	case api.CoreSingBox:
		return singBoxCaps
	case api.CoreXray:
		return xrayCaps
	default:
		return CoreCapabilities{}
	}
}
