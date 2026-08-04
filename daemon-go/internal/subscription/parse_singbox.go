// parseSingBox handles sing-box config subscriptions: one JSON config object
// whose "outbounds" carry the nodes. Outbounds are decoded with the pinned
// sing-box's own option types under the same registry context engine.BuildBox
// uses, so the dialect accepted here is exactly the dialect the engine
// accepts — no second field list to drift.
package subscription

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"

	box "github.com/sagernet/sing-box"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/experimental/deprecated"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	singjson "github.com/sagernet/sing/common/json"
	"github.com/sagernet/sing/service"
)

// singBoxInfra are outbound types that are routing infrastructure, not nodes.
// They are recognized and deliberately dropped without becoming entries — a
// config with a selector is not "partly understood". The dns type (removed in
// sing-box 1.13, its typed decode errors) is skipped here BEFORE any decode.
var singBoxInfra = map[string]bool{
	C.TypeDirect:   true,
	C.TypeBlock:    true,
	C.TypeDNS:      true,
	C.TypeSelector: true,
	C.TypeURLTest:  true,
}

func parseSingBox(body string) ([]rawEntry, bool) {
	// Structural gate with plain encoding/json: an object bearing an
	// "outbounds" array whose elements are discriminated by "type". Xray
	// bodies discriminate by "protocol" (and are usually arrays) and must
	// fall through to parseXray.
	var doc struct {
		Outbounds []json.RawMessage `json:"outbounds"`
	}
	if err := json.Unmarshal([]byte(body), &doc); err != nil || len(doc.Outbounds) == 0 {
		return nil, false
	}
	heads := make([]struct {
		Type string `json:"type"`
	}, len(doc.Outbounds))
	sawType := false
	for i, raw := range doc.Outbounds {
		_ = json.Unmarshal(raw, &heads[i]) // a non-object element just stays typeless
		sawType = sawType || heads[i].Type != ""
	}
	if !sawType {
		return nil, false
	}

	ctx := singBoxOptionContext()
	// Strict full-config decode first — the same path the engine runs, so a
	// well-formed provider config is understood exactly as it would run.
	if options, err := singjson.UnmarshalExtendedContext[option.Options](ctx, []byte(body)); err == nil {
		var entries []rawEntry
		for _, ob := range options.Outbounds {
			if singBoxInfra[ob.Type] {
				continue
			}
			entries = append(entries, singBoxEntry(ob))
		}
		return entries, true
	}
	// Providers ship half-valid configs (stale route sections, exotic outbound
	// types). Fall back to per-outbound decoding so a broken outbound — or a
	// broken section elsewhere — costs itself, not the document.
	var entries []rawEntry
	for i, raw := range doc.Outbounds {
		if singBoxInfra[heads[i].Type] {
			continue
		}
		ob, err := singjson.UnmarshalExtendedContext[option.Outbound](ctx, []byte(raw))
		if err != nil {
			entries = append(entries, rawEntry{}) // zero links → counted unrecognized
			continue
		}
		entries = append(entries, singBoxEntry(ob))
	}
	return entries, true
}

// singBoxOptionContext mirrors engine.BuildBox's decode context: the include
// registries supply the outbound option types and the deprecated manager
// absorbs deprecation notes instead of failing the decode.
func singBoxOptionContext() context.Context {
	return box.Context(
		service.ContextWith(context.Background(), deprecated.NewStderrManager(log.StdLogger())),
		include.InboundRegistry(), include.OutboundRegistry(), include.EndpointRegistry(),
		include.DNSTransportRegistry(), include.ServiceRegistry(),
	)
}

// singBoxEntry wraps one outbound's conversion; sing-box has no per-node
// balancer nesting, so an outbound is at most one link. An outbound whose type
// or shape we cannot express yields zero links and counts unrecognized.
func singBoxEntry(ob option.Outbound) rawEntry {
	link, ok := singBoxLink(ob)
	if !ok {
		return rawEntry{}
	}
	return rawEntry{links: []string{link}}
}

