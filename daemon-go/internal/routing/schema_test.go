package routing

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/sagernet/sing-box/option"

	"ironlink/daemon/internal/api"
)

// supportedMap flattens SchemaFor(goos) to key -> supported.
func supportedMap(t *testing.T, goos string) map[string]bool {
	t.Helper()
	s := SchemaFor(goos)
	out := make(map[string]bool, len(s.Conditions))
	for _, c := range s.Conditions {
		if _, dup := out[c.Key]; dup {
			t.Errorf("SchemaFor(%q): duplicate condition key %q", goos, c.Key)
		}
		out[c.Key] = c.Supported
	}
	return out
}

var desktopGOOS = []string{"linux", "darwin", "windows"}

// TestSchemaForSupportedFlags pins how `supported` flips across the daemon
// platforms for every platform-gated condition, and that the all-platform
// conditions never flip.
func TestSchemaForSupportedFlags(t *testing.T) {
	// key -> supported on linux / darwin / windows.
	want := map[string][3]bool{
		"process_name":           {true, true, true},
		"process_path":           {true, true, true},
		"process_path_regex":     {true, true, true},
		"user":                   {true, true, false},
		"user_id":                {true, true, false},
		"package_name":           {false, false, false},
		"wifi_ssid":              {false, true, false},
		"wifi_bssid":             {false, true, false},
		"network_type":           {false, false, false},
		"network_is_expensive":   {false, false, false},
		"network_is_constrained": {false, false, false},
	}
	for i, goos := range []string{"linux", "darwin", "windows"} {
		got := supportedMap(t, goos)
		for key, flags := range want {
			if got[key] != flags[i] {
				t.Errorf("SchemaFor(%q): %s supported = %v, want %v", goos, key, got[key], flags[i])
			}
		}
		// Every all-platform condition (nil Platforms) is supported everywhere.
		for _, c := range conditions {
			if c.Platforms == nil && !got[c.Key] {
				t.Errorf("SchemaFor(%q): all-platform condition %s not supported", goos, c.Key)
			}
		}
	}
}

// TestSchemaTargetsAndFormats pins the target and rule-set-format lists
// (DpiBypass deliberately absent — the compiler rejects it today).
func TestSchemaTargetsAndFormats(t *testing.T) {
	for _, goos := range desktopGOOS {
		s := SchemaFor(goos)
		if want := []string{"DefaultProxy", "Direct", "Block", "Node"}; !reflect.DeepEqual(s.Targets, want) {
			t.Errorf("SchemaFor(%q).Targets = %v, want %v", goos, s.Targets, want)
		}
		if want := []string{"binary", "source"}; !reflect.DeepEqual(s.RuleSetFormats, want) {
			t.Errorf("SchemaFor(%q).RuleSetFormats = %v, want %v", goos, s.RuleSetFormats, want)
		}
	}
}

// TestSchemaCompleteAgainstSingBox pins the condition table against the
// pinned sing-box: every json tag of option.RawDefaultRule must be either in
// our table or explicitly excluded, and every table key must exist in
// RawDefaultRule (no typos). A sing-box bump that adds/renames/removes a
// condition field fails here until the table catches up.
func TestSchemaCompleteAgainstSingBox(t *testing.T) {
	// Deliberately not served:
	//   geosite / geoip / source_geoip — REMOVED in sing-box 1.12, they
	//     error at rule build;
	//   rule_set_ipcidr_match_source — deprecated alias of
	//     rule_set_ip_cidr_match_source;
	//   interface_address / network_interface_address / default_interface_address
	//     / preferred_by — interface-address & preferred-by match conditions added
	//     in sing-box 1.13; iron_link's routing UI does not expose them.
	excluded := map[string]bool{
		"geosite":                      true,
		"geoip":                        true,
		"source_geoip":                 true,
		"rule_set_ipcidr_match_source": true,
		"interface_address":            true,
		"network_interface_address":    true,
		"default_interface_address":    true,
		"preferred_by":                 true,
		"source_mac_address":           true,
		"package_name_regex":           true,
		"source_hostname":              true,
	}

	tags := map[string]bool{}
	typ := reflect.TypeOf(option.RawDefaultRule{})
	for i := 0; i < typ.NumField(); i++ {
		name, _, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ",")
		if name == "" || name == "-" {
			continue
		}
		tags[name] = true
	}

	tableKeys := map[string]bool{}
	for _, c := range conditions {
		tableKeys[c.Key] = true
	}

	for tag := range tags {
		if !tableKeys[tag] && !excluded[tag] {
			t.Errorf("sing-box RawDefaultRule field %q is neither in the condition table nor excluded", tag)
		}
		if tableKeys[tag] && excluded[tag] {
			t.Errorf("condition %q is both in the table and excluded", tag)
		}
	}
	for key := range tableKeys {
		if !tags[key] {
			t.Errorf("condition table key %q does not exist in sing-box RawDefaultRule", key)
		}
	}
	// The exclusions themselves must still exist upstream — a sing-box bump
	// that drops one should prompt removing the stale entry.
	for tag := range excluded {
		if !tags[tag] {
			t.Errorf("excluded tag %q no longer exists in sing-box RawDefaultRule", tag)
		}
	}
}

// TestSchemaForLinuxMatchesContractFixture cross-checks that what the daemon
// actually computes for linux is byte-equivalent to the contract fixture (the
// api contract test pins the same bytes via an explicit literal — it cannot
// import this package without an import cycle).
func TestSchemaForLinuxMatchesContractFixture(t *testing.T) {
	raw, err := os.ReadFile("../../../contract/fixtures/responses/routing_schema.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	emitted, err := json.Marshal(api.Response{Status: api.StatusRoutingSchema, RoutingSchema: SchemaFor("linux")})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var want, got any
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatalf("fixture is invalid JSON: %v", err)
	}
	if err := json.Unmarshal(emitted, &got); err != nil {
		t.Fatalf("emission is invalid JSON: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("SchemaFor(\"linux\") emission drifted from the contract fixture\nemitted: %s\nfixture: %s", emitted, strings.TrimSpace(string(raw)))
	}
}
