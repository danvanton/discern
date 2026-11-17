// Package steps is the real implementation of jobs.Steps: MakeMKV for the
// disc, Radarr for identifying and importing, the filesystem for the rest.
// It connects the parts; the decisions live in the parts themselves.
package steps

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/danvanton/discern/internal/discdb"
	"github.com/danvanton/discern/internal/importer"
	"github.com/danvanton/discern/internal/jobs"
	"github.com/danvanton/discern/internal/makemkv"
	"github.com/danvanton/discern/internal/settings"
)

// Steps runs each job step against MakeMKV and Radarr.
type Steps struct {
	runner *makemkv.Runner
	radarr *importer.Radarr

	// Tray, if set, tells CheckDisc whether a drive's tray has changed since
	// a job's scan (drive.Tracker does).
	Tray interface {
		Generation(drive string) (uint64, bool)
	}

	// An incomplete scan (makemkv.IncompleteScanError) is tried again after
	// ScanRetryDelay, up to ScanAttempts scans in all: the usual cause is a
	// drive still spinning up. A damaged disc fails every attempt.
	ScanAttempts   int
	ScanRetryDelay time.Duration

	// WaitReady, if set, is called before every scan and returns once the
	// drive has a readable disc (drive.WaitReady), so a disc still spinning
	// up isn't scanned. Its error ends the scan.
	WaitReady func(ctx context.Context, drive string) error

	// BeforeRip, if set, runs before a job's titles are ripped: the app uses
	// it to apply the language settings to MakeMKV. Its error stops the rip.
	BeforeRip func(ctx context.Context, j *jobs.Job) error

	// Diagnostics, if set, is the folder where a stalled or hung rip's
	// evidence is saved, one folder per verdict (decision D37).
	Diagnostics string
	// Secrets are removed from a diagnostics bundle before it is written:
	// the MakeMKV key, which a bundle shared in a bug report mustn't carry.
	Secrets []string
	// Trimmed, if set, reports whether the job's rip keeps only chosen
	// languages, so Verify expects smaller files (app.LanguagesChosen).
	Trimmed func(ctx context.Context, j *jobs.Job) (bool, error)
	// Tracks, if set, reads a ripped file's tracks for Verify (Mkvmerge).
	Tracks func(ctx context.Context, file string) (FileTracks, error)
	// Index, if set, returns TheDiscDB's index in use, or nil when there is
	// none yet (App.DiscIndex). Select matches the disc against it.
	Index func() *discdb.Index
	// AutoImport, if set, returns the Settings page's auto-import mode
	// (decision D63). Unset, every disc waits for the user.
	AutoImport func(ctx context.Context) (settings.AutoImport, error)
	// HasMovie reports whether Radarr already has a file for the movie
	// (decision D61). New sets it to Radarr's; tests replace it.
	HasMovie func(ctx context.Context, tmdbID int) (bool, error)

	info func(ctx context.Context, drive string) (*makemkv.Disc, error) // runner.Info; tests replace it

	mu      sync.Mutex
	scanned map[int64]uint64 // job -> the drive's tray generation at its scan
}

var _ jobs.Steps = (*Steps)(nil)

func New(runner *makemkv.Runner, radarr *importer.Radarr) *Steps {
	s := &Steps{
		runner: runner, radarr: radarr, scanned: map[int64]uint64{},
		ScanAttempts: 3, ScanRetryDelay: 30 * time.Second,
		info: runner.Info,
	}
	if radarr != nil {
		s.HasMovie = radarr.HasFile
	}
	return s
}

// scanDisc scans the drive, retrying an incomplete scan.
func (s *Steps) scanDisc(ctx context.Context, drive string) (*makemkv.Disc, error) {
	for attempt := 1; ; attempt++ {
		if s.WaitReady != nil {
			if err := s.WaitReady(ctx, drive); err != nil {
				return nil, err
			}
		}
		d, err := s.info(ctx, drive)
		var inc *makemkv.IncompleteScanError
		if !errors.As(err, &inc) {
			return d, err
		}
		if attempt >= s.ScanAttempts {
			return nil, fmt.Errorf("after %d scans: %w", attempt, err)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(s.ScanRetryDelay):
		}
	}
}

