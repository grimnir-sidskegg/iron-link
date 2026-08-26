// The Clash/mihomo dialect: a YAML document with a top-level `proxies:` list
// of per-node maps — a full config and a bare proxy-provider file share the
// key. Each proxies[] element becomes one rawEntry whose link is the standard
// share URI for that protocol (the chain then funnels it through
// proxy.ParseURL like every other dialect).
//
// Field coverage is bounded by what our share-link parsers express, not by
// what clash can say:
//
//   - ss: cipher/password; a SIP003 plugin converts to the plugin/plugin_opts
//     params shadowsocks.go reads, but only for the two plugins the embedded
//     sing-box implements (clash "obfs" = the obfs-local binary,
//     "v2ray-plugin" verbatim) — anything else (shadow-tls, restls, …) has no
//     usable form and the node is skipped;
//   - vmess/vless/trojan: the shared tcp/ws/grpc stream model; an h2/http/
//     quic network has no transport in stream.go and the node is skipped.
//     skip-cert-verify has no field in their TLS params and is dropped;
//   - vmess + reality is unconstructable from the v2rayN link shape (no
//     pbk/sid) and parseVmessURL rejects it — such a node counts unrecognized;
//   - hysteria2: obfs/obfs-password pass through (hysteria2.go speaks
//     salamander); alpn has no field in Hysteria2Config and is dropped;
//   - tuic / anytls: direct query-param mapping;
//   - any other proxy type (snell, wireguard, …) yields zero links and is
//     counted unrecognized upstream.
package subscription

import (
	"encoding/base64"
	"encoding/json"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"ironlink/daemon/internal/store"
)

// clashReserved are the built-in policy targets a group member name can be —
// never a real node, so they are dropped from a group's membership. Matched
// case-SENSITIVELY: these are mihomo's exact reserved outbound names, so a user
// proxy named "direct" or "Global" is a distinct, valid node, not a policy.
var clashReserved = map[string]bool{
	"DIRECT": true, "REJECT": true, "REJECT-DROP": true,
	"PASS": true, "GLOBAL": true, "COMPATIBLE": true,
}

// parseClash converts a Clash/mihomo YAML subscription into share links and
// url-test groups. ok=false when the body is not the dialect: it must
// YAML-decode AND yield a `proxies` sequence with at least one map carrying both
// "type" and "server" (a mere decode is no signal — any JSON body is also valid
// YAML).
//
// proxy-groups is decoded as loose maps (NOT a typed struct): providers quote
// scalars freely (interval: "300", include-all: "true"), and a typed decode
// would fail the WHOLE body on the first such mismatch — even in a group type we
// never extract — silently freezing the subscription. The tolerant readers
// (clashInt/clashBool) coerce the shapes, exactly as the node path does.
func parseClash(body string) ([]rawEntry, []rawGroup, bool) {
	var doc struct {
		Proxies     []map[string]any `yaml:"proxies"`
		ProxyGroups []map[string]any `yaml:"proxy-groups"`
	}
	if err := yaml.Unmarshal([]byte(body), &doc); err != nil {
		return nil, nil, false
	}
	structural := false
	for _, p := range doc.Proxies {
		_, hasType := p["type"]
		_, hasServer := p["server"]
		if hasType && hasServer {
			structural = true
			break
		}
	}
	if !structural {
		return nil, nil, false
	}
	entries := make([]rawEntry, 0, len(doc.Proxies))
	for _, p := range doc.Proxies {
		entries = append(entries, rawEntry{links: clashLinks(p)})
	}
	return entries, clashGroups(doc.Proxies, doc.ProxyGroups), true
}

