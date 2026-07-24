// This file is Hysteria v1 — the LEGACY Hysteria protocol (scheme hysteria://),
// distinct from Hysteria2 (hysteria2://). It rides QUIC, so it does NOT use the
// V2Ray stream model in stream.go (its own wire shape, like Shadowsocks/TUIC).
//
// It implements Profile and registers itself from an init(). Only sing-box can
// dial it (xray has no Hysteria outbound), so XrayOutbound returns ok=false and
// dialableBy is true only for CoreSingBox.
//
// sing-box field names verified against the v1.12.x Hysteria (v1) outbound doc:
// https://sing-box.sagernet.org/configuration/outbound/hysteria/ — the bandwidth
// fields are up_mbps/down_mbps (integer) and the auth field is auth_str (string).
// TLS subfields (enabled/server_name/insecure/alpn) per the shared TLS doc:
// https://sing-box.sagernet.org/configuration/shared/tls/ — alpn is an ARRAY.

package proxy

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"

	"ironlink/daemon/internal/api"
)

const ProtocolHysteria Protocol = "hysteria"

// HysteriaConfig is a parsed Hysteria v1 node. The json tags ARE the on-disk
// profile body (the {"hysteria": …} arm of the store union). Unlike the
// V2Ray-family protocols there is no shared security/transport union here:
// Hysteria carries its own QUIC-native TLS + congestion fields inline.
type HysteriaConfig struct {
	ServerName string `json:"server_name"`
	Address    string `json:"address"`
	Port       uint16 `json:"port"`
	Auth       string `json:"auth,omitempty"`
	SNI        string `json:"sni,omitempty"`
	Insecure   bool   `json:"insecure,omitempty"`
	UpMbps     int    `json:"up_mbps,omitempty"`
	DownMbps   int    `json:"down_mbps,omitempty"`
	Obfs       string `json:"obfs,omitempty"`
	ALPN       string `json:"alpn,omitempty"`
	Protocol   string `json:"protocol,omitempty"`
}

func init() {
	registerProtocol(ProtocolHysteria, []string{"hysteria"}, codec{
		parse: func(u *url.URL) (Profile, error) { return parseHysteriaURL(u) },
		decode: func(raw json.RawMessage) (Profile, error) {
			var c HysteriaConfig
			if err := json.Unmarshal(raw, &c); err != nil {
				return nil, err
			}
			return &c, nil
		},
	})
}

// --- Profile ----------------------------------------------------------------

func (c *HysteriaConfig) isProfile()             {}
func (c *HysteriaConfig) DisplayName() string    { return c.ServerName }
func (c *HysteriaConfig) Kind() Protocol         { return ProtocolHysteria }
func (c *HysteriaConfig) ServerAddress() string  { return c.Address }
func (c *HysteriaConfig) ServerPort() uint16     { return c.Port }
func (c *HysteriaConfig) TransportLabel() string { return "quic" }
func (c *HysteriaConfig) SecurityLabel() string  { return "tls" }
func (c *HysteriaConfig) ServerNetwork() string  { return "udp" }

// Identity distinguishes two Hysteria nodes at the same server:port — the auth
// string, falling back to the address when no auth is set (so a refresh diff
// never collapses two distinct same-endpoint nodes to an empty key).
func (c *HysteriaConfig) Identity() string {
	if c.Auth != "" {
		return c.Auth
	}
	return c.Address
}

// CamouflageSNI: Hysteria always rides a TLS/QUIC layer, so it bears a probeable
// front (the configured SNI, possibly empty when the node validates against its
// real hostname).
func (c *HysteriaConfig) CamouflageSNI() (string, bool) {
	return c.SNI, true
}

// dialableBy: only native sing-box speaks Hysteria v1; xray has no Hysteria
// outbound, so xray (and any other core) cannot dial it.
func (c *HysteriaConfig) dialableBy(core api.CoreType) bool {
	return core == api.CoreSingBox
}

// SingBoxOutbound builds the native sing-box hysteria (v1) outbound. The
// own-traffic fwmark is injected by the engine, not here.
func (c *HysteriaConfig) SingBoxOutbound(tag string) (map[string]any, error) {
	tls := map[string]any{
		"enabled":  true,
		"insecure": c.Insecure,
	}
	if c.SNI != "" { // omit when absent so sing-box falls back to the dialed host
		tls["server_name"] = c.SNI
	}
	if c.ALPN != "" {
		tls["alpn"] = []string{c.ALPN}
	}
	ob := map[string]any{
		"type":        "hysteria",
		"tag":         tag,
		"server":      c.Address,
		"server_port": c.Port,
		"auth_str":    c.Auth,
		"tls":         tls,
	}
	if c.UpMbps != 0 {
		ob["up_mbps"] = c.UpMbps
	}
	if c.DownMbps != 0 {
		ob["down_mbps"] = c.DownMbps
	}
	if c.Obfs != "" {
		ob["obfs"] = c.Obfs
	}
	return ob, nil
}

// XrayOutbound: xray does not support Hysteria, so there is no outbound to emit.
func (c *HysteriaConfig) XrayOutbound() (map[string]any, bool, error) {
	return nil, false, nil
}

// --- share-link parsing -----------------------------------------------------

// parseHysteriaURL parses the (non-standard, varied) Hysteria v1 share link. The
// common form carries NO userinfo and the auth as a query param:
//
//	hysteria://host:port?protocol=udp&auth=<auth>&peer=<sni>&insecure=1
//	    &upmbps=100&downmbps=100&obfs=<obfs>&alpn=<alpn>#name
//
// host + port are required; the fragment is the display name; peer is the TLS
// SNI; insecure is a bool flag; upmbps/downmbps are integers (Mbps).
func parseHysteriaURL(u *url.URL) (*HysteriaConfig, error) {
	c := &HysteriaConfig{
		ServerName: "New Hysteria",
	}

	c.Address = u.Hostname()
	if c.Address == "" {
		return nil, fmt.Errorf("Hysteria URL is missing host")
	}
	p := u.Port()
	if p == "" {
		return nil, fmt.Errorf("Hysteria URL is missing port")
	}
	port, err := strconv.ParseUint(p, 10, 16)
	if err != nil {
		return nil, fmt.Errorf("invalid port %q: %w", p, err)
	}
	c.Port = uint16(port)

	if u.Fragment != "" {
		c.ServerName = fragmentName(u.EscapedFragment())
	}

	q := u.Query()
	c.Auth = q.Get("auth")
	c.SNI = q.Get("peer")
	c.Obfs = q.Get("obfs")
	c.ALPN = q.Get("alpn")
	c.Protocol = q.Get("protocol")
	c.Insecure = parseHysteriaBool(q.Get("insecure"))
	c.UpMbps = parseHysteriaInt(q.Get("upmbps"))
	c.DownMbps = parseHysteriaInt(q.Get("downmbps"))

	return c, nil
}

// parseHysteriaBool reads an insecure-style flag: "1"/"true" → true, else false.
func parseHysteriaBool(s string) bool {
	switch s {
	case "1", "true", "True", "TRUE":
		return true
	}
	return false
}

// parseHysteriaInt reads an Mbps query value, treating empty/garbage as 0.
func parseHysteriaInt(s string) int {
	if s == "" {
		return 0
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}
