package makemkv

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/danvanton/discern/internal/makemkv/fake"
)

// TestMain lets the test binary play makemkvcon. When the runner under test
// starts "makemkvcon", it actually starts this same binary again with
// FAKE_MAKEMKVCON set, and this function hands control to the fake instead of
// running the tests. That way the tests need no separate build step.
func TestMain(m *testing.M) {
	if os.Getenv(fake.EnvVar) != "" {
		os.Exit(fake.Main(os.Args[1:], os.Stdout, os.Stderr))
	}
	os.Exit(m.Run())
}

// fastWatchdog shrinks the thresholds from minutes to milliseconds, keeping
// their proportions, so a stall is caught within a second.
var fastWatchdog = WatchdogConfig{
	Interval:     20 * time.Millisecond,
	StartupGrace: 400 * time.Millisecond,
	StallAfter:   300 * time.Millisecond,
	HangAfter:    1 * time.Second,
}

// testRunner returns a Runner whose makemkvcon is the fake, playing sc.
func testRunner(t *testing.T, sc fake.Scenario) *Runner {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(sc)
	if err != nil {
		t.Fatal(err)
	}
	r := NewRunner(exe)
	r.Watchdog = fastWatchdog
	r.KillAfter = 2 * time.Second
	// Under -race the fake (this test binary) sleeps 1 s before it exits, by
	// default, for late race reports. That pause reads and writes nothing,
	// so fastWatchdog's 1 s HangAfter would call a finished run hung.
	r.Env = []string{fake.EnvVar + "=" + string(raw), "GORACE=atexit_sleep_ms=0"}
	return r
}

// counterFileReader reads the fake's own count of bytes read. It stands in
// for /proc, so the stall tests run on any operating system.
func counterFileReader(path string) func(int) (int64, error) {
	return func(int) (int64, error) {
		b, err := os.ReadFile(path)
		if err != nil {
			return 0, err
		}
		return strconv.ParseInt(string(b), 10, 64)
	}
}

func abs(t *testing.T, rel string) string {
	t.Helper()
	p, err := filepath.Abs(rel)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestInfoReplaysScan(t *testing.T) {
	r := testRunner(t, fake.Scenario{Info: fake.Info{File: abs(t, "../../testdata/makemkv/info-sample-movie.txt")}})
	d, err := r.Info(context.Background(), "/dev/sr0")
	if err != nil {
		t.Fatal(err)
	}
	if d.Volume != "SAMPLE_MOVIE" || len(d.Titles) != 4 {
		t.Errorf("got volume %q with %d titles, want SAMPLE_MOVIE with 4", d.Volume, len(d.Titles))
	}
}

func TestInfoExitErrorCarriesMakeMKVMessage(t *testing.T) {
	scan := filepath.Join(t.TempDir(), "failed.txt")
	os.WriteFile(scan, []byte(`MSG:5010,0,0,"Failed to open disc","Failed to open disc"`+"\n"), 0o644)
	r := testRunner(t, fake.Scenario{Info: fake.Info{File: scan, ExitCode: 1}})

	_, err := r.Info(context.Background(), "/dev/sr0")
	var ee *ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("err = %v, want *ExitError", err)
	}
	if ee.Code != 1 || len(ee.Messages) != 1 || ee.Messages[0].Code != 5010 {
		t.Errorf("got code %d, messages %+v; want 1 and MSG 5010", ee.Code, ee.Messages)
	}
}