func (s *Steps) Scan(ctx context.Context, j *jobs.Job) (*makemkv.Disc, error) {
	gen, known := s.trayGeneration(j.Drive)
	d, err := s.scanDisc(ctx, j.Drive)
	if err == nil && known {
		s.mu.Lock()
		s.scanned[j.ID] = gen
		s.mu.Unlock()
	}
	return d, err
}

// CheckDisc confirms the drive still holds the job's disc.
//
// If the tray hasn't changed since the scan (the drive watcher counts every
// change), the disc can't have been swapped, and nothing is read. Otherwise
// (a tray change, a restart since the scan, or a system where drives can't be
// watched), the drive is rescanned and compared with the job's scan. A rescan
// can take minutes on a UHD disc, but it is the only reliable test: many discs
// share a volume label, so the label alone can't tell two discs apart.
func (s *Steps) CheckDisc(ctx context.Context, j *jobs.Job) error {
	gen, known := s.trayGeneration(j.Drive)
	s.mu.Lock()
	at, scanned := s.scanned[j.ID]
	s.mu.Unlock()
	if known && scanned && at == gen {
		return nil
	}
	now, err := s.scanDisc(ctx, j.Drive)
	if err != nil {
		return err
	}
	if !SameDisc(j.Disc, now) {
		return jobs.ErrDiscChanged
	}
	if known {
		s.mu.Lock()
		s.scanned[j.ID] = gen
		s.mu.Unlock()
	}
	return nil
}

func (s *Steps) trayGeneration(drive string) (uint64, bool) {
	if s.Tray == nil {
		return 0, false
	}
	return s.Tray.Generation(drive)
}

func (s *Steps) Identify(ctx context.Context, j *jobs.Job) (*jobs.Movie, error) {
	return s.radarr.Identify(ctx, j)
}

// Rip saves each chosen title. makemkvcon needs an empty folder per rip (the
// watchdog measures everything in it), so each title is ripped into a
// subfolder and its file then moved up into dir, where Verify and Import
// look. Progress is reported for all titles together, weighted by size.
func (s *Steps) Rip(ctx context.Context, j *jobs.Job, dir string, progress func(float64)) error {
	if s.BeforeRip != nil {
		if err := s.BeforeRip(ctx, j); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	sizes := titleSizes(j)
	var total, done int64
	for _, t := range j.Titles {
		total += sizes[t]
	}
	for _, t := range j.Titles {
		sub := filepath.Join(dir, fmt.Sprintf(".rip-t%02d", t))
		size := sizes[t]
		onProgress := func(p makemkv.Progress) {
			if progress == nil || p.Max <= 0 || total <= 0 {
				return
			}
			frac := float64(p.Total) / float64(p.Max)
			progress((float64(done) + frac*float64(size)) / float64(total))
		}
		res, err := s.runner.Rip(ctx, j.Drive, t, sub, onProgress)
		if err != nil {
			return fmt.Errorf("title %d: %w", t, s.saveDiagnostics(j, err))
		}
		for _, f := range res.OutputFiles {
			if err := os.Rename(f, filepath.Join(dir, filepath.Base(f))); err != nil {
				return err
			}
		}
		if err := os.Remove(sub); err != nil {
			return err
		}
		done += size
	}
	return nil
}

func (s *Steps) Import(ctx context.Context, j *jobs.Job, dir string) error {
	return s.radarr.Import(ctx, j, dir)
}

func titleSizes(j *jobs.Job) map[int]int64 {
	sizes := map[int]int64{}
	if j.Disc != nil {
		for _, t := range j.Disc.Titles {
			sizes[t.Index] = t.SizeBytes
		}
	}
	return sizes
}
