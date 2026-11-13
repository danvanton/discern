package install

import (
	_ "embed"
	"fmt"
	"regexp"
	"strings"
)

// Release is one MakeMKV version and the SHA-256 of its two Linux tarballs,
// as MakeMKV's signed checksum file lists them.
type Release struct {
	Version   string
	BinSHA256 string // makemkv-bin-<version>.tar.gz: installed at first start
	OSSSHA256 string // makemkv-oss-<version>.tar.gz: built into the image
}

//go:embed pin.env
var pinFile string

var (
	versionRE = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
	sha256RE  = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// Pinned returns the release in pin.env, the one this build of Discern was
// tested with.
func Pinned() (Release, error) { return parsePin(pinFile) }

// parsePin reads pin.env: KEY=VALUE lines, blank lines and # comments. Every
// key must be there exactly once and every value well formed, or nothing is
// returned (fail closed).
func parsePin(s string) (Release, error) {
	vals := map[string]string{}
	for i, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return Release{}, fmt.Errorf("pin.env line %d: not KEY=VALUE", i+1)
		}
		if _, dup := vals[k]; dup {
			return Release{}, fmt.Errorf("pin.env: %s is set twice", k)
		}
		vals[k] = v
	}
	r := Release{
		Version:   vals["MAKEMKV_VERSION"],
		BinSHA256: vals["MAKEMKV_BIN_SHA256"],
		OSSSHA256: vals["MAKEMKV_OSS_SHA256"],
	}
	if !versionRE.MatchString(r.Version) {
		return Release{}, fmt.Errorf("pin.env: MAKEMKV_VERSION %q is not a version like 2.0.0", r.Version)
	}
	for k, v := range map[string]string{"MAKEMKV_BIN_SHA256": r.BinSHA256, "MAKEMKV_OSS_SHA256": r.OSSSHA256} {
		if !sha256RE.MatchString(v) {
			return Release{}, fmt.Errorf("pin.env: %s %q is not 64 lowercase hex digits", k, v)
		}
	}
	if len(vals) != 3 {
		return Release{}, fmt.Errorf("pin.env: expected 3 settings, found %d", len(vals))
	}
	return r, nil
}
