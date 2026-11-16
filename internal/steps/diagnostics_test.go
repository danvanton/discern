package steps

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danvanton/discern/internal/jobs"
	"github.com/danvanton/discern/internal/makemkv"
)

// A stalled rip's evidence is saved as a bundle, and the error says where
// (decision D37); other errors pass through untouched.
func TestSaveDiagnostics(t *testing.T) {
	s := New(nil, nil)
	s.Diagnostics = t.TempDir()
	stall := &makemkv.WatchdogError{Verdict: makemkv.Stalled, Evidence: &makemkv.Evidence{
		Verdict: makemkv.Stalled, At: time.Now(), Reason: "output stood still at 640.0 MiB",
		Samples: []makemkv.Sample{{At: time.Now(), ReadBytes: 1, ReadKnown: true, WrittenBytes: 2}},
	}}
	err := s.saveDiagnostics(&jobs.Job{ID: 12}, fmt.Errorf("title 3: %w", stall))

	var d interface{ DiagnosticsBundle() string }
	if !errors.As(err, &d) || !BundleName.MatchString(d.DiagnosticsBundle()) {
		t.Fatalf("err = %v; want it to name a bundle folder", err)
	}
	if !errors.As(err, new(*makemkv.WatchdogError)) || err.Error() != "title 3: rip stalled: output stood still at 640.0 MiB" {
		t.Errorf("the rip's own error was changed: %v", err)
	}
	for _, f := range []string{"summary.txt", "samples.csv", "robot-output.txt", "stderr.txt"} {
		if _, serr := os.Stat(filepath.Join(s.Diagnostics, d.DiagnosticsBundle(), f)); serr != nil {
			t.Errorf("the bundle is missing %s", f)
		}
	}

	other := errors.New("makemkvcon exited")
	if got := s.saveDiagnostics(&jobs.Job{ID: 12}, other); got != other {
		t.Errorf("another error was changed: %v", got)
	}
	s.Diagnostics = ""
	if got := s.saveDiagnostics(&jobs.Job{ID: 12}, stall); got != error(stall) {
		t.Error("without a diagnostics folder, nothing should be saved")
	}
	// A folder that can't be written: the rip's error stays, and says so.
	s.Diagnostics = filepath.Join(t.TempDir(), "file")
	os.WriteFile(s.Diagnostics, []byte("x"), 0o644)
	if got := s.saveDiagnostics(&jobs.Job{ID: 12}, stall); !errors.Is(got, stall) || !errors.As(got, new(*makemkv.WatchdogError)) {
		t.Errorf("a failed save hid the rip's error: %v", got)
	}
}

// Verify's failures say their kind.
func TestVerifyFailureKinds(t *testing.T) {
	var k interface{ FailKind() string }
	err := failed(jobs.KindVerifyNoAudio, "%s has no audio track", "x.mkv")
	if !errors.As(err, &k) || k.FailKind() != jobs.KindVerifyNoAudio || err.Error() != "x.mkv has no audio track" {
		t.Errorf("failed() = %v", err)
	}
}

// A failure of mkvmerge itself is a plain "verify" failure, whose advice is
// to check again, not to rip again.
func TestATrackReadFailureIsAPlainVerifyFailure(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "Movie_t00.mkv"), make([]byte, 950), 0o644)
	j := &jobs.Job{Titles: []int{0}, Disc: &makemkv.Disc{Titles: []makemkv.Title{{Index: 0, SizeBytes: 1000, OutputFile: "Movie_t00.mkv"}}}}
	s := New(nil, nil)
	s.Tracks = func(context.Context, string) (FileTracks, error) {
		return FileTracks{}, errors.New("mkvmerge timed out")
	}
	var k interface{ FailKind() string }
	if err := s.Verify(context.Background(), j, dir); !errors.As(err, &k) || k.FailKind() != "verify" {
		t.Errorf("err = %v; want the plain verify kind", err)
	}
}

// The MakeMKV key is removed from every file of a bundle before it is
// written: a bundle shared in a bug report mustn't carry it.
func TestDiagnosticsLeaveOutTheKey(t *testing.T) {
	const key = "T-zXc9pretendkeyAbCdEf0123456789"
	s := New(nil, nil)
	s.Diagnostics, s.Secrets = t.TempDir(), []string{key, ""}
	stall := &makemkv.WatchdogError{Verdict: makemkv.Stalled, Evidence: &makemkv.Evidence{
		Verdict: makemkv.Stalled, At: time.Now(), Reason: "stalled",
		RobotTail:  []string{`MSG:1005,0,1,"started with key ` + key + `"`},
		StderrTail: "key=" + key,
		DebugLogs:  []makemkv.DebugLog{{FD: 5, Data: []byte("registration " + key + " ok")}},
	}}
	err := s.saveDiagnostics(&jobs.Job{ID: 7}, stall)
	var d interface{ DiagnosticsBundle() string }
	if !errors.As(err, &d) {
		t.Fatalf("no bundle: %v", err)
	}
	dir := filepath.Join(s.Diagnostics, d.DiagnosticsBundle())
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		b, _ := os.ReadFile(filepath.Join(dir, e.Name()))
		if strings.Contains(string(b), key) {
			t.Errorf("%s carries the key", e.Name())
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "debug-fd5.log")); string(b) != "registration [redacted] ok" {
		t.Errorf("debug log = %q", b)
	}
}
