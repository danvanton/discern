package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/danvanton/discern/internal/lang"
	"github.com/danvanton/discern/internal/makemkv"
)

// Steps does the actual work of each state. The engine decides which step
// runs and records the outcome; Steps knows how (MakeMKV, Radarr, the
// filesystem). Tests use a fake.
//
// Every step must be safe to run again after a crash: Scan, Identify, Select
// and Verify only read; Rip gets an empty folder each time; Import must check
// whether its work is already done before doing it (Radarr may already have
// the file).
type Steps interface {
	// CheckDisc confirms that the disc in j's drive is still the one j
	// scanned, returning ErrDiscChanged if not. The engine calls it before
	// every rip: the user may have swapped discs while the job waited for
	// approval, or while Discern was stopped.
	CheckDisc(ctx context.Context, j *Job) error
	Scan(ctx context.Context, j *Job) (*makemkv.Disc, error)
	Identify(ctx context.Context, j *Job) (*Movie, error)
	Select(ctx context.Context, j *Job) (Selection, error)
	// Rip saves j.Titles into dir, which the engine has just emptied.
	Rip(ctx context.Context, j *Job, dir string, progress func(float64)) error
	Verify(ctx context.Context, j *Job, dir string) error
	Import(ctx context.Context, j *Job, dir string) error
}

// Engine moves jobs through their states, one step at a time, saving after
// each.
type Engine struct {
	store   Store
	steps   Steps
	staging string // each job rips into <staging>/<job id>
	log     *slog.Logger

	// ProgressEvery limits how often rip progress is written to the store.
	ProgressEvery time.Duration

	mu      sync.Mutex
	running map[int64]*run
}

type run struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// NewEngine returns an engine. A nil log means slog.Default().
func NewEngine(store Store, steps Steps, stagingRoot string, log *slog.Logger) *Engine {
	if log == nil {
		log = slog.Default()
	}
	return &Engine{
		store:         store,
		steps:         steps,
		staging:       stagingRoot,
		log:           log,
		ProgressEvery: time.Second,
		running:       map[int64]*run{},
	}
}

// StagingDir is where a job's rip lives until it is imported.
func (e *Engine) StagingDir(id int64) string {
	return filepath.Join(e.staging, strconv.FormatInt(id, 10))
}

