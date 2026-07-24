// Shadowsocks (ss://) — the second protocol and the first one OUTSIDE the
// V2Ray stream family. It has no tcp/ws/grpc/tls stream layer (stream.go is not
// used here): a node is just method + password + server:port, plus an OPTIONAL
// SIP003 transport plugin (obfs-local / v2ray-plugin). It implements Profile and
// registers itself from an init(), exactly like vless.go.
//
// Both cores speak Shadowsocks (including the SS2022 2022-blake3-* ciphers,
// where the password IS the pre-shared key, kept verbatim). The one asymmetry
// is the SIP003 plugin: sing-box wires plugin/plugin_opts natively, but xray's
// plugin mechanism is incompatible — so a plugin node is sing-box-only
// (dialableBy / XrayOutbound both gate on it).

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

const ProtocolShadowsocks Protocol = "shadowsocks"

// ShadowsocksConfig is a parsed Shadowsocks node. The json tags ARE the on-disk
// profile body (the {"shadowsocks": …} arm of the store union). Plugin /
// PluginOpts are the OPTIONAL SIP003 transport plugin (absent = "").
type ShadowsocksConfig struct {
	ServerName string `json:"server_name"`
	Address    string `json:"address"`
	Port       uint16 `json:"port"`
	Method     string `json:"method"`
	Password   string `json:"password"`
	Plugin     string `json:"plugin,omitempty"`
	PluginOpts string `json:"plugin_opts,omitempty"`
}

func init() {
	registerProtocol(ProtocolShadowsocks, []string{"ss"}, codec{
		parse: func(u *url.URL) (Profile, error) { return parseShadowsocks(u) },
		decode: func(raw json.RawMessage) (Profile, error) {
			var c ShadowsocksConfig
			if err := json.Unmarshal(raw, &c); err != nil {
				return nil, err
			}
			return &c, nil
		},
	})
}

// --- Profile ----------------------------------------------------------------

func (c *ShadowsocksConfig) isProfile()            {}
func (c *ShadowsocksConfig) DisplayName() string   { return c.ServerName }
func (c *ShadowsocksConfig) Kind() Protocol        { return ProtocolShadowsocks }
func (c *ShadowsocksConfig) ServerAddress() string { return c.Address }
func (c *ShadowsocksConfig) ServerPort() uint16    { return c.Port }

// Identity: the password (the PSK for SS2022) — distinguishes two nodes at the
// same server:port across a subscription refresh; never logged.
func (c *ShadowsocksConfig) Identity() string { return c.Password }

// Shadowsocks has no stream transport / TLS dimension, so both badge labels are
// empty and there is no camouflage front to probe.
func (c *ShadowsocksConfig) TransportLabel() string        { return "" }
func (c *ShadowsocksConfig) SecurityLabel() string         { return "" }
func (c *ShadowsocksConfig) ServerNetwork() string         { return "tcp" }
func (c *ShadowsocksConfig) CamouflageSNI() (string, bool) { return "", false }

// dialableBy: sing-box always; xray only for a plain (no-plugin) node — xray's
// SIP003 plugin wiring differs, so a plugin node is sing-box-only.
func (c *ShadowsocksConfig) dialableBy(core api.CoreType) bool {
	switch core {
	case api.CoreSingBox:
		return true
	case api.CoreXray:
		return c.Plugin == ""
	default:
		return false
	}
}

// SingBoxOutbound builds the native sing-box shadowsocks outbound. plugin /
// plugin_opts are emitted only when a SIP003 plugin is present.
func (c *ShadowsocksConfig) SingBoxOutbound(tag string) (map[string]any, error) {
	ob := map[string]any{
		"type":        "shadowsocks",
		"tag":         tag,
		"server":      c.Address,
		"server_port": c.Port,
		"method":      c.Method,
		"password":    c.Password,
	}
	if c.Plugin != "" {
		ob["plugin"] = c.Plugin
		ob["plugin_opts"] = c.PluginOpts
	}
	return ob, nil
}

// XrayOutbound builds the xray shadowsocks outbound (servers[] ServerObject +
// an empty tcp streamSettings for the engine to inject its sockopt into). A
// SIP003 plugin node returns ok=false — xray's plugin mechanism is incompatible,
// so such a node is sing-box-only.
func (c *ShadowsocksConfig) XrayOutbound() (map[string]any, bool, error) {
	if c.Plugin != "" {
		return nil, false, nil
	}
	ob := map[string]any{
		"protocol": "shadowsocks",
		"settings": map[string]any{
			"servers": []any{map[string]any{
				"address":  c.Address,
				"port":     c.Port,
				"method":   c.Method,
				"password": c.Password,
			}},
		},
		"streamSettings": map[string]any{"network": "tcp"},
	}
	return ob, true, nil
}

// --- share-link parsing -----------------------------------------------------