func TestRipHealthy(t *testing.T) {
	r := testRunner(t, fake.Scenario{Mkv: fake.Mkv{TotalBytes: 2 * mib, ChunkDelayMs: 1}})
	out := t.TempDir()
	var progress atomic.Int32

	res, err := r.Rip(context.Background(), "/dev/sr0", 3, out, func(Progress) { progress.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	if len(res.OutputFiles) != 1 || filepath.Base(res.OutputFiles[0]) != "Fake Title_t03.mkv" {
		t.Fatalf("output files = %v", res.OutputFiles)
	}
	if info, err := os.Stat(res.OutputFiles[0]); err != nil || info.Size() != 2*mib {
		t.Errorf("output size = %v, %v; want %d", info.Size(), err, 2*mib)
	}
	if progress.Load() == 0 {
		t.Error("no progress lines were passed on")
	}
	if n := len(res.Messages); n == 0 || res.Messages[n-1].Code != 5036 {
		t.Errorf("messages = %+v, want the last to be 5036 (copy complete)", res.Messages)
	}
}

func TestRipExitError(t *testing.T) {
	r := testRunner(t, fake.Scenario{Mkv: fake.Mkv{TotalBytes: 64 << 10, ExitCode: 3}})
	_, err := r.Rip(context.Background(), "/dev/sr0", 0, t.TempDir(), nil)
	var ee *ExitError
	if !errors.As(err, &ee) || ee.Code != 3 || ee.Op != "mkv" {
		t.Fatalf("err = %v, want *ExitError from mkv with code 3", err)
	}
}

// The Kill Bill pattern, scaled down: output stops at 1 MiB while the fake
// keeps reading. The watchdog must stop the rip and say why.
func TestRipStallIsCaughtAndStopped(t *testing.T) {
	counter := filepath.Join(t.TempDir(), "read-counter")
	r := testRunner(t, fake.Scenario{Mkv: fake.Mkv{
		TotalBytes: 100 * mib, StallAtBytes: 1 * mib, ChunkDelayMs: 2,
		StallMode: fake.StallReading, ReadCounterFile: counter,
	}})
	r.readCounter = counterFileReader(counter)

	start := time.Now()
	_, err := r.Rip(context.Background(), "/dev/sr0", 1, t.TempDir(), nil)
	var we *WatchdogError
	if !errors.As(err, &we) || we.Verdict != Stalled {
		t.Fatalf("err = %v, want a stalled *WatchdogError", err)
	}
	if took := time.Since(start); took > 10*time.Second {
		t.Errorf("took %v to stop a stalled rip", took)
	}
	if !strings.Contains(we.Evidence.Reason, "stood still at 1.0 MiB") {
		t.Errorf("reason = %q", we.Evidence.Reason)
	}
	if len(we.Evidence.Samples) < 3 || len(we.Evidence.RobotTail) == 0 {
		t.Errorf("evidence has %d samples and %d robot lines", len(we.Evidence.Samples), len(we.Evidence.RobotTail))
	}
	// MakeMKV's messages up to the stall, for the failure page.
	if msgs := we.MakeMKVMessages(); len(msgs) == 0 || msgs[0].Code != 1005 {
		t.Errorf("messages = %+v; want the start-up message first", msgs)
	}

	bundle := t.TempDir()
	if err := we.Evidence.WriteBundle(bundle); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"summary.txt", "samples.csv", "robot-output.txt", "stderr.txt"} {
		if _, err := os.Stat(filepath.Join(bundle, f)); err != nil {
			t.Errorf("bundle is missing %s", f)
		}
	}
}

func TestRipFrozenIsHung(t *testing.T) {
	counter := filepath.Join(t.TempDir(), "read-counter")
	r := testRunner(t, fake.Scenario{Mkv: fake.Mkv{
		TotalBytes: 100 * mib, StallAtBytes: 1 * mib, ChunkDelayMs: 2,
		StallMode: fake.StallFrozen, ReadCounterFile: counter,
	}})
	r.readCounter = counterFileReader(counter)

	_, err := r.Rip(context.Background(), "/dev/sr0", 1, t.TempDir(), nil)
	var we *WatchdogError
	if !errors.As(err, &we) || we.Verdict != Hung {
		t.Fatalf("err = %v, want a hung *WatchdogError", err)
	}
}

// On Linux the watchdog reads the real /proc counters and copies the debug
// log the process deleted but still holds open, as MakeMKV does.
func TestRipStallUsesProcAndSavesDebugLog(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("needs Linux /proc")
	}
	const logLine = "ak=1 seg=0 miss_count=774144 acc_size=268566528 off=695226"
	r := testRunner(t, fake.Scenario{Mkv: fake.Mkv{
		TotalBytes: 100 * mib, StallAtBytes: 1 * mib, ChunkDelayMs: 2,
		StallMode: fake.StallReading, DebugLog: logLine + "\n",
	}})

	_, err := r.Rip(context.Background(), "/dev/sr0", 1, t.TempDir(), nil)
	var we *WatchdogError
	if !errors.As(err, &we) || we.Verdict != Stalled {
		t.Fatalf("err = %v, want a stalled *WatchdogError", err)
	}
	found := false
	for _, d := range we.Evidence.DebugLogs {
		if strings.Contains(string(d.Data), logLine) {
			found = true
		}
	}
	if !found {
		t.Errorf("debug log not saved; got %d logs", len(we.Evidence.DebugLogs))
	}
}

