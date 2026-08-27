package update

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"aead.dev/minisign"
)

func genTestKeys(t *testing.T) (current, recovery minisign.PrivateKey, keys []trustedKey) {
	t.Helper()
	currentPub, currentPriv, err := minisign.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate current key: %v", err)
	}
	recoveryPub, recoveryPriv, err := minisign.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate recovery key: %v", err)
	}
	return currentPriv, recoveryPriv, []trustedKey{
		{name: KeyCurrent, pub: currentPub},
		{name: KeyRecovery, pub: recoveryPub},
	}
}

// signPrehashed signs data the way the publish path does: prehashed ("ED",
// the minisign -S -H format) with the given trusted comment.
func signPrehashed(t *testing.T, key minisign.PrivateKey, data []byte, trustedComment string) []byte {
	t.Helper()
	r := minisign.NewReader(bytes.NewReader(data))
	if _, err := io.Copy(io.Discard, r); err != nil {
		t.Fatalf("hash message: %v", err)
	}
	return r.SignWithComments(key, trustedComment, "test signature")
}

func fixtureManifest(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/update.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return data
}

func TestVerifyManifestRoundTrip(t *testing.T) {
	manifest := fixtureManifest(t) // version=v1.2.3 seq=7
	const comment = "version=v1.2.3 seq=7"
	cases := []struct {
		name    string
		build   func(t *testing.T, current, recovery minisign.PrivateKey) (data, sig []byte)
		wantKey string
		wantErr string
	}{
		{
			name: "current key ok",
			build: func(t *testing.T, current, _ minisign.PrivateKey) ([]byte, []byte) {
				return manifest, signPrehashed(t, current, manifest, comment)
			},
			wantKey: KeyCurrent,
		},
		{
			name: "recovery key ok",
			build: func(t *testing.T, _, recovery minisign.PrivateKey) ([]byte, []byte) {
				return manifest, signPrehashed(t, recovery, manifest, comment)
			},
			wantKey: KeyRecovery,
		},
		{
			name: "flipped byte in manifest",
			build: func(t *testing.T, current, _ minisign.PrivateKey) ([]byte, []byte) {
				sig := signPrehashed(t, current, manifest, comment)
				tampered := bytes.Clone(manifest)
				tampered[len(tampered)/2] ^= 0x01
				return tampered, sig
			},
			wantErr: "signature verification failed",
		},
		{
			// Correct key, correct comment, but the legacy (non-prehashed)
			// algorithm: the verifier pins the publish path's -H format.
			name: "legacy algorithm refused",
			build: func(t *testing.T, current, _ minisign.PrivateKey) ([]byte, []byte) {
				return manifest, minisign.SignWithComments(current, manifest, comment, "test signature")
			},
			wantErr: "not the prehashed",
		},
		{
			name: "signature by unknown key",
			build: func(t *testing.T, _, _ minisign.PrivateKey) ([]byte, []byte) {
				_, stranger, err := minisign.GenerateKey(nil)
				if err != nil {
					t.Fatalf("generate stranger key: %v", err)
				}
				return manifest, signPrehashed(t, stranger, manifest, comment)
			},
			wantErr: "matches no trusted key",
		},
		{
			name: "tampered trusted comment",
			build: func(t *testing.T, current, _ minisign.PrivateKey) ([]byte, []byte) {
				sig := signPrehashed(t, current, manifest, comment)
				return manifest, bytes.Replace(sig, []byte("seq=7"), []byte("seq=8"), 1)
			},
			wantErr: "signature verification failed",
		},
		{
			name: "comment version mismatch",
			build: func(t *testing.T, current, _ minisign.PrivateKey) ([]byte, []byte) {
				return manifest, signPrehashed(t, current, manifest, "version=v9.9.9 seq=7")
			},
			wantErr: "does not match manifest",
		},
		{
			name: "comment seq mismatch",
			build: func(t *testing.T, current, _ minisign.PrivateKey) ([]byte, []byte) {
				return manifest, signPrehashed(t, current, manifest, "version=v1.2.3 seq=8")
			},
			wantErr: "does not match manifest",
		},
		{
			name: "comment missing seq",
			build: func(t *testing.T, current, _ minisign.PrivateKey) ([]byte, []byte) {
				return manifest, signPrehashed(t, current, manifest, "version=v1.2.3")
			},
			wantErr: "lacks version=/seq=",
		},
		{
			name: "comment without values",
			build: func(t *testing.T, current, _ minisign.PrivateKey) ([]byte, []byte) {
				return manifest, signPrehashed(t, current, manifest, "timestamp:1756252800")
			},
			wantErr: "lacks version=/seq=",
		},
		{
			name: "comment seq not a number",
			build: func(t *testing.T, current, _ minisign.PrivateKey) ([]byte, []byte) {
				return manifest, signPrehashed(t, current, manifest, "version=v1.2.3 seq=x")
			},
			wantErr: "bad seq",
		},
		{
			name: "garbage signature",
			build: func(t *testing.T, _, _ minisign.PrivateKey) ([]byte, []byte) {
				return manifest, []byte("not a minisign signature")
			},
			wantErr: "malformed signature",
		},
		{
			name: "signed but invalid manifest",
			build: func(t *testing.T, current, _ minisign.PrivateKey) ([]byte, []byte) {
				doc := validManifestDoc()
				doc["schema"] = 2
				data := marshalDoc(t, doc)
				return data, signPrehashed(t, current, data, comment)
			},
			wantErr: "schema",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			current, recovery, keys := genTestKeys(t)
			data, sig := tc.build(t, current, recovery)
			v, err := verifyManifest(keys, data, sig)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatal("want error, got nil")
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %q does not mention %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("verifyManifest: %v", err)
			}
			if v.KeyName != tc.wantKey {
				t.Fatalf("verified by %q, want %q", v.KeyName, tc.wantKey)
			}
			wantID := keys[0].pub.ID()
			if tc.wantKey == KeyRecovery {
				wantID = keys[1].pub.ID()
			}
			if v.KeyID != wantID {
				t.Fatalf("verified key id %X, want %X", v.KeyID, wantID)
			}
			if v.Manifest.Version != "v1.2.3" || v.Manifest.Seq != 7 {
				t.Fatalf("unexpected verified manifest: %+v", v.Manifest)
			}
		})
	}
}

