package jobs_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/danvanton/discern/internal/jobs"
)

// approved runs a job to NeedsYou and approves title 0 with strategy s.
func (e *env) approved(t *testing.T, s jobs.Strategy) *jobs.Job {
	t.Helper()
	j, _ := e.store.CreateJob(ctx, "/dev/sr0")
	if err := e.engine.Run(ctx, j.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.engine.Approve(ctx, j.ID, []int{0}, nil, nil, s); err != nil {
		t.Fatal(err)
	}
	return j
}

// A stalled rip goes on by itself through the fallback (decision D64):
// Stalled, BackingUp, Remuxing, then Verifying as usual. The backup is gone
// before the import, and the history records the stall and the success.
func TestAStalledRipFallsBackToBackupAndRemux(t *testing.T) {
	e := newEnv(t, &fakeSteps{ripStalls: true, remuxNote: "made by backup + remux"})
	var backupAtImport bool
	e.steps.importDone = func(dir string) {
		_, err := os.Stat(filepath.Join(dir, "backup"))
		backupAtImport = err == nil
	}
	j := e.approved(t, jobs.Direct)
	if err := e.engine.Run(ctx, j.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := e.store.Job(ctx, j.ID)
	if got.State != jobs.Done || got.Strategy != jobs.Backup || got.Note != "made by backup + remux" {
		t.Fatalf("state %s, strategy %q, note %q", got.State, got.Strategy, got.Note)
	}
	want := []jobs.State{jobs.Ripping, jobs.Stalled, jobs.BackingUp, jobs.Remuxing, jobs.Verifying, jobs.Importing, jobs.Done}
	if h := e.history(t, j.ID); !slices.Equal(h[len(h)-len(want):], want) {
		t.Errorf("history %v", h)
	}
	if got := e.steps.ran(); !slices.Equal(got[len(got)-6:], []string{"rip", "fallback", "backup", "remux", "verify", "import"}) {
		t.Errorf("steps %v", got)
	}
	if backupAtImport {
		t.Error("the backup was still there at the import")
	}
	if e.steps.backupSaw != filepath.Join(e.engine.StagingDir(j.ID), "backup") {
		t.Errorf("backed up into %s", e.steps.backupSaw)
	}
	fp := sampleDisc.Fingerprint()
	if r, _ := e.store.LastResult(ctx, fp, "", jobs.Direct); r != jobs.AttemptStalled {
		t.Errorf("history: Direct %q", r)
	}
	if r, _ := e.store.LastResult(ctx, fp, "", jobs.Backup); r != jobs.AttemptOK {
		t.Errorf("history: Backup %q", r)
	}
}

// The strategy history (decision D66): the same disc with the same MakeMKV
// version starts with backup + remux, without trying Direct, unless the
// user chose Direct; another MakeMKV version tries Direct again.
func TestTheHistoryStartsWithBackup(t *testing.T) {
	e := newEnv(t, &fakeSteps{ripStalls: true})
	e.engine.MakeMKVVersion = "2.0.0"
	first := e.approved(t, jobs.Unset)
	e.engine.Run(ctx, first.ID)

	e.steps.ripStalls = false
	e.steps.calls = nil
	second := e.approved(t, jobs.Unset)
	if err := e.engine.Run(ctx, second.ID); err != nil {
		t.Fatal(err)
	}
	if got := e.steps.ran(); slices.Contains(got, "rip") || !slices.Contains(got, "backup") {
		t.Errorf("same disc, same version: steps %v; want backup without a rip", got)
	}

	e.steps.calls = nil
	chosen := e.approved(t, jobs.Direct)
	e.engine.Run(ctx, chosen.ID)
	if got := e.steps.ran(); !slices.Contains(got, "rip") || slices.Contains(got, "backup") {
		t.Errorf("Direct chosen: steps %v; want Direct, whatever the history says", got)
	}

	e.engine.MakeMKVVersion = "2.0.1"
	e.steps.calls = nil
	third := e.approved(t, jobs.Unset)
	e.engine.Run(ctx, third.ID)
	if got := e.steps.ran(); !slices.Contains(got, "rip") || slices.Contains(got, "backup") {
		t.Errorf("a new MakeMKV version: steps %v; want Direct", got)
	}
}

// Short of space, the job waits for the user with the reason (decision
// D68); approving it plainly then rips Direct.
func TestNoRoomWaitsForTheUser(t *testing.T) {
	e := newEnv(t, &fakeSteps{ripStalls: true,
		fallbackErr: fmt.Errorf("%w: it needs about 165 GB free in staging, and 80 GB is free", jobs.ErrNoRoom)})
	j := e.approved(t, jobs.Direct)
	if err := e.engine.Run(ctx, j.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := e.store.Job(ctx, j.ID)
	if got.State != jobs.NeedsYou || !strings.Contains(got.Note, "165 GB") {
		t.Fatalf("state %s, note %q", got.State, got.Note)
	}
	e.steps.ripStalls, e.steps.fallbackErr = false, nil
	if err := e.engine.Approve(ctx, j.ID, []int{0}, nil, nil, jobs.Direct); err != nil {
		t.Fatal(err)
	}
	e.engine.Run(ctx, j.ID)
	if st := e.state(t, j.ID); st != jobs.Done {
		t.Errorf("approved again: %s", st)
	}
}

// A route that can't be taken (a DVD, no mkvmerge) fails the job, saying
// why, as a failed rip with Direct chosen: Retry tries Direct again, as it
// did before the fallback, and the history doesn't divert it.
func TestAFallbackThatCantRunFails(t *testing.T) {
	e := newEnv(t, &fakeSteps{ripStalls: true, fallbackErr: errors.New("only Blu-ray and UHD discs can be backed up")})
	j := e.approved(t, jobs.Unset)
	e.engine.Run(ctx, j.ID)
	got, _ := e.store.Job(ctx, j.ID)
	if got.State != jobs.Failed || got.FailedIn != jobs.Ripping || got.Strategy != jobs.Direct || !strings.Contains(got.Note, "only Blu-ray") {
		t.Fatalf("state %s in %s, strategy %q, note %q", got.State, got.FailedIn, got.Strategy, got.Note)
	}
	e.steps.ripStalls = false
	e.steps.calls = nil
	if err := e.engine.Retry(ctx, j.ID); err != nil {
		t.Fatal(err)
	}
	e.engine.Run(ctx, j.ID)
	if got := e.steps.ran(); e.state(t, j.ID) != jobs.Done || !slices.Contains(got, "rip") || slices.Contains(got, "backup") {
		t.Errorf("after retry: %s, steps %v; want Direct", e.state(t, j.ID), got)
	}
}

// A retry that rescans (the disc changed) forgets the strategy: the new
// disc starts unset.
func TestARescanForgetsTheStrategy(t *testing.T) {
	e := newEnv(t, &fakeSteps{})
	j := e.approved(t, jobs.Backup)
	e.steps.discChanged = true
	e.engine.Run(ctx, j.ID)
	if got, _ := e.store.Job(ctx, j.ID); got.State != jobs.Failed || got.FailedIn != jobs.Detected {
		t.Fatalf("state %s in %s", got.State, got.FailedIn)
	}
	if err := e.engine.Retry(ctx, j.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.store.Job(ctx, j.ID); got.Strategy != jobs.Unset {
		t.Errorf("strategy after a rescan: %q", got.Strategy)
	}
}

// Crash recovery in the fallback's states: a job stopped in Stalled goes on
// to the backup; one stopped in BackingUp starts its backup again from an
// empty folder.
func TestRestartInTheFallbackStates(t *testing.T) {
	for _, crashedIn := range []jobs.State{jobs.Stalled, jobs.BackingUp} {
		t.Run(string(crashedIn), func(t *testing.T) {
			e := newEnv(t, &fakeSteps{})
			j, _ := e.store.CreateJob(ctx, "/dev/sr0")
			j.Disc, j.Titles, j.Strategy = sampleDisc, []int{0}, jobs.Backup
			j.Movie = &jobs.Movie{TMDbID: 1, Title: "Sample", Year: 2020}
			path := []jobs.State{jobs.Scanning, jobs.Identifying, jobs.Selecting, jobs.Ripping, jobs.Stalled}
			if crashedIn == jobs.BackingUp {
				path = append(path, jobs.BackingUp)
			}
			for _, st := range path {
				if err := e.store.Move(ctx, j, st, ""); err != nil {
					t.Fatal(err)
				}
			}
			partial := filepath.Join(e.engine.StagingDir(j.ID), "backup", "partial.m2ts")
			os.MkdirAll(filepath.Dir(partial), 0o755)
			os.WriteFile(partial, []byte("half"), 0o644)
			e.reopen(t)
			if err := e.engine.Run(ctx, j.ID); err != nil {
				t.Fatal(err)
			}
			if got := e.steps.ran(); !slices.Equal(got, []string{"fallback", "backup", "remux", "verify", "import"}) {
				t.Errorf("steps after the restart: %v", got)
			}
			if _, err := os.Stat(partial); err == nil || e.state(t, j.ID) != jobs.Done {
				t.Errorf("partial backup kept: %v; state %s", err == nil, e.state(t, j.ID))
			}
		})
	}
}

// The manual route (decision D69): approving with backup + remux skips
// Direct; a rip that failed can be retried with it.
func TestTheManualRoute(t *testing.T) {
	e := newEnv(t, &fakeSteps{})
	j := e.approved(t, jobs.Backup)
	e.engine.Run(ctx, j.ID)
	if got := e.steps.ran(); slices.Contains(got, "rip") || !slices.Contains(got, "remux") || e.state(t, j.ID) != jobs.Done {
		t.Errorf("approved with backup: steps %v, state %s", got, e.state(t, j.ID))
	}

	e = newEnv(t, &fakeSteps{failIn: "rip"})
	j = e.approved(t, jobs.Direct)
	e.engine.Run(ctx, j.ID)
	if err := e.engine.RetryWithBackup(ctx, j.ID); err != nil {
		t.Fatal(err)
	}
	e.engine.Run(ctx, j.ID)
	if got, _ := e.store.Job(ctx, j.ID); got.State != jobs.Done || got.Strategy != jobs.Backup {
		t.Errorf("retried with backup: %s, %q", got.State, got.Strategy)
	}
	if err := e.engine.RetryWithBackup(ctx, j.ID); err == nil {
		t.Error("a done job was retried with backup")
	}
}

// After a crash in Remuxing, the complete backup is kept and only the
// remux runs again; its partial MKV goes first.
func TestARestartInRemuxingKeepsTheBackup(t *testing.T) {
	e := newEnv(t, &fakeSteps{})
	j, _ := e.store.CreateJob(ctx, "/dev/sr0")
	j.Disc, j.Titles, j.Strategy = sampleDisc, []int{0}, jobs.Backup
	j.Movie = &jobs.Movie{TMDbID: 1, Title: "Sample", Year: 2020}
	for _, st := range []jobs.State{jobs.Scanning, jobs.Identifying, jobs.Selecting, jobs.Ripping, jobs.BackingUp, jobs.Remuxing} {
		if err := e.store.Move(ctx, j, st, ""); err != nil {
			t.Fatal(err)
		}
	}
	dir := e.engine.StagingDir(j.ID)
	os.MkdirAll(filepath.Join(dir, "backup"), 0o755)
	os.WriteFile(filepath.Join(dir, "backup", "disc.m2ts"), []byte("backup"), 0o644)
	os.WriteFile(filepath.Join(dir, "half.mkv"), []byte("partial"), 0o644)
	var partialAtVerify bool
	e.steps.onCheck = nil
	e.reopen(t)
	e.steps.importDone = func(d string) { _, err := os.Stat(filepath.Join(d, "half.mkv")); partialAtVerify = err == nil }
	if err := e.engine.Run(ctx, j.ID); err != nil {
		t.Fatal(err)
	}
	if got := e.steps.ran(); !slices.Equal(got, []string{"remux", "verify", "import"}) {
		t.Errorf("steps after the restart: %v", got)
	}
	if partialAtVerify || e.state(t, j.ID) != jobs.Done {
		t.Errorf("partial file kept: %v; state %s", partialAtVerify, e.state(t, j.ID))
	}
}
