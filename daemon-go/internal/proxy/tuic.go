// This file is TUIC (v5) — a QUIC-based proxy protocol. It is sing-box-only:
// xray has no TUIC outbound, so XrayOutbound returns ok=false and the node is
// dialable only by the native sing-box core. TUIC carries no V2Ray stream
// (stream.go) — QUIC owns its own transport/security shape, so the config is
// flat. It implements Profile and registers itself from an init(), following
// vless.go as the worked example.
package proxy

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"ironlink/daemon/internal/api"
)

const ProtocolTUIC Protocol = "tuic"

// TUICConfig is a parsed TUIC v5 node. The json tags ARE the on-disk profile
// body (the {"tuic": …} arm of the store union). Optional fields carry
// ",omitempty" so a minimal node round-trips without empty noise. ALPN is the
// QUIC application-protocol list (commonly ["h3"]); Insecure skips certificate
// verification (the share-link allow_insecure / insecure flag).
type TUICConfig struct {
	ServerName        string   `json:"server_name"`
	Address           string   `json:"address"`
	Port              uint16   `json:"port"`
	UUID              string   `json:"uuid"`
	Password          string   `json:"password"`
	SNI               string   `json:"sni,omitempty"`
	CongestionControl string   `json:"congestion_control,omitempty"`
	UDPRelayMode      string   `json:"udp_relay_mode,omitempty"`
	ALPN              []string `json:"alpn,omitempty"`
	Insecure          bool     `json:"insecure,omitempty"`
}

func init() {
	registerProtocol(ProtocolTUIC, []string{"tuic"}, codec{
		parse: func(u *url.URL) (Profile, error) { return parseTUICURL(u) },
		decode: func(raw json.RawMessage) (Profile, error) {
			var c TUICConfig
			if err := json.Unmarshal(raw, &c); err != nil {
				return nil, err
			}
			return &c, nil
		},
	})
}

// --- Profile ----------------------------------------------------------------

func (c *TUICConfig) isProfile()             {}
func (c *TUICConfig) DisplayName() string    { return c.ServerName }
func (c *TUICConfig) Kind() Protocol         { return ProtocolTUIC }
func (c *TUICConfig) ServerAddress() string  { return c.Address }
func (c *TUICConfig) ServerPort() uint16     { return c.Port }
func (c *TUICConfig) Identity() string       { return c.UUID }
func (c *TUICConfig) TransportLabel() string { return "quic" }
func (c *TUICConfig) SecurityLabel() string  { return "tls" }
func (c *TUICConfig) ServerNetwork() string  { return "udp" }

// CamouflageSNI: TUIC always rides QUIC/TLS, so the SNI is a probeable front.
func (c *TUICConfig) CamouflageSNI() (string, bool) { return c.SNI, true }

// dialableBy: only the native sing-box core speaks TUIC; xray cannot dial it.
func (c *TUICConfig) dialableBy(core api.CoreType) bool {
	return core == api.CoreSingBox
}

// SingBoxOutbound builds the native sing-box tuic outbound. The empty optionals
// (congestion_control, udp_relay_mode, server_name) are omitted rather than
// emitted as "" so sing-box applies its own defaults. sing-box marks its own
// sockets, so no fwmark here. See:
// https://sing-box.sagernet.org/configuration/outbound/tuic/
func (c *TUICConfig) SingBoxOutbound(tag string) (map[string]any, error) {
	tls := map[string]any{
		"enabled":  true,
		"insecure": c.Insecure,
	}
	if c.SNI != "" {
		tls["server_name"] = c.SNI
	}
	if len(c.ALPN) > 0 {
		tls["alpn"] = c.ALPN
	}
	ob := map[string]any{
		"type":        "tuic",
		"tag":         tag,
		"server":      c.Address,
		"server_port": c.Port,
		"uuid":        c.UUID,
		"password":    c.Password,
		"tls":         tls,
	}
	if c.CongestionControl != "" {
		ob["congestion_control"] = c.CongestionControl
	}
	if c.UDPRelayMode != "" {
		ob["udp_relay_mode"] = c.UDPRelayMode
	}
	return ob, nil
}

// XrayOutbound: xray has no TUIC outbound — such a node is sing-box-only.
func (c *TUICConfig) XrayOutbound() (map[string]any, bool, error) {
	return nil, false, nil
}

// --- share-link parsing -----------------------------------------------------

// parseTUICURL parses a TUIC v5 share link:
//
//	tuic://<uuid>:<password>@host:port?sni=…&congestion_control=bbr&alpn=h3&udp_relay_mode=native&allow_insecure=0|1#name
//
// userinfo is uuid:password (both percent-decoded by net/url), address = host
// (required), port (default 443), the display name from the fragment, then the
// QUIC/TLS query params. alpn may be comma-separated → []string. The insecure
// flag is read from allow_insecure or insecure. See:
// https://github.com/daeuniverse/dae/discussions/182
func parseTUICURL(u *url.URL) (*TUICConfig, error) {
	c := &TUICConfig{
		ServerName: "New TUIC",
		Port:       443,
	}

	if u.User != nil {
		c.UUID = u.User.Username()
		c.Password, _ = u.User.Password()
	}
	c.Address = u.Hostname()
	if c.Address == "" {
		return nil, fmt.Errorf("TUIC URL is missing host")
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
	c.SNI = q.Get("sni")
	c.CongestionControl = q.Get("congestion_control")
	c.UDPRelayMode = q.Get("udp_relay_mode")
	if alpn := q.Get("alpn"); alpn != "" {
		parts := strings.Split(alpn, ",")
		c.ALPN = make([]string, 0, len(parts))
		for _, p := range parts {
			if p = strings.TrimSpace(p); p != "" {
				c.ALPN = append(c.ALPN, p)
			}
		}
	}
	c.Insecure = parseInsecure(q.Get("allow_insecure")) || parseInsecure(q.Get("insecure"))

	return c, nil
}

// parseInsecure reads a share-link boolean flag: "1" / "true" → true, anything
// else (incl. "", "0", "false") → false.
func parseInsecure(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true":
		return true
	}
	return false
}
