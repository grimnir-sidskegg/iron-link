// Package ipc implements the iron-link control transport: a length-prefixed
// JSON framing over a peer-authenticated local connection (UDS on Linux/macOS,
// named pipe on Windows). G1 provides the codec; the server + peercred/DACL auth
// + the Subscribe event hub land in the following G1 steps.
//
// Framing (identical to the Dart client's codec): a 4-byte big-endian
// length prefix followed by exactly that many bytes of JSON body.
package ipc

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

// MaxFrameLen bounds a single message, guarding against a hostile or corrupt
// length prefix forcing a huge allocation. The control protocol is
// low-bandwidth (no bulk data crosses it), so this is generous.
const MaxFrameLen = 16 << 20 // 16 MiB

// WriteFrame encodes v as JSON and writes it prefixed by its 4-byte big-endian
// length.
func WriteFrame(w io.Writer, v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("ipc: encode frame: %w", err)
	}
	if len(body) > MaxFrameLen {
		return fmt.Errorf("ipc: frame too large to send: %d > %d", len(body), MaxFrameLen)
	}
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(body)))
	if _, err := w.Write(hdr[:]); err != nil {
		return fmt.Errorf("ipc: write length prefix: %w", err)
	}
	if _, err := w.Write(body); err != nil {
		return fmt.Errorf("ipc: write frame body: %w", err)
	}
	return nil
}

// ReadFrame reads exactly one length-prefixed JSON frame and unmarshals it into
// v. It returns io.EOF when the peer closes cleanly between frames, and
// io.ErrUnexpectedEOF on a truncated frame.
func ReadFrame(r io.Reader, v any) error {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return err // io.EOF / io.ErrUnexpectedEOF propagated verbatim
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n > MaxFrameLen {
		return fmt.Errorf("ipc: frame too large to read: %d > %d", n, MaxFrameLen)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return err
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("ipc: decode frame body: %w", err)
	}
	return nil
}
