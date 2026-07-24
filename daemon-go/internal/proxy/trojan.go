// Trojan — a peer protocol implementation registered through the proxy
// registry (profile.go). It is a V2Ray-family protocol: it carries the shared
// stream model (tcp/ws/grpc transport × tls/reality security from stream.go)
// and differs from VLESS only in that the credential is a password (the
// userinfo) rather than a uuid, and that the link is TLS by definition. The
// file is additive: it implements Profile and registers itself from an init();
// nothing else changes. vless.go is the worked example it mirrors.
package proxy

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"

	"ironlink/daemon/internal/api"
)

const ProtocolTrojan Protocol = "trojan"

// TrojanConfig is a parsed Trojan node. The credential is Password (the
// share-link userinfo). Trojan is TLS by definition, so Security defaults to
// tls when the link omits the `security` query. The json tags ARE the on-disk
// profile body (the {"trojan": …} arm of the store union).
type TrojanConfig struct {
	ServerName string    `json:"server_name"`
	Address    string    `json:"address"`
	Port       uint16    `json:"port"`
	Password   string    `json:"password"`
	Security   Security  `json:"security"`
	Transport  Transport `json:"transport"`
}

func init() {
	registerProtocol(ProtocolTrojan, []string{"trojan"}, codec{
		parse: func(u *url.URL) (Profile, error) { return parseTrojanURL(u) },
		decode: func(raw json.RawMessage) (Profile, error) {
			var v TrojanConfig
			if err := json.Unmarshal(raw, &v); err != nil {
				return nil, err
			}
			return &v, nil
		},
	})
}

// --- Profile ----------------------------------------------------------------

func (c *TrojanConfig) isProfile()             {}
func (c *TrojanConfig) DisplayName() string    { return c.ServerName }
func (c *TrojanConfig) Kind() Protocol         { return ProtocolTrojan }
func (c *TrojanConfig) ServerAddress() string  { return c.Address }
func (c *TrojanConfig) ServerPort() uint16     { return c.Port }
func (c *TrojanConfig) Identity() string       { return c.Password }
func (c *TrojanConfig) TransportLabel() string { return c.Transport.Label() }
func (c *TrojanConfig) SecurityLabel() string  { return c.Security.Label() }
func (c *TrojanConfig) ServerNetwork() string  { return "tcp" }

// CamouflageSNI: Trojan is always TLS (tls or reality), so a probeable front is
// present whenever the node bears one of those securities.
func (c *TrojanConfig) CamouflageSNI() (string, bool) {
	switch c.Security.Kind {
	case SecurityReality, SecurityTLS:
		return c.Security.SNI(), true
	}
	return "", false
}

// dialableBy: both cores speak Trojan; the constraint is the stream transport /
// security the core supports (xhttp is xray-only).
func (c *TrojanConfig) dialableBy(core api.CoreType) bool {
	caps := CoreCaps(core)
	return caps.hasTransport(c.Transport.Kind) && caps.hasSecurity(c.Security.Kind)
}

// SingBoxOutbound builds the native sing-box trojan outbound (tcp/ws/grpc ×
// tls/reality). An xhttp node has no native sing-box outbound and errors here —
// such a node is xray-routed (selection guarantees we are not called for it).
func (c *TrojanConfig) SingBoxOutbound(tag string) (map[string]any, error) {
	ob := map[string]any{
		"type":        "trojan",
		"tag":         tag,
		"server":      c.Address,
		"server_port": c.Port,
		"password":    c.Password,
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

// XrayOutbound builds the xray trojan outbound (servers + the shared stream
// settings). The own-traffic sockopt is injected by the engine.
func (c *TrojanConfig) XrayOutbound() (map[string]any, bool, error) {
	stream := map[string]any{}
	c.Security.applyXraySecurity(stream)
	c.Transport.applyXrayTransport(stream)
	ob := map[string]any{
		"protocol": "trojan",
		"settings": map[string]any{
			"servers": []any{map[string]any{
				"address":  c.Address,
				"port":     c.Port,
				"password": c.Password,
				"level":    0,
			}},
		},
		"streamSettings": stream,
	}
	return ob, true, nil
}

// --- share-link parsing -----------------------------------------------------

// parseTrojanURL ports the Trojan share-link parse: password = userinfo
// (percent-decoded), address = host (required), port (default 443), the display
// name from the fragment, then the tagged security and transport unions. Trojan
// is TLS by definition: an absent `security` query is treated as tls.
func parseTrojanURL(u *url.URL) (*TrojanConfig, error) {
	c := &TrojanConfig{
		ServerName: "New Trojan",
		Port:       443,
		Security:   Security{Kind: SecurityTLS, TLS: &TLSParams{}},
		Transport:  Transport{Kind: TransportTCP},
	}

	if u.User != nil {
		c.Password = u.User.Username()
	}
	c.Address = u.Hostname()
	if c.Address == "" {
		return nil, fmt.Errorf("Trojan URL is missing host")
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
