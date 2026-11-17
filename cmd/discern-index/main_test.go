package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danvanton/discern/internal/discdb"
)

// The sample records in testdata/discdb.
const sample = "../../testdata/discdb"

// run writes the index and its checksum; unsigned, no signature file.
func TestRunWritesTheIndexAndChecksum(t *testing.T) {
	out := filepath.Join(t.TempDir(), "discdb-index.json.gz")
	if err := run(sample, out, "test", time.Unix(0, 0), nil); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := discdb.Read(bytes.NewReader(b)); err != nil {
		t.Errorf("the index doesn't read: %v", err)
	}
	if sum, err := os.ReadFile(out + ".sha256"); err != nil || !strings.HasSuffix(string(sum), "  discdb-index.json.gz\n") {
		t.Errorf("checksum file %q, %v", sum, err)
	}
	if _, err := os.Stat(out + ".sig"); err == nil {
		t.Error("a signature without -sign")
	}
}

// A key that isn't the one Discern trusts is refused before a signature is
// written: the workflow then publishes nothing.
func TestRunRefusesAnUntrustedKey(t *testing.T) {
	out := filepath.Join(t.TempDir(), "discdb-index.json.gz")
	err := run(sample, out, "test", time.Unix(0, 0), bytes.Repeat([]byte{7}, 32))
	if err == nil || !strings.Contains(err.Error(), "isn't the key Discern trusts") {
		t.Errorf("an untrusted key: %v", err)
	}
	if _, err := os.Stat(out + ".sig"); err == nil {
		t.Error("a signature was written with an untrusted key")
	}
}