func TestRipCancelStopsTheProcess(t *testing.T) {
	r := testRunner(t, fake.Scenario{Mkv: fake.Mkv{TotalBytes: 1 << 40, ChunkDelayMs: 5}})
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_, err := r.Rip(ctx, "/dev/sr0", 0, t.TempDir(), nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
}

func TestRingKeepsTheLastItems(t *testing.T) {
	r := newRing[int](3)
	if got := r.items(); len(got) != 0 {
		t.Errorf("empty ring = %v", got)
	}
	for i := 1; i <= 5; i++ {
		r.add(i)
	}
	if got := r.items(); len(got) != 3 || got[0] != 3 || got[2] != 5 {
		t.Errorf("items = %v, want [3 4 5]", got)
	}
}

func TestCappedBufferKeepsTheTail(t *testing.T) {
	c := newCappedBuffer(5)
	c.Write([]byte("hello "))
	c.Write([]byte("world"))
	if got := c.String(); got != "world" {
		t.Errorf("got %q, want %q", got, "world")
	}
}

func TestDescribe(t *testing.T) {
	t0 := time.Now()
	samples := []Sample{
		{At: t0, ReadBytes: 0, WrittenBytes: 0},
		{At: t0.Add(60 * time.Second), ReadBytes: 1000 * mib, WrittenBytes: 640 * mib},
		{At: t0.Add(180 * time.Second), ReadBytes: 3048 * mib, WrittenBytes: 640 * mib},
	}
	want := "output stood still at 640.0 MiB for 2m0s while 2.0 GiB more was read"
	if got := describe(Stalled, samples); got != want {
		t.Errorf("describe = %q\nwant       %q", got, want)
	}
}

func TestRipRejectsBadWatchdogConfig(t *testing.T) {
	r := testRunner(t, fake.Scenario{})
	r.Watchdog = WatchdogConfig{} // would make time.NewTicker panic
	if _, err := r.Rip(context.Background(), "/dev/sr0", 0, t.TempDir(), nil); err == nil {
		t.Fatal("Rip with a zero watchdog config succeeded, want an error")
	}
}

// Leftovers would look like progress to the watchdog (skipping the start-up
// grace) and would be mixed up with this rip's files.
func TestRipRefusesNonEmptyOutDir(t *testing.T) {
	out := t.TempDir()
	os.WriteFile(filepath.Join(out, "partial_t00.mkv"), []byte("leftover"), 0o644)
	r := testRunner(t, fake.Scenario{Mkv: fake.Mkv{TotalBytes: 64 << 10}})
	_, err := r.Rip(context.Background(), "/dev/sr0", 0, out, nil)
	if err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Fatalf("err = %v, want a not-empty error", err)
	}
}

func TestRipCreatesMissingOutDir(t *testing.T) {
	out := filepath.Join(t.TempDir(), "job-1", "rip")
	r := testRunner(t, fake.Scenario{Mkv: fake.Mkv{TotalBytes: 64 << 10}})
	if _, err := r.Rip(context.Background(), "/dev/sr0", 0, out, nil); err != nil {
		t.Fatal(err)
	}
}

// A line too long to read must stop the rip, not leave it running with
// nobody reading its output (it would block on the full pipe forever).
func TestRipStopsOnUnreadableOutput(t *testing.T) {
	r := testRunner(t, fake.Scenario{Mkv: fake.Mkv{
		TotalBytes: 1 << 40, ChunkDelayMs: 5, JunkLineBytes: 2 * maxLine,
	}})
	start := time.Now()
	_, err := r.Rip(context.Background(), "/dev/sr0", 0, t.TempDir(), nil)
	if err == nil || !strings.Contains(err.Error(), "reading makemkvcon output") {
		t.Fatalf("err = %v, want a read error", err)
	}
	if took := time.Since(start); took > 10*time.Second {
		t.Errorf("took %v to stop", took)
	}
}

