// Package app ties Discern's parts together at run time: it starts a job's
// run when a disc goes in or the user acts, resumes jobs after a restart,
// ejects finished discs, and reports health. It holds no logic of its own
// about discs or Radarr; that lives in the parts.
package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/danvanton/discern/internal/discdb"
	"github.com/danvanton/discern/internal/drive"
	"github.com/danvanton/discern/internal/jobs"
	"github.com/danvanton/discern/internal/makemkv"
	"github.com/danvanton/discern/internal/poster"
	"github.com/danvanton/discern/internal/radarr"
	"github.com/danvanton/discern/internal/settings"
	"github.com/danvanton/discern/internal/steps"
	"github.com/danvanton/discern/internal/store"
)

// App runs jobs in the background and answers the web UI.
type App struct {
	store  *store.Store
	engine *jobs.Engine
	radarr *radarr.Client
	log    *slog.Logger

	// Drives are the device paths Discern manages.
	Drives []string
	// MakeMKVCon is the makemkvcon path, checked by Health.
	MakeMKVCon string
	// Eject, if set, is called with the drive when a job finishes and the
	// Settings page has eject on (decision D25).
	Eject func(device string) error
	// MakeMKVSettings is MakeMKV's settings.conf, where the language choice
	// goes before each rip; empty to leave it alone (development).
	MakeMKVSettings string
	// Posters, if set, fetches a job's poster once its movie is known.
	Posters *poster.Cache
	// Trays, if set, says what each drive's tray is doing.
	Trays *drive.Tracker
	// DiagnosticsDir is where stalled rips' evidence is saved (decision D37).
	DiagnosticsDir string
	// Catalog, if set, is TheDiscDB's disc data (decision D38).
	Catalog *discdb.Catalog

	base context.Context // runs end when this does (shutdown)
	wg   sync.WaitGroup

	mu      sync.Mutex
	pending map[string]bool      // drives with a new disc waiting for the drive to free up
	live    map[string]liveEntry // how each drive's rip is going (decision D30)
}

// liveEntry is a rip's latest report, and when it came.
type liveEntry struct {
	makemkv.Live
	at time.Time
}

// liveFresh is how old a rip's report may be and still be shown: a few
// watchdog samples. Anything older belongs to a rip that has ended.
const liveFresh = 2 * time.Minute

// New returns an App whose job runs stop when base is cancelled.
func New(base context.Context, st *store.Store, engine *jobs.Engine, rc *radarr.Client, log *slog.Logger) *App {
	return &App{store: st, engine: engine, radarr: rc, log: log, base: base,
		pending: map[string]bool{}, live: map[string]liveEntry{}}
}

