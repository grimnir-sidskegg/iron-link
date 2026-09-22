// parseXray handles xray client-config subscriptions — the v2rayN "full
// profile" shape: a JSON array of complete per-node client configs (each with
// remarks + log/dns/inbounds/outbounds/routing), or a single such object.
// Each entry's PROXY outbounds become share links; a multi-proxy entry is a
// balancer config and every member is expanded — regional balancers carry
// servers that exist nowhere else in the document, and the cross-entry dedup
// upstream collapses the members that do repeat.
//
// Decoding strategy: the entry/outbound shells and the per-protocol "settings"
// use minimal local structs — xray-core's infra/conf settings types keep users
// as raw JSON and exist to Build() protobuf, so they decode nothing useful
// here — while "streamSettings" reuses conf.StreamConfig, the exact field list
// xray itself reads (tlsSettings/realitySettings/wsSettings/grpcSettings/
// xhttpSettings/hysteriaSettings). "mux" is never decoded; of "finalmask" only
// the hysteria salamander UDP mask is read (dial-critical) — its quicParams and
// other masks are per-client tuning.
package subscription

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"

	"github.com/xtls/xray-core/infra/conf"

	"ironlink/daemon/internal/store"
)

// xrayProfileEntry is one document element: the v2rayN display name, the
// outbounds, and — when the entry is a balancer config — the routing.balancers
// that select among them plus the observatory that probes them. Everything else
// (log/dns/inbounds, non-balancer routing) is client tuning.
type xrayProfileEntry struct {
	Remarks   string            `json:"remarks"`
	Outbounds []json.RawMessage `json:"outbounds"`
	Routing   struct {
		Balancers []xrayBalancer `json:"balancers"`
	} `json:"routing"`
	BurstObservatory json.RawMessage `json:"burstObservatory"`
	Observatory      json.RawMessage `json:"observatory"`
}

// xrayBalancer is one routing balancer: a tag plus the outbound-tag PREFIXES it
// selects among (xray matches a balancer selector by prefix, so "proxy" selects
// proxy, proxy-2, …).
type xrayBalancer struct {
	Tag      string   `json:"tag"`
	Selector []string `json:"selector"`
}

// xrayOutboundShell is the outbound envelope. Settings stays raw for the
// per-protocol decode; the stream block is xray's own config struct.
type xrayOutboundShell struct {
	Protocol string             `json:"protocol"`
	Tag      string             `json:"tag"`
	Settings json.RawMessage    `json:"settings"`
	Stream   *conf.StreamConfig `json:"streamSettings"`
}

// xrayNonProxy are outbound protocols that are routing plumbing, not nodes —
// silently dropped inside an entry (the entry's proxy outbounds still count).
var xrayNonProxy = map[string]bool{
	"freedom":   true,
	"blackhole": true,
	"dns":       true,
	"loopback":  true,
}

func parseXray(body string) ([]rawEntry, []rawGroup, bool) {
	trimmed := strings.TrimSpace(body)
	var raws []json.RawMessage
	switch {
	case strings.HasPrefix(trimmed, "["):
		if json.Unmarshal([]byte(trimmed), &raws) != nil {
			return nil, nil, false
		}
	case strings.HasPrefix(trimmed, "{"):
		if !json.Valid([]byte(trimmed)) {
			return nil, nil, false
		}
		raws = []json.RawMessage{json.RawMessage(trimmed)}
	default:
		return nil, nil, false
	}

	// Structural gate: at least one element must carry an outbound keyed by
	// "protocol". Outbounds keyed by "type" are a sing-box config, not ours —
	// refuse so the body reaches (or stays with) parseSingBox.
	docs := make([]xrayProfileEntry, len(raws))
	sawProtocol := false
	for i, raw := range raws {
		// A malformed element stays empty here and counts unrecognized below —
		// per-entry failures never abort the document.
		_ = json.Unmarshal(raw, &docs[i])
		for _, ob := range docs[i].Outbounds {
			var head struct {
				Protocol string `json:"protocol"`
				Type     string `json:"type"`
			}
			_ = json.Unmarshal(ob, &head)
			if head.Type != "" && head.Protocol == "" {
				return nil, nil, false
			}
			sawProtocol = sawProtocol || head.Protocol != ""
		}
	}
	if !sawProtocol {
		return nil, nil, false
	}

	entries := make([]rawEntry, 0, len(docs))
	var groups []rawGroup
	for i := range docs {
		entry, gs := xrayEntry(&docs[i])
		entries = append(entries, entry)
		groups = append(groups, gs...)
	}
	return entries, groups, true
}