// A slow progress callback must not hold up makemkvcon: thousands of progress
// lines would otherwise fill the pipe and block it.
func TestRipSlowProgressCallbackDoesNotHoldUpTheRip(t *testing.T) {
	r := testRunner(t, fake.Scenario{Mkv: fake.Mkv{TotalBytes: 4 * mib, ChunkBytes: 1 << 10}})
	var calls atomic.Int32
	slow := func(Progress) { calls.Add(1); time.Sleep(50 * time.Millisecond) }

	start := time.Now()
	if _, err := r.Rip(context.Background(), "/dev/sr0", 0, t.TempDir(), slow); err != nil {
		t.Fatal(err)
	}
	// 4,096 progress lines at 50 ms each would take over 3 minutes if every
	// one were delivered in turn.
	if took := time.Since(start); took > 30*time.Second {
		t.Errorf("rip took %v with a slow callback", took)
	}
	if n := calls.Load(); n == 0 || n >= 4096 {
		t.Errorf("callback ran %d times, want some but fewer than the 4096 lines", n)
	}
}

func TestInfoUnreadableOutputIsAParseError(t *testing.T) {
	scan := filepath.Join(t.TempDir(), "bad.txt")
	os.WriteFile(scan, []byte("TCOUNT:1\nTINFO:0,9,broken\n"), 0o644)
	r := testRunner(t, fake.Scenario{Info: fake.Info{File: scan}})
	_, err := r.Info(context.Background(), "/dev/sr0")
	var ee *ExitError
	if err == nil || errors.As(err, &ee) || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("err = %v, want a parse error naming line 2", err)
	}
}

func TestIsDebugLogCandidate(t *testing.T) {
	cases := map[string]bool{
		"/tmp/makemkv_debug_x (deleted)": true,
		"/tmp/makemkv_debug_x":           false, // not deleted
		"/memfd:java-jit (deleted)":      false, // anonymous memory
		"/dev/sr0 (deleted)":             false,
		"socket:[12345]":                 false,
		"pipe:[678] (deleted)":           false,
	}
	for target, want := range cases {
		if got := isDebugLogCandidate(target); got != want {
			t.Errorf("isDebugLogCandidate(%q) = %v, want %v", target, got, want)
		}
	}
}

func TestDebugLogAllowance(t *testing.T) {
	cases := []struct {
		kept      int
		keptBytes int64
		want      int64
	}{
		{0, 0, maxDebugLog},
		{1, 20 << 20, 12 << 20},  // only what's left of the total
		{2, maxDebugLogTotal, 0}, // total used up
		{maxDebugLogs, 0, 0},     // enough files
	}
	for _, c := range cases {
		if got := debugLogAllowance(c.kept, c.keptBytes); got != c.want {
			t.Errorf("debugLogAllowance(%d, %d) = %d, want %d", c.kept, c.keptBytes, got, c.want)
		}
	}
}

// TestInfoFailsClosedOnAnIncompleteScan replays the pattern of a real UHD scan
// made while the drive was spinning up: read errors and skipped titles, then
// "Operation successfully completed" and exit status 0.
func TestInfoFailsClosedOnAnIncompleteScan(t *testing.T) {
	good, err := os.ReadFile(abs(t, "../../testdata/makemkv/info-sample-movie.txt"))
	if err != nil {
		t.Fatal(err)
	}
	notReady := `MSG:2003,0,3,"Error 'Scsi error - NOT READY:LOGICAL UNIT IS IN PROCESS OF BECOMING READY' occurred while reading '/BDMV/STREAM/00113.m2ts' at offset '10278912'","Error '%1' occurred while reading '%2' at offset '%3'","Scsi error - NOT READY:LOGICAL UNIT IS IN PROCESS OF BECOMING READY","/BDMV/STREAM/00113.m2ts","10278912"` + "\n"
	for name, extra := range map[string]string{
		"read errors":   notReady + notReady,
		"title skipped": `MSG:3016,0,1,"Title #00064.mpls was skipped","Title #%1 was skipped","00064.mpls"` + "\n",
		"undecodable":   `MSG:5043,0,1,"Failed to decode audio/video data for title #38 - invalid mux or internal error, title skipped.","Failed to decode audio/video data for title #%1 - invalid mux or internal error, title skipped.","38"` + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			scan := filepath.Join(t.TempDir(), "scan.txt")
			os.WriteFile(scan, append([]byte(extra), good...), 0o644)
			r := testRunner(t, fake.Scenario{Info: fake.Info{File: scan}})
			d, err := r.Info(context.Background(), "/dev/sr0")
			var inc *IncompleteScanError
			if !errors.As(err, &inc) || d != nil {
				t.Fatalf("Info = %v, %v; want nil and an *IncompleteScanError", d, err)
			}
			if inc.First == "" || !strings.Contains(err.Error(), "couldn't read the whole disc") {
				t.Errorf("error %q doesn't say what happened", err)
			}
		})
	}
}