// start runs a job in the background. A job already running is left alone.
func (a *App) start(id int64) {
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		err := a.engine.Run(a.base, id)
		if !errors.Is(err, jobs.ErrRunning) {
			// The run stopped: its rip, if any, has ended, and the movie is
			// known by now if identification found it.
			if j, jerr := a.store.Job(context.WithoutCancel(a.base), id); jerr == nil {
				a.endLive(j.Drive)
				a.fetchPoster(j.Movie)
			}
		}
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
			st, err := a.store.Settings(context.WithoutCancel(a.base))
			switch {
			case err != nil:
				a.log.Warn("not ejecting: the settings can't be read", "drive", j.Drive, "err", err)
			case !st.Eject:
				// Off on the Settings page: the tray stays shut.
			default:
				if err := a.Eject(j.Drive); err != nil {
					a.log.Warn("eject failed", "drive", j.Drive, "err", err)
				} else {
					a.log.Info("disc ejected", "drive", j.Drive)
				}
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
		return nil, fmt.Errorf("%s is not one of Discern's drives", drive)
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
// an error to. A disc that goes in while the drive's job waits for approval
// replaces that job (decision D58). One that goes in while the drive's job
// is further on (ripping, say) is remembered, and gets its job when the
// drive frees up: the watcher only reports changes, so it won't report this
// disc again.
func (a *App) OnDisc(ctx context.Context, drive string) {
	_, err := a.DiscInserted(ctx, drive)
	// A disc went in, so a job waiting for approval lost its disc, even if
	// the tray was swapped between two polls and never read as open
	// (decision D58): the new disc takes the drive.
	if errors.Is(err, jobs.ErrDriveBusy) && a.withdrawWaiting(ctx, drive, "another disc went in") {
		_, err = a.DiscInserted(ctx, drive)
	}
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

// OnDiscRemoved is the drive watcher's report that a drive has no disc: its
// tray opened, or it reads as empty, also at start. The drive's job, if it
// is waiting for approval, is cancelled, and the next disc to go in starts
// afresh (decision D58). A disc that was waiting for the drive (Pending) is
// gone too. A job in any other state is left to its step: a scan reports
// the disc it couldn't read (the tray isn't always locked while MakeMKV
// reads: it was opened during a scan on the homelab), and a rip checks its
// disc before it starts.
func (a *App) OnDiscRemoved(ctx context.Context, drive string) {
	a.mu.Lock()
	delete(a.pending, drive)
	a.mu.Unlock()
	a.withdrawWaiting(ctx, drive, "the disc was taken out")
}

// withdrawWaiting cancels drive's job if it is waiting for approval, with
// reason as its note, and reports whether it did.
func (a *App) withdrawWaiting(ctx context.Context, drive, reason string) bool {
	active, err := a.store.ActiveJobs(ctx)
	if err != nil {
		a.log.Warn("the jobs can't be read", "drive", drive, "err", err)
		return false
	}
	withdrawn := false
	for _, j := range active {
		if j.Drive != drive || j.State != jobs.NeedsYou {
			continue
		}
		ok, err := a.engine.Withdraw(ctx, j.ID, reason)
		switch {
		case err != nil:
			a.log.Warn("the waiting job couldn't be cancelled", "job", j.ID, "drive", drive, "err", err)
		case ok:
			a.log.Info("waiting job cancelled", "job", j.ID, "drive", drive, "reason", reason)
			withdrawn = true
		}
	}
	return withdrawn
}

// Approve starts the rip of the chosen titles. langs, if not nil, is this
// disc's own track choice (decision D34), checked like the Settings page's.
func (a *App) Approve(ctx context.Context, id int64, titles []int, movie *jobs.Movie, langs *settings.Languages) error {
	if langs != nil {
		if err := langs.Validate(); err != nil {
			return err
		}
	}
	if err := a.engine.Approve(ctx, id, titles, movie, langs); err != nil {
		return err
	}
	// The user may have chosen a different movie: its poster is wanted now,
	// not when the rip ends.
	a.fetchPoster(movie)
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
	// Posters that failed, or jobs from before posters existed: the
	// dashboard shows the active jobs and the recent ones. A poster is
	// decoration, so a failed read here is logged, never a reason not to start.
	// Posters no job on the dashboard uses (search results nobody picked,
	// jobs too old to be listed) are removed; a job's page fetches its
	// own again if it is viewed.
	active, err := a.store.ActiveJobs(ctx)
	if err == nil {
		var recent []*jobs.Job
		if recent, err = a.store.RecentJobs(ctx, RecentShown); err == nil {
			keep, bundles := map[int]bool{}, map[string]bool{}
			for _, j := range append(active, recent...) {
				if j.Movie != nil {
					keep[j.Movie.TMDbID] = true
				}
				if j.Failure != nil && j.Failure.Diagnostics != "" {
					bundles[j.Failure.Diagnostics] = true
				}
				a.fetchPoster(j.Movie)
			}
			if a.Posters != nil {
				err = a.Posters.Prune(keep)
			}
			err = errors.Join(err, a.pruneDiagnostics(bundles))
		}
	}
	if err != nil {
		a.log.Warn("not tidying the posters and diagnostics at start", "err", err)
	}
	return nil
}

// pruneDiagnostics removes diagnostics bundles no listed job refers to:
// each holds MakeMKV's debug log, and stalls would otherwise fill /config.
// Only folders named like a bundle are touched.
func (a *App) pruneDiagnostics(keep map[string]bool) error {
	if a.DiagnosticsDir == "" {
		return nil
	}
	entries, err := os.ReadDir(a.DiagnosticsDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() && steps.BundleName.MatchString(e.Name()) && !keep[e.Name()] {
			if err := os.RemoveAll(filepath.Join(a.DiagnosticsDir, e.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

// RecentShown is how many finished jobs the dashboard lists.
const RecentShown = 20

// fetchPoster gets the movie's poster in the background. A poster is
// decoration: nothing waits for it, and a failure is only logged.
func (a *App) fetchPoster(m *jobs.Movie) {
	// Nothing new starts once shutdown has begun: Wait may already be
	// waiting, and a page can still ask for a poster then.
	if a.Posters == nil || m == nil || m.TMDbID <= 0 || a.base.Err() != nil {
		return
	}
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		a.Posters.Fetch(a.base, m.TMDbID) // logs its own failure
	}()
}

// Limits on the review page's movie search (decision D33).
const (
	maxSearchTerm    = 100 // characters, as for the disc's own name
	maxSearchResults = 8
	searchPosterWait = 3 * time.Second
)

// SearchMovies finds movies by name through Radarr's lookup, for the user
// correcting an identification (decision D33). Their posters are fetched
// meanwhile, and the results wait for them at most searchPosterWait. A
// slower poster isn't cut off: it finishes in the background and shows on
// a later view, so picking that movie doesn't leave it without one.
func (a *App) SearchMovies(ctx context.Context, term string) ([]jobs.Movie, error) {
	term = strings.TrimSpace(term)
	if term == "" {
		return nil, errors.New("type part of the movie's name")
	}
	if utf8.RuneCountInString(term) > maxSearchTerm {
		return nil, fmt.Errorf("the search is over %d characters", maxSearchTerm)
	}
	found, err := a.radarr.Lookup(ctx, term)
	if err != nil {
		return nil, err
	}
	var out []jobs.Movie
	var wg sync.WaitGroup
	for _, m := range found {
		if m.TMDbID <= 0 || len(out) == maxSearchResults {
			continue
		}
		out = append(out, jobs.Movie{TMDbID: m.TMDbID, Title: m.Title, Year: m.Year})
		if a.Posters == nil || a.base.Err() != nil || m.PosterURL() == "" {
			continue
		}
		// On the app's context, tracked by a.wg, so shutdown waits for it and
		// a slow poster isn't cut off when the page stops waiting.
		wg.Add(1)
		a.wg.Add(1)
		go func(id int, raw string) {
			defer a.wg.Done()
			defer wg.Done()
			a.Posters.FetchFrom(a.base, id, raw) // logs its own failure
		}(m.TMDbID, m.PosterURL())
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(searchPosterWait):
	case <-ctx.Done():
	}
	return out, nil
}

// PosterURL is the poster's address according to Radarr's lookup, for
// poster.Cache.
func (a *App) PosterURL(ctx context.Context, tmdbID int) (string, error) {
	m, err := a.radarr.LookupTMDb(ctx, tmdbID)
	if err != nil {
		return "", err
	}
	if u := m.PosterURL(); u != "" {
		return u, nil
	}
	return "", fmt.Errorf("radarr's lookup has no poster for %s", m.Title)
}

// Poster returns the cached poster for a movie, if there is one. A missing
// poster whose last try is an hour old is fetched again in the background,
// so a brief outage doesn't cost it for good.
func (a *App) Poster(tmdbID int) (path, contentType string, ok bool) {
	if a.Posters == nil {
		return "", "", false
	}
	if path, contentType, ok = a.Posters.File(tmdbID); !ok && a.Posters.Due(tmdbID) {
		a.fetchPoster(&jobs.Movie{TMDbID: tmdbID})
	}
	return path, contentType, ok
}

// DiscIndex is TheDiscDB's index in use, or nil when there is none yet.
func (a *App) DiscIndex() *discdb.Index {
	if a.Catalog == nil {
		return nil
	}
	return a.Catalog.Index()
}

// Diagnostics returns the folder of the diagnostics bundle called name, if
// it exists. The name comes from the job, but is checked against the form
// the rip step gives it before it touches the disk.
func (a *App) Diagnostics(name string) (string, bool) {
	if a.DiagnosticsDir == "" || !steps.BundleName.MatchString(name) {
		return "", false
	}
	dir := filepath.Join(a.DiagnosticsDir, name)
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return "", false
	}
	return dir, true
}

// SetLive records how drive's rip is going. It is makemkv.Runner.OnLive.
// A job ripping several titles starts makemkvcon once per title; the run's
// first start is kept, since its progress counts all titles together.
func (a *App) SetLive(drive string, l makemkv.Live) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if prev, ok := a.live[drive]; ok && !prev.Started.IsZero() && prev.Started.Before(l.Started) {
		l.Started = prev.Started
	}
	a.live[drive] = liveEntry{Live: l, at: time.Now()}
}

// Live returns how drive's rip is going, if it reported recently.
func (a *App) Live(drive string) (makemkv.Live, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	e, ok := a.live[drive]
	if !ok || time.Since(e.at) > liveFresh {
		return makemkv.Live{}, false
	}
	return e.Live, true
}

func (a *App) endLive(drive string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.live, drive)
}

// DriveState is what drive's tray is doing, as the watcher last read it.
func (a *App) DriveState(d string) drive.State {
	if a.Trays == nil {
		return drive.Unknown
	}
	return a.Trays.State(d)
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

// Health checks what Discern needs to work: its database, makemkvcon and
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

// ConnectSelect gives the select step what it needs from the app: the
// selector matches each disc against TheDiscDB's index in use (a.Catalog;
// with none yet, it decides from the scan), and auto-import reads its mode
// at every selection, so a change on the Settings page applies from the
// next disc (decisions D60-D63).
func (a *App) ConnectSelect(s *steps.Steps) {
	s.Index = a.DiscIndex
	s.AutoImport = a.AutoImportMode
}

// AutoImportMode is the Settings page's auto-import mode, for the select
// step (Steps.AutoImport).
func (a *App) AutoImportMode(ctx context.Context) (settings.AutoImport, error) {
	st, err := a.store.Settings(ctx)
	return st.AutoImport, err
}

// TrialTally is how auto-import's trial has gone, for the dashboard.
func (a *App) TrialTally(ctx context.Context) (jobs.TrialTally, error) {
	return a.store.TrialTally(ctx)
}

// Settings returns the Settings page's values.
func (a *App) Settings(ctx context.Context) (settings.Settings, error) { return a.store.Settings(ctx) }

// SaveSettings checks and saves the Settings page. The language choice
// reaches MakeMKV at the next rip.
func (a *App) SaveSettings(ctx context.Context, st settings.Settings) error {
	if st.Languages != nil {
		if err := st.Languages.Validate(); err != nil {
			return err
		}
	}
	return a.store.SaveSettings(ctx, st)
}