// pendingLink is a converted outbound awaiting its display name (the name
// depends on how many links the whole entry produced). Exactly one of frag /
// vmessBody is set: frag is the fragmentless link body (shareLink with an
// empty name) for fragment-named schemes; vmessBody is the v2rayN vmess JSON
// whose "ps" field carries the name instead.
type pendingLink struct {
	addr      string
	tag       string
	frag      string
	vmessBody map[string]any
}

func (p *pendingLink) finish(name string) string {
	if p.vmessBody != nil {
		p.vmessBody["ps"] = name
		return encodeVmessLink(p.vmessBody)
	}
	// QueryEscape, matching shareLink: fragmentName decodes with query
	// semantics, which round-trips '&'/'='/'+' and multi-byte names exactly.
	return p.frag + "#" + url.QueryEscape(name)
}

// xrayEntry reduces one profile entry to links, and — when the entry is a
// balancer config — to the auto-select group(s) over its members. Name
// precedence: remarks, then the outbound tag, then the address; a multi-link
// entry (balancer) suffixes each name with its member address so the nodes stay
// distinguishable. A balancer entry emits its links AS WELL AS the group: the
// members are ordinary nodes (and regional balancers carry servers found
// nowhere else in the document), so node expansion is never skipped.
func xrayEntry(doc *xrayProfileEntry) (rawEntry, []rawGroup) {
	var pend []pendingLink
	for _, raw := range doc.Outbounds {
		var ob xrayOutboundShell
		if json.Unmarshal(raw, &ob) != nil {
			continue // a broken outbound costs itself, not the entry
		}
		if xrayNonProxy[strings.ToLower(ob.Protocol)] {
			continue
		}
		pend = append(pend, convertXrayOutbound(&ob)...)
	}
	// A balancer entry's members are suffixed with their address even when there
	// is only one, so a lone balancer-only member gets a distinct name rather
	// than bare remarks (which equals the group name).
	fromBalancer := len(doc.Routing.Balancers) > 0
	suffix := len(pend) > 1 || fromBalancer
	links := make([]string, 0, len(pend))
	for i := range pend {
		name := doc.Remarks
		if name == "" {
			name = pend[i].tag
		}
		if name == "" {
			name = pend[i].addr
		}
		if suffix {
			name += " · " + pend[i].addr
		}
		links = append(links, pend[i].finish(name))
	}
	return rawEntry{links: links, fromBalancer: fromBalancer}, xrayGroups(doc, pend, links)
}

// xrayGroups builds one rawGroup per routing balancer in the entry, pairing the
// finished member links with their outbound tags: a member is any of the
// entry's own outbounds whose tag has one of the balancer's selector strings as
// a prefix (xray's balancer selection is prefix-based). The group name is the
// entry's remarks when there is a single balancer (the placeholder-host entry
// carries the "Auto" label in remarks), else the balancer's own tag so multiple
// balancers stay distinct. Probe tuning comes from the entry's observatory.
func xrayGroups(doc *xrayProfileEntry, pend []pendingLink, links []string) []rawGroup {
	if len(doc.Routing.Balancers) == 0 || len(pend) == 0 {
		return nil
	}
	probe := xrayProbe(doc)
	single := len(doc.Routing.Balancers) == 1
	var groups []rawGroup
	for _, bal := range doc.Routing.Balancers {
		var members []string
		for i := range pend {
			if tagHasPrefix(pend[i].tag, bal.Selector) {
				members = append(members, links[i])
			}
		}
		if len(members) == 0 {
			continue
		}
		name := bal.Tag
		if single && doc.Remarks != "" {
			name = doc.Remarks
		}
		if name == "" {
			continue
		}
		groups = append(groups, rawGroup{name: name, links: members, probe: probe})
	}
	return groups
}

// tagHasPrefix reports whether tag begins with any of the selector prefixes
// (xray balancer selection semantics). An empty selector matches nothing.
func tagHasPrefix(tag string, selector []string) bool {
	for _, s := range selector {
		if s != "" && strings.HasPrefix(tag, s) {
			return true
		}
	}
	return false
}