func TestIncompleteScanErrorCountsAndBoundsMessages(t *testing.T) {
	d := &Disc{Messages: []Message{
		{Code: 1005, Text: "MakeMKV v2.0.0 started"},
		{Code: msgReadError, Text: strings.Repeat("x", 500)},
		{Code: msgReadError, Text: "second"},
		{Code: msgTitleSkipped, Text: "Title #00064.mpls was skipped"},
		{Code: 5011, Text: "Operation successfully completed"},
	}}
	err := checkComplete(d)
	var inc *IncompleteScanError
	if !errors.As(err, &inc) || inc.ReadErrors != 2 || inc.Skipped != 1 {
		t.Fatalf("checkComplete = %+v; want 2 read errors and 1 skipped", err)
	}
	if len([]rune(inc.First)) > 201 {
		t.Errorf("first message is %d runes; disc text must be bounded", len([]rune(inc.First)))
	}
	if !strings.Contains(err.Error(), "2 read errors, 1 title skipped") {
		t.Errorf("error = %q", err)
	}
	// The scan's messages go with it, for the failure page.
	if msgs := inc.MakeMKVMessages(); len(msgs) != 5 || !msgs[1].Problem() || msgs[0].Problem() {
		t.Errorf("messages = %d, problems marked %v %v", len(msgs), msgs[0].Problem(), msgs[1].Problem())
	}
	if checkComplete(&Disc{Messages: []Message{{Code: 5011}}}) != nil {
		t.Error("a scan without errors was called incomplete")
	}
	// A copy-protected DVD: MakeMKV reads around planted bad sectors and
	// skips nothing. Those read errors alone don't make the scan incomplete.
	protected := &Disc{Messages: []Message{
		{Code: msgReadError, Text: "Error 'Scsi error - MEDIUM ERROR:L-EC UNCORRECTABLE ERROR' occurred while reading '/VIDEO_TS/VTS_03_1.VOB' at offset '1048576'"},
		{Code: 5011, Text: "Operation successfully completed"},
	}}
	if err := checkComplete(protected); err != nil {
		t.Errorf("read errors without skipped titles or a not-ready drive failed the scan: %v", err)
	}
}

