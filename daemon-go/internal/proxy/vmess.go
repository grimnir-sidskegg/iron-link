// This file is VMess — a V2Ray-family peer of VLESS (vless.go). It carries the
// same tcp/ws/grpc + none/tls/reality stream model (stream.go), so it reuses
// the shared Security/Transport unions and their sing-box / xray emit helpers
// instead of reimplementing them. It implements Profile and registers itself
// from an init().
package proxy

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"ironlink/daemon/internal/api"
)

const ProtocolVmess Protocol = "vmess"

// VmessConfig is a parsed VMess node. Cipher is the VMess request encryption
// (the share-link `scy`; "auto" default) — distinct from Security, which is the
// transport-layer security (tls/reality). The json tags ARE the on-disk profile
// body (the {"vmess": …} arm of the store union).
type VmessConfig struct {
	ServerName string    `json:"server_name"`
	UUID       string    `json:"uuid"`
	Address    string    `json:"address"`
	Port       uint16    `json:"port"`
	AlterID    int       `json:"alter_id"`
	Cipher     string    `json:"cipher"`
	Security   Security  `json:"security"`
	Transport  Transport `json:"transport"`
}

func init() {
	registerProtocol(ProtocolVmess, []string{"vmess"}, codec{
		parse: func(u *url.URL) (Profile, error) { return parseVmessURL(u) },
		decode: func(raw json.RawMessage) (Profile, error) {
			var v VmessConfig
			if err := json.Unmarshal(raw, &v); err != nil {
				return nil, err
			}
			return &v, nil
		},
	})
}

// --- Profile ----------------------------------------------------------------

func (c *VmessConfig) isProfile()             {}
func (c *VmessConfig) DisplayName() string    { return c.ServerName }
func (c *VmessConfig) Kind() Protocol         { return ProtocolVmess }
func (c *VmessConfig) ServerAddress() string  { return c.Address }
func (c *VmessConfig) ServerPort() uint16     { return c.Port }
func (c *VmessConfig) Identity() string       { return c.UUID }
func (c *VmessConfig) TransportLabel() string { return c.Transport.Label() }
func (c *VmessConfig) SecurityLabel() string  { return c.Security.Label() }
func (c *VmessConfig) ServerNetwork() string  { return "tcp" }

// CamouflageSNI: a TLS/Reality node hides behind a front worth probing.
func (c *VmessConfig) CamouflageSNI() (string, bool) {
	switch c.Security.Kind {
	case SecurityReality, SecurityTLS:
		return c.Security.SNI(), true
	}
	return "", false
}

// dialableBy: both cores speak VMess; the constraint is the stream transport /
// security the core supports (VMess has no vision flow).
func (c *VmessConfig) dialableBy(core api.CoreType) bool {
	caps := CoreCaps(core)
	return caps.hasTransport(c.Transport.Kind) && caps.hasSecurity(c.Security.Kind)
}