// TestCompiledInKeysParse guards the constants in keys.go against a
// broken edit: both must parse as minisign public keys.
func TestCompiledInKeysParse(t *testing.T) {
	keys, err := trustedKeys()
	if err != nil {
		t.Fatalf("trustedKeys: %v", err)
	}
	if len(keys) != 2 || keys[0].name != KeyCurrent || keys[1].name != KeyRecovery {
		t.Fatalf("unexpected key set: %+v", keys)
	}
	if keys[0].pub.ID() == keys[1].pub.ID() {
		t.Fatal("current and recovery keys must differ")
	}
}

// TestFixtureVerifiesWithCompiledKeys pins the whole chain end to end: the
// static fixture signature was made by the current key whose public half is
// compiled into keys.go. Replacing the dev placeholder keys with production
// keys unbinds the fixture, so the test skips (with the re-sign command in
// keys.go) rather than fail the suite at key-replacement time; a tampered
// fixture under a matching key still fails hard.
func TestFixtureVerifiesWithCompiledKeys(t *testing.T) {
	manifest := fixtureManifest(t)
	sig, err := os.ReadFile("testdata/update.json.minisig")
	if err != nil {
		t.Fatalf("read fixture signature: %v", err)
	}
	var parsed minisign.Signature
	if err := parsed.UnmarshalText(sig); err != nil {
		t.Fatalf("parse fixture signature: %v", err)
	}
	keys, err := trustedKeys()
	if err != nil {
		t.Fatalf("trustedKeys: %v", err)
	}
	if keys[0].pub.ID() != parsed.KeyID {
		t.Skipf("fixture signed by key %X, compiled-in current key is %X — re-sign testdata/update.json with the current secret key (command in keys.go) to re-pin this test",
			parsed.KeyID, keys[0].pub.ID())
	}
	v, err := VerifyManifest(manifest, sig)
	if err != nil {
		t.Fatalf("VerifyManifest: %v", err)
	}
	if v.KeyName != KeyCurrent {
		t.Fatalf("verified by %q, want %q", v.KeyName, KeyCurrent)
	}
	if v.Manifest.Version != "v1.2.3" || v.Manifest.Seq != 7 || v.Manifest.Channel != "stable" {
		t.Fatalf("unexpected fixture manifest: %+v", v.Manifest)
	}
	if len(v.Manifest.Artifacts) != 3 || v.Manifest.Artifacts[0].Kind != KindInstaller {
		t.Fatalf("unexpected fixture artifacts: %+v", v.Manifest.Artifacts)
	}
}

// TestVerifySignedFile verifies an externally supplied manifest + signature
// pair against the compiled-in keys. scripts/sign-update.sh runs it right
// after signing so a key or manifest mismatch is caught before publishing;
// it skips in a normal test run.
func TestVerifySignedFile(t *testing.T) {
	manifestPath := os.Getenv("IRON_LINK_UPDATE_MANIFEST")
	sigPath := os.Getenv("IRON_LINK_UPDATE_SIG")
	if manifestPath == "" || sigPath == "" {
		t.Skip("IRON_LINK_UPDATE_MANIFEST / IRON_LINK_UPDATE_SIG not set")
	}
	manifest, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	sig, err := os.ReadFile(sigPath)
	if err != nil {
		t.Fatalf("read signature: %v", err)
	}
	v, err := VerifyManifest(manifest, sig)
	if err != nil {
		t.Fatalf("VerifyManifest: %v", err)
	}
	t.Logf("verified by the %s key: version=%s seq=%d channel=%s",
		v.KeyName, v.Manifest.Version, v.Manifest.Seq, v.Manifest.Channel)
}
