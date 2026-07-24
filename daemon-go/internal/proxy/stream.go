// The shared stream model: the transport (tcp/ws/grpc/xhttp) and security
// (none/tls/reality) unions that the V2Ray-family protocols — VLESS, VMess,
// Trojan — all carry, plus the reusable sing-box / xray emit helpers so each of
// those protocols builds its outbound from the same blocks instead of
// reimplementing TLS/Reality/ws/grpc. Protocols with their own wire shape
// (Shadowsocks, Hysteria2, TUIC, …) do not use this file.

package proxy

import "encoding/json"

// SecurityKind is the transport-layer security variant.
type SecurityKind string

const (
	SecurityNone    SecurityKind = "none"
	SecurityTLS     SecurityKind = "tls"
	SecurityReality SecurityKind = "reality"
)

// TransportKind is the stream transport variant.
type TransportKind string

const (
	TransportTCP  TransportKind = "tcp"
	TransportWs   TransportKind = "ws"
	TransportGrpc TransportKind = "grpc"
	// TransportXhttp is xhttp / splithttp. **xray-only** — sing-box has no
	// xhttp client (PR #3879 closed unmerged), which is exactly what forces the
	// xray core for these nodes (the V2Ray-family dialableBy).
	TransportXhttp TransportKind = "xhttp"
)

// TLSParams carries the `security=tls` options. Empty string = absent.
type TLSParams struct {
	SNI string `json:"sni,omitempty"`
	Fp  string `json:"fp,omitempty"`
}

// RealityParams carries the `security=reality` options — all four are REQUIRED
// by the V2Ray-family parsers (a reality link without them is malformed).
type RealityParams struct {
	SNI string `json:"sni"`
	Fp  string `json:"fp"`
	Pbk string `json:"pbk"`
	Sid string `json:"sid"`
}

// Security is the tagged security union: Kind selects the variant; exactly the
// matching payload pointer is set (none for SecurityNone). The on-disk and
// in-memory shape is this struct verbatim.
type Security struct {
	Kind    SecurityKind   `json:"kind"`
	TLS     *TLSParams     `json:"tls,omitempty"`
	Reality *RealityParams `json:"reality,omitempty"`
}

// WsParams carries the `type=ws` transport options. Empty string = absent.
type WsParams struct {
	Path string `json:"path"`
	Host string `json:"host,omitempty"`
}

// GrpcParams carries the `type=grpc` transport options.
type GrpcParams struct {
	ServiceName string `json:"service_name"`
}

// XhttpParams carries the `type=xhttp` transport options. Extra is the raw
// `extra` JSON blob passed through to xray verbatim (nil = absent).
type XhttpParams struct {
	Path  string          `json:"path"`
	Host  string          `json:"host,omitempty"`
	Mode  string          `json:"mode"`
	Extra json.RawMessage `json:"extra,omitempty"`
}

// Transport is the tagged transport union: Kind selects the variant; exactly
// the matching payload pointer is set (none for TransportTCP).
type Transport struct {
	Kind  TransportKind `json:"kind"`
	Ws    *WsParams     `json:"ws,omitempty"`
	Grpc  *GrpcParams   `json:"grpc,omitempty"`
	Xhttp *XhttpParams  `json:"xhttp,omitempty"`
}

// Label / SNI: small accessors the Profile impls forward for UI badges + the
// doctor camouflage front.
func (s Security) Label() string  { return string(s.Kind) }
func (t Transport) Label() string { return string(t.Kind) }

// SNI returns the TLS/Reality front domain ("" for a plain node).
func (s Security) SNI() string {
	switch s.Kind {
	case SecurityReality:
		if s.Reality != nil {
			return s.Reality.SNI
		}
	case SecurityTLS:
		if s.TLS != nil {
			return s.TLS.SNI
		}
	}
	return ""
}

// --- sing-box emit ----------------------------------------------------------

