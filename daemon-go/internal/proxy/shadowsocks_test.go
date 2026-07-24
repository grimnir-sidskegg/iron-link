package proxy

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"ironlink/daemon/internal/api"
)

// TestParseShadowsocksSIP002 parses the canonical SIP002 link with a base64url
// userinfo and asserts every field.
func TestParseShadowsocksSIP002(t *testing.T) {
	// base64("aes-256-gcm:pass") = YWVzLTI1Ni1nY206cGFzcw==
	p, err := ParseURL("ss://YWVzLTI1Ni1nY206cGFzcw==@host.example:443#Tokyo%20Node")
	if err != nil {
		t.Fatalf("ParseURL: %v", err)
	}
	c, ok := p.(*ShadowsocksConfig)
	if !ok {
		t.Fatalf("ParseURL returned %T, want *ShadowsocksConfig", p)
	}
	if c.Method != "aes-256-gcm" || c.Password != "pass" {
		t.Errorf("method/password = %q/%q, want aes-256-gcm/pass", c.Method, c.Password)
	}
	if c.Address != "host.example" || c.Port != 443 {
		t.Errorf("endpoint = %s:%d, want host.example:443", c.Address, c.Port)
	}
	if c.ServerName != "Tokyo Node" {
		t.Errorf("name = %q, want %q", c.ServerName, "Tokyo Node")
	}
	if c.Plugin != "" {
		t.Errorf("unexpected plugin %q", c.Plugin)
	}
}

// TestParseShadowsocksSIP002Literal parses a SIP002 link whose userinfo is a
// literal percent-encoded method:password (not base64) — the SS2022 PSK case.
func TestParseShadowsocksSIP002Literal(t *testing.T) {
	// password "psk+base64==" percent-encoded; method is a 2022-blake3 cipher.
	p, err := ParseURL("ss://2022-blake3-aes-256-gcm:psk%2Bbase64%3D%3D@1.2.3.4:8388#x")
	if err != nil {
		t.Fatalf("ParseURL: %v", err)
	}
	c := p.(*ShadowsocksConfig)
	if c.Method != "2022-blake3-aes-256-gcm" {
		t.Errorf("method = %q, want 2022-blake3-aes-256-gcm", c.Method)
	}
	if c.Password != "psk+base64==" {
		t.Errorf("password = %q, want psk+base64==", c.Password)
	}
	if c.Address != "1.2.3.4" || c.Port != 8388 {
		t.Errorf("endpoint = %s:%d, want 1.2.3.4:8388", c.Address, c.Port)
	}
}

// TestParseShadowsocksLegacy parses the legacy fully-base64 form
// ss://base64(method:password@host:port)#name and asserts the same fields the
// SIP002 form would yield.
func TestParseShadowsocksLegacy(t *testing.T) {
	body := base64.StdEncoding.EncodeToString([]byte("aes-256-gcm:secretpass@example.com:8388"))
	p, err := ParseURL("ss://" + body + "#Berlin")
	if err != nil {
		t.Fatalf("ParseURL: %v", err)
	}
	c := p.(*ShadowsocksConfig)
	if c.Method != "aes-256-gcm" || c.Password != "secretpass" {
		t.Errorf("method/password = %q/%q, want aes-256-gcm/secretpass", c.Method, c.Password)
	}
	if c.Address != "example.com" || c.Port != 8388 {
		t.Errorf("endpoint = %s:%d, want example.com:8388", c.Address, c.Port)
	}
	if c.ServerName != "Berlin" {
		t.Errorf("name = %q, want Berlin", c.ServerName)
	}
}

// TestParseShadowsocksLegacyRawURL parses a legacy link encoded with the
// raw (unpadded) url-safe base64 alphabet — share links use all four variants.
func TestParseShadowsocksLegacyRawURL(t *testing.T) {
	body := base64.RawURLEncoding.EncodeToString([]byte("chacha20-ietf-poly1305:pw@1.2.3.4:443"))
	p, err := ParseURL("ss://" + body + "#y")
	if err != nil {
		t.Fatalf("ParseURL: %v", err)
	}
	c := p.(*ShadowsocksConfig)
	if c.Method != "chacha20-ietf-poly1305" || c.Password != "pw" || c.Address != "1.2.3.4" || c.Port != 443 {
		t.Errorf("legacy raw-url parse wrong: %+v", c)
	}
}

