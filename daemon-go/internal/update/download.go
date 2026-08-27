package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Download fetches an installer artifact into destDir and returns the
// final file path. The stream is size-capped by the declared size and
// sha256-hashed while it lands in a same-directory temp file; only a body
// that matches both facts is atomically renamed to the declared (base)
// name. Download never executes anything — applying is a later, separate
// step that re-verifies with VerifyFile first.
//
// URLs are tried in order for reachability failures (connect errors,
// non-200 statuses, truncated bodies). A full body with the wrong size or
// hash aborts the whole download: that is corrupt or hostile data, not a
// mirror hiccup, and it must surface instead of being retried elsewhere.
func Download(ctx context.Context, artifact Artifact, destDir string, transport http.RoundTripper) (string, error) {
	if artifact.Kind != KindInstaller {
		return "", fmt.Errorf("update: artifact kind %q is not downloadable", artifact.Kind)
	}
	if transport == nil {
		return "", errors.New("update: no transport configured")
	}
	if err := artifact.validate(); err != nil {
		return "", fmt.Errorf("update: artifact: %w", err)
	}
	name, err := sanitizeArtifactName(artifact.Name)
	if err != nil {
		return "", err
	}
	client := &http.Client{Transport: transport}
	var errs []error
	for _, url := range artifact.URLs {
		dest, retryable, err := downloadOne(ctx, client, url, artifact, destDir, name)
		if err == nil {
			return dest, nil
		}
		errs = append(errs, err)
		if !retryable {
			break
		}
	}
	return "", fmt.Errorf("update: download failed: %w", errors.Join(errs...))
}

// downloadOne pulls one URL into destDir/name via a temp file. retryable
// reports whether the next mirror is worth trying.
func downloadOne(ctx context.Context, client *http.Client, url string, artifact Artifact, destDir, name string) (dest string, retryable bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", true, fmt.Errorf("%s: %w", url, err)
	}
	req.Header.Set("User-Agent", checkUserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return "", true, fmt.Errorf("%s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", true, fmt.Errorf("GET %s: %s", url, resp.Status)
	}

	tmp, err := os.CreateTemp(destDir, tempPrefix+"*")
	if err != nil {
		return "", false, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename
	if runtime.GOOS != "windows" {
		if err := tmp.Chmod(0o600); err != nil {
			tmp.Close()
			return "", false, err
		}
	}
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, hash), io.LimitReader(resp.Body, artifact.Size+1))
	if err != nil {
		tmp.Close()
		return "", true, fmt.Errorf("%s: %w", url, err)
	}
	if n > artifact.Size {
		tmp.Close()
		return "", false, fmt.Errorf("%s: body exceeds the declared size %d", url, artifact.Size)
	}
	if n < artifact.Size {
		tmp.Close()
		return "", true, fmt.Errorf("%s: body truncated at %d of %d bytes", url, n, artifact.Size)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return "", false, err
	}
	if err := tmp.Close(); err != nil {
		return "", false, err
	}
	if sum := hex.EncodeToString(hash.Sum(nil)); !strings.EqualFold(sum, artifact.SHA256) {
		return "", false, fmt.Errorf("%s: sha256 mismatch: got %s, manifest says %s", url, sum, artifact.SHA256)
	}
	dest = filepath.Join(destDir, name)
	if err := renameWithRetry(tmpName, dest); err != nil {
		return "", false, err
	}
	return dest, false, nil
}

// tempPrefix names the same-directory temp file a body lands in before the
// rename; RemoveStaleTemps sweeps by it.
const tempPrefix = ".update-"

// RemoveStaleTemps deletes temp files a Download never finalized: a process
// that exits mid-transfer (service stop, crash) never runs the deferred
// remove, and nothing else would ever clear them. The caller runs it while
// no transfer writes to dir. Returns how many were removed; an error is
// informational, a failed sweep never blocks the next download.
func RemoveStaleTemps(dir string) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, fmt.Errorf("update: sweep %s: %w", dir, err)
	}
	removed := 0
	var errs []error
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), tempPrefix) {
			continue
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
			errs = append(errs, err)
			continue
		}
		removed++
	}
	if len(errs) > 0 {
		return removed, fmt.Errorf("update: sweep %s: %w", dir, errors.Join(errs...))
	}
	return removed, nil
}

// VerifyFile re-checks a downloaded artifact on disk against the manifest
// facts (size + sha256). The pre-apply gate runs it again right before the
// file is handed over for execution, so a file swapped after download is
// caught.
func VerifyFile(filePath string, artifact Artifact) error {
	f, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("update: verify %s: %w", filePath, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("update: verify %s: %w", filePath, err)
	}
	if info.Size() != artifact.Size {
		return fmt.Errorf("update: verify %s: size %d, manifest says %d", filePath, info.Size(), artifact.Size)
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return fmt.Errorf("update: verify %s: %w", filePath, err)
	}
	if sum := hex.EncodeToString(hash.Sum(nil)); !strings.EqualFold(sum, artifact.SHA256) {
		return fmt.Errorf("update: verify %s: sha256 mismatch: got %s, manifest says %s", filePath, sum, artifact.SHA256)
	}
	return nil
}

// sanitizeArtifactName reduces a manifest-supplied file name to a safe
// single path component. The manifest is signed, but the name still
// crosses a trust boundary before becoming a path: strip any directory
// part (both separator styles) and refuse names that could escape the
// destination or address an NTFS alternate data stream.
func sanitizeArtifactName(name string) (string, error) {
	base := path.Base(strings.ReplaceAll(name, `\`, "/"))
	if base == "" || base == "." || base == ".." || base == "/" {
		return "", fmt.Errorf("update: artifact name %q has no usable base name", name)
	}
	if strings.Contains(base, ":") {
		return "", fmt.Errorf("update: artifact name %q contains ':'", name)
	}
	return base, nil
}

// renameWithRetry finalizes the temp→dest swap. On Windows a freshly
// written file can be transiently locked (Defender/indexer hold a handle
// for a beat), failing the rename — retry briefly, same as the store's
// atomic writes. On other systems this is a single attempt.
func renameWithRetry(from, to string) error {
	var err error
	for attempt := 0; ; attempt++ {
		if err = os.Rename(from, to); err == nil {
			return nil
		}
		if runtime.GOOS != "windows" || attempt >= 10 {
			return err
		}
		time.Sleep(50 * time.Millisecond)
	}
}