// xrayProbe reads the group probe tuning from the entry's observatory: the
// newer burstObservatory.pingConfig (destination + interval) first, else the
// classic observatory (probeUrl + probeInterval). interval/probeInterval are
// xray DURATION STRINGS ("3m", "90s"); a numeric decode would silently yield 0,
// so they are decoded as strings and parsed. Tolerance has no xray analogue.
func xrayProbe(doc *xrayProfileEntry) store.GroupProbe {
	var probe store.GroupProbe
	if len(doc.BurstObservatory) > 0 {
		var bo struct {
			PingConfig struct {
				Destination string `json:"destination"`
				Interval    string `json:"interval"`
			} `json:"pingConfig"`
		}
		if json.Unmarshal(doc.BurstObservatory, &bo) == nil {
			probe.URL = bo.PingConfig.Destination
			probe.IntervalSec = durationSeconds(bo.PingConfig.Interval)
			return probe
		}
	}
	if len(doc.Observatory) > 0 {
		var o struct {
			ProbeURL      string `json:"probeUrl"`
			ProbeInterval string `json:"probeInterval"`
		}
		if json.Unmarshal(doc.Observatory, &o) == nil {
			probe.URL = o.ProbeURL
			probe.IntervalSec = durationSeconds(o.ProbeInterval)
		}
	}
	return probe
}

// convertXrayOutbound emits the links for one proxy outbound. An unsupported
// protocol contributes nothing — an entry left with zero links is counted
// unrecognized by the chain.
func convertXrayOutbound(ob *xrayOutboundShell) []pendingLink {
	switch strings.ToLower(ob.Protocol) {
	case "vless":
		return xrayVlessLinks(ob)
	case "vmess":
		return xrayVmessLinks(ob)
	case "trojan":
		return xrayTrojanLinks(ob)
	case "shadowsocks":
		return xrayShadowsocksLinks(ob)
	case "hysteria":
		return xrayHysteriaLinks(ob)
	}
	return nil
}

// --- vnext protocols (vless / vmess) ----------------------------------------

// xrayVnext is one settings.vnext endpoint. Users stays raw: vless and vmess
// carry different user fields.
type xrayVnext struct {
	Address string            `json:"address"`
	Port    uint16            `json:"port"`
	Users   []json.RawMessage `json:"users"`
}

func xrayVnexts(ob *xrayOutboundShell) []xrayVnext {
	var s struct {
		Vnext []xrayVnext `json:"vnext"`
	}
	if json.Unmarshal(ob.Settings, &s) != nil {
		return nil
	}
	return s.Vnext
}

func xrayVlessLinks(ob *xrayOutboundShell) []pendingLink {
	var out []pendingLink
	for _, v := range xrayVnexts(ob) {
		if v.Address == "" || len(v.Users) == 0 {
			continue
		}
		var u struct {
			ID         string `json:"id"`
			Flow       string `json:"flow"`
			Encryption string `json:"encryption"`
		}
		if json.Unmarshal(v.Users[0], &u) != nil || u.ID == "" {
			continue
		}
		q := url.Values{}
		if u.Flow != "" {
			q.Set("flow", u.Flow)
		}
		if u.Encryption != "" {
			q.Set("encryption", u.Encryption)
		}
		if !applyXrayStream(q, ob.Stream) {
			continue
		}
		out = append(out, pendingLink{addr: v.Address, tag: ob.Tag,
			frag: shareLink("vless", url.User(u.ID).String(), v.Address, v.Port, q, "")})
	}
	return out
}

func xrayVmessLinks(ob *xrayOutboundShell) []pendingLink {
	var out []pendingLink
	for _, v := range xrayVnexts(ob) {
		if v.Address == "" || len(v.Users) == 0 {
			continue
		}
		var u struct {
			ID       string `json:"id"`
			AlterID  int    `json:"alterId"`
			Security string `json:"security"`
		}
		if json.Unmarshal(v.Users[0], &u) != nil || u.ID == "" {
			continue
		}
		body, ok := xrayVmessBody(v.Address, v.Port, u.ID, u.AlterID, u.Security, ob.Stream)
		if !ok {
			continue
		}
		out = append(out, pendingLink{addr: v.Address, tag: ob.Tag, vmessBody: body})
	}
	return out
}

// xrayVmessBody builds the v2rayN vmess:// JSON body sans "ps". Reality and
// xhttp are unexpressable in that link format (no pbk/sid, no net=xhttp — our
// vmess.go rejects/mis-dials them), so such nodes are skipped.
func xrayVmessBody(addr string, port uint16, id string, aid int, scy string, sc *conf.StreamConfig) (map[string]any, bool) {
	body := map[string]any{
		"v": "2", "add": addr, "port": int(port),
		"id": id, "aid": aid, "scy": scy,
	}
	network, security := xrayStreamKind(sc)
	switch network {
	case "tcp":
	case "ws":
		body["net"] = "ws"
		if ws := sc.WSSettings; ws != nil {
			body["path"] = ws.Path
			if h := xrayWsHost(ws); h != "" {
				body["host"] = h
			}
		}
	case "grpc":
		body["net"] = "grpc"
		if g := sc.GRPCSettings; g != nil {
			body["path"] = g.ServiceName
		}
	default:
		return nil, false
	}
	switch security {
	case "none":
	case "tls":
		body["tls"] = "tls"
		if t := sc.TLSSettings; t != nil {
			body["sni"] = t.ServerName
			if t.Fingerprint != "" {
				body["fp"] = t.Fingerprint
			}
		}
	default:
		return nil, false
	}
	return body, true
}

