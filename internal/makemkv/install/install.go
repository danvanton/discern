// Package install puts MakeMKV's command-line program, makemkvcon, into
// Discern's data folder at first start (decision D6), so MakeMKV's
// proprietary part is never in the published image.
//
// MakeMKV for Linux comes in two tarballs:
//
//   - makemkv-oss: the libraries makemkvcon loads, under LGPL and GPL file by
//     file. The image build compiles it (see the Dockerfile).
//   - makemkv-bin: makemkvcon itself, prebuilt for each CPU, under MakeMKV's
//     EULA. This package downloads it, but only once the user has accepted
//     the EULA.
//
// The download is trusted because its SHA-256 matches the pin compiled into
// Discern (pin.env). The pin itself is trusted because it came from MakeMKV's
// GPG-signed checksum file, checked against a pinned key when the pin was set
// (scripts/update-makemkv-pin.sh) and again by the tests on every CI run.
// That is how go.sum works: verify once when the pin is added, then compare
// hashes.
package install

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DownloadURL is where MakeMKV publishes its Linux tarballs.
const DownloadURL = "https://www.makemkv.com/download/"

// EULAURL is MakeMKV's licence, which the user accepts with
// MAKEMKV_ACCEPT_EULA=yes.
const EULAURL = "https://www.makemkv.com/eula/"

// ErrEULANotAccepted means MAKEMKV_ACCEPT_EULA is not "yes". Discern neither
// installs nor uses MakeMKV until it is.
var ErrEULANotAccepted = errors.New("MakeMKV's licence is not accepted: read it at " + EULAURL +
	" and set MAKEMKV_ACCEPT_EULA=yes if you accept it")

// Size limits. The 2.0.0 tarball is 19 MB and its largest file, makemkvcon,
// 9 MB; the limits only stop a broken download from filling the disk.
const (
	maxTarball = 256 << 20
	maxFile    = 128 << 20
)

// Installer installs one MakeMKV release into Dir/<version>.
type Installer struct {
	Dir        string // e.g. /config/makemkv
	Release    Release
	AcceptEULA bool
	Arch       string // Go's GOARCH: amd64 or arm64

	BaseURL string       // defaults to DownloadURL
	Client  *http.Client // defaults to a client with a 10-minute timeout

	maxTarball int64 // tests shrink it
}

// Makemkvcon is where makemkvcon is once installed. It looks for its data
// files in ../share/MakeMKV next to itself.
func (in *Installer) Makemkvcon() string {
	return filepath.Join(in.versionDir(), "bin", "makemkvcon")
}

func (in *Installer) versionDir() string { return filepath.Join(in.Dir, in.Release.Version) }

// marker is written last into an install, so a folder without it (a crash
// mid-install) is never mistaken for a finished one.
const markerName = "installed.json"

type marker struct {
	Version   string    `json:"version"`
	BinSHA256 string    `json:"bin_sha256"`
	Arch      string    `json:"arch"`
	Installed time.Time `json:"installed"`
}

// Ensure installs the release unless it is already there, and reports
// whether it installed anything. Running it again is a no-op. It refuses to
// do anything, even with MakeMKV already installed, while the EULA is not
// accepted.
func (in *Installer) Ensure(ctx context.Context) (installed bool, err error) {
	if !in.AcceptEULA {
		return false, ErrEULANotAccepted
	}
	arch, err := binArch(in.Arch)
	if err != nil {
		return false, err
	}
	if in.done(arch) {
		// Also here, not only after an install: a stop between the rename
		// and the clean-up would otherwise leave them for good.
		in.removeLeftovers()
		in.removeOtherVersions()
		return false, nil
	}
	if err := os.MkdirAll(in.Dir, 0o755); err != nil {
		return false, err
	}
	in.removeLeftovers()

	// Everything is built in a temporary folder next to the final one and
	// renamed into place at the end. A rename within one filesystem is
	// atomic, so the install is either complete or absent.
	stage, err := os.MkdirTemp(in.Dir, ".tmp-")
	if err != nil {
		return false, err
	}
	defer os.RemoveAll(stage)

	tarball := filepath.Join(stage, "makemkv-bin.tar.gz")
	name := "makemkv-bin-" + in.Release.Version + ".tar.gz"
	sum, err := in.download(ctx, name, tarball)
	if err != nil {
		return false, err
	}
	// Check before parsing: the tarball is opened only once it is known to
	// be exactly the file MakeMKV signed.
	if sum != in.Release.BinSHA256 {
		return false, fmt.Errorf("%s: SHA-256 is %s, but the pinned release says %s", name, sum, in.Release.BinSHA256)
	}
	root := filepath.Join(stage, "root")
	if err := extract(tarball, root, in.Release.Version, arch); err != nil {
		return false, fmt.Errorf("%s: %w", name, err)
	}
	m, err := json.MarshalIndent(marker{in.Release.Version, sum, arch, time.Now().UTC()}, "", "  ")
	if err != nil {
		return false, err
	}
	if err := os.WriteFile(filepath.Join(root, markerName), m, 0o644); err != nil {
		return false, err
	}
	if err := os.RemoveAll(in.versionDir()); err != nil { // an unfinished earlier attempt
		return false, err
	}
	if err := os.Rename(root, in.versionDir()); err != nil {
		return false, err
	}
	in.removeOtherVersions()
	return true, nil
}