// parseShadowsocks parses an ss:// share link, handling both encodings:
//
//	SIP002:  ss://<base64url(method:password)>@host:port[?plugin=…]#name
//	         (userinfo may also be a literal percent-encoded method:password)
//	legacy:  ss://<base64(method:password@host:port)>#name
//
// The display name is the percent-decoded fragment (default "New Shadowsocks").
func parseShadowsocks(u *url.URL) (*ShadowsocksConfig, error) {
	c := &ShadowsocksConfig{ServerName: "New Shadowsocks"}
	if u.Fragment != "" {
		c.ServerName = fragmentName(u.EscapedFragment())
	}

	// Form discriminator: the SIP002 link has a literal '@' (so url.Parse sets
	// u.User); the legacy link is one base64 token with no '@', which url.Parse
	// stores whole in u.Host (it sees no real ':port'). A missing '@' ⇒ legacy.
	if u.User == nil {
		return parseShadowsocksLegacy(u, c)
	}

	c.Address = u.Hostname()
	if err := setShadowsocksPort(c, u.Port()); err != nil {
		return nil, err
	}
	// u.User is non-nil here (the '@' discriminator above). url.Parse only sets
	// the userinfo password when the literal userinfo carried a ':'.
	rawPass, _ := u.User.Password()
	method, password := decodeUserinfo(u.User.Username(), rawPass)
	if method == "" {
		return nil, fmt.Errorf("shadowsocks URL is missing method")
	}
	c.Method, c.Password = method, password

	q := u.Query()
	if plugin := q.Get("plugin"); plugin != "" {
		c.Plugin = plugin
		c.PluginOpts = q.Get("plugin_opts")
	}
	return c, nil
}

// decodeUserinfo resolves the SIP002 userinfo into (method, password). The
// userinfo is either base64(method:password) — try that first, in any of the
// std/url-safe, padded/unpadded variants — or already a literal method:password
// (then url.Parse split it into Username/Password on the first ':').
func decodeUserinfo(username, password string) (method, pass string) {
	// url.Parse only populates Password when the literal userinfo contained a
	// ':'. A base64 blob has no ':' (it is a single token in Username), so when
	// password == "" we attempt a base64 decode of the whole token.
	if password == "" {
		if dec, ok := decodeBase64(username); ok {
			if m, p, found := strings.Cut(dec, ":"); found {
				return m, p
			}
		}
	}
	return username, password
}

// parseShadowsocksLegacy handles the legacy fully-base64 form: the entire body
// before '#' is base64 of "method:password@host:port". url.Parse leaves it in
// u.Opaque (no "//") or u.Host — recover the raw token and decode it.
func parseShadowsocksLegacy(u *url.URL, c *ShadowsocksConfig) (*ShadowsocksConfig, error) {
	token := legacyBody(u)
	dec, ok := decodeBase64(token)
	if !ok {
		return nil, fmt.Errorf("shadowsocks URL is missing host")
	}
	// dec == "method:password@host:port" — split on the LAST '@' (a password
	// could in theory contain '@').
	at := strings.LastIndex(dec, "@")
	if at < 0 {
		return nil, fmt.Errorf("legacy shadowsocks link missing @host:port")
	}
	cred, endpoint := dec[:at], dec[at+1:]
	method, password, found := strings.Cut(cred, ":")
	if !found || method == "" {
		return nil, fmt.Errorf("legacy shadowsocks link missing method:password")
	}
	host, port, err := splitHostPort(endpoint)
	if err != nil {
		return nil, err
	}
	c.Address, c.Method, c.Password = host, method, password
	return c, setShadowsocksPort(c, port)
}

// legacyBody recovers the raw base64 token from an @-less ss:// URL. The token
// has no ':' (base64 alphabet), so url.Parse with the "//" authority stores it
// whole in u.Host; the rarer no-"//" shape lands it in u.Opaque. The fragment
// is already split off by url.Parse, so the token is just that text.
func legacyBody(u *url.URL) string {
	if u.Host != "" {
		return u.Host
	}
	return u.Opaque
}

// splitHostPort splits "host:port" (the decoded legacy endpoint) into its parts.
func splitHostPort(endpoint string) (host, port string, err error) {
	i := strings.LastIndex(endpoint, ":")
	if i < 0 {
		return "", "", fmt.Errorf("legacy shadowsocks endpoint missing :port")
	}
	host = strings.Trim(endpoint[:i], "[]") // strip IPv6 brackets if present
	port = endpoint[i+1:]
	if host == "" {
		return "", "", fmt.Errorf("legacy shadowsocks endpoint missing host")
	}
	return host, port, nil
}

// setShadowsocksPort parses the port (REQUIRED — Shadowsocks has no sensible
// default like VLESS's 443) into the config.
func setShadowsocksPort(c *ShadowsocksConfig, p string) error {
	if p == "" {
		return fmt.Errorf("shadowsocks URL is missing port")
	}
	port, err := strconv.ParseUint(p, 10, 16)
	if err != nil {
		return fmt.Errorf("invalid port %q: %w", p, err)
	}
	c.Port = uint16(port)
	return nil
}

// decodeBase64 tries the four base64 variants share links use — std/url-safe ×
// padded/raw — and reports ok only when one yields valid UTF-8-ish bytes.
func decodeBase64(s string) (string, bool) {
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding,
		base64.URLEncoding, base64.RawURLEncoding,
	} {
		if b, err := enc.DecodeString(s); err == nil {
			return string(b), true
		}
	}
	return "", false
}
