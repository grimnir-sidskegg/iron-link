package update

import (
	"fmt"
	"strconv"
	"strings"

	"aead.dev/minisign"
)

// Names of the compiled-in verification keys, reported in Verified.KeyName.
const (
	KeyCurrent  = "current"
	KeyRecovery = "recovery"
)

// Verified is the outcome of a successful manifest verification: the parsed
// document plus the name and minisign id of the compiled-in key whose
// signature checked out.
type Verified struct {
	Manifest Manifest
	KeyName  string
	KeyID    uint64
}

type trustedKey struct {
	name string
	pub  minisign.PublicKey
}

func trustedKeys() ([]trustedKey, error) {
	named := []struct{ name, text string }{
		{KeyCurrent, publicKeyCurrent},
		{KeyRecovery, publicKeyRecovery},
	}
	keys := make([]trustedKey, 0, len(named))
	for _, n := range named {
		var pub minisign.PublicKey
		if err := pub.UnmarshalText([]byte(n.text)); err != nil {
			return nil, fmt.Errorf("update: compiled-in %s public key: %w", n.name, err)
		}
		keys = append(keys, trustedKey{name: n.name, pub: pub})
	}
	return keys, nil
}

// VerifyManifest checks the detached minisign signature over the raw
// manifest bytes against the compiled-in public keys, then parses and
// validates the manifest and cross-checks the signed trusted comment
// (version=<v> seq=<n>) against the JSON fields. Nothing about the manifest
// is trusted — or even parsed — before the signature verifies.
func VerifyManifest(manifestJSON, signature []byte) (*Verified, error) {
	keys, err := trustedKeys()
	if err != nil {
		return nil, err
	}
	return verifyManifest(keys, manifestJSON, signature)
}

func verifyManifest(keys []trustedKey, manifestJSON, signature []byte) (*Verified, error) {
	var sig minisign.Signature
	if err := sig.UnmarshalText(signature); err != nil {
		return nil, fmt.Errorf("update: malformed signature: %w", err)
	}
	// Only the prehashed algorithm (minisign -S -H, the publish path) is
	// accepted. The algorithm byte is unsigned, so the verifier pins it here
	// instead of following whatever the signature declares.
	if sig.Algorithm != minisign.HashEdDSA {
		return nil, fmt.Errorf("update: signature algorithm %#x is not the prehashed minisign format", sig.Algorithm)
	}
	var key *trustedKey
	for i := range keys {
		if keys[i].pub.ID() == sig.KeyID {
			key = &keys[i]
			break
		}
	}
	if key == nil {
		return nil, fmt.Errorf("update: signature key %X matches no trusted key", sig.KeyID)
	}
	if !minisign.Verify(key.pub, manifestJSON, signature) {
		return nil, fmt.Errorf("update: signature verification failed (%s key)", key.name)
	}
	m, err := ParseManifest(manifestJSON)
	if err != nil {
		return nil, err
	}
	version, seq, err := parseTrustedComment(sig.TrustedComment)
	if err != nil {
		return nil, err
	}
	if version != m.Version || seq != m.Seq {
		return nil, fmt.Errorf("update: trusted comment version=%s seq=%d does not match manifest version=%s seq=%d",
			version, seq, m.Version, m.Seq)
	}
	return &Verified{Manifest: *m, KeyName: key.name, KeyID: key.pub.ID()}, nil
}

// parseTrustedComment extracts the version=<v> and seq=<n> tokens from the
// signed trusted comment. Both are mandatory: they duplicate the JSON fields
// under the signature so a signature cannot be replayed onto a different
// manifest body.
func parseTrustedComment(comment string) (string, uint64, error) {
	var version, seqText string
	for _, field := range strings.Fields(comment) {
		if v, ok := strings.CutPrefix(field, "version="); ok {
			version = v
		} else if v, ok := strings.CutPrefix(field, "seq="); ok {
			seqText = v
		}
	}
	if version == "" || seqText == "" {
		return "", 0, fmt.Errorf("update: trusted comment %q lacks version=/seq=", comment)
	}
	seq, err := strconv.ParseUint(seqText, 10, 64)
	if err != nil {
		return "", 0, fmt.Errorf("update: trusted comment %q: bad seq: %v", comment, err)
	}
	return version, seq, nil
}
