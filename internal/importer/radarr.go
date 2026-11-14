package importer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/danvanton/discern/internal/identify"
	"github.com/danvanton/discern/internal/jobs"
	"github.com/danvanton/discern/internal/radarr"
)

// RadarrConfig is how Discern uses one Radarr.
type RadarrConfig struct {
	// RootFolder is the Radarr root folder new movies are added under, as
	// Radarr sees it, e.g. "/movies".
	RootFolder string
	// QualityProfile is the name of the profile new movies get. Movies are
	// added unmonitored, so it only matters for later upgrades.
	QualityProfile string
	// Staging maps Discern's staging folder to Radarr's view of it.
	Staging PathMap
	// CommandTimeout bounds one import from start to verified. Moving a
	// file is a rename when staging and library share a filesystem (the
	// design requires it), but Radarr may be busy with other work.
	CommandTimeout time.Duration
	// PollEvery is how often the import command's status is checked.
	PollEvery time.Duration
}

// Radarr imports finished rips into Radarr, and identifies discs with
// Radarr's movie lookup.
type Radarr struct {
	client *radarr.Client
	cfg    RadarrConfig
}

func NewRadarr(client *radarr.Client, cfg RadarrConfig) (*Radarr, error) {
	if cfg.RootFolder == "" {
		return nil, errors.New("radarr root folder is not set")
	}
	if cfg.Staging.Local == "" || cfg.Staging.Remote == "" {
		return nil, errors.New("radarr staging path mapping is not set")
	}
	if cfg.QualityProfile == "" {
		cfg.QualityProfile = "Any"
	}
	if cfg.CommandTimeout <= 0 {
		cfg.CommandTimeout = 30 * time.Minute
	}
	if cfg.PollEvery <= 0 {
		cfg.PollEvery = 2 * time.Second
	}
	return &Radarr{client: client, cfg: cfg}, nil
}

// Identify works out which movie the disc holds from its label, through
// Radarr's TMDb lookup: the volume label first, then MakeMKV's disc name. It
// returns nil, not an error, when the labels say nothing about the movie
// (generic labels such as DVD_VIDEO) or nothing matches: the user then picks
// the movie. A raw label is never searched, since a generic one would match
// a random film. With a year in the label, the first result from that year
// wins; otherwise the first result.
func (r *Radarr) Identify(ctx context.Context, j *jobs.Job) (*jobs.Movie, error) {
	if j.Disc == nil {
		return nil, errors.New("no scan to identify the disc from")
	}
	var label identify.Label
	for _, raw := range []string{j.Disc.Volume, j.Disc.Name} {
		if label = identify.Parse(raw); label.Term != "" {
			break
		}
	}
	term := label.Term
	if term == "" {
		return nil, nil
	}
	results, err := r.client.Lookup(ctx, term)
	if err != nil {
		return nil, err
	}
	pick := -1
	for i, m := range results {
		if m.TMDbID == 0 {
			continue
		}
		if pick < 0 {
			pick = i
		}
		if label.Year != 0 && m.Year == label.Year {
			pick = i
			break
		}
	}
	if pick < 0 {
		return nil, nil
	}
	m := results[pick]
	return &jobs.Movie{TMDbID: m.TMDbID, Title: m.Title, Year: m.Year}, nil
}