// TestParseShadowsocksSS2022 confirms an SS2022 cipher passes through verbatim
// (the password IS the pre-shared key, kept untouched).
func TestParseShadowsocksSS2022(t *testing.T) {
	// base64("2022-blake3-aes-128-gcm:GsUYjlH2dPZJ...") userinfo.
	ui := base64.URLEncoding.EncodeToString([]byte("2022-blake3-aes-128-gcm:GsUYjlH2dPZJ"))
	p, err := ParseURL("ss://" + ui + "@h:443#ss2022")
	if err != nil {
		t.Fatalf("ParseURL: %v", err)
	}
	c := p.(*ShadowsocksConfig)
	if !strings.HasPrefix(c.Method, "2022-blake3-") {
		t.Errorf("method = %q, want a 2022-blake3-* cipher", c.Method)
	}
	if c.Password != "GsUYjlH2dPZJ" {
		t.Errorf("PSK = %q, want GsUYjlH2dPZJ (verbatim)", c.Password)
	}
}

// TestParseShadowsocksPlugin parses a SIP003 plugin link (plugin + plugin_opts).
func TestParseShadowsocksPlugin(t *testing.T) {
	p, err := ParseURL("ss://YWVzLTI1Ni1nY206cGFzcw==@h:443?plugin=obfs-local%3Bobfs%3Dhttp%3Bobfs-host%3Dx.com#withplugin")
	if err != nil {
		t.Fatalf("ParseURL: %v", err)
	}
	c := p.(*ShadowsocksConfig)
	if c.Plugin != "obfs-local;obfs=http;obfs-host=x.com" {
		t.Errorf("plugin = %q, unexpected", c.Plugin)
	}
}

// TestParseShadowsocksPortRequired: a SIP002 link without a port is an error.
func TestParseShadowsocksPortRequired(t *testing.T) {
	if _, err := ParseURL("ss://YWVzLTI1Ni1nY206cGFzcw==@host.example#noport"); err == nil {
		t.Error("expected error for missing port")
	}
}

// TestParseShadowsocksDefaultName: an absent fragment yields the default name.
func TestParseShadowsocksDefaultName(t *testing.T) {
	p, err := ParseURL("ss://YWVzLTI1Ni1nY206cGFzcw==@h:443")
	if err != nil {
		t.Fatalf("ParseURL: %v", err)
	}
	if got := p.DisplayName(); got != "New Shadowsocks" {
		t.Errorf("DisplayName = %q, want %q", got, "New Shadowsocks")
	}
}

// TestShadowsocksSingBoxOutbound asserts the native sing-box outbound shape.
func TestShadowsocksSingBoxOutbound(t *testing.T) {
	c := &ShadowsocksConfig{Address: "h", Port: 443, Method: "aes-256-gcm", Password: "pw"}
	ob, err := c.SingBoxOutbound("ss-out")
	if err != nil {
		t.Fatalf("SingBoxOutbound: %v", err)
	}
	if ob["type"] != "shadowsocks" {
		t.Errorf("type = %v, want shadowsocks", ob["type"])
	}
	if ob["method"] != "aes-256-gcm" || ob["password"] != "pw" || ob["server"] != "h" {
		t.Errorf("outbound fields wrong: %+v", ob)
	}
	if ob["tag"] != "ss-out" {
		t.Errorf("tag = %v, want ss-out", ob["tag"])
	}
	if _, hasPlugin := ob["plugin"]; hasPlugin {
		t.Error("no-plugin node must not emit a plugin field")
	}
}

// TestShadowsocksSingBoxOutboundPlugin asserts plugin/plugin_opts are emitted
// when present.
func TestShadowsocksSingBoxOutboundPlugin(t *testing.T) {
	c := &ShadowsocksConfig{Address: "h", Port: 443, Method: "aes-256-gcm", Password: "pw", Plugin: "obfs-local", PluginOpts: "obfs=http"}
	ob, err := c.SingBoxOutbound("t")
	if err != nil {
		t.Fatalf("SingBoxOutbound: %v", err)
	}
	if ob["plugin"] != "obfs-local" || ob["plugin_opts"] != "obfs=http" {
		t.Errorf("plugin fields wrong: %+v", ob)
	}
}

