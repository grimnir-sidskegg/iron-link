// Hysteria2 (hysteria2:// / hy2://) — a QUIC protocol with its OWN wire shape
// (stream.go is not used). Both cores dial it: sing-box natively (the default,
// TUN engine) and xray through its native "hysteria" outbound, reached by a
// core override. The one asymmetry: xray removed allowInsecure, so a
// self-signed server is xray-dialable only with a certificate pin (pinSHA256),
// which sing-box cannot express and honours as insecure. The file mirrors
// shadowsocks.go, the other dual-core non-stream protocol; it implements
// Profile and registers itself from an init().
//
// Share-link scheme: hysteria2:// (alias hy2://), per the official Hysteria2
// URI scheme — https://v2.hysteria.network/docs/developers/URI-Scheme/
// sing-box outbound shape per the v1.12 hysteria2 outbound doc —
// https://sing-box.sagernet.org/configuration/outbound/hysteria2/
package proxy

import (
	"cmp"
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
	// PinSHA256 is the server certificate's SHA-256 (URI pinSHA256: hex, colons
	// allowed), kept verbatim — xray strips colons and hex-decodes at build.
	PinSHA256 string `json:"pin_sha256,omitempty"`
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

// dialableBy: sing-box always; xray unless the node is insecure without a pin
// (xray has no allowInsecure any more — a pin is its only way past a
// self-signed certificate).
func (c *Hysteria2Config) dialableBy(core api.CoreType) bool {
	switch core {
	case api.CoreSingBox:
		return true
	case api.CoreXray:
		return !c.Insecure || c.PinSHA256 != ""
	default:
		return false
	}
}

// SingBoxOutbound builds the native sing-box hysteria2 outbound. tls is always
// enabled (Hysteria2 is QUIC/TLS); server_name is omitted when SNI is empty so
// sing-box falls back to the dialed host. The salamander obfs block is added
// only when Obfs is set. No fwmark/sockopt — the engine adds own-traffic marks.
func (c *Hysteria2Config) SingBoxOutbound(tag string) (map[string]any, error) {
	tls := map[string]any{
		"enabled": true,
		// sing-box has no certificate-hash pin (certificate_public_key_sha256 is
		// an SPKI hash, a different value), so a pinned node is dialed insecure —
		// the reference client's pin-only verification, as panels emit it.
		"insecure": c.Insecure || c.PinSHA256 != "",
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

// XrayOutbound builds xray's native hysteria (v2) outbound. serverName is
// always set: the hysteria dialer never derives it from the destination and
// its http3 auth request would otherwise send the literal SNI "hysteria". No
// alpn: http3 forces h3 regardless. Salamander rides finalmask.udp; an unknown
// obfs type is rejected by xray at build, as by sing-box. quicParams stay at
// xray's defaults (BBR).
func (c *Hysteria2Config) XrayOutbound() (map[string]any, bool, error) {
	if !c.dialableBy(api.CoreXray) {
		return nil, false, nil
	}
	tls := map[string]any{"serverName": cmp.Or(c.SNI, c.Address)}
	if c.PinSHA256 != "" {
		tls["pinnedPeerCertSha256"] = c.PinSHA256
	}
	stream := map[string]any{
		"network":          "hysteria",
		"security":         "tls",
		"tlsSettings":      tls,
		"hysteriaSettings": map[string]any{"version": 2, "auth": c.Password},
	}
	if c.Obfs != "" {
		stream["finalmask"] = map[string]any{"udp": []any{map[string]any{
			"type":     c.Obfs,
			"settings": map[string]any{"password": c.ObfsPassword},
		}}}
	}
	return map[string]any{
		"protocol":       "hysteria",
		"settings":       map[string]any{"address": c.Address, "port": c.Port, "version": 2},
		"streamSettings": stream,
	}, true, nil
}

// --- share-link parsing -----------------------------------------------------

// parseHysteria2 parses a hysteria2:// (or hy2://) share link: the auth
// credential is the URI userinfo (percent-decoded), the host is required, the
// port defaults to 443, the display name is the fragment, and sni/insecure/
// obfs/obfs-password/pinSHA256 come from the query, per the official URI
// scheme. The panel fm= (finalmask JSON) and the ecosystem mport are ignored:
// obfs-password already carries the salamander key and nothing here models the
// rest.
func parseHysteria2(u *url.URL) (*Hysteria2Config, error) {
	c := &Hysteria2Config{
		ServerName: "New Hysteria2",
		Port:       443,
	}

	if u.User != nil {
		// The auth string is the whole userinfo; url.Parse splits "user:pass" on
		// the first ':' and the reference client joins it back.
		c.Password = u.User.Username()
		if pass, ok := u.User.Password(); ok {
			c.Password += ":" + pass
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
	c.PinSHA256 = q.Get("pinSHA256")

	return c, nil
}

// parseHy2Bool reads the URI scheme's boolean query values: "1"/"true" → true,
// everything else (incl. "0"/"") → false.
func parseHy2Bool(v string) bool {
	return v == "1" || v == "true"
}
