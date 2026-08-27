package main

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

// TestApplyUpdateURLOverride: unset → the production list, silently; a
// list of http(s) URLs → the override, trimmed, with the startup log line;
// any entry that is not an http(s) URL drops the whole override back to
// the production list, with a log line saying so.
func TestApplyUpdateURLOverride(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want []string
		log  string // "" = nothing logged
	}{
		{"unset", "", updateManifestURLs, ""},
		{"blank entries only", " , ", updateManifestURLs, ""},
		{"one http URL", "http://198.51.100.1:8000/update.json",
			[]string{"http://198.51.100.1:8000/update.json"}, "overridden by IRON_LINK_UPDATE_URL"},
		{"list with spaces", " https://update.example.com/update.json , http://127.0.0.1:8000/update.json ,",
			[]string{"https://update.example.com/update.json", "http://127.0.0.1:8000/update.json"},
			"overridden by IRON_LINK_UPDATE_URL"},
		{"file scheme", "file:///tmp/update.json", updateManifestURLs, "override ignored"},
		{"no scheme", "198.51.100.1:8000/update.json", updateManifestURLs, "override ignored"},
		{"no host", "http:///update.json", updateManifestURLs, "override ignored"},
		{"one bad entry rejects all", "https://update.example.com/update.json,ftp://example.com/update.json",
			updateManifestURLs, "override ignored"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := emptyManager(t)
			var logw bytes.Buffer
			m.applyUpdateURLOverride(func(string) string { return tc.raw }, &logw)
			if got := m.manifestURLs(); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("manifest URLs = %q, want %q", got, tc.want)
			}
			switch {
			case tc.log == "" && logw.Len() != 0:
				t.Fatalf("unexpected log: %q", logw.String())
			case tc.log != "" && !strings.Contains(logw.String(), tc.log):
				t.Fatalf("log = %q, want it to contain %q", logw.String(), tc.log)
			}
		})
	}
}
