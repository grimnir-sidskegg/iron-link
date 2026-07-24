package routing

import (
	"encoding/json"
	"strings"
	"testing"
)

// realShapedConfig mirrors the user-facing "basic" config the Rust app writes
// today: a remote rule set, legacy condition fields serialized as null, a
// 1.12 condition (ip_cidr), Direct default.
const realShapedConfig = `{
  "id": "03c9d0ff-c200-4990-8edf-621572573db6",
  "name": "basic",
  "rule_sets": [
    {
      "tag": "refilter_domains",
      "url": "https://example.com/ruleset.srs",
      "format": "binary",
      "download_detour": "proxy"
    }
  ],
  "rules": [
    {
      "target": "DefaultProxy",
      "process_name": ["chrome", "Discord"],
      "domain": null,
      "domain_keyword": null,
      "port": null,
      "rule_set": null
    },
    {
      "target": "DefaultProxy",
      "process_name": null,
      "domain": null,
      "domain_keyword": ["example.com"],
      "port": null,
      "rule_set": null
    },
    {
      "target": {"Node": "11111111-1111-1111-1111-111111111111"},
      "process_name": null,
      "domain": null,
      "domain_keyword": null,
      "port": null,
      "rule_set": ["refilter_domains"],
      "ip_cidr": ["198.51.100.20"]
    }
  ],
  "default_target": "Direct"
}`

func TestConfigRoundTripRealShape(t *testing.T) {
	var c Config
	if err := json.Unmarshal([]byte(realShapedConfig), &c); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if c.Name != "basic" || len(c.Rules) != 3 || len(c.RuleSets) != 1 {
		t.Fatalf("decoded: %+v", c)
	}
	if c.DefaultTarget.Kind != TargetDirect {
		t.Errorf("default target = %+v", c.DefaultTarget)
	}
	if c.RuleSets[0].DownloadDetour == nil || *c.RuleSets[0].DownloadDetour != "proxy" {
		t.Errorf("download_detour = %v", c.RuleSets[0].DownloadDetour)
	}
	if c.Rules[2].Target.Kind != TargetNode || c.Rules[2].Target.Node != "11111111-1111-1111-1111-111111111111" {
		t.Errorf("node target = %+v", c.Rules[2].Target)
	}
	// Conditions pass through verbatim — incl. nulls and 1.12 fields.
	if string(c.Rules[0].Conditions["process_name"]) != `["chrome", "Discord"]` &&
		string(c.Rules[0].Conditions["process_name"]) != `["chrome","Discord"]` {
		t.Errorf("process_name passthrough: %s", c.Rules[0].Conditions["process_name"])
	}
	if _, ok := c.Rules[1].Conditions["domain"]; !ok {
		t.Error("null legacy keys must survive in the passthrough")
	}
	if string(c.Rules[2].Conditions["ip_cidr"]) != `["198.51.100.20"]` {
		t.Errorf("ip_cidr passthrough: %s", c.Rules[2].Conditions["ip_cidr"])
	}

	// Round trip: decode(encode(c)) must be semantically identical.
	enc, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var c2 Config
	if err := json.Unmarshal(enc, &c2); err != nil {
		t.Fatalf("re-decode: %v", err)
	}
	enc2, _ := json.Marshal(c2)
	if string(enc) != string(enc2) {
		t.Errorf("round trip drifted:\n1: %s\n2: %s", enc, enc2)
	}
}

func TestConfigDecodeDefaultsID(t *testing.T) {
	var c Config
	if err := json.Unmarshal([]byte(`{"name":"x","rule_sets":[],"rules":[],"default_target":"DefaultProxy"}`), &c); err != nil {
		t.Fatal(err)
	}
	if len(c.ID) != 36 {
		t.Errorf("missing id must default to a fresh uuid, got %q", c.ID)
	}
}

func TestTargetVariants(t *testing.T) {
	cases := []struct {
		json string
		kind TargetKind
	}{
		{`"Direct"`, TargetDirect},
		{`"Block"`, TargetBlock},
		{`"DefaultProxy"`, TargetDefaultProxy},
		{`"DpiBypass"`, TargetDpiBypass},
		{`"HijackDns"`, TargetHijackDns},
		{`{"Node":"u-1"}`, TargetNode},
		{`{"Sniff":{"sniffer":["tls"],"timeout":"300ms"}}`, TargetSniff},
		{`{"Resolve":{"server":"dns-google","strategy":"prefer_ipv4"}}`, TargetResolve},
	}
	for _, tc := range cases {
		var target RuleTarget
		if err := json.Unmarshal([]byte(tc.json), &target); err != nil {
			t.Errorf("%s: %v", tc.json, err)
			continue
		}
		if target.Kind != tc.kind {
			t.Errorf("%s: kind = %s, want %s", tc.json, target.Kind, tc.kind)
		}
		enc, err := json.Marshal(target)
		if err != nil {
			t.Errorf("%s marshal: %v", tc.json, err)
			continue
		}
		var back RuleTarget
		if err := json.Unmarshal(enc, &back); err != nil || back.Kind != tc.kind {
			t.Errorf("%s: round trip -> %s, %v", tc.json, enc, err)
		}
	}

	if err := json.Unmarshal([]byte(`"Reject"`), &RuleTarget{}); err == nil {
		t.Error("unknown unit variant must be rejected")
	}
	routeKinds := map[TargetKind]bool{
		TargetDirect: true, TargetBlock: true, TargetDefaultProxy: true,
		TargetDpiBypass: true, TargetNode: true,
	}
	for _, tc := range cases {
		if (RuleTarget{Kind: tc.kind}).IsRoute() != routeKinds[tc.kind] {
			t.Errorf("IsRoute(%s) wrong", tc.kind)
		}
	}
}

func TestLogicalRuleNesting(t *testing.T) {
	src := `{
	  "target": "Block",
	  "type": "logical",
	  "mode": "and",
	  "invert": true,
	  "rules": [
	    {"target": "Direct", "domain_suffix": [".ads.example.com"]},
	    {"target": "Direct", "network": ["udp"]}
	  ]
	}`
	var r Rule
	if err := json.Unmarshal([]byte(src), &r); err != nil {
		t.Fatal(err)
	}
	if !r.IsLogical() || len(r.Rules) != 2 || r.Target.Kind != TargetBlock {
		t.Fatalf("logical decode: %+v", r)
	}
	if string(r.Conditions["mode"]) != `"and"` {
		t.Errorf("mode passthrough: %s", r.Conditions["mode"])
	}
	enc, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(enc), `"rules":[`) || !strings.Contains(string(enc), `"domain_suffix"`) {
		t.Errorf("nested rules must re-encode: %s", enc)
	}
}

func TestRuleWithoutTargetIsRejected(t *testing.T) {
	var r Rule
	if err := json.Unmarshal([]byte(`{"domain":["x.com"]}`), &r); err == nil {
		t.Error("a rule without target must be rejected")
	}
}
