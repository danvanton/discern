package install

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPinnedParses(t *testing.T) {
	r, err := Pinned()
	if err != nil {
		t.Fatal(err)
	}
	if r.Version == "" || r.BinSHA256 == "" || r.OSSSHA256 == "" {
		t.Fatalf("Pinned() = %+v", r)
	}
}

func TestParsePinFailsClosed(t *testing.T) {
	good := "MAKEMKV_VERSION=2.0.0\nMAKEMKV_BIN_SHA256=" + strings.Repeat("a", 64) + "\nMAKEMKV_OSS_SHA256=" + strings.Repeat("b", 64) + "\n"
	if _, err := parsePin("# comment\n\n" + good); err != nil {
		t.Fatalf("good pin: %v", err)
	}
	for name, s := range map[string]string{
		"missing key":   "MAKEMKV_VERSION=2.0.0\nMAKEMKV_BIN_SHA256=" + strings.Repeat("a", 64) + "\n",
		"bad version":   strings.Replace(good, "2.0.0", "2.0", 1),
		"short hash":    strings.Replace(good, strings.Repeat("a", 64), "abc", 1),
		"uppercase":     strings.Replace(good, strings.Repeat("a", 64), strings.Repeat("A", 64), 1),
		"duplicate":     good + "MAKEMKV_VERSION=2.0.1\n",
		"unknown key":   good + "OTHER=1\n",
		"not key=value": good + "junk\n",
	} {
		if _, err := parsePin(s); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// TestPinMatchesSignedFile is the check that makes the pin trustworthy. It
// verifies makemkv-sha.txt, MakeMKV's own signed checksum file, with the
// pinned key, and checks that pin.env holds exactly the hashes it lists for
// the pinned version. Anyone changing the pin, the key or the signed file by
// hand breaks it.
//
// It needs gpgv. It is skipped where gpgv is missing, except in CI, where a
// skip would hide the check.
func TestPinMatchesSignedFile(t *testing.T) {
	gpgv, err := exec.LookPath("gpgv")
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("gpgv is not installed; CI must run this test")
		}
		t.Skip("gpgv is not installed")
	}
	fpr, err := os.ReadFile("makemkv-signing-key.fingerprint")
	if err != nil {
		t.Fatal(err)
	}
	wantFpr := strings.TrimSpace(string(fpr))

	// --output writes only the signed text. Text outside the signed block
	// would still give "Good signature", so the hashes are read from this
	// output, never from the file itself.
	signed := filepath.Join(t.TempDir(), "signed.txt")
	cmd := exec.Command(gpgv, "--status-fd", "1", "--keyring", "./makemkv-signing-key.gpg", "--output", signed, "makemkv-sha.txt")
	cmd.Env = append(os.Environ(), "GNUPGHOME="+t.TempDir())
	status, err := cmd.Output()
	if err != nil {
		t.Fatalf("gpgv rejected makemkv-sha.txt: %v\n%s", err, status)
	}
	if !hasValidSig(string(status), wantFpr) {
		t.Fatalf("makemkv-sha.txt is not signed by the pinned key %s:\n%s", wantFpr, status)
	}

	pin, err := Pinned()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(signed)
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(string(b)))
	for sc.Scan() {
		if f := strings.Fields(sc.Text()); len(f) == 2 {
			listed[f[1]] = f[0]
		}
	}
	for file, want := range map[string]string{
		"makemkv-bin-" + pin.Version + ".tar.gz": pin.BinSHA256,
		"makemkv-oss-" + pin.Version + ".tar.gz": pin.OSSSHA256,
	} {
		if got := listed[file]; got != want {
			t.Errorf("%s: pin.env says %s, the signed file says %q", file, want, got)
		}
	}
}

// hasValidSig looks for gpgv's VALIDSIG status line and compares the primary
// key fingerprint, its last field.
func hasValidSig(status, fpr string) bool {
	for _, line := range strings.Split(status, "\n") {
		f := strings.Fields(line)
		if len(f) >= 3 && f[0] == "[GNUPG:]" && f[1] == "VALIDSIG" && strings.EqualFold(f[len(f)-1], fpr) {
			return true
		}
	}
	return false
}
