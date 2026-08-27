package update

import (
	"encoding/json"
	"strings"
	"testing"
)

// validManifestDoc returns a structurally valid schema-1 manifest as a
// mutable document; tests break one field at a time.
func validManifestDoc() map[string]any {
	return map[string]any{
		"schema":       1,
		"seq":          7,
		"channel":      "stable",
		"version":      "v1.2.3",
		"min_version":  "v1.0.0",
		"published_at": "2026-08-27T00:00:00Z",
		"expires_at":   "2027-02-23T00:00:00Z",
		"notes_url":    "https://example.com/notes/v1.2.3",
		"artifacts": []any{
			map[string]any{
				"os":     "windows",
				"arch":   "amd64",
				"kind":   "installer",
				"name":   "iron-link-1.2.3-windows-amd64-setup.exe",
				"size":   12345678,
				"sha256": strings.Repeat("ab", 32),
				"urls":   []any{"https://example.com/iron-link-1.2.3-windows-amd64-setup.exe"},
			},
			map[string]any{
				"os":   "linux",
				"arch": "amd64",
				"kind": "none",
			},
		},
	}
}

func marshalDoc(t *testing.T, doc map[string]any) []byte {
	t.Helper()
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal doc: %v", err)
	}
	return data
}

func TestParseManifestValid(t *testing.T) {
	m, err := ParseManifest(marshalDoc(t, validManifestDoc()))
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if m.Schema != 1 || m.Seq != 7 || m.Channel != "stable" || m.Version != "v1.2.3" {
		t.Fatalf("unexpected header fields: %+v", m)
	}
	if len(m.Artifacts) != 2 || m.Artifacts[0].Kind != KindInstaller || m.Artifacts[1].Kind != KindNone {
		t.Fatalf("unexpected artifacts: %+v", m.Artifacts)
	}
}

func TestParseManifestIgnoresUnknownFields(t *testing.T) {
	doc := validManifestDoc()
	doc["future_field"] = "something new"
	if _, err := ParseManifest(marshalDoc(t, doc)); err != nil {
		t.Fatalf("unknown top-level field must be ignored, got: %v", err)
	}
}

func TestParseManifestMalformedJSON(t *testing.T) {
	if _, err := ParseManifest([]byte("{not json")); err == nil {
		t.Fatal("want parse error for malformed JSON")
	}
}

func TestParseManifestValidation(t *testing.T) {
	setArtifact := func(key string, value any) func(map[string]any) {
		return func(doc map[string]any) {
			doc["artifacts"].([]any)[0].(map[string]any)[key] = value
		}
	}
	cases := []struct {
		name    string
		mutate  func(doc map[string]any)
		wantErr string
	}{
		{"schema zero", func(doc map[string]any) { doc["schema"] = 0 }, "schema"},
		{"schema future", func(doc map[string]any) { doc["schema"] = 2 }, "schema"},
		{"version without v prefix", func(doc map[string]any) { doc["version"] = "1.2.3" }, "version"},
		{"version not full semver", func(doc map[string]any) { doc["version"] = "v1.2" }, "version"},
		{"version with build metadata", func(doc map[string]any) { doc["version"] = "v1.2.3+extra" }, "version"},
		{"version empty", func(doc map[string]any) { doc["version"] = "" }, "version"},
		{"min_version invalid", func(doc map[string]any) { doc["min_version"] = "1.0" }, "min_version"},
		{"artifact kind unknown", setArtifact("kind", "exe"), "kind"},
		{"artifact sha256 short", setArtifact("sha256", strings.Repeat("ab", 31)), "sha256"},
		{"artifact sha256 not hex", setArtifact("sha256", strings.Repeat("zz", 32)), "sha256"},
		{"artifact sha256 empty", setArtifact("sha256", ""), "sha256"},
		{"artifact size zero", setArtifact("size", 0), "size"},
		{"artifact size negative", setArtifact("size", -1), "size"},
		{"artifact urls empty", setArtifact("urls", []any{}), "urls"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := validManifestDoc()
			tc.mutate(doc)
			_, err := ParseManifest(marshalDoc(t, doc))
			if err == nil {
				t.Fatal("want validation error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not mention %q", err, tc.wantErr)
			}
		})
	}
}

func TestParseManifestVersionPrerelease(t *testing.T) {
	doc := validManifestDoc()
	doc["version"] = "v1.2.3-alpha.1"
	if _, err := ParseManifest(marshalDoc(t, doc)); err != nil {
		t.Fatalf("prerelease version must validate, got: %v", err)
	}
}

func TestParseManifestKindNoneSkipsFileChecks(t *testing.T) {
	doc := validManifestDoc()
	doc["artifacts"] = []any{map[string]any{"os": "linux", "arch": "amd64", "kind": "none"}}
	if _, err := ParseManifest(marshalDoc(t, doc)); err != nil {
		t.Fatalf("kind=none entry must not need file facts, got: %v", err)
	}
}
