// Package proxy is the app-side proxy domain model: the protocol-agnostic
// Profile interface + registry (profile.go), the shared V2Ray stream model
// (stream.go), the per-protocol implementations (this file + its peers), and
// the capability-driven per-node core selection (capabilities.go/selection.go).
//
// This file is VLESS — the first protocol and the worked example the others
// follow. It implements Profile and registers itself from an init().
package proxy

import (
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"ironlink/daemon/internal/api"
)

// VlessConfig is a parsed VLESS node. Flow "" means no flow;
// "xtls-rprx-vision[-udp443]" gates on the core's vision capability. The json
// tags ARE the on-disk profile body (the {"vless": …} arm of the store union).
type VlessConfig struct {
	ServerName string    `json:"server_name"`
	UUID       string    `json:"uuid"`
	Address    string    `json:"address"`
	Port       uint16    `json:"port"`
	Encryption string    `json:"encryption"`
	Flow       string    `json:"flow,omitempty"`
	Security   Security  `json:"security"`
	Transport  Transport `json:"transport"`
}

func init() {
	registerProtocol(ProtocolVless, []string{"vless"}, codec{
		parse: func(u *url.URL) (Profile, error) { return parseVlessURL(u) },
		decode: func(raw json.RawMessage) (Profile, error) {
			var v VlessConfig
			if err := json.Unmarshal(raw, &v); err != nil {
				return nil, err
			}
			return &v, nil
		},
	})
}

// --- Profile ----------------------------------------------------------------

func (c *VlessConfig) isProfile()             {}
func (c *VlessConfig) DisplayName() string    { return c.ServerName }
func (c *VlessConfig) Kind() Protocol         { return ProtocolVless }
func (c *VlessConfig) ServerAddress() string  { return c.Address }
func (c *VlessConfig) ServerPort() uint16     { return c.Port }
func (c *VlessConfig) Identity() string       { return c.UUID }
func (c *VlessConfig) TransportLabel() string { return c.Transport.Label() }
func (c *VlessConfig) SecurityLabel() string  { return c.Security.Label() }
func (c *VlessConfig) ServerNetwork() string  { return "tcp" }

// CamouflageSNI: a TLS/Reality node hides behind a front worth probing.
func (c *VlessConfig) CamouflageSNI() (string, bool) {
	switch c.Security.Kind {
	case SecurityReality, SecurityTLS:
		return c.Security.SNI(), true
	}
	return "", false
}

// dialableBy: both cores speak VLESS; the constraint is the stream transport /
// security the core supports (xhttp is xray-only) and the vision flow.
func (c *VlessConfig) dialableBy(core api.CoreType) bool {
	caps := CoreCaps(core)
	return slices.Contains(caps.Transports, c.Transport.Kind) &&
		slices.Contains(caps.Securities, c.Security.Kind) &&
		caps.coversFlow(c.Flow)
}

// SingBoxOutbound builds the native sing-box vless outbound (tcp/ws/grpc ×
// none/tls/reality). xhttp has no native sing-box outbound and errors here —
// such a node is xray-routed (selection guarantees we are not called for it).
func (c *VlessConfig) SingBoxOutbound(tag string) (map[string]any, error) {
	ob := map[string]any{
		"type":        "vless",
		"tag":         tag,
		"server":      c.Address,
		"server_port": c.Port,
		"uuid":        c.UUID,
	}
	if c.Flow != "" {
		ob["flow"] = c.Flow
	}
	if tls, ok := c.Security.singBoxTLS(); ok {
		ob["tls"] = tls
	}
	block, ok, err := c.Transport.singBoxTransport()
	if err != nil {
		return nil, err
	}
	if ok {
		ob["transport"] = block
	}
	return ob, nil
}

