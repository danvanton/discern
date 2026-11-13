//go:build unix

package makemkv

import (
	"os"
	"syscall"
)

// terminate asks the process to stop (SIGTERM), so makemkvcon can close its
// files. If it hasn't exited after Runner.KillAfter, exec kills it.
func terminate(p *os.Process) error { return p.Signal(syscall.SIGTERM) }
