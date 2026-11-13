// Command fake-makemkvcon stands in for makemkvcon during development: point
// Discern's MakeMKV path at it and describe its behaviour in the
// FAKE_MAKEMKVCON environment variable (see internal/makemkv/fake).
//
// Example, a rip that stalls at 640 MiB while it keeps reading:
//
//	FAKE_MAKEMKVCON='{"mkv":{"total_bytes":4000000000,"stall_at_bytes":671088640,"chunk_delay_ms":5}}'
package main

import (
	"os"

	"github.com/danvanton/discern/internal/makemkv/fake"
)

func main() {
	os.Exit(fake.Main(os.Args[1:], os.Stdout, os.Stderr))
}
