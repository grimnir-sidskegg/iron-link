// This file is Hysteria2 — a QUIC-based protocol that, unlike the V2Ray family
// (vless/vmess/trojan), has its OWN wire shape: it does not use the shared
// stream model (stream.go) and is sing-box-only (xray cannot dial it). It
// implements Profile and registers itself from an init(), mirroring vless.go.
//
// Share-link scheme: hysteria2:// (alias hy2://), per the official Hysteria2
// URI scheme — https://v2.hysteria.network/docs/developers/URI-Scheme/
// sing-box outbound shape per the v1.12 hysteria2 outbound doc —
// https://sing-box.sagernet.org/configuration/outbound/hysteria2/
package proxy

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"

	"ironlink/daemon/internal/api"
)

const ProtocolHysteria2 Protocol = "hysteria2"

// Hysteria2Config is a parsed Hysteria2 node. Password is the auth credential
// (carried as URI userinfo). Obfs, when "salamander", wraps the QUIC packets in
// the salamander obfuscation keyed by ObfsPassword. The json tags ARE the
// on-disk profile body (the {"hysteria2": …} arm of the store union).
type Hysteria2Config struct {
	ServerName   string `json:"server_name"`
	Address      string `json:"address"`
	Port         uint16 `json:"port"`
	Password     string `json:"password"`
	SNI          string `json:"sni,omitempty"`
	Insecure     bool   `json:"insecure,omitempty"`
	Obfs         string `json:"obfs,omitempty"`
	ObfsPassword string `json:"obfs_password,omitempty"`
}

func init() {
	registerProtocol(ProtocolHysteria2, []string{"hysteria2", "hy2"}, codec{
		parse: func(u *url.URL) (Profile, error) { return parseHysteria2(u) },
		decode: func(raw json.RawMessage) (Profile, error) {
			var c Hysteria2Config
			if err := json.Unmarshal(raw, &c); err != nil {
				return nil, err
			}
			return &c, nil
		},
	})
}

// --- Profile ----------------------------------------------------------------

func (c *Hysteria2Config) isProfile()             {}
func (c *Hysteria2Config) DisplayName() string    { return c.ServerName }
func (c *Hysteria2Config) Kind() Protocol         { return ProtocolHysteria2 }
func (c *Hysteria2Config) ServerAddress() string  { return c.Address }
func (c *Hysteria2Config) ServerPort() uint16     { return c.Port }
func (c *Hysteria2Config) Identity() string       { return c.Password }
func (c *Hysteria2Config) TransportLabel() string { return "quic" }
func (c *Hysteria2Config) SecurityLabel() string  { return "tls" }
func (c *Hysteria2Config) ServerNetwork() string  { return "udp" }

// CamouflageSNI: Hysteria2 always rides TLS over QUIC, so it always bears a
// probeable front — the SNI it presents (empty SNI still probes the bare host).
func (c *Hysteria2Config) CamouflageSNI() (string, bool) { return c.SNI, true }

// dialableBy: only the embedded sing-box can dial Hysteria2; xray has no
// hysteria2 outbound, so it (and any unknown core) cannot.
func (c *Hysteria2Config) dialableBy(core api.CoreType) bool {
	return core == api.CoreSingBox
}

// SingBoxOutbound builds the native sing-box hysteria2 outbound. tls is always
// enabled (Hysteria2 is QUIC/TLS); server_name is omitted when SNI is empty so
// sing-box falls back to the dialed host. The salamander obfs block is added
// only when Obfs is set. No fwmark/sockopt — the engine adds own-traffic marks.
func (c *Hysteria2Config) SingBoxOutbound(tag string) (map[string]any, error) {
	tls := map[string]any{
		"enabled":  true,
		"insecure": c.Insecure,
	}
	if c.SNI != "" {
		tls["server_name"] = c.SNI
	}
	ob := map[string]any{
		"type":        "hysteria2",
		"tag":         tag,
		"server":      c.Address,
		"server_port": c.Port,
		"password":    c.Password,
		"tls":         tls,
	}
	if c.Obfs != "" {
		// Pass the parsed obfs type through (salamander is the only one sing-box
		// supports today; let it reject an unknown type rather than us coercing).
		ob["obfs"] = map[string]any{
			"type":     c.Obfs,
			"password": c.ObfsPassword,
		}
	}
	return ob, nil
}

// XrayOutbound: xray has no Hysteria2 outbound — ok=false routes the node to
// sing-box (selection guarantees we are never asked to xray-dial it).
func (c *Hysteria2Config) XrayOutbound() (map[string]any, bool, error) {
	return nil, false, nil
}

// --- share-link parsing -----------------------------------------------------

// parseHysteria2 parses a hysteria2:// (or hy2://) share link: the auth
// credential is the URI userinfo (percent-decoded), the host is required, the
// port defaults to 443, the display name is the fragment, and sni/obfs/
// obfs-password/insecure come from the query — per the official URI scheme.
func parseHysteria2(u *url.URL) (*Hysteria2Config, error) {
	c := &Hysteria2Config{
		ServerName: "New Hysteria2",
		Port:       443,
	}

	if u.User != nil {
		// userinfo is "<auth>" or "<user>:<pass>"; both halves percent-decode.
		// url.Parse already decodes Username()/Password(). The auth credential
		// is the password half if present, else the whole userinfo.
		if pass, ok := u.User.Password(); ok {
			c.Password = pass
		} else {
			c.Password = u.User.Username()
		}
	}

	c.Address = u.Hostname()
	if c.Address == "" {
		return nil, fmt.Errorf("Hysteria2 URL is missing host")
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
	c.Insecure = parseHy2Bool(q.Get("insecure")) || parseHy2Bool(q.Get("allowInsecure"))
	if obfs := q.Get("obfs"); obfs != "" {
		c.Obfs = obfs
		c.ObfsPassword = q.Get("obfs-password")
	}

	return c, nil
}

// parseHy2Bool reads the URI scheme's boolean query values: "1"/"true" → true,
// everything else (incl. "0"/"") → false.
func parseHy2Bool(v string) bool {
	return v == "1" || v == "true"
}
