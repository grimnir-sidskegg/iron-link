// The SIP008 dialect — the Shadowsocks online-config standard: a JSON object
// {"version": 1, "servers": [{"remarks", "server", "server_port", "password",
// "method", "plugin", "plugin_opts", …}]}. Each servers[] element becomes one
// rawEntry carrying the SIP002 ss:// form of that server (built by ssLink,
// shared with the clash converter), remarks in the fragment.
//
// Plugin decision: shadowsocks.go round-trips plugin/plugin_opts (such a node
// is sing-box-only via dialableBy), and SIP008 already carries them as SIP003
// strings, so a plugin server passes through verbatim — but only for the two
// plugins the embedded sing-box implements (obfs-local, v2ray-plugin). An
// unknown plugin binary would fail engine start, so that server is skipped
// (zero links → counted unrecognized upstream).
package subscription

import (
	"encoding/json"
	"strconv"
)

// parseSIP008 converts a SIP008 document into ss:// share links. ok=false
// when the body is not the dialect: it must decode as a JSON OBJECT whose
// "servers" array has at least one element carrying both "server" and
// "server_port" (a mere decode is no signal — see the package comment).
func parseSIP008(body string) ([]rawEntry, bool) {
	var doc struct {
		Servers []map[string]any `json:"servers"`
	}
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		return nil, false
	}
	structural := false
	for _, s := range doc.Servers {
		_, hasServer := s["server"]
		_, hasPort := s["server_port"]
		if hasServer && hasPort {
			structural = true
			break
		}
	}
	if !structural {
		return nil, false
	}
	entries := make([]rawEntry, 0, len(doc.Servers))
	for _, s := range doc.Servers {
		entries = append(entries, rawEntry{links: sip008Links(s)})
	}
	return entries, true
}

// sip008Links converts one servers[] element into its ss:// link (nil when a
// required field is missing or the plugin is unsupported).
func sip008Links(s map[string]any) []string {
	host := sipString(s, "server")
	port := sipPort(s, "server_port")
	method := sipString(s, "method")
	if host == "" || port == 0 || method == "" {
		return nil
	}
	var plugin, pluginOpts string
	if plugin = sipString(s, "plugin"); plugin != "" {
		if plugin != "obfs-local" && plugin != "v2ray-plugin" {
			return nil
		}
		pluginOpts = sipString(s, "plugin_opts")
	}
	return []string{ssLink(method, sipString(s, "password"), host, port, plugin, pluginOpts, sipString(s, "remarks"))}
}

// sipString reads a string field ("" when absent or not a string).
func sipString(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}

// sipPort reads server_port, tolerating the JSON number the spec mandates and
// the quoted string some exporters emit (0 when absent or out of range).
func sipPort(m map[string]any, key string) uint16 {
	switch v := m[key].(type) {
	case float64:
		if v == float64(uint16(v)) && v > 0 {
			return uint16(v)
		}
	case string:
		if n, err := strconv.ParseUint(v, 10, 16); err == nil {
			return uint16(n)
		}
	}
	return 0
}
