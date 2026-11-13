// Package app ties Trayarr's parts together at run time: it starts a job's
// run when a disc goes in or the user acts, resumes jobs after a restart,
// ejects finished discs, and reports health. It holds no logic of its own
// about discs or Radarr; that lives in the parts.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"sync"
	"time"

	"github.com/NotNad/trayarr/internal/jobs"
	"github.com/NotNad/trayarr/internal/radarr"
	"github.com/NotNad/trayarr/internal/store"
)

// App runs jobs in the background and answers the web UI.
type App struct {
	store  *store.Store
	engine *jobs.Engine
	radarr *radarr.Client
	log    *slog.Logger

	// Drives are the device paths Trayarr manages.
	Drives []string
	// MakeMKVCon is the makemkvcon path, checked by Health.
	MakeMKVCon string
	// Eject, if set, is called with the drive when a job finishes.
	Eject func(device string) error

	base context.Context // runs end when this does (shutdown)
	wg   sync.WaitGroup

	mu      sync.Mutex
	pending map[string]bool // drives with a new disc waiting for the drive to free up
}

// New returns an App whose job runs stop when base is cancelled.
func New(base context.Context, st *store.Store, engine *jobs.Engine, rc *radarr.Client, log *slog.Logger) *App {
	return &App{store: st, engine: engine, radarr: rc, log: log, base: base, pending: map[string]bool{}}
}

// start runs a job in the background. A job already running is left alone.
func (a *App) start(id int64) {
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		err := a.engine.Run(a.base, id)
		switch {
		case errors.Is(err, jobs.ErrRunning):
			return
		case errors.Is(err, context.Canceled):
			a.log.Info("job interrupted; it resumes at the next start", "job", id)
			return
		case err != nil:
			a.log.Error("job run stopped", "job", id, "err", err)
			return
		}
		j, err := a.store.Job(context.WithoutCancel(a.base), id)
		if err != nil || j.State.Active() {
			return
		}
		// The drive is free. A disc that went in meanwhile gets its job now,
		// and is not ejected: it is the next disc, not this job's.
		if a.startPending(j.Drive) {
			return
		}
		if j.State == jobs.Done && a.Eject != nil {
			if err := a.Eject(j.Drive); err != nil {
				a.log.Warn("eject failed", "drive", j.Drive, "err", err)
			}
		}
	}()
}

// startPending starts the job for a disc that went in while drive was busy,
// and reports whether there was one.
func (a *App) startPending(drive string) bool {
	a.mu.Lock()
	waiting := a.pending[drive]
	delete(a.pending, drive)
	a.mu.Unlock()
	if !waiting {
		return false
	}
	a.log.Info("starting the disc that went in while the drive was busy", "drive", drive)
	if _, err := a.DiscInserted(a.base, drive); err != nil {
		a.log.Warn("not starting a job", "drive", drive, "err", err)
	}
	return true
}

// Pending reports whether a new disc is waiting for drive to free up.
func (a *App) Pending(drive string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.pending[drive]
}

// DiscInserted starts a job for the disc in drive.
func (a *App) DiscInserted(ctx context.Context, drive string) (*jobs.Job, error) {
	if !a.knows(drive) {
		return nil, fmt.Errorf("%s is not one of Trayarr's drives", drive)
	}
	j, err := a.store.CreateJob(ctx, drive)
	if err != nil {
		return nil, err
	}
	a.log.Info("job created", "job", j.ID, "drive", drive)
	a.start(j.ID)
	return j, nil
}

// OnDisc is DiscInserted for the drive watcher, which has nobody to return
// an error to. A disc that goes in while the drive still has a job (one
// waiting for approval, say) is remembered, and gets its job when the drive
// frees up: the watcher only reports changes, so it won't report this disc
// again.
func (a *App) OnDisc(ctx context.Context, drive string) {
	_, err := a.DiscInserted(ctx, drive)
	if errors.Is(err, jobs.ErrDriveBusy) {
		a.mu.Lock()
		a.pending[drive] = true
		a.mu.Unlock()
		a.log.Info("a new disc went in while the drive is busy; it starts when the current job ends", "drive", drive)
		return
	}
	if err != nil {
		a.log.Warn("not starting a job", "drive", drive, "err", err)
	}
}

func (a *App) Approve(ctx context.Context, id int64, titles []int, movie *jobs.Movie) error {
	if err := a.engine.Approve(ctx, id, titles, movie); err != nil {
		return err
	}
	a.start(id)
	return nil
}

func (a *App) Retry(ctx context.Context, id int64) error {
	if err := a.engine.Retry(ctx, id); err != nil {
		return err
	}
	a.start(id)
	return nil
}

func (a *App) Cancel(ctx context.Context, id int64) error {
	j, err := a.store.Job(ctx, id)
	if err != nil {
		return err
	}
	if err := a.engine.Cancel(ctx, id); err != nil {
		return err
	}
	a.startPending(j.Drive)
	return nil
}

// Resume starts every job a previous run left unfinished.
func (a *App) Resume(ctx context.Context) error {
	ids, err := a.engine.ResumableJobs(ctx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		a.log.Info("resuming job", "job", id)
		a.start(id)
	}
	return nil
}

// Wait waits for running jobs to stop after base is cancelled, at most
// timeout. It reports whether they all did.
func (a *App) Wait(timeout time.Duration) bool {
	done := make(chan struct{})
	go func() { a.wg.Wait(); close(done) }()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

// LookupMovie finds a movie by TMDb id, for the user correcting an
// identification.
func (a *App) LookupMovie(ctx context.Context, tmdbID int) (*jobs.Movie, error) {
	m, err := a.radarr.LookupTMDb(ctx, tmdbID)
	if err != nil {
		return nil, err
	}
	return &jobs.Movie{TMDbID: m.TMDbID, Title: m.Title, Year: m.Year}, nil
}

// Reads, passed through to the store for the web UI.

func (a *App) Job(ctx context.Context, id int64) (*jobs.Job, error) { return a.store.Job(ctx, id) }
func (a *App) Events(ctx context.Context, id int64) ([]jobs.Event, error) {
	return a.store.Events(ctx, id)
}
func (a *App) ActiveJobs(ctx context.Context) ([]*jobs.Job, error) { return a.store.ActiveJobs(ctx) }
func (a *App) RecentJobs(ctx context.Context, n int) ([]*jobs.Job, error) {
	return a.store.RecentJobs(ctx, n)
}
func (a *App) DriveList() []string { return a.Drives }

// Check is one line of the health report.
type Check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// Health checks what Trayarr needs to work: its database, makemkvcon and
// Radarr. It is what /readyz reports.
func (a *App) Health(ctx context.Context) []Check {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var out []Check
	if err := a.store.Ping(ctx); err != nil {
		out = append(out, Check{"database", false, err.Error()})
	} else {
		out = append(out, Check{"database", true, "open"})
	}
	if p, err := exec.LookPath(a.MakeMKVCon); err != nil {
		out = append(out, Check{"makemkvcon", false, err.Error()})
	} else {
		out = append(out, Check{"makemkvcon", true, p})
	}
	if s, err := a.radarr.SystemStatus(ctx); err != nil {
		out = append(out, Check{"radarr", false, err.Error()})
	} else {
		out = append(out, Check{"radarr", true, "version " + s.Version})
	}
	return out
}

func (a *App) knows(drive string) bool {
	for _, d := range a.Drives {
		if d == drive {
			return true
		}
	}
	return false
}