// Import hands the rip in dir to Radarr and checks that Radarr filed it.
//
// It is safe to run again after a crash, as the engine requires: if the
// staging folder is empty and the movie has a file added since the job
// started, the import already happened and Import returns nil. A file older
// than the job is someone else's: then the rip has gone missing, and that is
// an error, not a success.
//
// If a Manual Import for this file is already running in Radarr (a retry
// after a timeout), Import waits for that one instead of starting another.
//
// Before anything is sent, it refuses: a job without a movie, more than one
// file (v0.1 imports one title per movie), a movie Radarr already has a file
// for (no silent replacement), a resolution it can't name a quality for, and
// a folder outside the staging mapping.
func (r *Radarr) Import(ctx context.Context, j *jobs.Job, dir string) error {
	ctx, cancel := context.WithTimeout(ctx, r.cfg.CommandTimeout)
	defer cancel()
	if j.Movie == nil || j.Movie.TMDbID == 0 {
		return errors.New("the job has no movie to import as")
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.mkv"))
	if err != nil {
		return err
	}
	existing, err := r.client.MovieByTMDb(ctx, j.Movie.TMDbID)
	if err != nil {
		return err
	}

	switch {
	case len(files) == 0 && importedForJob(existing, j):
		// Imported before a crash or a failed check. Nothing is left to
		// move, but a Retry mustn't call an import filed as Unknown done.
		// Any known quality is accepted: the user may have set a different
		// one in Radarr on purpose.
		got, err := fileQuality(existing)
		if err != nil {
			return err
		}
		if got == "" || got == "Unknown" {
			return fmt.Errorf("radarr has %s (%d) with quality Unknown: set the quality under Manage Files in Radarr, then retry",
				existing.Title, existing.Year)
		}
		return nil
	case len(files) == 0 && existing != nil && existing.HasFile:
		return fmt.Errorf("the rip is missing from %s, and Radarr's file for %s (%d) is older than this job, so it isn't this rip; nothing was changed",
			dir, existing.Title, existing.Year)
	case len(files) == 0:
		return fmt.Errorf("nothing to import: no .mkv in %s", dir)
	case len(files) > 1:
		return fmt.Errorf("found %d files to import; this version imports one title per movie", len(files))
	case existing != nil && existing.HasFile:
		return fmt.Errorf("radarr already has a file for %s (%d); replacing it isn't supported yet, so nothing was changed",
			existing.Title, existing.Year)
	}
	file := files[0]

	qualityName, err := QualityName(j)
	if err != nil {
		return err
	}
	remoteDir, err := r.cfg.Staging.ToRemote(dir)
	if err != nil {
		return err
	}
	quality, err := r.client.QualityNamed(ctx, qualityName)
	if err != nil {
		return err
	}
	// Scan before adding the movie: if Radarr can't see the file (a wrong
	// path mapping, say), nothing has been added to its library.
	entry, err := r.scanEntry(ctx, remoteDir, filepath.Base(file))
	if err != nil {
		return err
	}

	cmdID, err := r.runningImport(ctx, entry)
	if err != nil {
		return err
	}
	movie := existing
	if cmdID == 0 {
		if movie == nil {
			if movie, err = r.addMovie(ctx, j.Movie); err != nil {
				return err
			}
		}
		entry["movieId"] = movie.ID
		entry["quality"] = radarr.QualityModel(quality, entry["quality"])
		// Radarr guesses a release group from the folder name ("staging");
		// a rip from a disc has none.
		entry["releaseGroup"] = ""
		if _, ok := entry["languages"]; !ok {
			entry["languages"] = []any{}
		}
		cmd, err := r.client.StartManualImport(ctx, []map[string]any{entry})
		if err != nil {
			return err
		}
		cmdID = cmd.ID
	} else if movie == nil {
		// The running import was started by an earlier attempt, which added
		// the movie; look it up again.
		if movie, err = r.client.MovieByTMDb(ctx, j.Movie.TMDbID); err != nil {
			return err
		}
		if movie == nil {
			return fmt.Errorf("a Radarr import for %s is running, but the movie isn't in Radarr", filepath.Base(file))
		}
	}
	done, err := r.client.WaitForCommand(ctx, cmdID, r.cfg.PollEvery)
	if err != nil {
		return err
	}
	if done.Status != "completed" {
		return fmt.Errorf("radarr's import ended %s: %s%s", done.Status, done.Message, suffix(done.Exception))
	}
	return r.verify(ctx, movie.ID, file, qualityName)
}

// importedForJob reports whether the movie's file arrived after the job
// started, so it can be this job's rip.
func importedForJob(m *radarr.Movie, j *jobs.Job) bool {
	return m != nil && m.HasFile && m.MovieFile != nil && !m.MovieFile.DateAdded.IsZero() &&
		!m.MovieFile.DateAdded.Before(j.CreatedAt)
}

// runningImport returns the id of a Manual Import already running in Radarr
// for the scanned file, or 0.
func (r *Radarr) runningImport(ctx context.Context, entry map[string]any) (int, error) {
	remote, _ := entry["path"].(string)
	cmds, err := r.client.Commands(ctx)
	if err != nil {
		return 0, err
	}
	for _, c := range cmds {
		if !c.Done() && c.ImportsPath(remote) {
			return c.ID, nil
		}
	}
	return 0, nil
}

// addMovie adds the job's movie to Radarr, unmonitored and without a search.
func (r *Radarr) addMovie(ctx context.Context, m *jobs.Movie) (*radarr.Movie, error) {
	profiles, err := r.client.QualityProfiles(ctx)
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(profiles, func(p radarr.QualityProfile) bool { return p.Name == r.cfg.QualityProfile })
	if i < 0 {
		names := make([]string, len(profiles))
		for k, p := range profiles {
			names[k] = p.Name
		}
		return nil, fmt.Errorf("radarr has no quality profile named %q (it has: %s)", r.cfg.QualityProfile, strings.Join(names, ", "))
	}
	return r.client.AddMovie(ctx, radarr.NewMovie{
		TMDbID:           m.TMDbID,
		Title:            m.Title,
		Year:             m.Year,
		QualityProfileID: profiles[i].ID,
		RootFolderPath:   r.cfg.RootFolder,
	})
}

// scanEntry asks Radarr to scan the job's staging folder and returns its
// entry for the file. Using Radarr's own entry, rather than building one,
// means the path and languages are what Radarr itself read.
func (r *Radarr) scanEntry(ctx context.Context, remoteDir, name string) (map[string]any, error) {
	entries, err := r.client.ManualImportScan(ctx, remoteDir)
	if err != nil {
		return nil, err
	}
	var seen []string
	for _, e := range entries {
		p, _ := e["path"].(string)
		if path.Base(p) == name {
			return e, nil
		}
		seen = append(seen, path.Base(p))
	}
	if len(seen) == 0 {
		return nil, fmt.Errorf("radarr's scan of %s found nothing; can Radarr see that folder? (check the staging path mapping)", remoteDir)
	}
	return nil, fmt.Errorf("radarr's scan of %s has no %q (it saw: %s)", remoteDir, name, strings.Join(seen, ", "))
}

// verify checks the import really happened: Radarr says the movie has a file
// of the expected quality, and the file has left the staging folder.
func (r *Radarr) verify(ctx context.Context, movieID int, staged, qualityName string) error {
	m, err := r.client.Movie(ctx, movieID)
	if err != nil {
		return err
	}
	got, err := fileQuality(m)
	if err != nil {
		return err
	}
	if got != qualityName {
		return fmt.Errorf("radarr filed %s (%d) as %s, expected %s: set the quality under Manage Files in Radarr, then retry",
			m.Title, m.Year, got, qualityName)
	}
	if _, err := os.Stat(staged); err == nil {
		return fmt.Errorf("radarr reported the import complete, but %s is still in staging", staged)
	}
	return nil
}

// fileQuality is the quality Radarr filed the movie's file with. A wrong one
// is not fixed here: the user sets it in Radarr, then retries.
func fileQuality(m *radarr.Movie) (string, error) {
	if !m.HasFile || m.MovieFile == nil {
		return "", fmt.Errorf("radarr reported the import complete, but %s (%d) has no file", m.Title, m.Year)
	}
	return m.MovieFile.Quality.Quality.Name, nil
}

// QualityName is the Radarr quality for the job's rip, from the chosen
// title's video size: the higher of what the width and the height say. Width
// catches films cropped to 2.39:1 and stored as 1920x800; height catches
// anamorphic 1440x1080 Blu-rays. Both are 1080p. Discs are ripped without
// re-encoding, so Blu-ray rips are remuxes.
func QualityName(j *jobs.Job) (string, error) {
	if j.Disc == nil || len(j.Titles) == 0 {
		return "", errors.New("no scanned title to take the quality from")
	}
	for _, t := range j.Disc.Titles {
		if t.Index != j.Titles[0] {
			continue
		}
		for _, s := range t.Streams {
			if s.Type != "Video" || s.VideoSize == "" {
				continue
			}
			ws, hs, ok := strings.Cut(s.VideoSize, "x")
			w, errW := strconv.Atoi(ws)
			h, errH := strconv.Atoi(hs)
			if !ok || errW != nil || errH != nil || w <= 0 || h <= 0 {
				break
			}
			switch grade := max(gradeOf(w, 3800, 1900, 1270), gradeOf(h, 2000, 1000, 700)); grade {
			case 3:
				return "Remux-2160p", nil
			case 2:
				return "Remux-1080p", nil
			case 1:
				return "Bluray-720p", nil
			default:
				return "DVD", nil
			}
		}
		return "", fmt.Errorf("title %d has no readable video size, so its quality is unknown; not importing", t.Index)
	}
	return "", fmt.Errorf("title %d is not in the scan", j.Titles[0])
}

// gradeOf places a dimension against the 2160p, 1080p and 720p thresholds:
// 3, 2, 1, or 0 for standard definition.
func gradeOf(v, uhd, fhd, hd int) int {
	switch {
	case v >= uhd:
		return 3
	case v >= fhd:
		return 2
	case v >= hd:
		return 1
	}
	return 0
}

func suffix(s string) string {
	if s == "" {
		return ""
	}
	return " (" + s + ")"
}
