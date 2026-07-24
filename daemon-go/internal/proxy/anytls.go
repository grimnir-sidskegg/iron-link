// AnyTLS — the AnyTLS proxy protocol (anytls://). Added to sing-box in 1.12
// (our pin is v1.12.25, where it ships as a native outbound); xray has no
// AnyTLS outbound, so these nodes are sing-box-only.
//
// AnyTLS does NOT use the shared V2Ray stream model (stream.go): it has no
// stream transport and its own flat TLS shape (just SNI + insecure), so its
// config is self-contained. It implements Profile and registers itself from an
// init(), exactly like the vless.go worked example.
package proxy

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"

	"ironlink/daemon/internal/api"
)

// ProtocolAnyTLS is the protocol tag — the store union key and the NodeInfo
// badge for AnyTLS nodes.
const ProtocolAnyTLS Protocol = "anytls"

// AnyTLSConfig is a parsed AnyTLS node. Password is the shared secret (the
// share-link userinfo). SNI is the TLS front domain ("" = none, let the dialer
// default to the server address). Insecure skips certificate verification. The
// json tags ARE the on-disk profile body (the {"anytls": …} arm of the store
// union).
type AnyTLSConfig struct {
	ServerName string `json:"server_name"`
	Address    string `json:"address"`
	Port       uint16 `json:"port"`
	Password   string `json:"password"`
	SNI        string `json:"sni,omitempty"`
	Insecure   bool   `json:"insecure,omitempty"`
}

func init() {
	registerProtocol(ProtocolAnyTLS, []string{"anytls"}, codec{
		parse: func(u *url.URL) (Profile, error) { return parseAnyTLSURL(u) },
		decode: func(raw json.RawMessage) (Profile, error) {
			var c AnyTLSConfig
			if err := json.Unmarshal(raw, &c); err != nil {
				return nil, err
			}
			return &c, nil
		},
	})
}

// --- Profile ----------------------------------------------------------------

func (c *AnyTLSConfig) isProfile()            {}
func (c *AnyTLSConfig) DisplayName() string   { return c.ServerName }
func (c *AnyTLSConfig) Kind() Protocol        { return ProtocolAnyTLS }
func (c *AnyTLSConfig) ServerAddress() string { return c.Address }
func (c *AnyTLSConfig) ServerPort() uint16    { return c.Port }
func (c *AnyTLSConfig) Identity() string      { return c.Password }

// TransportLabel: AnyTLS has no stream transport dimension — "" (no badge).
func (c *AnyTLSConfig) TransportLabel() string { return "" }

// SecurityLabel: AnyTLS is always TLS over the wire.
func (c *AnyTLSConfig) SecurityLabel() string { return "tls" }

// ServerNetwork: AnyTLS rides TCP.
func (c *AnyTLSConfig) ServerNetwork() string { return "tcp" }

// CamouflageSNI: an AnyTLS node always bears a TLS layer worth probing; the
// front is the SNI (which may be "" when it defaults to the server address).
func (c *AnyTLSConfig) CamouflageSNI() (string, bool) { return c.SNI, true }

// dialableBy: only sing-box has an AnyTLS outbound; xray has none.
func (c *AnyTLSConfig) dialableBy(core api.CoreType) bool {
	return core == api.CoreSingBox
}

// SingBoxOutbound builds the native sing-box anytls outbound. The `tls` block
// is required by sing-box for anytls; server_name is omitted when SNI is empty
// (the dialer then derives it from the server address). The own-traffic fwmark
// is injected by the engine, not here.
func (c *AnyTLSConfig) SingBoxOutbound(tag string) (map[string]any, error) {
	tls := map[string]any{"enabled": true}
	if c.SNI != "" {
		tls["server_name"] = c.SNI
	}
	if c.Insecure {
		tls["insecure"] = true
	}
	return map[string]any{
		"type":        "anytls",
		"tag":         tag,
		"server":      c.Address,
		"server_port": c.Port,
		"password":    c.Password,
		"tls":         tls,
	}, nil
}

// XrayOutbound: xray has no AnyTLS outbound — not dialable by xray.
func (c *AnyTLSConfig) XrayOutbound() (map[string]any, bool, error) {
	return nil, false, nil
}

// --- share-link parsing -----------------------------------------------------

// parseAnyTLSURL parses an anytls:// share link:
//
//	anytls://<password>@host:port?sni=…&insecure=0|1#name
//
// password = userinfo (percent-decoded by url.Parse), address = host
// (required), port (default 443), name from the fragment (default "New
// AnyTLS"). `sni`/`servername` set the TLS front; `insecure`/`allowInsecure`
// (value "1" or "true") skips cert verification.
func parseAnyTLSURL(u *url.URL) (*AnyTLSConfig, error) {
	c := &AnyTLSConfig{
		ServerName: "New AnyTLS",
		Port:       443,
	}

	if u.User != nil {
		c.Password = u.User.Username()
	}
	c.Address = u.Hostname()
	if c.Address == "" {
		return nil, fmt.Errorf("AnyTLS URL is missing host")
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
	if sni := q.Get("sni"); sni != "" {
		c.SNI = sni
	} else if sni := q.Get("servername"); sni != "" {
		c.SNI = sni
	}
	c.Insecure = isTruthy(q.Get("insecure")) || isTruthy(q.Get("allowInsecure"))

	return c, nil
}

// isTruthy reads the share-link boolean convention: "1" or "true" → true;
// anything else (incl. "" and "0") → false.
func isTruthy(v string) bool { return v == "1" || v == "true" }
