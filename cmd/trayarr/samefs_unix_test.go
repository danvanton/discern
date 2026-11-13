//go:build unix

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSameFilesystem(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	os.Mkdir(a, 0o755)
	os.Mkdir(b, 0o755)
	if same, err := sameFilesystem(a, b); err != nil || !same {
		t.Errorf("two folders in one temp dir: same = %v, err = %v", same, err)
	}
	if _, err := sameFilesystem(a, filepath.Join(dir, "missing")); err == nil {
		t.Error("a missing folder should be an error")
	}
}
