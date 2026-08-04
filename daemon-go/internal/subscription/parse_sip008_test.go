package subscription

import (
	"testing"

	"ironlink/daemon/internal/proxy"
)

func TestParseSIP008Fixture(t *testing.T) {
	o, err := Parse(readFixture(t, "sip008.json"), "sub", FormatAuto)
	if err != nil {
		t.Fatal(err)
	}
	if o.Format != FormatSIP008 {
		t.Fatalf("format = %s, want sip008", o.Format)
	}
	if o.Entries != 3 || len(o.Nodes) != 3 || o.Duplicates != 0 || o.Unrecognized != 0 {
		t.Fatalf("accounting = %d entries / %d nodes / %d duplicates / %d unrecognized, want 3/3/0/0",
			o.Entries, len(o.Nodes), o.Duplicates, o.Unrecognized)
	}

	one, ok := findProfile(t, o, "Plain One").(*proxy.ShadowsocksConfig)
	if !ok {
		t.Fatal("Plain One is not a shadowsocks node")
	}
	if one.Kind() != proxy.ProtocolShadowsocks || one.ServerAddress() != "198.51.100.101" || one.ServerPort() != 8388 {
		t.Errorf("endpoint: %s %s:%d", one.Kind(), one.ServerAddress(), one.ServerPort())
	}
	if one.Method != "aes-256-gcm" || one.Password != "sip008-pass-one" || one.Plugin != "" {
		t.Errorf("fields: %+v", one)
	}
	// Shadowsocks has no transport/security badge dimension.
	if one.TransportLabel() != "" || one.SecurityLabel() != "" {
		t.Errorf("labels: %q/%q", one.TransportLabel(), one.SecurityLabel())
	}

	two, ok := findProfile(t, o, "Plain Two").(*proxy.ShadowsocksConfig)
	if !ok {
		t.Fatal("Plain Two is not a shadowsocks node")
	}
	if two.Method != "chacha20-ietf-poly1305" || two.ServerAddress() != "203.0.113.102" || two.ServerPort() != 443 {
		t.Errorf("fields: %+v", two)
	}

	// The SIP003 plugin passes through verbatim (sing-box speaks obfs-local).
	front, ok := findProfile(t, o, "Obfs Front").(*proxy.ShadowsocksConfig)
	if !ok {
		t.Fatal("Obfs Front is not a shadowsocks node")
	}
	if front.Plugin != "obfs-local" || front.PluginOpts != "obfs=http;obfs-host=www.example.com" {
		t.Errorf("plugin passthrough: %q / %q", front.Plugin, front.PluginOpts)
	}
}

func TestParseSIP008UnsupportedPluginIsSkipped(t *testing.T) {
	// A plugin binary sing-box does not implement must skip that server, not
	// store a node that would fail engine start.
	body := `{"version": 1, "servers": [
		{"remarks": "plain", "server": "203.0.113.110", "server_port": 8388, "password": "pw", "method": "aes-128-gcm"},
		{"remarks": "kcp", "server": "203.0.113.111", "server_port": 8388, "password": "pw", "method": "aes-128-gcm", "plugin": "kcptun", "plugin_opts": "mode=fast2"}
	]}`
	o, err := Parse(body, "s", FormatSIP008)
	if err != nil {
		t.Fatal(err)
	}
	if o.Entries != 2 || len(o.Nodes) != 1 || o.Unrecognized != 1 {
		t.Fatalf("accounting = %d entries / %d nodes / %d unrecognized, want 2/1/1",
			o.Entries, len(o.Nodes), o.Unrecognized)
	}
	if o.Nodes[0].DisplayName() != "plain" {
		t.Errorf("surviving node = %q", o.Nodes[0].DisplayName())
	}
}

func TestParseSIP008StructuralGates(t *testing.T) {
	// A clash YAML body is not JSON at all.
	if _, ok := parseSIP008(readFixture(t, "clash.yaml")); ok {
		t.Error("clash YAML must not match sip008")
	}
	for _, body := range []string{
		`{"servers": []}`,
		`{"version": 1, "servers": [{"remarks": "no endpoint"}]}`,
		`{"proxies": []}`,
		`[{"server": "203.0.113.1", "server_port": 1}]`, // array, not an object
		"vless://uuid@203.0.113.1:443#link",
	} {
		if _, ok := parseSIP008(body); ok {
			t.Errorf("body %q must not match sip008", body)
		}
	}
}