// singBoxLink converts one typed outbound to its share link. The node name is
// the outbound tag, carried in the link fragment (or the vmess "ps").
func singBoxLink(ob option.Outbound) (string, bool) {
	switch o := ob.Options.(type) {
	case *option.VLESSOutboundOptions:
		q := url.Values{}
		if o.Flow != "" {
			q.Set("flow", o.Flow)
		}
		if !applySingBoxStream(q, o.TLS, o.Transport) {
			return "", false
		}
		return shareLink("vless", url.User(o.UUID).String(), o.Server, o.ServerPort, q, ob.Tag), true
	case *option.VMessOutboundOptions:
		return singBoxVmessLink(ob.Tag, o)
	case *option.TrojanOutboundOptions:
		q := url.Values{}
		if !applySingBoxStream(q, o.TLS, o.Transport) {
			return "", false
		}
		return shareLink("trojan", url.User(o.Password).String(), o.Server, o.ServerPort, q, ob.Tag), true
	case *option.ShadowsocksOutboundOptions:
		// SIP003 plugins pass through only for the two binaries the embedded
		// sing-box implements — same policy as the SIP008/clash converters.
		if o.Plugin != "" && o.Plugin != "obfs-local" && o.Plugin != "v2ray-plugin" {
			return "", false
		}
		return ssLink(o.Method, o.Password, o.Server, o.ServerPort, o.Plugin, o.PluginOptions, ob.Tag), true
	case *option.Hysteria2OutboundOptions:
		q := url.Values{}
		if tls := o.TLS; tls != nil {
			if tls.ServerName != "" {
				q.Set("sni", tls.ServerName)
			}
			if tls.Insecure {
				q.Set("insecure", "1")
			}
		}
		if o.Obfs != nil && o.Obfs.Type != "" {
			q.Set("obfs", o.Obfs.Type)
			q.Set("obfs-password", o.Obfs.Password)
		}
		userinfo := ""
		if o.Password != "" {
			userinfo = url.User(o.Password).String()
		}
		return shareLink("hysteria2", userinfo, o.Server, o.ServerPort, q, ob.Tag), true
	case *option.HysteriaOutboundOptions:
		if len(o.Auth) > 0 && o.AuthString == "" {
			return "", false // binary auth has no hysteria:// representation
		}
		q := url.Values{}
		if o.AuthString != "" {
			q.Set("auth", o.AuthString)
		}
		if tls := o.TLS; tls != nil {
			if tls.ServerName != "" {
				q.Set("peer", tls.ServerName)
			}
			if tls.Insecure {
				q.Set("insecure", "1")
			}
			if len(tls.ALPN) > 0 {
				q.Set("alpn", strings.Join(tls.ALPN, ","))
			}
		}
		if o.UpMbps != 0 {
			q.Set("upmbps", strconv.Itoa(o.UpMbps))
		}
		if o.DownMbps != 0 {
			q.Set("downmbps", strconv.Itoa(o.DownMbps))
		}
		if o.Obfs != "" {
			q.Set("obfs", o.Obfs)
		}
		return shareLink("hysteria", "", o.Server, o.ServerPort, q, ob.Tag), true
	case *option.TUICOutboundOptions:
		q := url.Values{}
		if tls := o.TLS; tls != nil {
			if tls.ServerName != "" {
				q.Set("sni", tls.ServerName)
			}
			if len(tls.ALPN) > 0 {
				q.Set("alpn", strings.Join(tls.ALPN, ","))
			}
			if tls.Insecure {
				q.Set("allow_insecure", "1")
			}
		}
		if o.CongestionControl != "" {
			q.Set("congestion_control", o.CongestionControl)
		}
		if o.UDPRelayMode != "" {
			q.Set("udp_relay_mode", o.UDPRelayMode)
		}
		return shareLink("tuic", url.UserPassword(o.UUID, o.Password).String(), o.Server, o.ServerPort, q, ob.Tag), true
	case *option.AnyTLSOutboundOptions:
		q := url.Values{}
		if tls := o.TLS; tls != nil && tls.Enabled {
			if tls.ServerName != "" {
				q.Set("sni", tls.ServerName)
			}
			if tls.Insecure {
				q.Set("insecure", "1")
			}
		}
		return shareLink("anytls", url.User(o.Password).String(), o.Server, o.ServerPort, q, ob.Tag), true
	}
	return "", false
}

// singBoxVmessLink builds the v2rayN vmess:// form our vmess.go parses.
// Reality is unexpressable there (no pbk/sid slots — vmess.go rejects it), as
// is any transport beyond tcp/ws/grpc.
func singBoxVmessLink(tag string, o *option.VMessOutboundOptions) (string, bool) {
	body := map[string]any{
		"v": "2", "ps": tag,
		"add": o.Server, "port": int(o.ServerPort),
		"id": o.UUID, "aid": o.AlterId, "scy": o.Security,
	}
	if tls := o.TLS; tls != nil && tls.Enabled {
		if tls.Reality != nil && tls.Reality.Enabled {
			return "", false
		}
		body["tls"] = "tls"
		body["sni"] = tls.ServerName
		if tls.UTLS != nil && tls.UTLS.Enabled {
			body["fp"] = tls.UTLS.Fingerprint
		}
	}
	if t := o.Transport; t != nil {
		switch t.Type {
		case C.V2RayTransportTypeWebsocket:
			body["net"] = "ws"
			body["path"] = t.WebsocketOptions.Path
			if h := t.WebsocketOptions.Headers.Build().Get("Host"); h != "" {
				body["host"] = h
			}
		case C.V2RayTransportTypeGRPC:
			body["net"] = "grpc"
			body["path"] = t.GRPCOptions.ServiceName
		default:
			return "", false
		}
	}
	return encodeVmessLink(body), true
}

// applySingBoxStream maps the sing-box tls/transport blocks onto the shared
// V2Ray-family share-link query (the exact params vless.go/trojan.go read
// back). ok=false when the transport has no share-link form our parsers accept
// (http/quic/httpupgrade).
func applySingBoxStream(q url.Values, tls *option.OutboundTLSOptions, transport *option.V2RayTransportOptions) bool {
	if tls != nil && tls.Enabled {
		fp := ""
		if tls.UTLS != nil && tls.UTLS.Enabled {
			fp = tls.UTLS.Fingerprint
		}
		if r := tls.Reality; r != nil && r.Enabled {
			// All four keys must be PRESENT — our reality parse requires them.
			q.Set("security", "reality")
			q.Set("sni", tls.ServerName)
			q.Set("fp", fp)
			q.Set("pbk", r.PublicKey)
			q.Set("sid", r.ShortID)
		} else {
			q.Set("security", "tls")
			if tls.ServerName != "" {
				q.Set("sni", tls.ServerName)
			}
			if fp != "" {
				q.Set("fp", fp)
			}
		}
	}
	if transport == nil {
		return true
	}
	switch transport.Type {
	case C.V2RayTransportTypeWebsocket:
		q.Set("type", "ws")
		if p := transport.WebsocketOptions.Path; p != "" {
			q.Set("path", p)
		}
		if h := transport.WebsocketOptions.Headers.Build().Get("Host"); h != "" {
			q.Set("host", h)
		}
	case C.V2RayTransportTypeGRPC:
		q.Set("type", "grpc")
		if s := transport.GRPCOptions.ServiceName; s != "" {
			q.Set("serviceName", s)
		}
	default:
		return false
	}
	return true
}