// SingBoxOutbound builds the native sing-box vmess outbound (tcp/ws/grpc ×
// none/tls/reality). Field names per sing-box v1.12.x vmess outbound docs:
// uuid / alter_id / security + the shared tls/transport blocks.
func (c *VmessConfig) SingBoxOutbound(tag string) (map[string]any, error) {
	ob := map[string]any{
		"type":        "vmess",
		"tag":         tag,
		"server":      c.Address,
		"server_port": c.Port,
		"uuid":        c.UUID,
		"alter_id":    c.AlterID,
		"security":    c.cipher(),
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

// XrayOutbound builds the xray vmess outbound (vnext + the shared stream
// settings). User fields per xray-core vmess outbound docs: id / alterId /
// security / level. The own-traffic sockopt is injected by the engine.
func (c *VmessConfig) XrayOutbound() (map[string]any, bool, error) {
	stream := map[string]any{}
	c.Security.applyXraySecurity(stream)
	c.Transport.applyXrayTransport(stream)
	ob := map[string]any{
		"protocol": "vmess",
		"settings": map[string]any{
			"vnext": []any{map[string]any{
				"address": c.Address,
				"port":    c.Port,
				"users": []any{map[string]any{
					"id":       c.UUID,
					"alterId":  c.AlterID,
					"security": c.cipher(),
					"level":    0,
				}},
			}},
		},
		"streamSettings": stream,
	}
	return ob, true, nil
}

// cipher returns the VMess request encryption, defaulting "auto" — both cores
// accept "auto" and it is the v2rayN default.
func (c *VmessConfig) cipher() string {
	if c.Cipher == "" {
		return "auto"
	}
	return c.Cipher
}

// --- share-link parsing -----------------------------------------------------

// vmessLink is the v2rayN `vmess://base64(JSON)` body. Every field is a string-
// or-number tolerant value: v2rayN exporters emit port/aid as a string in some
// builds and a number in others, so json.Number is decoded permissively below.
type vmessLink struct {
	V    string      `json:"v"`
	Ps   string      `json:"ps"`
	Add  string      `json:"add"`
	Port json.Number `json:"port"`
	ID   string      `json:"id"`
	Aid  json.Number `json:"aid"`
	Scy  string      `json:"scy"`
	Net  string      `json:"net"`
	Type string      `json:"type"`
	Host string      `json:"host"`
	Path string      `json:"path"`
	TLS  string      `json:"tls"`
	SNI  string      `json:"sni"`
	ALPN string      `json:"alpn"`
	Fp   string      `json:"fp"`
}

// parseVmessURL decodes a `vmess://base64(JSON)` share link. The base64 is the
// v2rayN JSON body; we accept std/url base64, with or without padding. Port and
// aid arrive as a string OR a number across exporters — json.Number absorbs
// both, then we parse the numeric strings ourselves.
func parseVmessURL(u *url.URL) (*VmessConfig, error) {
	// The payload is everything after "vmess://". url.Parse stuffs it into Opaque
	// (no "//"), but be robust to Host/Path too.
	payload := u.Opaque
	if payload == "" {
		payload = strings.TrimPrefix(u.String(), "vmess://")
	}
	payload = strings.TrimSpace(payload)
	if payload == "" {
		return nil, fmt.Errorf("vmess URL is empty")
	}

	raw, err := decodeVmessBase64(payload)
	if err != nil {
		return nil, fmt.Errorf("vmess base64: %w", err)
	}

	var link vmessLink
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	if err := dec.Decode(&link); err != nil {
		return nil, fmt.Errorf("vmess json: %w", err)
	}

	if link.Add == "" {
		return nil, fmt.Errorf("vmess link is missing add (host)")
	}
	if link.ID == "" {
		return nil, fmt.Errorf("vmess link is missing id (uuid)")
	}

	c := &VmessConfig{
		ServerName: link.Ps,
		UUID:       link.ID,
		Address:    link.Add,
		Port:       443,
		Cipher:     "auto",
		Security:   Security{Kind: SecurityNone},
		Transport:  Transport{Kind: TransportTCP},
	}
	if c.ServerName == "" {
		c.ServerName = "New Profile"
	}

	port, err := numToUint16(link.Port)
	if err != nil {
		return nil, fmt.Errorf("vmess port: %w", err)
	}
	if port != 0 {
		c.Port = port
	}

	aid, err := numToInt(link.Aid)
	if err != nil {
		return nil, fmt.Errorf("vmess aid: %w", err)
	}
	c.AlterID = aid

	if link.Scy != "" {
		c.Cipher = link.Scy
	}

	// net → transport. host/path are overloaded by net: for ws they are the Host
	// header and the path; for grpc the path is the serviceName.
	switch link.Net {
	case "ws":
		c.Transport = Transport{Kind: TransportWs, Ws: &WsParams{
			Path: link.Path,
			Host: link.Host,
		}}
	case "grpc":
		c.Transport = Transport{Kind: TransportGrpc, Grpc: &GrpcParams{
			ServiceName: link.Path,
		}}
	default:
		c.Transport = Transport{Kind: TransportTCP}
	}

	// tls → security. The SNI front falls back to the host header when sni is
	// absent, matching how v2rayN clients dial these nodes.
	sni := link.SNI
	if sni == "" {
		sni = link.Host
	}
	switch link.TLS {
	case "tls":
		c.Security = Security{Kind: SecurityTLS, TLS: &TLSParams{
			SNI: sni,
			Fp:  link.Fp,
		}}
	case "reality":
		// The v2rayN vmess link format carries no pbk/sid, so a reality node is
		// unconstructable from it — reject rather than store a node that selection
		// would treat as dialable but that can never complete a handshake.
		return nil, fmt.Errorf("vmess reality is not supported (link carries no public key / short id)")
	default:
		c.Security = Security{Kind: SecurityNone}
	}

	return c, nil
}

// decodeVmessBase64 decodes the share-link payload, tolerating std/url alphabets
// and missing padding (different exporters pick different conventions).
func decodeVmessBase64(s string) ([]byte, error) {
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	} {
		if b, err := enc.DecodeString(s); err == nil {
			return b, nil
		}
	}
	return nil, fmt.Errorf("not valid base64")
}

// numToUint16 reads a json.Number that may be an empty string, a numeric string,
// or a JSON number into a uint16 (0 when absent).
func numToUint16(n json.Number) (uint16, error) {
	s := strings.TrimSpace(n.String())
	if s == "" {
		return 0, nil
	}
	v, err := strconv.ParseUint(s, 10, 16)
	if err != nil {
		return 0, err
	}
	return uint16(v), nil
}

// numToInt reads a json.Number that may be empty, a numeric string, or a JSON
// number into an int (0 when absent).
func numToInt(n json.Number) (int, error) {
	s := strings.TrimSpace(n.String())
	if s == "" {
		return 0, nil
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, err
	}
	return v, nil
}