// TestShadowsocksXrayOutbound asserts the xray outbound (servers[] form) for a
// plain node, and ok==false when a SIP003 plugin is present (xray-incompatible).
func TestShadowsocksXrayOutbound(t *testing.T) {
	c := &ShadowsocksConfig{Address: "h", Port: 443, Method: "aes-256-gcm", Password: "pw"}
	ob, ok, err := c.XrayOutbound()
	if err != nil {
		t.Fatalf("XrayOutbound: %v", err)
	}
	if !ok {
		t.Fatal("ok = false, want true for a plain node")
	}
	if ob["protocol"] != "shadowsocks" {
		t.Errorf("protocol = %v, want shadowsocks", ob["protocol"])
	}
	// servers[0] ServerObject must carry address/port/method/password.
	settings := ob["settings"].(map[string]any)
	servers := settings["servers"].([]any)
	srv := servers[0].(map[string]any)
	if srv["address"] != "h" || srv["method"] != "aes-256-gcm" || srv["password"] != "pw" {
		t.Errorf("server object wrong: %+v", srv)
	}
	if _, hasStream := ob["streamSettings"]; !hasStream {
		t.Error("XrayOutbound must carry a streamSettings map for the engine to inject into")
	}

	// With a plugin → xray cannot dial it.
	c.Plugin = "v2ray-plugin"
	if _, ok, _ := c.XrayOutbound(); ok {
		t.Error("plugin node must yield ok=false (xray plugin incompatible)")
	}
}

// TestShadowsocksDialableBy: sing-box always; xray only without a plugin.
func TestShadowsocksDialableBy(t *testing.T) {
	plain := &ShadowsocksConfig{Address: "h", Port: 443, Method: "aes-256-gcm", Password: "pw"}
	if !plain.dialableBy(api.CoreSingBox) {
		t.Error("sing-box must dial a plain ss node")
	}
	if !plain.dialableBy(api.CoreXray) {
		t.Error("xray must dial a plain (no-plugin) ss node")
	}
	plugin := &ShadowsocksConfig{Address: "h", Port: 443, Method: "aes-256-gcm", Password: "pw", Plugin: "obfs-local"}
	if !plugin.dialableBy(api.CoreSingBox) {
		t.Error("sing-box must dial a plugin ss node")
	}
	if plugin.dialableBy(api.CoreXray) {
		t.Error("xray must NOT dial a plugin ss node")
	}
}

// TestShadowsocksRoundTrip: EncodeProfileDoc produces the {"shadowsocks": …}
// tagged-union body and DecodeProfile reverses it field-for-field.
func TestShadowsocksRoundTrip(t *testing.T) {
	orig := &ShadowsocksConfig{
		ServerName: "rt",
		Address:    "h",
		Port:       8388,
		Method:     "2022-blake3-aes-256-gcm",
		Password:   "psk==",
		Plugin:     "obfs-local",
		PluginOpts: "obfs=tls",
	}
	doc, err := EncodeProfileDoc(orig)
	if err != nil {
		t.Fatalf("EncodeProfileDoc: %v", err)
	}
	// The on-disk shape is a single-key union under "shadowsocks".
	var union map[string]json.RawMessage
	if err := json.Unmarshal(doc, &union); err != nil {
		t.Fatalf("unmarshal doc: %v", err)
	}
	body, ok := union["shadowsocks"]
	if !ok {
		t.Fatalf("doc has no \"shadowsocks\" key: %s", doc)
	}
	back, err := DecodeProfile(ProtocolShadowsocks, body)
	if err != nil {
		t.Fatalf("DecodeProfile: %v", err)
	}
	got := back.(*ShadowsocksConfig)
	if *got != *orig {
		t.Errorf("round-trip mismatch:\n got: %+v\nwant: %+v", got, orig)
	}
}
