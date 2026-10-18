// Package uuid generates UUID strings — node / subscription / routing
// identity, in the canonical lowercase-hyphenated format. Kept tiny and
// dependency-free on purpose.
package uuid

import (
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
)

// New returns a fresh random v4 UUID string.
func New() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // crypto/rand failure is unrecoverable
	}
	return format(b, 0x40)
}

// FromName returns a name-based (SHA-1, v5-style) UUID string: the same name
// always yields the same id, so an entity re-created under the same key keeps
// its identity.
func FromName(name string) string {
	sum := sha1.Sum([]byte("iron-link:" + name))
	return format([16]byte(sum[:16]), 0x50)
}

// format stamps the version nibble and the RFC 4122 variant onto b and renders
// it.
func format(b [16]byte, version byte) string {
	b[6] = (b[6] & 0x0f) | version
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}