// clashGroups builds a rawGroup per url-test proxy-group. Membership is the
// group's explicit `proxies` (kept verbatim — mihomo does NOT filter these) plus
// (with include-all / include-all-proxies) every convertible proxy SUBJECT to
// filter (keep) then exclude-filter (drop); nested group refs, reserved
// policies, the group's own name, and unconvertible nodes are always dropped.
// mihomo compiles filters with a lookaround-capable engine; Go's RE2 cannot, so
// a pattern that will not compile drops the WHOLE group (importing it with wrong
// membership would be worse than not importing it).
func clashGroups(proxies, groups []map[string]any) []rawGroup {
	linkByName := map[string]string{}
	var allNames []string
	for _, p := range proxies {
		name := clashString(p, "name")
		links := clashLinks(p)
		if name == "" || len(links) == 0 {
			continue
		}
		if _, dup := linkByName[name]; !dup {
			allNames = append(allNames, name)
		}
		linkByName[name] = links[0]
	}
	groupNames := map[string]bool{}
	for _, g := range groups {
		if n := clashString(g, "name"); n != "" {
			groupNames[n] = true
		}
	}

	var out []rawGroup
	for _, g := range groups {
		if clashString(g, "type") != "url-test" {
			continue
		}
		name := clashString(g, "name")
		if name == "" {
			continue
		}
		var filterRe, excludeRe *regexp.Regexp
		var err error
		if f := clashString(g, "filter"); f != "" {
			if filterRe, err = regexp.Compile(f); err != nil {
				continue // RE2 cannot express this filter — drop the group
			}
		}
		if ef := clashString(g, "exclude-filter"); ef != "" {
			if excludeRe, err = regexp.Compile(ef); err != nil {
				continue
			}
		}

		var links []string
		picked := map[string]bool{}
		addMember := func(n string, filtered bool) {
			if picked[n] || n == name || groupNames[n] || clashReserved[n] {
				return
			}
			link, ok := linkByName[n]
			if !ok {
				return // a name we could not convert to a node
			}
			// filter/exclude-filter apply only to the include-all set; an
			// explicitly listed proxy is always kept (mihomo semantics).
			if filtered {
				if filterRe != nil && !filterRe.MatchString(n) {
					return
				}
				if excludeRe != nil && excludeRe.MatchString(n) {
					return
				}
			}
			picked[n] = true
			links = append(links, link)
		}
		for _, n := range clashStrings(g, "proxies") {
			addMember(n, false)
		}
		if clashBool(g, "include-all") || clashBool(g, "include-all-proxies") {
			for _, n := range allNames {
				addMember(n, true)
			}
		}
		if len(links) == 0 {
			continue
		}
		tolerance := clashInt(g, "tolerance")
		if tolerance < 0 {
			tolerance = 0
		} else if tolerance > 65535 {
			tolerance = 65535
		}
		out = append(out, rawGroup{
			name:  name,
			links: links,
			probe: store.GroupProbe{
				URL:         clashString(g, "url"),
				IntervalSec: uint32(max(clashInt(g, "interval"), 0)),
				Tolerance:   uint16(tolerance),
			},
		})
	}
	return out
}

// clashLinks converts one proxies[] map into its share link (nil when the
// type, or a detail of the node, is beyond what our parsers express).
func clashLinks(p map[string]any) []string {
	host := clashString(p, "server")
	port := clashPort(p, "port")
	name := clashString(p, "name")
	if host == "" || port == 0 {
		return nil
	}
	switch clashString(p, "type") {
	case "ss":
		return clashSS(p, host, port, name)
	case "vmess":
		return clashVmess(p, host, port, name)
	case "vless":
		return clashVless(p, host, port, name)
	case "trojan":
		return clashTrojan(p, host, port, name)
	case "hysteria2":
		return clashHysteria2(p, host, port, name)
	case "tuic":
		return clashTUIC(p, host, port, name)
	case "anytls":
		return clashAnyTLS(p, host, port, name)
	}
	return nil
}

// --- per-type converters ------------------------------------------------------

func clashSS(p map[string]any, host string, port uint16, name string) []string {
	method := clashString(p, "cipher")
	password := clashString(p, "password")
	if method == "" {
		return nil
	}
	var pluginName, pluginOpts string
	if plugin := clashString(p, "plugin"); plugin != "" {
		var ok bool
		pluginName, pluginOpts, ok = clashSIP003(plugin, clashMap(p, "plugin-opts"))
		if !ok {
			return nil
		}
	}
	return []string{ssLink(method, password, host, port, pluginName, pluginOpts, name)}
}