// --- servers protocols (trojan / shadowsocks) -------------------------------

// xrayServer is one settings.servers endpoint (trojan and shadowsocks share
// the shape; each carries only its own credential fields).
type xrayServer struct {
	Address  string `json:"address"`
	Port     uint16 `json:"port"`
	Password string `json:"password"`
	Method   string `json:"method"`
}

func xrayServers(ob *xrayOutboundShell) []xrayServer {
	var s struct {
		Servers []xrayServer `json:"servers"`
	}
	if json.Unmarshal(ob.Settings, &s) != nil {
		return nil
	}
	return s.Servers
}

func xrayTrojanLinks(ob *xrayOutboundShell) []pendingLink {
	var out []pendingLink
	for _, srv := range xrayServers(ob) {
		if srv.Address == "" || srv.Password == "" {
			continue
		}
		q := url.Values{}
		if !applyXrayStream(q, ob.Stream) {
			continue
		}
		out = append(out, pendingLink{addr: srv.Address, tag: ob.Tag,
			frag: shareLink("trojan", url.User(srv.Password).String(), srv.Address, srv.Port, q, "")})
	}
	return out
}

// xrayShadowsocksLinks emits SIP002 links (ssLink, shared with the SIP008 and
// clash converters). The stream block is ignored: our ss:// model (like the
// protocol itself) has no transport/TLS dimension, and xray's plugin story is
// incompatible with SIP003 anyway.
func xrayShadowsocksLinks(ob *xrayOutboundShell) []pendingLink {
	var out []pendingLink
	for _, srv := range xrayServers(ob) {
		if srv.Address == "" || srv.Method == "" {
			continue
		}
		out = append(out, pendingLink{addr: srv.Address, tag: ob.Tag,
			frag: ssLink(srv.Method, srv.Password, srv.Address, srv.Port, "", "", "")})
	}
	return out
}

// --- hysteria ---------------------------------------------------------------

// xrayHysteriaLinks handles the hysteria outbound: settings carry the
// endpoint, streamSettings.hysteriaSettings carries {auth, version} and
// tlsSettings the TLS front. Version 2 becomes hysteria2:// (auth → password);
// version 1/absent becomes hysteria:// (auth/peer/insecure/alpn — the fields
// our hysteria.go reads); anything else is unexpressable. v2 keeps
// tlsSettings.pinnedPeerCertSha256 as pinSHA256 and the finalmask salamander
// mask as obfs/obfs-password; alpn is still dropped (h3 is implied).
func xrayHysteriaLinks(ob *xrayOutboundShell) []pendingLink {
	var s struct {
		Address string `json:"address"`
		Port    uint16 `json:"port"`
	}
	if json.Unmarshal(ob.Settings, &s) != nil || s.Address == "" {
		return nil
	}
	var hy *conf.HysteriaConfig
	var tc *conf.TLSConfig
	var fm *conf.FinalMask
	if sc := ob.Stream; sc != nil {
		hy, tc, fm = sc.HysteriaSettings, sc.TLSSettings, sc.FinalMask
	}
	version, auth := 0, ""
	if hy != nil {
		version, auth = int(hy.Version), hy.Auth
	}
	q := url.Values{}
	switch version {
	case 2:
		if tc != nil {
			if tc.ServerName != "" {
				q.Set("sni", tc.ServerName)
			}
			if tc.AllowInsecure {
				q.Set("insecure", "1")
			}
			if tc.PinnedPeerCertSha256 != "" {
				q.Set("pinSHA256", tc.PinnedPeerCertSha256)
			}
		}
		if fm != nil {
			for _, m := range fm.Udp {
				var sal conf.Salamander
				if m.Type == "salamander" && m.Settings != nil && json.Unmarshal(*m.Settings, &sal) == nil {
					q.Set("obfs", m.Type)
					q.Set("obfs-password", sal.Password)
				}
			}
		}
		userinfo := ""
		if auth != "" {
			userinfo = url.User(auth).String()
		}
		return []pendingLink{{addr: s.Address, tag: ob.Tag,
			frag: shareLink("hysteria2", userinfo, s.Address, s.Port, q, "")}}
	case 0, 1:
		if auth != "" {
			q.Set("auth", auth)
		}
		if tc != nil {
			if tc.ServerName != "" {
				q.Set("peer", tc.ServerName)
			}
			if tc.AllowInsecure {
				q.Set("insecure", "1")
			}
			if tc.ALPN != nil && len(*tc.ALPN) > 0 {
				q.Set("alpn", strings.Join([]string(*tc.ALPN), ","))
			}
		}
		return []pendingLink{{addr: s.Address, tag: ob.Tag,
			frag: shareLink("hysteria", "", s.Address, s.Port, q, "")}}
	}
	return nil
}

