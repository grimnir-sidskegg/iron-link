package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var testPayload = []byte("iron-link fake installer payload: not executable, just bytes\n")

// payloadArtifact declares testPayload's true facts under the given name.
func payloadArtifact(name string, urls ...string) Artifact {
	sum := sha256.Sum256(testPayload)
	return Artifact{
		OS:     "windows",
		Arch:   "amd64",
		Kind:   KindInstaller,
		Name:   name,
		Size:   int64(len(testPayload)),
		SHA256: hex.EncodeToString(sum[:]),
		URLs:   urls,
	}
}

// servePayload serves testPayload at /file.
func servePayload(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/file", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(testPayload)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// dirEntries lists the names left in dir — after a failure it must be
// empty (no temp leftovers), after a success exactly the final file.
func dirEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestDownloadSuccess(t *testing.T) {
	srv := servePayload(t)
	dest := t.TempDir()
	artifact := payloadArtifact("iron-link-1.2.3-setup.exe", srv.URL+"/file")

	got, err := Download(context.Background(), artifact, dest, http.DefaultTransport)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	want := filepath.Join(dest, "iron-link-1.2.3-setup.exe")
	if got != want {
		t.Fatalf("path %q, want %q", got, want)
	}
	data, err := os.ReadFile(got)
	if err != nil {
		t.Fatalf("read downloaded file: %v", err)
	}
	if string(data) != string(testPayload) {
		t.Fatalf("content mismatch: %q", data)
	}
	// Rename atomicity: only the final name remains, no temp files.
	if names := dirEntries(t, dest); len(names) != 1 || names[0] != "iron-link-1.2.3-setup.exe" {
		t.Fatalf("dest dir holds %v, want only the final file", names)
	}
	if err := VerifyFile(got, artifact); err != nil {
		t.Fatalf("VerifyFile on a fresh download: %v", err)
	}
}

func TestDownloadShaMismatch(t *testing.T) {
	srv := servePayload(t)
	dest := t.TempDir()
	artifact := payloadArtifact("setup.exe", srv.URL+"/file")
	artifact.SHA256 = strings.Repeat("00", 32)

	_, err := Download(context.Background(), artifact, dest, http.DefaultTransport)
	if err == nil || !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Fatalf("want the sha256 mismatch error, got %v", err)
	}
	if names := dirEntries(t, dest); len(names) != 0 {
		t.Fatalf("temp not cleaned up after mismatch: %v", names)
	}
}

func TestDownloadOversize(t *testing.T) {
	srv := servePayload(t)
	dest := t.TempDir()
	artifact := payloadArtifact("setup.exe", srv.URL+"/file")
	artifact.Size = int64(len(testPayload)) - 1

	_, err := Download(context.Background(), artifact, dest, http.DefaultTransport)
	if err == nil || !strings.Contains(err.Error(), "exceeds the declared size") {
		t.Fatalf("want the oversize error, got %v", err)
	}
	if names := dirEntries(t, dest); len(names) != 0 {
		t.Fatalf("temp not cleaned up after oversize: %v", names)
	}
}

func TestDownloadTruncated(t *testing.T) {
	srv := servePayload(t)
	dest := t.TempDir()
	artifact := payloadArtifact("setup.exe", srv.URL+"/file")
	artifact.Size = int64(len(testPayload)) + 3

	_, err := Download(context.Background(), artifact, dest, http.DefaultTransport)
	if err == nil || !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("want the truncation error, got %v", err)
	}
	if names := dirEntries(t, dest); len(names) != 0 {
		t.Fatalf("temp not cleaned up after truncation: %v", names)
	}
}

func TestDownloadMirrorFallback(t *testing.T) {
	empty := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(empty.Close)
	srv := servePayload(t)
	dest := t.TempDir()
	artifact := payloadArtifact("setup.exe", empty.URL+"/file", srv.URL+"/file")

	got, err := Download(context.Background(), artifact, dest, http.DefaultTransport)
	if err != nil {
		t.Fatalf("Download with a 404 first mirror: %v", err)
	}
	if got != filepath.Join(dest, "setup.exe") {
		t.Fatalf("unexpected path %q", got)
	}
}

func TestDownloadRefusesNonInstaller(t *testing.T) {
	for _, kind := range []string{KindNone, KindPkg, "exe"} {
		artifact := payloadArtifact("setup.exe", "http://198.51.100.1/file")
		artifact.Kind = kind
		if _, err := Download(context.Background(), artifact, t.TempDir(), http.DefaultTransport); err == nil {
			t.Fatalf("kind %q must be refused", kind)
		}
	}
}

func TestDownloadNameSanitization(t *testing.T) {
	flattened := []struct {
		name string
		want string
	}{
		{"sub/dir/tool.exe", "tool.exe"},
		{`..\..\evil.exe`, "evil.exe"},
		{"/abs/path/setup.exe", "setup.exe"},
		{`C:\x.exe`, "x.exe"}, // the colon sits in the stripped directory part
	}
	for _, tc := range flattened {
		t.Run(tc.name, func(t *testing.T) {
			srv := servePayload(t)
			dest := t.TempDir()
			artifact := payloadArtifact(tc.name, srv.URL+"/file")
			got, err := Download(context.Background(), artifact, dest, http.DefaultTransport)
			if err != nil {
				t.Fatalf("Download: %v", err)
			}
			if got != filepath.Join(dest, tc.want) {
				t.Fatalf("path %q, want basename %q inside the dest dir", got, tc.want)
			}
		})
	}
	// Rejections happen before any fetch — the URL is never dialed.
	rejected := []string{"..", ".", "", "a:b.exe", "name:ads.exe"}
	for _, name := range rejected {
		artifact := payloadArtifact(name, "http://198.51.100.1/file")
		_, err := Download(context.Background(), artifact, t.TempDir(), http.DefaultTransport)
		if err == nil || !strings.Contains(err.Error(), "artifact name") {
			t.Fatalf("name %q must be rejected by the sanitizer, got %v", name, err)
		}
	}
}

func TestVerifyFileDetectsTampering(t *testing.T) {
	srv := servePayload(t)
	dest := t.TempDir()
	artifact := payloadArtifact("setup.exe", srv.URL+"/file")
	path, err := Download(context.Background(), artifact, dest, http.DefaultTransport)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}

	// Same-size corruption: only the hash can catch it.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data[0] ^= 0x01
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyFile(path, artifact); err == nil || !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Fatalf("want the sha256 mismatch, got %v", err)
	}

	// Size drift is caught before hashing.
	if err := os.WriteFile(path, append(data, '!'), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyFile(path, artifact); err == nil || !strings.Contains(err.Error(), "size") {
		t.Fatalf("want the size error, got %v", err)
	}

	if err := VerifyFile(filepath.Join(dest, "missing.exe"), artifact); err == nil {
		t.Fatal("want an error for a missing file")
	}
}

// TestRemoveStaleTemps sweeps only the leftover temp files: the finished
// artifact, unrelated files and directories stay.
func TestRemoveStaleTemps(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{".update-1234", ".update-abcd", "iron-link-setup.exe", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, ".update-dir"), 0o700); err != nil {
		t.Fatal(err)
	}
	n, err := RemoveStaleTemps(dir)
	if err != nil {
		t.Fatalf("RemoveStaleTemps: %v", err)
	}
	if n != 2 {
		t.Fatalf("removed %d, want 2", n)
	}
	got := strings.Join(dirEntries(t, dir), ",")
	if got != ".update-dir,iron-link-setup.exe,notes.txt" {
		t.Fatalf("left %q", got)
	}
	if _, err := RemoveStaleTemps(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("want an error for a missing directory")
	}
}
