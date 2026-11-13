package install

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

const testVersion = "9.8.7"

// binFiles is a stand-in makemkv-bin tarball's content: the files Discern
// extracts, plus one for another CPU that it must leave alone.
func binFiles() map[string]string {
	p := "makemkv-bin-" + testVersion + "/"
	return map[string]string{
		p + "Makefile":                "all:\n",
		p + "bin/amd64/makemkvcon":    "amd64 makemkvcon",
		p + "bin/arm64/makemkvcon":    "arm64 makemkvcon",
		p + "bin/i386/makemkvcon":     "i386 makemkvcon",
		p + "src/share/appdata.tar":   "appdata",
		p + "src/share/blues.jar":     "blues jar",
		p + "src/share/blues.policy":  "blues policy",
		p + "src/eula_en_linux.txt":   "the EULA",
		p + "src/ask_eula.sh":         "#!/bin/bash\n",
		"../outside-the-install-root": "never written",
	}
}

func makeTarball(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		tw.Write([]byte(body))
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	gz.Close()
	return buf.Bytes()
}

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// server serves body as the release's makemkv-bin tarball and counts
// requests.
func server(t *testing.T, body []byte) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/makemkv-bin-"+testVersion+".tar.gz" {
			http.NotFound(w, r)
			return
		}
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// setup returns an installer pinned to a tarball made from files, and the
// server's request counter.
func setup(t *testing.T, files map[string]string) (*Installer, *atomic.Int32) {
	t.Helper()
	tb := makeTarball(t, files)
	srv, hits := server(t, tb)
	return &Installer{
		Dir:        t.TempDir(),
		Release:    Release{Version: testVersion, BinSHA256: sum(tb)},
		AcceptEULA: true,
		Arch:       "arm64",
		BaseURL:    srv.URL + "/",
	}, hits
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// assertNotInstalled checks that a failed install left nothing behind.
func assertNotInstalled(t *testing.T, in *Installer) {
	t.Helper()
	if _, err := os.Stat(in.versionDir()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("%s exists after a failed install (err %v)", in.versionDir(), err)
	}
	if left, _ := filepath.Glob(filepath.Join(in.Dir, ".tmp-*")); len(left) > 0 {
		t.Errorf("temporary folders left behind: %v", left)
	}
}

func TestEnsureInstallsThenDoesNothing(t *testing.T) {
	in, hits := setup(t, binFiles())
	did, err := in.Ensure(context.Background())
	if err != nil || !did {
		t.Fatalf("Ensure = %v, %v; want true, nil", did, err)
	}
	if got := read(t, in.Makemkvcon()); got != "arm64 makemkvcon" {
		t.Errorf("makemkvcon is %q; want the arm64 build", got)
	}
	share := filepath.Join(in.versionDir(), "share", "MakeMKV")
	for name, want := range map[string]string{
		"appdata.tar": "appdata", "blues.jar": "blues jar", "blues.policy": "blues policy", "eula_en_linux.txt": "the EULA",
	} {
		if got := read(t, filepath.Join(share, name)); got != want {
			t.Errorf("%s = %q; want %q", name, got, want)
		}
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(in.Makemkvcon())
		if err != nil || fi.Mode().Perm() != 0o755 {
			t.Errorf("makemkvcon mode = %v (err %v); want 0755", fi.Mode(), err)
		}
	}
	if hits.Load() != 1 {
		t.Errorf("%d downloads; want 1", hits.Load())
	}

	did, err = in.Ensure(context.Background())
	if err != nil || did {
		t.Fatalf("second Ensure = %v, %v; want false, nil", did, err)
	}
	if hits.Load() != 1 {
		t.Errorf("the second Ensure downloaded again (%d requests)", hits.Load())
	}
}

func TestEnsureRefusesWithoutEULA(t *testing.T) {
	in, hits := setup(t, binFiles())
	in.AcceptEULA = false
	if _, err := in.Ensure(context.Background()); !errors.Is(err, ErrEULANotAccepted) {
		t.Fatalf("err = %v; want ErrEULANotAccepted", err)
	}
	if hits.Load() != 0 {
		t.Error("downloaded MakeMKV without the EULA accepted")
	}

	// Taking the acceptance back stops Discern using an existing install too.
	in.AcceptEULA = true
	if _, err := in.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	in.AcceptEULA = false
	if _, err := in.Ensure(context.Background()); !errors.Is(err, ErrEULANotAccepted) {
		t.Fatalf("with MakeMKV installed: err = %v; want ErrEULANotAccepted", err)
	}
}

func TestEnsureRejectsWrongHash(t *testing.T) {
	in, _ := setup(t, binFiles())
	in.Release.BinSHA256 = strings.Repeat("0", 64)
	_, err := in.Ensure(context.Background())
	if err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("err = %v; want a SHA-256 mismatch", err)
	}
	assertNotInstalled(t, in)
}

