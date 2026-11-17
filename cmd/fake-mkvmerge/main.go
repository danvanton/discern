// Command fake-mkvmerge stands in for mkvmerge during development, for the
// Backup + remux strategy: put it on the PATH as "mkvmerge" with
// FAKE_MKVMERGE=on (see internal/makemkv/fake).
package main

import (
	"os"

	"github.com/danvanton/discern/internal/makemkv/fake"
)

func main() {
	if os.Getenv(fake.MkvmergeEnvVar) == "" {
		os.Setenv(fake.MkvmergeEnvVar, "on")
	}
	os.Exit(fake.Mkvmerge(os.Args[1:], os.Stdout, os.Stderr))
}
