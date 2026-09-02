// Package uuid generates random (v4) UUID strings — node / subscription /
// routing identity, in the canonical lowercase-hyphenated format. Kept tiny
// and dependency-free on purpose.
package uuid

import (
	"crypto/rand"
	"encoding/hex"
)

// New returns a fresh random v4 UUID string.
func New() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // crypto/rand failure is unrecoverable
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // RFC 4122 variant
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}
