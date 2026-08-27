//go:build windows

package main

// updateApplySupported gates the download/apply update verbs: Windows is
// the only platform with a downloadable update channel (Linux/macOS are
// notify-only; the package manager owns the Linux install).
const updateApplySupported = true
