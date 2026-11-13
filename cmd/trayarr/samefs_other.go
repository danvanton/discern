//go:build !unix

package main

import "errors"

// sameFilesystem can't be checked here; Trayarr runs on Linux.
func sameFilesystem(string, string) (bool, error) {
	return false, errors.New("the filesystem check needs Linux")
}
