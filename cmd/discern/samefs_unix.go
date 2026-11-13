//go:build unix

package main

import (
	"fmt"
	"syscall"
)

// sameFilesystem reports whether two paths live on the same filesystem: the
// same device id, which is what makes a move between them a rename.
func sameFilesystem(a, b string) (bool, error) {
	var sa, sb syscall.Stat_t
	if err := syscall.Stat(a, &sa); err != nil {
		return false, fmt.Errorf("%s: %w", a, err)
	}
	if err := syscall.Stat(b, &sb); err != nil {
		return false, fmt.Errorf("%s: %w", b, err)
	}
	return sa.Dev == sb.Dev, nil
}