// A backup writes the disc's folder (BDMV with its playlists and streams)
// into an empty folder, with progress, and returns MakeMKV's messages.
//
// The read counter is on, as /proc gives it on Linux, so the watchdog
// judges stalls: output written into the backup's subfolders must count,
// or every backup looks stalled.
func TestBackupHealthy(t *testing.T) {
	counter := filepath.Join(t.TempDir(), "read-counter")
	r := testRunner(t, fake.Scenario{Backup: fake.Backup{
		Mkv:       fake.Mkv{TotalBytes: 8 * mib, ChunkDelayMs: 3, ReadCounterFile: counter},
		Playlists: []string{"00800.mpls"}}})
	r.readCounter = counterFileReader(counter)
	out := filepath.Join(t.TempDir(), "backup")
	var progress atomic.Int32

	msgs, err := r.Backup(context.Background(), "/dev/sr0", 0, out, func(Progress) { progress.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(out, "BDMV", "PLAYLIST", "00800.mpls")); err != nil {
		t.Errorf("no playlist in the backup: %v", err)
	}
	if info, err := os.Stat(filepath.Join(out, "BDMV", "STREAM", "00000.m2ts")); err != nil || info.Size() != 8*mib {
		t.Errorf("stream file: %v, %v; want %d bytes", info, err, 8*mib)
	}
	if progress.Load() == 0 || len(msgs) == 0 {
		t.Errorf("%d progress lines, %d messages", progress.Load(), len(msgs))
	}
}

// A backup is watched like a rip, and its errors name the backup.
func TestBackupStallAndExitError(t *testing.T) {
	counter := filepath.Join(t.TempDir(), "read-counter")
	r := testRunner(t, fake.Scenario{Backup: fake.Backup{Mkv: fake.Mkv{
		TotalBytes: 100 * mib, StallAtBytes: 1 * mib, ChunkDelayMs: 2,
		StallMode: fake.StallReading, ReadCounterFile: counter,
	}}})
	r.readCounter = counterFileReader(counter)
	_, err := r.Backup(context.Background(), "/dev/sr0", 0, t.TempDir(), nil)
	var we *WatchdogError
	if !errors.As(err, &we) || we.Verdict != Stalled {
		t.Errorf("a stalled backup: %v", err)
	}

	r = testRunner(t, fake.Scenario{Backup: fake.Backup{Mkv: fake.Mkv{TotalBytes: 64 << 10, ExitCode: 3}}})
	_, err = r.Backup(context.Background(), "/dev/sr0", 0, t.TempDir(), nil)
	var ee *ExitError
	if !errors.As(err, &ee) || ee.Code != 3 || ee.Op != "backup" {
		t.Errorf("err = %v, want *ExitError from backup with code 3", err)
	}
}

// A backup names its drive by MakeMKV's number ("disc:N"; the fake, like
// makemkvcon 2.0.0, refuses anything else), and is stopped when the drive
// list MakeMKV prints at start says that number is another drive now
// (decision D72).
func TestBackupChecksTheDriveNumber(t *testing.T) {
	drives := []string{"/dev/sr1", "/dev/sr0"}
	small := testRunner(t, fake.Scenario{Backup: fake.Backup{Mkv: fake.Mkv{TotalBytes: 1 * mib}, Drives: drives}})
	if _, err := small.Backup(context.Background(), "/dev/sr0", 1, t.TempDir(), nil); err != nil {
		t.Errorf("drive 1 is /dev/sr0: %v", err)
	}

	// Slow and large, so a backup that wasn't stopped would show.
	r := testRunner(t, fake.Scenario{Backup: fake.Backup{
		Mkv: fake.Mkv{TotalBytes: 64 * mib, ChunkDelayMs: 5}, Drives: drives}})
	out := t.TempDir()
	_, err := r.Backup(context.Background(), "/dev/sr0", 0, out, nil)
	var refused *RefusedError
	if !errors.As(err, &refused) || refused.Finished || len(refused.MakeMKVMessages()) == 0 ||
		!strings.Contains(err.Error(), "drive numbers changed") {
		t.Errorf("drive 0 is /dev/sr1: err = %v", err)
	}
	if n := dirSize(out); n >= 64*mib {
		t.Errorf("the backup of the wrong drive ran to the end (%d bytes)", n)
	}

	_, err = r.Backup(context.Background(), "/dev/sr0", 5, t.TempDir(), nil)
	if err == nil || !strings.Contains(err.Error(), "drive 5") {
		t.Errorf("drive 5 is an empty slot: err = %v", err)
	}
}

// The check also refuses a run whose output ends without the drive list.
func TestSameDriveNeedsTheDriveListed(t *testing.T) {
	c := &driveCheck{device: "/dev/sr0", disc: 0}
	if err := c.check(Progress{Max: 65536}); err != nil {
		t.Errorf("progress before the list: %v", err)
	}
	if err := c.ended(); err == nil {
		t.Error("a run that never listed its drive passed")
	}
	c = &driveCheck{device: "/dev/sr0", disc: 2}
	c.check(Drive{Index: 0, Device: "/dev/sr0"})
	if err := c.check(Message{Code: 5010}); err == nil {
		t.Error("a drive list without drive 2 passed")
	}
}

// MakeMKV prints some errors as plain text, not MSG lines. The real
// makemkvcon 2.0.0 answered "backup dev:/dev/sr0" (2026-11-17) with this,
// then exit status 10; the plain line must reach the failure as a message.
func TestPlainTextErrorsAreKept(t *testing.T) {
	out := `MSG:1005,0,1,"MakeMKV v2.0.0 linux(x64-release) started","%1 started","MakeMKV v2.0.0 linux(x64-release)"
PRGT:5018,0,"Scanning CD-ROM devices"
DRV:0,2,999,12,"BD-RE PIONEER BD-RW   BDR-211M 1.53 MLDL002857WL","THE_WHOLE_BLOODY_AFFAIR_UHD_D1","/dev/sr0"
DRV:1,256,999,0,"","",""
Backup source must start with "disc:"
`
	l := &robotLog{tail: newRing[string](10)}
	if err := l.consume(strings.NewReader(out), func(Progress) {}, func() {}); err != nil {
		t.Fatal(err)
	}
	msgs := l.messages()
	if len(msgs) != 2 || msgs[1].Code != MsgPlainText || !msgs[1].Problem() || msgs[1].Text != `Backup source must start with "disc:"` {
		t.Fatalf("messages = %+v", msgs)
	}
	ee := &ExitError{Op: "backup", Code: 10, Messages: msgs}
	if got, want := ee.Error(), `makemkvcon backup exited with status 10: "Backup source must start with \"disc:\""`; got != want {
		t.Errorf("Error() = %s, want %s", got, want)
	}

	// The note quotes MakeMKV's own last message, not stderr's, and only so
	// much of it.
	long := strings.Repeat("x", 5000)
	ee = &ExitError{Op: "mkv", Code: 2, Messages: []Message{{Code: 1005}, {Code: 5003, Text: long}},
		Stderr: []Message{{Code: MsgPlainText, Text: "a library warning"}}}
	if s := ee.Error(); !strings.Contains(s, "MSG 5003") || len(s) > 400 {
		t.Errorf("Error() = %.100s... (%d bytes)", s, len(s))
	}

	// What makemkvcon writes to stderr reaches the failure page too: the
	// fake reports a missing scan file there and exits with status 2. With
	// nothing on stdout, the note quotes it.
	r := testRunner(t, fake.Scenario{Info: fake.Info{File: filepath.Join(t.TempDir(), "missing.txt")}})
	_, err := r.Info(context.Background(), "/dev/sr0")
	var exit *ExitError
	if !errors.As(err, &exit) || len(exit.MakeMKVMessages()) == 0 {
		t.Fatalf("err = %v, want an *ExitError with messages", err)
	}
	all := exit.MakeMKVMessages()
	if last := all[len(all)-1]; last.Code != MsgPlainText || !strings.HasPrefix(last.Text, "fake makemkvcon:") ||
		!strings.Contains(err.Error(), "fake makemkvcon:") {
		t.Errorf("last message = %+v, err = %v; want the stderr line", last, err)
	}
}

// A capped tail that lost its front drops the cut-off first line, so the
// fragment doesn't pass for a message.
func TestCappedBufferWholeLines(t *testing.T) {
	c := newCappedBuffer(16)
	c.Write([]byte("TINFO:0,2,0,\"The Matrix\"\nDRV:1\nx"))
	if got := c.wholeLines(); got != "DRV:1\nx" {
		t.Errorf("wholeLines = %q", got)
	}
	c = newCappedBuffer(64)
	c.Write([]byte("whole\nlines"))
	if got := c.wholeLines(); got != "whole\nlines" {
		t.Errorf("an uncut buffer: %q", got)
	}
}

// Output is everything below the folder: a backup writes into subfolders.
func TestDirSizeCountsSubfolders(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "BDMV", "STREAM"), 0o755)
	os.WriteFile(filepath.Join(dir, "top.mkv"), make([]byte, 10), 0o644)
	os.WriteFile(filepath.Join(dir, "BDMV", "STREAM", "00000.m2ts"), make([]byte, 32), 0o644)
	if got := dirSize(dir); got != 42 {
		t.Errorf("dirSize = %d, want 42", got)
	}
	if got := dirSize(filepath.Join(dir, "missing")); got != 0 {
		t.Errorf("a missing folder: %d", got)
	}
}
