//go:build !windows

package main

// updateApplySupported: no downloadable update channel off Windows —
// download_update / apply_update answer with the not-supported error. The
// orchestration behind them stays cross-platform for tests.
const updateApplySupported = false