// XrayOutbound builds the xray vless outbound (vnext + the shared stream
// settings). The own-traffic sockopt is injected by the engine.
func (c *VlessConfig) XrayOutbound() (map[string]any, bool, error) {
	stream := map[string]any{}
	c.Security.applyXraySecurity(stream)
	c.Transport.applyXrayTransport(stream)
	// xray rejects a VLESS user without encryption ("please add/set
	// "encryption":"none""); the parser defaults it, this guards a decoded
	// store doc that predates/omits the field.
	encryption := c.Encryption
	if encryption == "" {
		encryption = "none"
	}
	ob := map[string]any{
		"protocol": "vless",
		"settings": map[string]any{
			"vnext": []any{map[string]any{
				"address": c.Address,
				"port":    c.Port,
				"users": []any{map[string]any{
					"id":         c.UUID,
					"encryption": encryption,
					"flow":       c.Flow,
					"level":      0,
					"security":   "auto",
				}},
			}},
		},
		"streamSettings": stream,
		"mux": map[string]any{
			"enabled":         false,
			"concurrency":     -1,
			"xudpConcurrency": 8,
			"xudpProxyUDP443": "",
		},
	}
	return ob, true, nil
}

// --- share-link parsing -----------------------------------------------------

// parseVlessURL ports the field-by-field VLESS share-link parse: uuid =
// userinfo, address = host (required), port (default 443), the display name
// from the fragment, flow/encryption from the query, then the tagged security
// and transport unions.
func parseVlessURL(u *url.URL) (*VlessConfig, error) {
	c := &VlessConfig{
		ServerName: "New Profile",
		Port:       443,
		Encryption: "none",
		Security:   Security{Kind: SecurityNone},
		Transport:  Transport{Kind: TransportTCP},
	}

	if u.User != nil {
		c.UUID = u.User.Username()
	}
	c.Address = u.Hostname()
	if c.Address == "" {
		return nil, fmt.Errorf("VLESS URL is missing host")
	}
	if p := u.Port(); p != "" {
		port, err := strconv.ParseUint(p, 10, 16)
		if err != nil {
			return nil, fmt.Errorf("invalid port %q: %w", p, err)
		}
		c.Port = uint16(port)
	}
	if u.Fragment != "" {
		c.ServerName = fragmentName(u.EscapedFragment())
	}

	q := u.Query()
	if flow := q.Get("flow"); flow != "" {
		c.Flow = flow
	}
	if enc := q.Get("encryption"); enc != "" {
		c.Encryption = enc
	}

	switch q.Get("security") {
	case "reality":
		params := &RealityParams{}
		for _, f := range []struct {
			key string
			dst *string
		}{{"sni", &params.SNI}, {"fp", &params.Fp}, {"pbk", &params.Pbk}, {"sid", &params.Sid}} {
			if !q.Has(f.key) {
				return nil, fmt.Errorf("reality requires %s", f.key)
			}
			*f.dst = q.Get(f.key)
		}
		c.Security = Security{Kind: SecurityReality, Reality: params}
	case "tls":
		c.Security = Security{Kind: SecurityTLS, TLS: &TLSParams{
			SNI: q.Get("sni"),
			Fp:  q.Get("fp"),
		}}
	}

	switch q.Get("type") {
	case "xhttp", "splithttp":
		params := &XhttpParams{
			Path: q.Get("path"),
			Host: q.Get("host"),
			Mode: q.Get("mode"),
		}
		if extra := q.Get("extra"); extra != "" {
			if !json.Valid([]byte(extra)) {
				return nil, fmt.Errorf("xhttp extra is not valid JSON")
			}
			params.Extra = json.RawMessage(extra)
		}
		c.Transport = Transport{Kind: TransportXhttp, Xhttp: params}
	case "ws":
		c.Transport = Transport{Kind: TransportWs, Ws: &WsParams{
			Path: q.Get("path"),
			Host: q.Get("host"),
		}}
	case "grpc":
		c.Transport = Transport{Kind: TransportGrpc, Grpc: &GrpcParams{
			ServiceName: q.Get("serviceName"),
		}}
	}

	return c, nil
}

// fragmentName decodes the share-link fragment into the display name the way
// the V2Ray parsers do: the fragment is read as form pairs, VALUES ARE DROPPED,
// and the decoded keys concatenate ('+' decodes to space). For the
// overwhelmingly common no-'&'/'=' fragment this is plain percent+plus decoding.
func fragmentName(escaped string) string {
	var b strings.Builder
	for pair := range strings.SplitSeq(escaped, "&") {
		key, _, _ := strings.Cut(pair, "=")
		decoded, err := url.QueryUnescape(key)
		if err != nil {
			decoded = key
		}
		b.WriteString(decoded)
	}
	return b.String()
}
