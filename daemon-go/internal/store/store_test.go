package store

import (
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"testing"

	"ironlink/daemon/internal/api"
	"ironlink/daemon/internal/proxy"
)

// TestSudoUserConfigDir checks the elevated-for-TUN store resolution: with
// SUDO_UID set to a real user, the config root is THAT user's ~/.config dir
// (not root's), so `sudo iron-link-daemon` reads the user's profiles.
func TestSudoUserConfigDir(t *testing.T) {
	me, err := user.Current()
	if err != nil {
		t.Skipf("user.Current: %v", err)
	}
	env := func(k string) string {
		if k == "SUDO_UID" {
			return me.Uid
		}
		return ""
	}
	got, ok := sudoUserConfigDir(env)
	if !ok {
		t.Fatal("expected ok for a real SUDO_UID")
	}
	want := filepath.Join(me.HomeDir, ".config", "iron-link")
	if runtime.GOOS == "darwin" {
		want = filepath.Join(me.HomeDir, "Library", "Application Support", "iron-link")
	}
	if got != want {
		t.Fatalf("sudoUserConfigDir = %q, want %q", got, want)
	}

	// No SUDO_UID -> not under sudo -> fall through.
	if _, ok := sudoUserConfigDir(func(string) string { return "" }); ok {
		t.Fatal("expected ok=false without SUDO_UID")
	}
}

// fixtureProfile pins the v2 (Go-native) on-disk shape: kind+payload
// unions, snake_case, absent keys for absent options — covering every
// union variant (none/tls/reality, tcp/ws/xhttp), flow set and absent,
// and a core_override.
const fixtureProfile = `{
  "schema_version": 2,
  "name": "main",
  "active_node_id": "11111111-1111-1111-1111-111111111111",
  "subscriptions": [
    {
      "id": "33333333-3333-3333-3333-333333333333",
      "name": "prov",
      "url": "https://sub.example.com/x",
      "last_updated": "2026-06-09T07:18:45.123456Z",
      "update_interval_sec": 86400,
      "enabled": true,
      "allow_invalid_certs": false
    }
  ],
  "nodes": [
    {
      "id": "11111111-1111-1111-1111-111111111111",
      "sub_id": null,
      "profile": {
        "vless": {
          "server_name": "NL xhttp",
          "uuid": "88f2c0dc-a8e3-49f4-89b9-b3b54f1cad3a",
          "address": "198.51.100.7",
          "port": 443,
          "encryption": "none",
          "security": {
            "kind": "reality",
            "reality": {"sni": "google.com", "fp": "chrome", "pbk": "PBK", "sid": "01ab"}
          },
          "transport": {
            "kind": "xhttp",
            "xhttp": {"path": "/x", "mode": "auto"}
          }
        }
      },
      "preferences": {"core_override": "Xray"}
    },
    {
      "id": "22222222-2222-2222-2222-222222222222",
      "sub_id": "33333333-3333-3333-3333-333333333333",
      "profile": {
        "vless": {
          "server_name": "DE tcp vision",
          "uuid": "99f2c0dc-a8e3-49f4-89b9-b3b54f1cad3a",
          "address": "198.51.100.8",
          "port": 8443,
          "encryption": "none",
          "flow": "xtls-rprx-vision",
          "security": {"kind": "none"},
          "transport": {"kind": "tcp"}
        }
      },
      "preferences": {"core_override": null}
    },
    {
      "id": "44444444-4444-4444-4444-444444444444",
      "sub_id": null,
      "profile": {
        "vless": {
          "server_name": "WS tls",
          "uuid": "aaf2c0dc-a8e3-49f4-89b9-b3b54f1cad3a",
          "address": "cdn.example.com",
          "port": 443,
          "encryption": "none",
          "security": {"kind": "tls", "tls": {"sni": "cdn.example.com"}},
          "transport": {"kind": "ws", "ws": {"path": "/ws", "host": "cdn.example.com"}}
        }
      },
      "preferences": {"core_override": null}
    }
  ],
  "routing_configs": [
    {
      "id": "9cc3a8d2-aeed-4e1d-b184-2c55971fbf08",
      "name": "default",
      "rule_sets": [
        {"tag": "ads", "url": "https://example.com/ads.srs", "format": "binary", "download_detour": "proxy"}
      ],
      "rules": [
        {"target": "Block", "process_name": null, "domain": ["ads.example.com"],
         "domain_keyword": null, "port": null, "rule_set": ["ads"]}
      ],
      "default_target": "DefaultProxy"
    }
  ],
  "active_routing_id": "9cc3a8d2-aeed-4e1d-b184-2c55971fbf08"
}`

func fixtureStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "profiles"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "profiles", "main.json"), []byte(fixtureProfile), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(`{"active":"main"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return OpenAt(dir)
}

func TestLoadProfileDecodesV2Shapes(t *testing.T) {
	p, err := fixtureStore(t).LoadProfile("main")
	if err != nil {
		t.Fatalf("LoadProfile: %v", err)
	}
	if p.Name != "main" || len(p.Nodes) != 3 {
		t.Fatalf("profile = %+v", p)
	}

	xh := p.Nodes[0].Vless()
	if xh.Security.Kind != proxy.SecurityReality || xh.Security.Reality.Pbk != "PBK" {
		t.Errorf("reality decode: %+v", xh.Security)
	}
	if xh.Transport.Kind != proxy.TransportXhttp || xh.Transport.Xhttp.Path != "/x" ||
		xh.Transport.Xhttp.Mode != "auto" || xh.Transport.Xhttp.Extra != nil {
		t.Errorf("xhttp decode: %+v", xh.Transport.Xhttp)
	}
	if got := p.Nodes[0].Preferences.CoreOverride; got == nil || *got != api.CoreXray {
		t.Errorf("core_override = %v, want Xray", got)
	}

	tcp := p.Nodes[1].Vless()
	if tcp.Security.Kind != proxy.SecurityNone || tcp.Transport.Kind != proxy.TransportTCP {
		t.Errorf("unit variants decode: %+v", tcp)
	}
	if tcp.Flow != "xtls-rprx-vision" {
		t.Errorf("flow = %q", tcp.Flow)
	}

	ws := p.Nodes[2].Vless()
	if ws.Security.Kind != proxy.SecurityTLS || ws.Security.TLS.SNI != "cdn.example.com" || ws.Security.TLS.Fp != "" {
		t.Errorf("tls decode: %+v", ws.Security)
	}
	if ws.Transport.Kind != proxy.TransportWs || ws.Transport.Ws.Path != "/ws" {
		t.Errorf("ws decode: %+v", ws.Transport)
	}
}

func TestFindNodeByNameAndActiveNode(t *testing.T) {
	p, err := fixtureStore(t).LoadProfile("main")
	if err != nil {
		t.Fatal(err)
	}
	if n := p.FindNodeByName("DE tcp vision"); n == nil || n.ID != "22222222-2222-2222-2222-222222222222" {
		t.Errorf("FindNodeByName = %+v", n)
	}
	if n := p.FindNodeByName("nope"); n != nil {
		t.Errorf("FindNodeByName(nope) = %+v, want nil", n)
	}
	if n := p.ActiveNode(); n == nil || n.DisplayName() != "NL xhttp" {
		t.Errorf("ActiveNode = %+v", n)
	}
}

func TestActiveProfileName(t *testing.T) {
	s := fixtureStore(t)
	name, err := s.ActiveProfileName()
	if err != nil || name != "main" {
		t.Errorf("ActiveProfileName = %q, %v", name, err)
	}
	empty := OpenAt(t.TempDir())
	name, err = empty.ActiveProfileName()
	if err != nil || name != "" {
		t.Errorf("missing state.json: %q, %v (want empty, nil)", name, err)
	}
}

func TestLoadProfileRejectsBadNames(t *testing.T) {
	s := fixtureStore(t)
	for _, bad := range []string{"", "../etc/passwd", "a b", "x/y"} {
		if _, err := s.LoadProfile(bad); err == nil {
			t.Errorf("LoadProfile(%q) succeeded, want name validation error", bad)
		}
	}
}
