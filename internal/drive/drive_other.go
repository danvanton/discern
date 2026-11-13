//go:build !linux

package drive

// Status is only available on Linux, where Trayarr runs. On a development
// machine discs are started from the web UI instead.
func Status(string) (State, error) { return Unknown, ErrUnsupported }

// Eject is only available on Linux.
func Eject(string) error { return ErrUnsupported }