func TestEnsureRejectsIncompleteTarball(t *testing.T) {
	files := binFiles()
	delete(files, "makemkv-bin-"+testVersion+"/src/share/blues.jar")
	in, _ := setup(t, files)
	_, err := in.Ensure(context.Background())
	if err == nil || !strings.Contains(err.Error(), "blues.jar") {
		t.Fatalf("err = %v; want blues.jar missing", err)
	}
	assertNotInstalled(t, in)
}

func TestEnsureRejectsUnsupportedArch(t *testing.T) {
	in, hits := setup(t, binFiles())
	in.Arch = "riscv64"
	if _, err := in.Ensure(context.Background()); err == nil {
		t.Fatal("no error for riscv64")
	}
	if hits.Load() != 0 {
		t.Error("downloaded for an unsupported CPU")
	}
}

func TestEnsureFailsOnHTTPErrorAndTooLarge(t *testing.T) {
	in, _ := setup(t, binFiles())
	in.Release.Version = "1.2.3" // the server only has testVersion: 404
	if _, err := in.Ensure(context.Background()); err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("err = %v; want HTTP 404", err)
	}
	assertNotInstalled(t, in)

	in, _ = setup(t, binFiles())
	in.maxTarball = 100
	if _, err := in.Ensure(context.Background()); err == nil || !strings.Contains(err.Error(), "allowed") {
		t.Fatalf("err = %v; want a size error", err)
	}
	assertNotInstalled(t, in)
}

func TestEnsureRepairsUnfinishedInstallAndRemovesOldVersions(t *testing.T) {
	in, _ := setup(t, binFiles())
	// A crash mid-install: the version folder exists without its marker,
	// and a temporary folder is left over. An older release is installed.
	for _, dir := range []string{
		filepath.Join(in.versionDir(), "bin"),
		filepath.Join(in.Dir, ".tmp-123"),
		filepath.Join(in.Dir, "1.18.4", "bin"),
		filepath.Join(in.Dir, "not-a-version"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(in.Makemkvcon(), []byte("half written"), 0o755)

	did, err := in.Ensure(context.Background())
	if err != nil || !did {
		t.Fatalf("Ensure = %v, %v; want true, nil", did, err)
	}
	if got := read(t, in.Makemkvcon()); got != "arm64 makemkvcon" {
		t.Errorf("makemkvcon is %q after the repair", got)
	}
	for _, gone := range []string{".tmp-123", "1.18.4"} {
		if _, err := os.Stat(filepath.Join(in.Dir, gone)); err == nil {
			t.Errorf("%s is still there", gone)
		}
	}
	if _, err := os.Stat(filepath.Join(in.Dir, "not-a-version")); err != nil {
		t.Error("removed a folder that isn't a MakeMKV version")
	}
}

func TestEnsureReinstallsForAnotherCPU(t *testing.T) {
	in, hits := setup(t, binFiles())
	if _, err := in.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	in.Arch = "amd64" // the /config volume moved to another machine
	did, err := in.Ensure(context.Background())
	if err != nil || !did {
		t.Fatalf("Ensure = %v, %v; want a reinstall", did, err)
	}
	if got := read(t, in.Makemkvcon()); got != "amd64 makemkvcon" || hits.Load() != 2 {
		t.Errorf("makemkvcon = %q after %d downloads; want the amd64 build after 2", got, hits.Load())
	}
}

// TestExtractWritesOnlyKnownFiles calls extract directly, so the folder
// around root survives and can be checked: an entry named "../x" would land
// next to root.
func TestExtractWritesOnlyKnownFiles(t *testing.T) {
	dir := t.TempDir()
	tarball := filepath.Join(dir, "bin.tar.gz")
	if err := os.WriteFile(tarball, makeTarball(t, binFiles()), 0o644); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(dir, "root")
	if err := extract(tarball, root, testVersion, "amd64"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "outside-the-install-root")); err == nil {
		t.Error("an entry outside the install folder was written")
	}
	var files []string
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(root, p)
			files = append(files, filepath.ToSlash(rel))
		}
		return err
	})
	if len(files) != 5 {
		t.Errorf("extracted %v; want only the 5 files makemkvcon needs", files)
	}
}

func TestEnsureCleansUpWhenAlreadyInstalled(t *testing.T) {
	in, _ := setup(t, binFiles())
	if _, err := in.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Left by a stop between the rename and the clean-up.
	for _, d := range []string{"1.18.4", ".tmp-456"} {
		os.MkdirAll(filepath.Join(in.Dir, d), 0o755)
	}
	if did, err := in.Ensure(context.Background()); err != nil || did {
		t.Fatalf("Ensure = %v, %v; want false, nil", did, err)
	}
	for _, d := range []string{"1.18.4", ".tmp-456"} {
		if _, err := os.Stat(filepath.Join(in.Dir, d)); err == nil {
			t.Errorf("%s is still there", d)
		}
	}
}
