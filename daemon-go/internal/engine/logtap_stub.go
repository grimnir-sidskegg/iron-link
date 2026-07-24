//go:build !with_clash_api

package engine

// logSinkSupported — see logtap_clash.go. Without the with_clash_api tag the
// box cannot take a platform log writer; StartWithOptions silently degrades
// (no Log events, lines stay on the box's own output).
func logSinkSupported() bool { return false }
