// The engine is tested against the real SQLite store (in a temporary file),
// with fake steps. This is an external test package (jobs_test), so it can
// import internal/store, which itself imports jobs.
package jobs_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/NotNad/trayarr/internal/jobs"
	"github.com/NotNad/trayarr/internal/makemkv"
	"github.com/NotNad/trayarr/internal/store"
)

var ctx = context.Background()

// fakeSteps records which steps ran and can make one fail or block.
type fakeSteps struct {
	mu    sync.Mutex
	calls []string

	auto        bool                // Select returns Auto
	discChanged bool                // CheckDisc reports a different disc
	checks      int                 // how often CheckDisc ran
	failIn      string              // the step that returns an error
	blockRip    chan struct{}       // if set, Rip waits for ctx or this
	ripStarted  chan struct{}       // closed when Rip starts
	ripSawDir   func(dir string)    // called with the staging folder
	importDone  func(dir string)    // called by Import
	onRip       func(func(float64)) // progress reports to send
}

func (f *fakeSteps) record(step string) error {
	f.mu.Lock()
	f.calls = append(f.calls, step)
	f.mu.Unlock()
	if f.failIn == step {
		return errors.New(step + " broke")
	}
	return nil
}

func (f *fakeSteps) ran() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

var sampleDisc = &makemkv.Disc{Volume: "SAMPLE", Titles: []makemkv.Title{{Index: 0}, {Index: 1}}}

func (f *fakeSteps) CheckDisc(context.Context, *jobs.Job) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checks++
	if f.discChanged {
		return jobs.ErrDiscChanged
	}
	return nil
}
func (f *fakeSteps) Scan(context.Context, *jobs.Job) (*makemkv.Disc, error) {
	return sampleDisc, f.record("scan")
}
func (f *fakeSteps) Identify(context.Context, *jobs.Job) (*jobs.Movie, error) {
	return &jobs.Movie{TMDbID: 1, Title: "Sample", Year: 2020}, f.record("identify")
}
func (f *fakeSteps) Select(context.Context, *jobs.Job) (jobs.Selection, error) {
	return jobs.Selection{Titles: []int{0}, Auto: f.auto, Reason: "longest title"}, f.record("select")
}
func (f *fakeSteps) Rip(ctx context.Context, j *jobs.Job, dir string, progress func(float64)) error {
	if f.ripSawDir != nil {
		f.ripSawDir(dir)
	}
	if f.onRip != nil {
		f.onRip(progress)
	}
	if f.ripStarted != nil {
		close(f.ripStarted)
	}
	if f.blockRip != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-f.blockRip:
		}
	}
	if f.failIn == "rip" {
		return f.record("rip")
	}
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "Sample_t00.mkv"), []byte("rip"), 0o644)
	return f.record("rip")
}
func (f *fakeSteps) Verify(context.Context, *jobs.Job, string) error { return f.record("verify") }
func (f *fakeSteps) Import(_ context.Context, _ *jobs.Job, dir string) error {
	if f.importDone != nil {
		f.importDone(dir)
	}
	return f.record("import")
}

type env struct {
	store  *store.Store
	engine *jobs.Engine
	steps  *fakeSteps
	dbPath string
	stage  string
}

func newEnv(t *testing.T, steps *fakeSteps) *env {
	t.Helper()
	dir := t.TempDir()
	e := &env{steps: steps, dbPath: filepath.Join(dir, "trayarr.db"), stage: filepath.Join(dir, "staging")}
	e.reopen(t)
	return e
}