// singBoxTLS builds the sing-box `tls` block for this security, or ok=false for
// a plain node. sing-box requires uTLS for reality; the parser guarantees fp.
func (s Security) singBoxTLS() (map[string]any, bool) {
	switch s.Kind {
	case SecurityReality:
		r := s.Reality
		if r == nil { // malformed stored union — no usable front, emit no tls
			return nil, false
		}
		return map[string]any{
			"enabled":     true,
			"server_name": r.SNI,
			"utls":        map[string]any{"enabled": true, "fingerprint": r.Fp},
			"reality":     map[string]any{"enabled": true, "public_key": r.Pbk, "short_id": r.Sid},
		}, true
	case SecurityTLS:
		tp := s.TLS
		if tp == nil {
			tp = &TLSParams{}
		}
		tls := map[string]any{"enabled": true}
		if tp.SNI != "" {
			tls["server_name"] = tp.SNI
		}
		if tp.Fp != "" {
			tls["utls"] = map[string]any{"enabled": true, "fingerprint": tp.Fp}
		}
		return tls, true
	default:
		return nil, false
	}
}

// singBoxTransport builds the sing-box `transport` block for this transport, or
// ok=false for plain TCP (no block). xhttp errors: sing-box has no xhttp client
// — those nodes are xray-routed, never native.
func (t Transport) singBoxTransport() (block map[string]any, ok bool, err error) {
	switch t.Kind {
	case TransportTCP:
		return nil, false, nil
	case TransportWs:
		w := t.Ws
		if w == nil {
			w = &WsParams{}
		}
		ws := map[string]any{"type": "ws"}
		if w.Path != "" {
			ws["path"] = w.Path
		}
		if w.Host != "" {
			ws["headers"] = map[string]any{"Host": w.Host}
		}
		return ws, true, nil
	case TransportGrpc:
		g := t.Grpc
		if g == nil {
			g = &GrpcParams{}
		}
		grpc := map[string]any{"type": "grpc"}
		if g.ServiceName != "" {
			grpc["service_name"] = g.ServiceName
		}
		return grpc, true, nil
	default:
		return nil, false, &unnativeTransportError{t.Kind}
	}
}

// unnativeTransportError marks a transport sing-box cannot dial natively.
type unnativeTransportError struct{ kind TransportKind }

func (e *unnativeTransportError) Error() string {
	return "transport " + string(e.kind) + " has no native sing-box outbound"
}

// --- xray emit --------------------------------------------------------------

// applyXraySecurity sets `security` + its settings block into the xray stream.
func (s Security) applyXraySecurity(stream map[string]any) {
	switch s.Kind {
	case SecurityReality:
		r := s.Reality
		if r == nil { // malformed stored union — fall back to no security
			stream["security"] = "none"
			return
		}
		stream["security"] = "reality"
		stream["realitySettings"] = map[string]any{
			"serverName":  r.SNI,
			"fingerprint": r.Fp,
			"publicKey":   r.Pbk,
			"shortId":     r.Sid,
			"show":        false,
			"spiderX":     "",
		}
	case SecurityTLS:
		tp := s.TLS
		if tp == nil {
			tp = &TLSParams{}
		}
		tls := map[string]any{"allowInsecure": false}
		if tp.SNI != "" {
			tls["serverName"] = tp.SNI
		}
		if tp.Fp != "" {
			tls["fingerprint"] = tp.Fp
		}
		stream["security"] = "tls"
		stream["tlsSettings"] = tls
	default:
		stream["security"] = "none"
	}
}

// applyXrayTransport sets `network` + its settings block into the xray stream.
func (t Transport) applyXrayTransport(stream map[string]any) {
	switch t.Kind {
	case TransportXhttp:
		x := t.Xhttp
		if x == nil {
			x = &XhttpParams{}
		}
		mode := x.Mode
		if mode == "" {
			mode = "auto"
		}
		xhttp := map[string]any{"path": x.Path, "host": x.Host, "mode": mode}
		if len(x.Extra) > 0 {
			xhttp["extra"] = json.RawMessage(x.Extra)
		}
		stream["network"] = "xhttp"
		stream["xhttpSettings"] = xhttp
	case TransportWs:
		w := t.Ws
		if w == nil {
			w = &WsParams{}
		}
		stream["network"] = "ws"
		stream["wsSettings"] = map[string]any{
			"path":    w.Path,
			"headers": map[string]any{"Host": w.Host},
		}
	case TransportGrpc:
		g := t.Grpc
		if g == nil {
			g = &GrpcParams{}
		}
		stream["network"] = "grpc"
		stream["grpcSettings"] = map[string]any{"serviceName": g.ServiceName}
	default:
		stream["network"] = "tcp"
	}
}