// --- stream mapping ---------------------------------------------------------

// xrayStreamKind normalizes the stream's (network, security) with xray's
// defaults and aliases applied. A nil stream is plain tcp/none.
func xrayStreamKind(sc *conf.StreamConfig) (network, security string) {
	network, security = "tcp", "none"
	if sc == nil {
		return
	}
	if sc.Network != nil && string(*sc.Network) != "" {
		switch n := strings.ToLower(string(*sc.Network)); n {
		case "raw":
			network = "tcp"
		case "websocket":
			network = "ws"
		case "gun":
			network = "grpc"
		case "splithttp":
			network = "xhttp"
		default:
			network = n
		}
	}
	if sc.Security != "" {
		security = strings.ToLower(sc.Security)
	}
	return
}

// applyXrayStream maps the stream block onto the shared V2Ray-family share-link
// query (type/path/host/serviceName/mode + security/sni/fp/pbk/sid — exactly
// what our vless.go/trojan.go read back). ok=false when the transport or
// security has no share-link form our parsers accept (kcp/quic/httpupgrade).
func applyXrayStream(q url.Values, sc *conf.StreamConfig) bool {
	network, security := xrayStreamKind(sc)
	switch network {
	case "tcp":
	case "ws":
		q.Set("type", "ws")
		if ws := sc.WSSettings; ws != nil {
			if ws.Path != "" {
				q.Set("path", ws.Path)
			}
			if h := xrayWsHost(ws); h != "" {
				q.Set("host", h)
			}
		}
	case "grpc":
		q.Set("type", "grpc")
		if g := sc.GRPCSettings; g != nil && g.ServiceName != "" {
			q.Set("serviceName", g.ServiceName)
		}
	case "xhttp":
		q.Set("type", "xhttp")
		x := sc.XHTTPSettings
		if x == nil {
			x = sc.SplitHTTPSettings
		}
		if x != nil {
			if x.Path != "" {
				q.Set("path", x.Path)
			}
			if x.Host != "" {
				q.Set("host", x.Host)
			}
			if x.Mode != "" {
				q.Set("mode", x.Mode)
			}
		}
	default:
		return false
	}
	switch security {
	case "none":
	case "tls":
		q.Set("security", "tls")
		if t := sc.TLSSettings; t != nil {
			if t.ServerName != "" {
				q.Set("sni", t.ServerName)
			}
			if t.Fingerprint != "" {
				q.Set("fp", t.Fingerprint)
			}
		}
	case "reality":
		r := sc.REALITYSettings
		if r == nil {
			return false
		}
		// All four keys must be PRESENT — our reality parse requires them.
		q.Set("security", "reality")
		q.Set("sni", r.ServerName)
		q.Set("fp", r.Fingerprint)
		q.Set("pbk", r.PublicKey)
		q.Set("sid", r.ShortId)
	default:
		return false
	}
	return true
}

// xrayWsHost resolves the ws Host header: the flat "host" field wins, then the
// headers map (either capitalization — providers use both).
func xrayWsHost(ws *conf.WebSocketConfig) string {
	if ws.Host != "" {
		return ws.Host
	}
	if h := ws.Headers["Host"]; h != "" {
		return h
	}
	return ws.Headers["host"]
}

// encodeVmessLink wraps a v2rayN vmess JSON body into the vmess://base64 form
// (shared with parse_singbox.go; link assembly for the other schemes is
// shareLink/ssLink in parse_clash.go).
func encodeVmessLink(body map[string]any) string {
	raw, _ := json.Marshal(body) // a map of plain scalars cannot fail
	return "vmess://" + base64.StdEncoding.EncodeToString(raw)
}