// clashSIP003 converts the clash plugin declaration (a name + an opts MAP)
// into the (plugin, plugin_opts) pair our ss:// parser reads. Only the two
// SIP003 plugins the embedded sing-box implements are convertible; ok=false
// for anything else — an unknown plugin name would fail engine start, so the
// node is skipped rather than stored broken.
func clashSIP003(plugin string, opts map[string]any) (name, optsStr string, ok bool) {
	var parts []string
	switch plugin {
	case "obfs": // clash's name for the obfs-local binary (simple-obfs)
		if mode := clashString(opts, "mode"); mode != "" {
			parts = append(parts, "obfs="+mode)
		}
		if host := clashString(opts, "host"); host != "" {
			parts = append(parts, "obfs-host="+host)
		}
		return "obfs-local", strings.Join(parts, ";"), true
	case "v2ray-plugin":
		// The SIP003 args mirror the clash opts keys; websocket is the
		// plugin's default mode so it is elided. Booleans become bare flags.
		if mode := clashString(opts, "mode"); mode != "" && mode != "websocket" {
			parts = append(parts, "mode="+mode)
		}
		if clashBool(opts, "tls") {
			parts = append(parts, "tls")
		}
		if host := clashString(opts, "host"); host != "" {
			parts = append(parts, "host="+host)
		}
		if path := clashString(opts, "path"); path != "" {
			parts = append(parts, "path="+path)
		}
		return "v2ray-plugin", strings.Join(parts, ";"), true
	}
	return "", "", false
}

func clashVmess(p map[string]any, host string, port uint16, name string) []string {
	uuid := clashString(p, "uuid")
	if uuid == "" {
		return nil
	}
	// The v2rayN vmess://base64(JSON) body — the shape parseVmessURL reads.
	link := map[string]any{
		"v":    "2",
		"ps":   name,
		"add":  host,
		"port": int(port),
		"id":   uuid,
		"aid":  clashInt(p, "alterId"),
		"scy":  clashString(p, "cipher"),
	}
	switch clashString(p, "network") {
	case "", "tcp":
		link["net"] = "tcp"
	case "ws":
		ws := clashMap(p, "ws-opts")
		link["net"] = "ws"
		link["path"] = clashString(ws, "path")
		link["host"] = clashHostHeader(clashMap(ws, "headers"))
	case "grpc":
		link["net"] = "grpc"
		link["path"] = clashString(clashMap(p, "grpc-opts"), "grpc-service-name")
	default: // h2 / http / quic — no such transport in stream.go
		return nil
	}
	if clashBool(p, "tls") {
		link["tls"] = "tls"
		link["sni"] = clashString(p, "servername")
		link["fp"] = clashString(p, "client-fingerprint")
	}
	b, err := json.Marshal(link)
	if err != nil {
		return nil
	}
	return []string{"vmess://" + base64.StdEncoding.EncodeToString(b)}
}

func clashVless(p map[string]any, host string, port uint16, name string) []string {
	uuid := clashString(p, "uuid")
	if uuid == "" {
		return nil
	}
	q := url.Values{}
	if flow := clashString(p, "flow"); flow != "" {
		q.Set("flow", flow)
	}
	clashV2raySecurity(p, q, clashString(p, "servername"), false)
	if !clashStreamQuery(p, q) {
		return nil
	}
	return []string{shareLink("vless", url.User(uuid).String(), host, port, q, name)}
}

func clashTrojan(p map[string]any, host string, port uint16, name string) []string {
	password := clashString(p, "password")
	if password == "" {
		return nil
	}
	q := url.Values{}
	// Trojan is TLS by definition (clash spells the front `sni`, not
	// `servername`); security=tls must be explicit for the link parser to
	// read the sni param.
	clashV2raySecurity(p, q, clashString(p, "sni"), true)
	if !clashStreamQuery(p, q) {
		return nil
	}
	return []string{shareLink("trojan", url.User(password).String(), host, port, q, name)}
}