// Run takes the job from its current state as far as it can go: to Done, to
// Failed, or to NeedsYou to wait for the user. A step that fails puts the
// job in Failed; that is an outcome, not an error, so Run returns nil.
//
// Run returns an error when it could not record progress (the store failed)
// or when ctx was cancelled. Then the job stays in the state it was in, and
// running it again later carries on from there. That is how a shutdown
// mid-rip resumes after a restart.
func (e *Engine) Run(ctx context.Context, id int64) error {
	ctx, r, err := e.register(ctx, id)
	if err != nil {
		return err
	}
	defer e.unregister(id, r)

	for {
		// Read without ctx's cancellation: after an import that finished
		// despite a cancel, the job is Done, and Run should say so rather
		// than report an interruption.
		j, err := e.store.Job(context.WithoutCancel(ctx), id)
		if err != nil {
			return err
		}
		if j.State == NeedsYou || j.State == Failed || j.State.Terminal() {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := e.step(ctx, j); err != nil {
			return err
		}
	}
}

// step runs the work of j's current state and moves it to the next one.
func (e *Engine) step(ctx context.Context, j *Job) error {
	switch j.State {
	case Detected:
		return e.move(ctx, j, Scanning, "")

	case Scanning:
		disc, err := e.steps.Scan(ctx, j)
		if err != nil {
			return e.fail(ctx, j, "scan", err)
		}
		j.Disc = disc
		return e.move(ctx, j, Identifying, fmt.Sprintf("%d titles", len(disc.Titles)))

	case Identifying:
		movie, err := e.steps.Identify(ctx, j)
		if err != nil {
			return e.fail(ctx, j, "identify", err)
		}
		j.Movie = movie
		return e.move(ctx, j, Selecting, "")

	case Selecting:
		sel, err := e.steps.Select(ctx, j)
		if err != nil {
			return e.fail(ctx, j, "select", err)
		}
		if err := checkTitles(j.Disc, sel.Titles); err != nil {
			return e.fail(ctx, j, "select", err)
		}
		j.Titles, j.Note = sel.Titles, sel.Reason
		if sel.Auto && len(sel.Titles) > 0 {
			return e.move(ctx, j, Ripping, sel.Reason)
		}
		return e.move(ctx, j, NeedsYou, sel.Reason)

	case Ripping:
		// A fresh attempt starts at 0, before the disc check: that can take
		// minutes (a rescan after a restart), and the pages must not show the
		// last attempt's progress, or a time left worked out from it.
		if err := e.store.SetProgress(ctx, j.ID, 0); err != nil {
			return err
		}
		if err := e.steps.CheckDisc(ctx, j); err != nil {
			if errors.Is(err, ErrDiscChanged) {
				// The job's disc is gone: retrying it means starting over
				// with a fresh scan, so it "failed in" Detected.
				return e.failIn(ctx, j, Detected, "check disc", err)
			}
			return e.fail(ctx, j, "check disc", err)
		}
		dir := e.StagingDir(j.ID)
		// Start from an empty folder every time: whatever a crashed attempt
		// left is never trusted. This is what makes the step safe to rerun.
		if err := os.RemoveAll(dir); err != nil {
			return e.fail(ctx, j, "clear staging", err)
		}
		if err := e.steps.Rip(ctx, j, dir, e.progressWriter(j.ID)); err != nil {
			return e.fail(ctx, j, "rip", err)
		}
		if err := e.store.SetProgress(ctx, j.ID, 1); err != nil {
			return err
		}
		return e.move(ctx, j, Verifying, "")

	case Verifying:
		if err := e.steps.Verify(ctx, j, e.StagingDir(j.ID)); err != nil {
			return e.fail(ctx, j, "verify", err)
		}
		return e.move(ctx, j, Importing, "")

	case Importing:
		// An import that has started finishes even if Discern is shutting
		// down or the user cancels (the Importer bounds it with its own
		// timeouts). Stopping halfway could leave Radarr and Discern
		// disagreeing about where the file is.
		ictx := context.WithoutCancel(ctx)
		if err := e.steps.Import(ictx, j, e.StagingDir(j.ID)); err != nil {
			return e.fail(ictx, j, "import", err)
		}
		if err := e.move(ictx, j, Done, ""); err != nil {
			return err
		}
		e.removeStaging(j.ID)
		return nil
	}
	return fmt.Errorf("job %d: no step for state %s", j.ID, j.State)
}

// move checks the transition against the table and saves it.
func (e *Engine) move(ctx context.Context, j *Job, to State, note string) error {
	if !CanMove(j.State, to) {
		return &TransitionError{From: j.State, To: to}
	}
	from := j.State
	if err := e.store.Move(ctx, j, to, note); err != nil {
		return err
	}
	e.log.Info("job moved", "job", j.ID, "drive", j.Drive, "from", from, "to", to, "note", note)
	return nil
}

// fail records a failed step. If the step failed because ctx was cancelled
// (shutdown or Cancel), the job is left as it is instead: it didn't fail, it
// was interrupted.
func (e *Engine) fail(ctx context.Context, j *Job, what string, err error) error {
	return e.failIn(ctx, j, j.State, what, err)
}

// failIn is fail with the state Retry should resume in.
func (e *Engine) failIn(ctx context.Context, j *Job, resumeIn State, what string, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	j.FailedIn = resumeIn
	j.Note = fmt.Sprintf("%s failed: %v", what, err)
	return e.move(ctx, j, Failed, j.Note)
}

// Approve is the user's answer in NeedsYou: rip these titles.
//
// movie, if not nil, replaces the identified movie: the user corrected it.
// A job can't be approved without a movie, because the import needs one.
// langs, if not nil, is this disc's own track choice (decision D34); nil
// leaves the Settings page's choice in charge.
func (e *Engine) Approve(ctx context.Context, id int64, titles []int, movie *Movie, langs *lang.Choice) error {
	j, err := e.store.Job(ctx, id)
	if err != nil {
		return err
	}
	if j.State != NeedsYou {
		return fmt.Errorf("job %d is %s, not waiting for approval", id, j.State)
	}
	if len(titles) == 0 {
		return errors.New("choose at least one title")
	}
	if err := checkTitles(j.Disc, titles); err != nil {
		return err
	}
	if movie != nil {
		if movie.TMDbID <= 0 {
			return errors.New("the movie needs a TMDb id")
		}
		j.Movie = movie
	}
	if j.Movie == nil {
		return ErrNoMovie
	}
	j.Titles, j.Note, j.Languages = slices.Clone(titles), "", langs
	return e.move(ctx, j, Ripping, fmt.Sprintf("approved titles %v", titles))
}

// Retry puts a failed job back to the step that failed. Steps after the scan
// resume where they stopped; a failed scan, identify or select starts over.
// It returns ErrDriveBusy if another job has the drive by now.
func (e *Engine) Retry(ctx context.Context, id int64) error {
	j, err := e.store.Job(ctx, id)
	if err != nil {
		return err
	}
	if j.State != Failed {
		return fmt.Errorf("job %d is %s, not failed", id, j.State)
	}
	to := Detected
	if j.FailedIn == Ripping || j.FailedIn == Verifying || j.FailedIn == Importing {
		to = j.FailedIn
	}
	j.FailedIn, j.Note = "", ""
	return e.move(ctx, j, to, "retry")
}

// Cancel stops a job. If it is running, its step is interrupted first (an
// import is the exception: it finishes, and the job ends up Done). The
// staging folder is removed.
//
// Cancel takes the job's run slot for itself while it works, so a Run that
// starts at the same moment gets ErrRunning instead of starting a step on a
// job that is being cancelled.
func (e *Engine) Cancel(ctx context.Context, id int64) error {
	for {
		e.mu.Lock()
		r := e.running[id]
		if r == nil {
			mine := &run{cancel: func() {}, done: make(chan struct{})}
			e.running[id] = mine
			e.mu.Unlock()
			defer e.unregister(id, mine)
			return e.cancelIdle(ctx, id)
		}
		e.mu.Unlock()
		r.cancel()
		select {
		case <-r.done: // the run has stopped; take the slot on the next turn
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// cancelIdle cancels a job nobody is running.
func (e *Engine) cancelIdle(ctx context.Context, id int64) error {
	j, err := e.store.Job(ctx, id)
	if err != nil {
		return err
	}
	if j.State.Terminal() {
		return fmt.Errorf("job %d is already %s", id, j.State)
	}
	if err := e.move(ctx, j, Cancelled, "cancelled by user"); err != nil {
		return err
	}
	e.removeStaging(id)
	return nil
}

// ResumableJobs lists the jobs to run again after a restart: every active
// job except those waiting for the user.
func (e *Engine) ResumableJobs(ctx context.Context) ([]int64, error) {
	active, err := e.store.ActiveJobs(ctx)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for _, j := range active {
		if j.State != NeedsYou {
			ids = append(ids, j.ID)
		}
	}
	return ids, nil
}

// register records that a job is running, so it is never run twice at once
// and Cancel can interrupt it.
func (e *Engine) register(ctx context.Context, id int64) (context.Context, *run, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.running[id] != nil {
		return nil, nil, fmt.Errorf("%w: %d", ErrRunning, id)
	}
	ctx, cancel := context.WithCancel(ctx)
	r := &run{cancel: cancel, done: make(chan struct{})}
	e.running[id] = r
	return ctx, r, nil
}

func (e *Engine) unregister(id int64, r *run) {
	e.mu.Lock()
	delete(e.running, id)
	e.mu.Unlock()
	r.cancel()
	close(r.done)
}

// progressWriter returns a progress callback that saves at most once per
// ProgressEvery, plus the final value.
func (e *Engine) progressWriter(id int64) func(float64) {
	var last time.Time
	return func(p float64) {
		if p < 1 && time.Since(last) < e.ProgressEvery {
			return
		}
		last = time.Now()
		// Background context: progress is saved even while a cancelled rip
		// winds down; it is one small write.
		if err := e.store.SetProgress(context.Background(), id, p); err != nil {
			e.log.Warn("saving progress failed", "job", id, "err", err)
		}
	}
}

func (e *Engine) removeStaging(id int64) {
	if err := os.RemoveAll(e.StagingDir(id)); err != nil {
		e.log.Warn("removing staging folder failed", "job", id, "err", err)
	}
}

// checkTitles makes sure every chosen title exists on the scanned disc.
func checkTitles(d *makemkv.Disc, titles []int) error {
	if d == nil {
		return errors.New("no scan to choose titles from")
	}
	for _, t := range titles {
		if !slices.ContainsFunc(d.Titles, func(x makemkv.Title) bool { return x.Index == t }) {
			return fmt.Errorf("title %d is not on the disc", t)
		}
	}
	return nil
}