// reopen starts a fresh store and engine on the same database file: what a
// restart of Trayarr looks like.
func (e *env) reopen(t *testing.T) {
	t.Helper()
	if e.store != nil {
		e.store.Close()
	}
	s, err := store.Open(ctx, e.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	e.store = s
	e.engine = jobs.NewEngine(s, e.steps, e.stage, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func (e *env) state(t *testing.T, id int64) jobs.State {
	t.Helper()
	j, err := e.store.Job(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return j.State
}

func (e *env) history(t *testing.T, id int64) []jobs.State {
	t.Helper()
	events, err := e.store.Events(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	var out []jobs.State
	for _, ev := range events {
		out = append(out, ev.To)
	}
	return out
}

func TestAutomaticJobRunsToDone(t *testing.T) {
	e := newEnv(t, &fakeSteps{auto: true})
	j, _ := e.store.CreateJob(ctx, "/dev/sr0")
	if err := e.engine.Run(ctx, j.ID); err != nil {
		t.Fatal(err)
	}
	want := []jobs.State{jobs.Detected, jobs.Scanning, jobs.Identifying, jobs.Selecting,
		jobs.Ripping, jobs.Verifying, jobs.Importing, jobs.Done}
	if got := e.history(t, j.ID); !slices.Equal(got, want) {
		t.Errorf("history = %v\nwant      %v", got, want)
	}
	if got := e.steps.ran(); !slices.Equal(got, []string{"scan", "identify", "select", "rip", "verify", "import"}) {
		t.Errorf("steps = %v", got)
	}
	if _, err := os.Stat(e.engine.StagingDir(j.ID)); !os.IsNotExist(err) {
		t.Errorf("staging folder still there after Done: %v", err)
	}
}

// v0.1: every disc waits for the user's approval.
func TestJobWaitsForApprovalThenFinishes(t *testing.T) {
	e := newEnv(t, &fakeSteps{auto: false})
	j, _ := e.store.CreateJob(ctx, "/dev/sr0")
	if err := e.engine.Run(ctx, j.ID); err != nil {
		t.Fatal(err)
	}
	if st := e.state(t, j.ID); st != jobs.NeedsYou {
		t.Fatalf("state = %s, want needs_you", st)
	}
	if err := e.engine.Approve(ctx, j.ID, []int{7}, nil); err == nil {
		t.Error("approved a title that isn't on the disc")
	}
	if err := e.engine.Approve(ctx, j.ID, nil, nil); err == nil {
		t.Error("approved no titles")
	}
	if err := e.engine.Approve(ctx, j.ID, []int{1}, nil); err != nil {
		t.Fatal(err)
	}
	if err := e.engine.Run(ctx, j.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := e.store.Job(ctx, j.ID)
	if got.State != jobs.Done || !slices.Equal(got.Titles, []int{1}) {
		t.Errorf("state %s, titles %v; want done with [1]", got.State, got.Titles)
	}
}

func TestFailedStepIsRecordedAndRetryResumesThere(t *testing.T) {
	steps := &fakeSteps{auto: true, failIn: "verify"}
	e := newEnv(t, steps)
	j, _ := e.store.CreateJob(ctx, "/dev/sr0")
	if err := e.engine.Run(ctx, j.ID); err != nil {
		t.Fatalf("a failed step is an outcome, not an error: %v", err)
	}
	got, _ := e.store.Job(ctx, j.ID)
	if got.State != jobs.Failed || got.FailedIn != jobs.Verifying || got.Note != "verify failed: verify broke" {
		t.Fatalf("got state %s, failed in %s, note %q", got.State, got.FailedIn, got.Note)
	}

	steps.failIn = ""
	if err := e.engine.Retry(ctx, j.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.engine.Run(ctx, j.ID); err != nil {
		t.Fatal(err)
	}
	if st := e.state(t, j.ID); st != jobs.Done {
		t.Fatalf("after retry: %s", st)
	}
	// The retry resumed at verify: no second scan or rip.
	if got := steps.ran(); !slices.Equal(got, []string{"scan", "identify", "select", "rip", "verify", "verify", "import"}) {
		t.Errorf("steps = %v", got)
	}
}

func TestFailedScanRetriesFromTheStart(t *testing.T) {
	steps := &fakeSteps{auto: true, failIn: "scan"}
	e := newEnv(t, steps)
	j, _ := e.store.CreateJob(ctx, "/dev/sr0")
	e.engine.Run(ctx, j.ID)
	steps.failIn = ""
	e.engine.Retry(ctx, j.ID)
	if st := e.state(t, j.ID); st != jobs.Detected {
		t.Errorf("retry of a failed scan went to %s, want detected", st)
	}
}

// A shutdown in the middle of a rip leaves the job in Ripping (it was
// interrupted, not failed). After a restart, the rip runs again from an
// empty folder and the job finishes.
func TestShutdownMidRipResumesAfterRestart(t *testing.T) {
	steps := &fakeSteps{auto: true, blockRip: make(chan struct{}), ripStarted: make(chan struct{})}
	e := newEnv(t, steps)
	j, _ := e.store.CreateJob(ctx, "/dev/sr0")

	runCtx, shutdown := context.WithCancel(ctx)
	errc := make(chan error, 1)
	go func() { errc <- e.engine.Run(runCtx, j.ID) }()
	<-steps.ripStarted
	// Leave a partial file, as a real interrupted rip would.
	dir := e.engine.StagingDir(j.ID)
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "partial.mkv"), []byte("half"), 0o644)
	shutdown()
	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run after shutdown: err = %v, want context.Canceled", err)
	}
	if st := e.state(t, j.ID); st != jobs.Ripping {
		t.Fatalf("state after shutdown = %s, want ripping", st)
	}

	// Restart.
	*steps = fakeSteps{auto: true, ripSawDir: func(dir string) {
		if entries, _ := os.ReadDir(dir); len(entries) != 0 {
			t.Errorf("rip after restart found %d leftover files", len(entries))
		}
	}}
	e.reopen(t)
	ids, err := e.engine.ResumableJobs(ctx)
	if err != nil || !slices.Equal(ids, []int64{j.ID}) {
		t.Fatalf("resumable = %v, %v", ids, err)
	}
	if err := e.engine.Run(ctx, j.ID); err != nil {
		t.Fatal(err)
	}
	if st := e.state(t, j.ID); st != jobs.Done {
		t.Errorf("state after resume = %s", st)
	}
}

// Crash recovery, state by state: put a job in each state as a crash would
// leave it, restart, and check it runs from that state to Done, running
// exactly the remaining steps.
func TestRestartResumesFromEveryState(t *testing.T) {
	path := []jobs.State{jobs.Detected, jobs.Scanning, jobs.Identifying, jobs.Selecting,
		jobs.Ripping, jobs.Verifying, jobs.Importing}
	allSteps := []string{"scan", "identify", "select", "rip", "verify", "import"}
	for i, crashedIn := range path {
		t.Run(string(crashedIn), func(t *testing.T) {
			e := newEnv(t, &fakeSteps{auto: true})
			j, _ := e.store.CreateJob(ctx, "/dev/sr0")
			j.Disc, j.Titles = sampleDisc, []int{0}
			for _, st := range path[1 : i+1] {
				if err := e.store.Move(ctx, j, st, ""); err != nil {
					t.Fatal(err)
				}
			}
			e.reopen(t)
			if err := e.engine.Run(ctx, j.ID); err != nil {
				t.Fatal(err)
			}
			if st := e.state(t, j.ID); st != jobs.Done {
				t.Fatalf("ended in %s", st)
			}
			// Detected and Scanning both start with the scan.
			first := max(i-1, 0)
			if got, want := e.steps.ran(), allSteps[first:]; !slices.Equal(got, want) {
				t.Errorf("steps after restart = %v, want %v", got, want)
			}
		})
	}
}

func TestCancelInterruptsARunningRip(t *testing.T) {
	steps := &fakeSteps{auto: true, blockRip: make(chan struct{}), ripStarted: make(chan struct{})}
	e := newEnv(t, steps)
	j, _ := e.store.CreateJob(ctx, "/dev/sr0")
	errc := make(chan error, 1)
	go func() { errc <- e.engine.Run(ctx, j.ID) }()
	<-steps.ripStarted

	if err := e.engine.Cancel(ctx, j.ID); err != nil {
		t.Fatal(err)
	}
	<-errc
	if st := e.state(t, j.ID); st != jobs.Cancelled {
		t.Errorf("state = %s, want cancelled", st)
	}
	if _, err := os.Stat(e.engine.StagingDir(j.ID)); !os.IsNotExist(err) {
		t.Errorf("staging folder kept after cancel")
	}
	if err := e.engine.Cancel(ctx, j.ID); err == nil {
		t.Error("cancelled a job twice")
	}
}

// An import that has started finishes, even when the job is cancelled.
func TestImportIsNotInterrupted(t *testing.T) {
	steps := &fakeSteps{auto: true}
	e := newEnv(t, steps)
	j, _ := e.store.CreateJob(ctx, "/dev/sr0")
	importing := make(chan struct{})
	release := make(chan struct{})
	steps.importDone = func(string) { close(importing); <-release }

	errc := make(chan error, 1)
	go func() { errc <- e.engine.Run(ctx, j.ID) }()
	<-importing
	cancelErr := make(chan error, 1)
	go func() { cancelErr <- e.engine.Cancel(ctx, j.ID) }()
	time.Sleep(50 * time.Millisecond) // let Cancel interrupt, if it could
	close(release)

	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	if st := e.state(t, j.ID); st != jobs.Done {
		t.Errorf("state = %s, want done: the import must finish", st)
	}
	if err := <-cancelErr; err == nil {
		t.Error("Cancel reported success on a job that finished")
	}
}

func TestAJobRunsOnlyOnceAtATime(t *testing.T) {
	steps := &fakeSteps{auto: true, blockRip: make(chan struct{}), ripStarted: make(chan struct{})}
	e := newEnv(t, steps)
	j, _ := e.store.CreateJob(ctx, "/dev/sr0")
	errc := make(chan error, 1)
	go func() { errc <- e.engine.Run(ctx, j.ID) }()
	<-steps.ripStarted
	if err := e.engine.Run(ctx, j.ID); !errors.Is(err, jobs.ErrRunning) {
		t.Errorf("second Run: err = %v, want ErrRunning", err)
	}
	close(steps.blockRip)
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
}

func TestProgressIsSavedButThrottled(t *testing.T) {
	steps := &fakeSteps{auto: true}
	steps.onRip = func(progress func(float64)) {
		for i := range 1000 {
			progress(float64(i) / 1000)
		}
		progress(1)
	}
	e := newEnv(t, steps)
	e.engine.ProgressEvery = time.Hour // only the first and the final value get through
	j, _ := e.store.CreateJob(ctx, "/dev/sr0")
	if err := e.engine.Run(ctx, j.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.store.Job(ctx, j.ID); got.Progress != 1 {
		t.Errorf("progress = %v, want 1", got.Progress)
	}
}

func TestTransitionTable(t *testing.T) {
	allowed := [][2]jobs.State{
		{jobs.Detected, jobs.Scanning}, {jobs.Selecting, jobs.NeedsYou}, {jobs.NeedsYou, jobs.Ripping},
		{jobs.Importing, jobs.Done}, {jobs.Failed, jobs.Ripping}, {jobs.Ripping, jobs.Cancelled},
	}
	forbidden := [][2]jobs.State{
		{jobs.Detected, jobs.Ripping},    // can't skip the scan
		{jobs.Importing, jobs.Cancelled}, // an import finishes
		{jobs.Done, jobs.Ripping},        // done is final
		{jobs.Cancelled, jobs.Detected},  // so is cancelled
		{jobs.NeedsYou, jobs.Done},
	}
	for _, p := range allowed {
		if !jobs.CanMove(p[0], p[1]) {
			t.Errorf("%s → %s should be allowed", p[0], p[1])
		}
	}
	for _, p := range forbidden {
		if jobs.CanMove(p[0], p[1]) {
			t.Errorf("%s → %s should be forbidden", p[0], p[1])
		}
	}
}

// The disc is checked before every rip. A swapped disc fails the job, and a
// retry starts over with a fresh scan rather than ripping the new disc with
// the old disc's titles.
func TestSwappedDiscIsNotRipped(t *testing.T) {
	steps := &fakeSteps{auto: true, discChanged: true}
	e := newEnv(t, steps)
	j, _ := e.store.CreateJob(ctx, "/dev/sr0")
	if err := e.engine.Run(ctx, j.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := e.store.Job(ctx, j.ID)
	if got.State != jobs.Failed || got.FailedIn != jobs.Detected {
		t.Fatalf("state %s, failed in %s; want failed, resuming in detected", got.State, got.FailedIn)
	}
	if slices.Contains(steps.ran(), "rip") {
		t.Error("ripped a disc that was swapped")
	}
	if err := e.engine.Retry(ctx, j.ID); err != nil {
		t.Fatal(err)
	}
	if st := e.state(t, j.ID); st != jobs.Detected {
		t.Errorf("retry went to %s, want detected", st)
	}
}

// After a restart, a job found in Ripping checks the disc before ripping.
func TestRestartInRippingChecksTheDisc(t *testing.T) {
	steps := &fakeSteps{auto: true}
	e := newEnv(t, steps)
	j, _ := e.store.CreateJob(ctx, "/dev/sr0")
	j.Disc, j.Titles = sampleDisc, []int{0}
	for _, st := range []jobs.State{jobs.Scanning, jobs.Identifying, jobs.Selecting, jobs.Ripping} {
		e.store.Move(ctx, j, st, "")
	}
	e.reopen(t)
	e.engine.Run(ctx, j.ID)
	if steps.checks != 1 {
		t.Errorf("CheckDisc ran %d times, want 1", steps.checks)
	}
}

// blockingStore holds up the move to Cancelled until released, so a test can
// try to start a Run while Cancel is half-way.
type blockingStore struct {
	jobs.Store
	cancelling chan struct{}
	release    chan struct{}
}

func (b *blockingStore) Move(ctx context.Context, j *jobs.Job, to jobs.State, note string) error {
	if to == jobs.Cancelled {
		close(b.cancelling)
		<-b.release
	}
	return b.Store.Move(ctx, j, to, note)
}

func TestRunCantStartWhileCancelIsUnderway(t *testing.T) {
	e := newEnv(t, &fakeSteps{auto: false})
	j, _ := e.store.CreateJob(ctx, "/dev/sr0")
	bs := &blockingStore{Store: e.store, cancelling: make(chan struct{}), release: make(chan struct{})}
	eng := jobs.NewEngine(bs, e.steps, e.stage, nil)

	cancelErr := make(chan error, 1)
	go func() { cancelErr <- eng.Cancel(ctx, j.ID) }()
	<-bs.cancelling
	if err := eng.Run(ctx, j.ID); !errors.Is(err, jobs.ErrRunning) {
		t.Errorf("Run during Cancel: err = %v, want ErrRunning", err)
	}
	close(bs.release)
	if err := <-cancelErr; err != nil {
		t.Fatal(err)
	}
	if st := e.state(t, j.ID); st != jobs.Cancelled {
		t.Errorf("state = %s", st)
	}
}

// Progress is only written by SetProgress, so failing the job (a Move made
// from the engine's older copy) keeps how far the rip got.
func TestProgressSurvivesAFailedRip(t *testing.T) {
	steps := &fakeSteps{auto: true, failIn: "rip"}
	steps.onRip = func(progress func(float64)) { progress(0.73) }
	e := newEnv(t, steps)
	j, _ := e.store.CreateJob(ctx, "/dev/sr0")
	e.engine.Run(ctx, j.ID)
	got, _ := e.store.Job(ctx, j.ID)
	if got.State != jobs.Failed || got.Progress != 0.73 {
		t.Errorf("state %s, progress %v; want failed at 0.73", got.State, got.Progress)
	}
}

func TestNilLoggerIsAllowed(t *testing.T) {
	e := newEnv(t, &fakeSteps{auto: true})
	eng := jobs.NewEngine(e.store, e.steps, e.stage, nil)
	j, _ := e.store.CreateJob(ctx, "/dev/sr0")
	if err := eng.Run(ctx, j.ID); err != nil {
		t.Fatal(err)
	}
}

// After a restart, a job left in Importing can't be cancelled: it must be
// resumed and finish.
func TestCancelRefusesAnImportAfterRestart(t *testing.T) {
	e := newEnv(t, &fakeSteps{auto: true})
	j, _ := e.store.CreateJob(ctx, "/dev/sr0")
	j.Disc, j.Titles = sampleDisc, []int{0}
	for _, st := range []jobs.State{jobs.Scanning, jobs.Identifying, jobs.Selecting, jobs.Ripping, jobs.Verifying, jobs.Importing} {
		e.store.Move(ctx, j, st, "")
	}
	e.reopen(t)
	var te *jobs.TransitionError
	if err := e.engine.Cancel(ctx, j.ID); !errors.As(err, &te) {
		t.Fatalf("err = %v, want a *TransitionError", err)
	}
	if st := e.state(t, j.ID); st != jobs.Importing {
		t.Errorf("state = %s, want importing", st)
	}
}

func TestRetryOnABusyDriveFails(t *testing.T) {
	steps := &fakeSteps{auto: true, failIn: "verify"}
	e := newEnv(t, steps)
	first, _ := e.store.CreateJob(ctx, "/dev/sr0")
	e.engine.Run(ctx, first.ID) // fails, freeing the drive
	if _, err := e.store.CreateJob(ctx, "/dev/sr0"); err != nil {
		t.Fatal(err)
	}
	if err := e.engine.Retry(ctx, first.ID); !errors.Is(err, jobs.ErrDriveBusy) {
		t.Errorf("err = %v, want ErrDriveBusy", err)
	}
}

func TestApproveCanCorrectTheMovie(t *testing.T) {
	e := newEnv(t, &fakeSteps{auto: false})
	j, _ := e.store.CreateJob(ctx, "/dev/sr0")
	e.engine.Run(ctx, j.ID)
	if err := e.engine.Approve(ctx, j.ID, []int{0}, &jobs.Movie{TMDbID: 0}); err == nil {
		t.Error("approved a movie without a TMDb id")
	}
	fixed := &jobs.Movie{TMDbID: 559, Title: "Spider-Man 3", Year: 2007}
	if err := e.engine.Approve(ctx, j.ID, []int{0}, fixed); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.store.Job(ctx, j.ID); got.Movie == nil || got.Movie.TMDbID != 559 {
		t.Errorf("movie = %+v, want the corrected one", got.Movie)
	}
}