func clashHysteria2(p map[string]any, host string, port uint16, name string) []string {
	q := url.Values{}
	if sni := clashString(p, "sni"); sni != "" {
		q.Set("sni", sni)
	}
	if clashBool(p, "skip-cert-verify") {
		q.Set("insecure", "1")
	}
	if obfs := clashString(p, "obfs"); obfs != "" {
		q.Set("obfs", obfs)
		q.Set("obfs-password", clashString(p, "obfs-password"))
	}
	userinfo := ""
	if password := clashString(p, "password"); password != "" {
		userinfo = url.User(password).String()
	}
	return []string{shareLink("hysteria2", userinfo, host, port, q, name)}
}

func clashTUIC(p map[string]any, host string, port uint16, name string) []string {
	uuid := clashString(p, "uuid")
	if uuid == "" {
		return nil
	}
	q := url.Values{}
	if sni := clashString(p, "sni"); sni != "" {
		q.Set("sni", sni)
	}
	if cc := clashString(p, "congestion-controller"); cc != "" {
		q.Set("congestion_control", cc)
	}
	if mode := clashString(p, "udp-relay-mode"); mode != "" {
		q.Set("udp_relay_mode", mode)
	}
	if alpn := clashStrings(p, "alpn"); len(alpn) > 0 {
		q.Set("alpn", strings.Join(alpn, ","))
	}
	if clashBool(p, "skip-cert-verify") {
		q.Set("allow_insecure", "1")
	}
	userinfo := url.UserPassword(uuid, clashString(p, "password")).String()
	return []string{shareLink("tuic", userinfo, host, port, q, name)}
}

func clashAnyTLS(p map[string]any, host string, port uint16, name string) []string {
	password := clashString(p, "password")
	if password == "" {
		return nil
	}
	q := url.Values{}
	if sni := clashString(p, "sni"); sni != "" {
		q.Set("sni", sni)
	}
	if clashBool(p, "skip-cert-verify") {
		q.Set("insecure", "1")
	}
	return []string{shareLink("anytls", url.User(password).String(), host, port, q, name)}
}

// --- shared stream/security emit ---------------------------------------------

// clashV2raySecurity emits the security query params for a V2Ray-family
// proxy. reality-opts wins over the tls flag (mihomo reality nodes carry
// both); all four reality params are emitted even when empty because the link
// parsers require their PRESENCE. alwaysTLS marks trojan, TLS by definition.
func clashV2raySecurity(p map[string]any, q url.Values, sni string, alwaysTLS bool) {
	if r := clashMap(p, "reality-opts"); r != nil {
		q.Set("security", "reality")
		q.Set("sni", sni)
		q.Set("fp", clashString(p, "client-fingerprint"))
		q.Set("pbk", clashString(r, "public-key"))
		q.Set("sid", clashString(r, "short-id"))
		return
	}
	if alwaysTLS || clashBool(p, "tls") {
		q.Set("security", "tls")
		if sni != "" {
			q.Set("sni", sni)
		}
		if fp := clashString(p, "client-fingerprint"); fp != "" {
			q.Set("fp", fp)
		}
	}
}

// clashStreamQuery emits the type=ws/grpc transport params for a V2Ray-family
// proxy (vless/trojan; vmess carries them inside its JSON body instead).
// false when the network has no transport in stream.go (h2/http/quic).
func clashStreamQuery(p map[string]any, q url.Values) bool {
	switch clashString(p, "network") {
	case "", "tcp":
	case "ws":
		ws := clashMap(p, "ws-opts")
		q.Set("type", "ws")
		if path := clashString(ws, "path"); path != "" {
			q.Set("path", path)
		}
		if host := clashHostHeader(clashMap(ws, "headers")); host != "" {
			q.Set("host", host)
		}
	case "grpc":
		q.Set("type", "grpc")
		if svc := clashString(clashMap(p, "grpc-opts"), "grpc-service-name"); svc != "" {
			q.Set("serviceName", svc)
		}
	default:
		return false
	}
	return true
}