// binArch maps Go's architecture name to the folder makemkv-bin uses.
func binArch(goarch string) (string, error) {
	switch goarch {
	case "amd64", "arm64":
		return goarch, nil
	}
	return "", fmt.Errorf("MakeMKV for Linux has no %s build that Discern supports (amd64 and arm64 only)", goarch)
}

// done reports whether this release is fully installed for this CPU.
func (in *Installer) done(arch string) bool {
	b, err := os.ReadFile(filepath.Join(in.versionDir(), markerName))
	if err != nil {
		return false
	}
	var m marker
	if json.Unmarshal(b, &m) != nil {
		return false
	}
	if m.Version != in.Release.Version || m.BinSHA256 != in.Release.BinSHA256 || m.Arch != arch {
		return false
	}
	_, err = os.Stat(in.Makemkvcon())
	return err == nil
}

// removeLeftovers deletes temporary folders from installs that were cut off.
func (in *Installer) removeLeftovers() {
	old, _ := filepath.Glob(filepath.Join(in.Dir, ".tmp-*"))
	for _, p := range old {
		os.RemoveAll(p)
	}
}

// removeOtherVersions deletes earlier releases once the new one is in place.
// Only folders named like a version are touched.
func (in *Installer) removeOtherVersions() {
	entries, _ := os.ReadDir(in.Dir)
	for _, e := range entries {
		if e.IsDir() && e.Name() != in.Release.Version && versionRE.MatchString(e.Name()) {
			os.RemoveAll(filepath.Join(in.Dir, e.Name()))
		}
	}
}

// download saves BaseURL+name to dst and returns its SHA-256 in hex.
func (in *Installer) download(ctx context.Context, name, dst string) (string, error) {
	base, client, limit := in.BaseURL, in.Client, in.maxTarball
	if base == "" {
		base = DownloadURL
	}
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Minute}
	}
	if limit == 0 {
		limit = maxTarball
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+name, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("downloading %s: %w", name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("downloading %s: HTTP %d", name, resp.StatusCode)
	}
	if resp.ContentLength > limit {
		return "", fmt.Errorf("downloading %s: %d bytes is more than the %d allowed", name, resp.ContentLength, limit)
	}
	f, err := os.Create(dst)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return "", fmt.Errorf("downloading %s: %w", name, err)
	}
	if n > limit {
		return "", fmt.Errorf("downloading %s: more than the %d bytes allowed", name, limit)
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// extract copies the files makemkvcon needs out of the tarball into root,
// laid out as MakeMKV's own "make install" does: bin/makemkvcon and
// share/MakeMKV/. The EULA goes with them, as the licence asks. Only these
// exact names are read, so no entry can write outside root.
func extract(tarball, root, version, arch string) error {
	prefix := "makemkv-bin-" + version + "/"
	want := map[string]struct {
		dst  string
		mode os.FileMode
	}{
		prefix + "bin/" + arch + "/makemkvcon": {"bin/makemkvcon", 0o755},
		prefix + "src/share/appdata.tar":       {"share/MakeMKV/appdata.tar", 0o644},
		prefix + "src/share/blues.jar":         {"share/MakeMKV/blues.jar", 0o644},
		prefix + "src/share/blues.policy":      {"share/MakeMKV/blues.policy", 0o644},
		prefix + "src/eula_en_linux.txt":       {"share/MakeMKV/eula_en_linux.txt", 0o644},
	}
	f, err := os.Open(tarball)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	got := map[string]bool{}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		w, ok := want[h.Name]
		if !ok {
			continue
		}
		if h.Typeflag != tar.TypeReg || got[h.Name] || h.Size > maxFile {
			return fmt.Errorf("%s: unexpected entry (type %c, %d bytes)", h.Name, h.Typeflag, h.Size)
		}
		got[h.Name] = true
		if err := writeFile(filepath.Join(root, filepath.FromSlash(w.dst)), tr, h.Size, w.mode); err != nil {
			return err
		}
	}
	var missing []string
	for name := range want {
		if !got[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing %s", strings.Join(missing, ", "))
	}
	return nil
}

func writeFile(path string, r io.Reader, size int64, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if n, err := io.Copy(f, r); err != nil || n != size {
		f.Close()
		return fmt.Errorf("%s: wrote %d of %d bytes: %v", path, n, size, err)
	}
	return f.Close()
}
