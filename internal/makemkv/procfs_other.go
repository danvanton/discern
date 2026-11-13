//go:build !linux

package makemkv

import "errors"

// Discern runs on Linux. Elsewhere (a developer's Windows or macOS machine)
// there is no /proc: the watchdog then works from output growth alone, and
// can only detect hangs, not stalls.

var errNoProc = errors.New("process I/O counters need Linux /proc")

func procReadCounter(int) (int64, error) { return 0, errNoProc }

func procDebugLogs(int) []DebugLog { return nil }
