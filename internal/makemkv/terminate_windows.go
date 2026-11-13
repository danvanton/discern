//go:build windows

package makemkv

import "os"

// Windows has no SIGTERM, so a process is killed outright. This only matters
// for running the tests on a Windows development machine.
func terminate(p *os.Process) error { return p.Kill() }
