//go:build with_clash_api

package engine

// logSinkSupported: sing-box couples the PlatformLogWriter hook (our
// in-process log tap) to the clash-api BUILD TAG — box.New creates the clash
// server object whenever a platform writer is set. With no
// external_controller configured NOTHING LISTENS (the object only tracks the
// mode), so the §0.3 "no external management surface" decision holds at
// runtime; this is exactly how sing-box's own mobile apps build.
func logSinkSupported() bool { return true }