// --- link assembly (shared with the SIP008 converter) -------------------------

// shareLink assembles scheme://userinfo@host:port?query#name. The caller
// escapes the userinfo (url.User(…).String()); the fragment is QueryEscaped
// because fragmentName decodes it with query semantics — that round-trips
// spaces, '&'/'=' and multi-byte names (emoji) exactly.
func shareLink(scheme, userinfo, host string, port uint16, q url.Values, name string) string {
	var b strings.Builder
	b.WriteString(scheme)
	b.WriteString("://")
	if userinfo != "" {
		b.WriteString(userinfo)
		b.WriteByte('@')
	}
	b.WriteString(net.JoinHostPort(host, strconv.Itoa(int(port))))
	if len(q) > 0 {
		b.WriteByte('?')
		b.WriteString(q.Encode())
	}
	if name != "" {
		b.WriteByte('#')
		b.WriteString(url.QueryEscape(name))
	}
	return b.String()
}

// ssLink assembles the SIP002 ss:// form shadowsocks.go parses: userinfo =
// base64url(method:password), the plugin as the plugin/plugin_opts params.
func ssLink(method, password, host string, port uint16, plugin, pluginOpts, name string) string {
	q := url.Values{}
	if plugin != "" {
		q.Set("plugin", plugin)
		if pluginOpts != "" {
			q.Set("plugin_opts", pluginOpts)
		}
	}
	userinfo := base64.RawURLEncoding.EncodeToString([]byte(method + ":" + password))
	return shareLink("ss", userinfo, host, port, q, name)
}

// --- tolerant map readers -----------------------------------------------------
// yaml.v3 decodes scalars into string/int/bool by tag, and providers are loose
// about quoting (a numeric password, a quoted port), so every reader accepts
// the plausible YAML shapes for its field.

// clashString reads a scalar as its string form ("" when absent or a
// non-scalar).
func clashString(m map[string]any, key string) string {
	return clashScalar(m[key])
}

// clashScalar renders one decoded YAML scalar as a string.
func clashScalar(v any) string {
	switch v := v.(type) {
	case string:
		return v
	case int:
		return strconv.Itoa(v)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	}
	return ""
}

// clashPort reads a port (0 when absent or out of range).
func clashPort(m map[string]any, key string) uint16 {
	switch v := m[key].(type) {
	case int:
		if v > 0 && v <= 65535 {
			return uint16(v)
		}
	case string:
		if n, err := strconv.ParseUint(v, 10, 16); err == nil {
			return uint16(n)
		}
	}
	return 0
}

// clashInt reads an integer (0 when absent).
func clashInt(m map[string]any, key string) int {
	switch v := m[key].(type) {
	case int:
		return v
	case string:
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return 0
}

// clashBool reads a boolean flag (false when absent).
func clashBool(m map[string]any, key string) bool {
	switch v := m[key].(type) {
	case bool:
		return v
	case string:
		return v == "true" || v == "1"
	}
	return false
}

// clashMap reads a nested mapping (nil when absent — the readers above all
// tolerate a nil map).
func clashMap(m map[string]any, key string) map[string]any {
	v, _ := m[key].(map[string]any)
	return v
}

// clashStrings reads a sequence of scalars ([] when absent).
func clashStrings(m map[string]any, key string) []string {
	seq, _ := m[key].([]any)
	out := make([]string, 0, len(seq))
	for _, item := range seq {
		if s := clashScalar(item); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// clashHostHeader reads the ws Host header, tolerating both spellings.
func clashHostHeader(headers map[string]any) string {
	if h := clashString(headers, "Host"); h != "" {
		return h
	}
	return clashString(headers, "host")
}
