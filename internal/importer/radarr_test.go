package importer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danvanton/discern/internal/jobs"
	"github.com/danvanton/discern/internal/makemkv"
	"github.com/danvanton/discern/internal/radarr"
	"github.com/danvanton/discern/internal/radarr/radarrtest"
)

var ctx = context.Background()

type setup struct {
	fake    *radarrtest.Server
	url     string
	imp     *Radarr
	staging string // the job's staging folder, with one rip in it
	job     *jobs.Job
}

// newSetup gives a fake Radarr, an importer pointed at it, and a job whose
// staging folder holds one 4K rip of TMDb 603.
func newSetup(t *testing.T) *setup {
	t.Helper()
	root := t.TempDir()
	paths := PathMap{Local: root, Remote: "/data/staging"}
	fake, url := radarrtest.NewTest(t, paths.Local, paths.Remote)
	client, err := radarr.New(url, fake.APIKey)
	if err != nil {
		t.Fatal(err)
	}
	imp, err := NewRadarr(client, RadarrConfig{
		RootFolder: "/movies", Staging: paths,
		CommandTimeout: 5 * time.Second, PollEvery: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	staging := filepath.Join(root, "42")
	os.MkdirAll(staging, 0o755)
	os.WriteFile(filepath.Join(staging, "The Matrix_t00.mkv"), []byte("rip"), 0o644)
	job := &jobs.Job{
		ID:        42,
		CreatedAt: time.Now().Add(-time.Minute),
		Titles:    []int{0},
		Movie:     &jobs.Movie{TMDbID: 603, Title: "The Matrix", Year: 1999},
		Disc: &makemkv.Disc{Volume: "MATRIX_THE", Titles: []makemkv.Title{{
			Index:   0,
			Streams: []makemkv.Stream{{Type: "Video", VideoSize: "3840x2160"}, {Type: "Audio"}},
		}}},
	}
	return &setup{fake: fake, url: url, imp: imp, staging: staging, job: job}
}

func (s *setup) stagedFile() string { return filepath.Join(s.staging, "The Matrix_t00.mkv") }

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func TestImportAddsTheMovieUnmonitoredAndImports(t *testing.T) {
	s := newSetup(t)
	if err := s.imp.Import(ctx, s.job, s.staging); err != nil {
		t.Fatal(err)
	}

	// The add must never make Radarr search or download.
	if len(s.fake.Added()) != 1 {
		t.Fatalf("added %d movies, want 1", len(s.fake.Added()))
	}
	add := s.fake.Added()[0]
	opts, _ := add["addOptions"].(map[string]any)
	if add["monitored"] != false || opts["searchForMovie"] != false || opts["monitor"] != "none" {
		t.Errorf("movie added as %v: must be unmonitored with no search", add)
	}
	if add["rootFolderPath"] != "/movies" || add["qualityProfileId"] != float64(4) {
		t.Errorf("root folder / profile = %v / %v, want /movies / 4 (Any)", add["rootFolderPath"], add["qualityProfileId"])
	}

	// The import: a move, of Radarr's path for the file, as Remux-2160p.
	// Quality is Radarr's QualityModel (the quality plus the revision from
	// Radarr's own scan), and the folder-name release group is cleared.
	cmd := s.fake.Imports()[0]
	file := cmd["files"].([]any)[0].(map[string]any)
	model, _ := file["quality"].(map[string]any)
	quality, _ := model["quality"].(map[string]any)
	revision, _ := model["revision"].(map[string]any)
	if cmd["importMode"] != "move" || file["path"] != "/data/staging/42/The Matrix_t00.mkv" ||
		quality["name"] != "Remux-2160p" || quality["source"] != "bluray" || revision["version"] != float64(1) {
		t.Errorf("import command = %v", cmd)
	}
	if file["releaseGroup"] != "" {
		t.Errorf("release group = %v, want empty (not the staging folder's name)", file["releaseGroup"])
	}
	if langs, _ := file["languages"].([]any); len(langs) != 1 {
		t.Errorf("languages from Radarr's own scan were not kept: %v", file["languages"])
	}
	if exists(s.stagedFile()) {
		t.Error("file is still in staging")
	}
	if s.fake.BadKeys() != 0 {
		t.Errorf("%d requests went without the API key header", s.fake.BadKeys())
	}
}

func TestImportUsesAMovieAlreadyInTheLibrary(t *testing.T) {
	s := newSetup(t)
	s.fake.AddLibraryMovie(603, "The Matrix", 1999, false)
	if err := s.imp.Import(ctx, s.job, s.staging); err != nil {
		t.Fatal(err)
	}
	if len(s.fake.Added()) != 0 {
		t.Error("added a movie the library already had")
	}
}

// No silent replacements: if Radarr already has a file for the movie, nothing
// is sent and the rip stays where it is.
func TestImportRefusesToReplaceAFile(t *testing.T) {
	s := newSetup(t)
	s.fake.AddLibraryMovie(603, "The Matrix", 1999, true)
	err := s.imp.Import(ctx, s.job, s.staging)
	if err == nil || !strings.Contains(err.Error(), "already has a file") {
		t.Fatalf("err = %v, want a refusal", err)
	}
	if len(s.fake.Imports()) != 0 || !exists(s.stagedFile()) {
		t.Error("something was imported or moved anyway")
	}
}

// After a crash between Radarr finishing and the job being saved as Done,
// the engine runs Import again. It must see the work is done.
func TestImportAgainAfterItHappenedIsANoOp(t *testing.T) {
	s := newSetup(t)
	if err := s.imp.Import(ctx, s.job, s.staging); err != nil {
		t.Fatal(err)
	}
	if err := s.imp.Import(ctx, s.job, s.staging); err != nil {
		t.Fatalf("second Import: %v", err)
	}
	if len(s.fake.Imports()) != 1 {
		t.Errorf("sent %d imports, want 1", len(s.fake.Imports()))
	}
}

func TestImportRefusals(t *testing.T) {
	cases := []struct {
		name    string
		prepare func(*setup)
		dir     func(*setup) string
		wantErr string
	}{
		{"no movie", func(s *setup) { s.job.Movie = nil }, nil, "no movie"},
		{"nothing staged", func(s *setup) { os.Remove(s.stagedFile()) }, nil, "nothing to import"},
		{"two files", func(s *setup) {
			os.WriteFile(filepath.Join(s.staging, "extra_t01.mkv"), []byte("x"), 0o644)
		}, nil, "2 files"},
		{"unknown resolution", func(s *setup) { s.job.Disc.Titles[0].Streams[0].VideoSize = "" }, nil, "quality is unknown"},
		{"outside staging", nil, func(s *setup) string {
			other := t.TempDir()
			os.WriteFile(filepath.Join(other, "x.mkv"), []byte("x"), 0o644)
			return other
		}, "outside the staging folder"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newSetup(t)
			if c.prepare != nil {
				c.prepare(s)
			}
			dir := s.staging
			if c.dir != nil {
				dir = c.dir(s)
			}
			err := s.imp.Import(ctx, s.job, dir)
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("err = %v, want one containing %q", err, c.wantErr)
			}
			if len(s.fake.Imports()) != 0 || len(s.fake.Added()) != 0 {
				t.Error("sent something to Radarr before refusing")
			}
		})
	}
}

// When Radarr's side goes wrong, the error says how, and the rip is left in
// staging for a retry.
func TestImportFailuresOnRadarrsSide(t *testing.T) {
	cases := []struct {
		name    string
		b       radarrtest.Behaviour
		wantErr string
		kept    bool // the rip should still be in staging
	}{
		{"command failed", radarrtest.Behaviour{FailImport: true}, "path does not exist", true},
		{"file stayed", radarrtest.Behaviour{KeepFile: true}, "still in staging", true},
		{"wrong quality", radarrtest.Behaviour{WrongQuality: true}, "expected Remux-2160p", false},
		{"scan saw nothing", radarrtest.Behaviour{EmptyScan: true}, "check the staging path mapping", true},
		{"never finishes", radarrtest.Behaviour{NeverFinish: true}, "still started", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newSetup(t)
			s.fake.SetBehaviour(c.b)
			s.imp.cfg.CommandTimeout = 300 * time.Millisecond
			err := s.imp.Import(ctx, s.job, s.staging)
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("err = %v, want one containing %q", err, c.wantErr)
			}
			if exists(s.stagedFile()) != c.kept {
				t.Errorf("rip in staging = %v, want %v", !c.kept, c.kept)
			}
		})
	}
}

func TestWrongAPIKeyIsAClearError(t *testing.T) {
	s := newSetup(t)
	client, _ := radarr.New(s.url, "wrong")
	imp, _ := NewRadarr(client, s.imp.cfg)
	err := imp.Import(ctx, s.job, s.staging)
	var apiErr *radarr.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 401 {
		t.Fatalf("err = %v, want a 401 APIError", err)
	}
	if strings.Contains(err.Error(), "wrong") {
		t.Error("the API key appears in the error message")
	}
}

func TestIdentify(t *testing.T) {
	s := newSetup(t)
	s.fake.SetLookup(
		map[string]any{"tmdbId": 1, "title": "Spider-Man 3", "year": 2017}, // a different film first
		map[string]any{"tmdbId": 559, "title": "Spider-Man 3", "year": 2007},
	)
	s.job.Disc.Volume = "Spider-Man 3 (2007)"
	m, err := s.imp.Identify(ctx, s.job)
	if err != nil {
		t.Fatal(err)
	}
	if m == nil || m.TMDbID != 559 {
		t.Errorf("identified %+v, want the 2007 film (559)", m)
	}

	s.fake.SetLookup()
	if m, err := s.imp.Identify(ctx, s.job); err != nil || m != nil {
		t.Errorf("no results: got %+v, %v; want nil, nil (the user picks)", m, err)
	}
}

func TestQualityName(t *testing.T) {
	cases := map[string]string{
		"3840x2160": "Remux-2160p",
		"1920x1080": "Remux-1080p",
		"1920x800":  "Remux-1080p", // cropped scope film, still 1080p
		"1280x720":  "Bluray-720p",
		"720x480":   "DVD",
		"720x576":   "DVD",
		"1440x1080": "Remux-1080p", // anamorphic HD Blu-ray
		"3840x1600": "Remux-2160p", // cropped UHD
	}
	for size, want := range cases {
		j := &jobs.Job{Titles: []int{0}, Disc: &makemkv.Disc{Titles: []makemkv.Title{
			{Index: 0, Streams: []makemkv.Stream{{Type: "Video", VideoSize: size}}},
		}}}
		if got, err := QualityName(j); err != nil || got != want {
			t.Errorf("%s: got %q, %v; want %q", size, got, err, want)
		}
	}
}

func TestPathMap(t *testing.T) {
	root := t.TempDir()
	m := PathMap{Local: root, Remote: "/data/staging"}
	ok := map[string]string{
		root:                           "/data/staging",
		filepath.Join(root, "42"):      "/data/staging/42",
		filepath.Join(root, "42", "x"): "/data/staging/42/x",
	}
	for in, want := range ok {
		if got, err := m.ToRemote(in); err != nil || got != want {
			t.Errorf("ToRemote(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{filepath.Dir(root), filepath.Join(root, "..", "elsewhere"), root + "-sibling"} {
		if got, err := m.ToRemote(bad); err == nil {
			t.Errorf("ToRemote(%q) = %q, want an error", bad, got)
		}
	}
}

func TestNewRadarrRequiresItsSettings(t *testing.T) {
	client, _ := radarr.New("http://radarr:7878", "k")
	if _, err := NewRadarr(client, RadarrConfig{Staging: PathMap{"/a", "/b"}}); err == nil {
		t.Error("accepted a config without a root folder")
	}
	if _, err := NewRadarr(client, RadarrConfig{RootFolder: "/movies"}); err == nil {
		t.Error("accepted a config without a staging mapping")
	}
	for _, bad := range []string{"", "radarr:7878", "ftp://radarr"} {
		if _, err := radarr.New(bad, "k"); err == nil {
			t.Errorf("radarr.New(%q) accepted", bad)
		}
	}
	if _, err := radarr.New("http://radarr:7878", ""); err == nil {
		t.Error("radarr.New accepted an empty API key")
	}
}

// A rip that vanished from staging is not "already imported" just because
// the user had an older copy of the movie.
func TestMissingRipWithAnOlderLibraryFileIsAnError(t *testing.T) {
	s := newSetup(t)
	s.fake.AddLibraryMovie(603, "The Matrix", 1999, true) // dateAdded 2020
	os.Remove(s.stagedFile())
	err := s.imp.Import(ctx, s.job, s.staging)
	if err == nil || !strings.Contains(err.Error(), "older than this job") {
		t.Fatalf("err = %v, want the rip reported missing", err)
	}
}

// If Radarr can't see the staging folder, nothing is added to its library.
func TestFailedScanAddsNothing(t *testing.T) {
	s := newSetup(t)
	s.fake.SetBehaviour(radarrtest.Behaviour{EmptyScan: true})
	if err := s.imp.Import(ctx, s.job, s.staging); err == nil {
		t.Fatal("import succeeded with an empty scan")
	}
	if len(s.fake.Added()) != 0 {
		t.Errorf("added %d movies before finding the file", len(s.fake.Added()))
	}
}

// A retry after a timeout waits for the import already running in Radarr
// instead of starting a second one for the same file.
func TestRetryJoinsTheRunningImport(t *testing.T) {
	s := newSetup(t)
	s.fake.AddLibraryMovie(603, "The Matrix", 1999, false)
	s.fake.RunningImport("/data/staging/42/The Matrix_t00.mkv", 1)
	if err := s.imp.Import(ctx, s.job, s.staging); err != nil {
		t.Fatal(err)
	}
	if len(s.fake.Imports()) != 0 {
		t.Errorf("started %d new imports, want 0: the running one should have been reused", len(s.fake.Imports()))
	}
	if exists(s.stagedFile()) {
		t.Error("the running import didn't move the file")
	}
}

// Joining a running import needs no languages, so a file that can't be read
// for them (Radarr may be moving it) doesn't fail the retry.
func TestRetryJoinsTheRunningImportWithoutReadingLanguages(t *testing.T) {
	s := newSetup(t)
	s.fake.AddLibraryMovie(603, "The Matrix", 1999, false)
	s.fake.RunningImport("/data/staging/42/The Matrix_t00.mkv", 1)
	read := 0
	s.imp.AudioLanguages = func(context.Context, string) ([]string, error) {
		read++
		return nil, errors.New("the file is gone")
	}
	if err := s.imp.Import(ctx, s.job, s.staging); err != nil || read != 0 {
		t.Errorf("err = %v after %d language reads; want the running import joined and no read", err, read)
	}
}

func TestIdentifyLabelsThatSayNothing(t *testing.T) {
	s := newSetup(t)
	s.fake.SetLookup(map[string]any{"tmdbId": 9, "title": "Video", "year": 2001})
	s.job.Disc.Volume, s.job.Disc.Name = "DVD_VIDEO", ""
	if m, err := s.imp.Identify(ctx, s.job); err != nil || m != nil {
		t.Errorf("generic label: got %+v, %v; want nil, nil", m, err)
	}
	if s.fake.Lookups() != 0 {
		t.Errorf("searched Radarr %d times for a generic label", s.fake.Lookups())
	}

	// A generic volume label with a useful disc name uses the name.
	s.fake.SetLookup(map[string]any{"tmdbId": 603, "title": "The Matrix", "year": 1999})
	s.job.Disc.Volume, s.job.Disc.Name = "BD_ROM", "The Matrix"
	if m, err := s.imp.Identify(ctx, s.job); err != nil || m == nil || m.TMDbID != 603 {
		t.Errorf("fell back to the disc name: got %+v, %v", m, err)
	}
}

// TestRetryAfterAWronglyFiledImportChecksAgain is the homelab case: Radarr
// filed the rip as Unknown, the job failed, and a retry found the import
// already done. The retry must not call it done while it is Unknown, must
// accept whatever quality the user then sets (even one Discern wouldn't
// choose), and must not import again.
func TestRetryAfterAWronglyFiledImportChecksAgain(t *testing.T) {
	s := newSetup(t)
	s.fake.SetBehaviour(radarrtest.Behaviour{WrongQuality: true})
	err := s.imp.Import(ctx, s.job, s.staging)
	if err == nil || !strings.Contains(err.Error(), "set the quality under Manage Files") {
		t.Fatalf("first Import: err = %v, want a wrong-quality error that says how to fix it", err)
	}

	s.fake.SetFileQuality(s.job.Movie.TMDbID, "Unknown")
	if err := s.imp.Import(ctx, s.job, s.staging); err == nil || !strings.Contains(err.Error(), "quality Unknown") {
		t.Fatalf("retry with the quality Unknown: err = %v, want an error", err)
	}

	s.fake.SetFileQuality(s.job.Movie.TMDbID, "Bluray-2160p")
	if err := s.imp.Import(ctx, s.job, s.staging); err != nil {
		t.Fatalf("retry after the user set a quality in Radarr: %v", err)
	}
	if n := len(s.fake.Imports()); n != 1 {
		t.Errorf("sent %d imports, want 1: a retry must not import again", n)
	}
}

// TestIdentifyTriesTheDiscNameFirst uses two real discs from the first
// homelab runs. Their volume labels are abbreviated and find nothing in
// Radarr; their disc names find the movie with one search.
func TestIdentifyTriesTheDiscNameFirst(t *testing.T) {
	for _, d := range []struct {
		volume, name, term string
		tmdb               int
		title              string
		year               int
	}{
		{"STAR_TREK_I_TMP", "Star Trek I: The Motion Picture", "Star Trek I The Motion Picture", 152, "Star Trek: The Motion Picture", 1979},
		{"STAR_TREK_II_TWOK", "Star Trek II: The Wrath of Khan", "Star Trek II The Wrath of Khan", 154, "Star Trek II: The Wrath of Khan", 1982},
	} {
		s := newSetup(t)
		s.fake.SetLookup() // every other term finds nothing, as "Star Trek Ii Twok" did
		s.fake.SetLookupFor(d.term, map[string]any{"tmdbId": d.tmdb, "title": d.title, "year": d.year})
		s.job.Disc.Volume, s.job.Disc.Name = d.volume, d.name
		m, err := s.imp.Identify(ctx, s.job)
		if err != nil || m == nil || m.TMDbID != d.tmdb {
			t.Errorf("%s: identified %+v, %v; want TMDb %d", d.volume, m, err, d.tmdb)
		}
		if terms := s.fake.LookupTerms(); len(terms) != 1 || terms[0] != d.term {
			t.Errorf("%s: searched %q; want only the disc name, %q", d.volume, terms, d.term)
		}
	}
}

func TestIdentifyFallsBackToTheVolumeLabel(t *testing.T) {
	s := newSetup(t)
	s.fake.SetLookup()
	s.fake.SetLookupFor("The Whole Bloody Affair",
		map[string]any{"tmdbId": 414419, "title": "Kill Bill: The Whole Bloody Affair", "year": 2011})
	s.job.Disc.Volume, s.job.Disc.Name = "THE_WHOLE_BLOODY_AFFAIR_UHD_D2", "Tarantino Collection"
	m, err := s.imp.Identify(ctx, s.job)
	if err != nil || m == nil || m.TMDbID != 414419 {
		t.Fatalf("identified %+v, %v; want TMDb 414419 from the volume label", m, err)
	}
	if terms := s.fake.LookupTerms(); len(terms) != 2 || terms[0] != "Tarantino Collection" {
		t.Errorf("searched %q; want the disc name, then the volume label", terms)
	}

	// A name and label that clean to the same term are searched once.
	s = newSetup(t)
	s.fake.SetLookup()
	s.job.Disc.Volume, s.job.Disc.Name = "THE_MATRIX", "The Matrix"
	if m, err := s.imp.Identify(ctx, s.job); err != nil || m != nil {
		t.Errorf("no results: got %+v, %v; want nil, nil", m, err)
	}
	if n := len(s.fake.LookupTerms()); n != 1 {
		t.Errorf("searched %d times for one term; want 1", n)
	}
}

// The year can come from either label: here only the volume label has it.
func TestIdentifyUsesEitherLabelsYear(t *testing.T) {
	s := newSetup(t)
	s.fake.SetLookup(
		map[string]any{"tmdbId": 1, "title": "Spider-Man 3", "year": 2017},
		map[string]any{"tmdbId": 559, "title": "Spider-Man 3", "year": 2007},
	)
	s.job.Disc.Volume, s.job.Disc.Name = "Spider-Man 3 (2007)", "Spider-Man 3"
	if m, err := s.imp.Identify(ctx, s.job); err != nil || m == nil || m.TMDbID != 559 {
		t.Errorf("identified %+v, %v; want the 2007 film (559)", m, err)
	}
}

// A result whose title doesn't fit the term is not the movie, and a bonus
// disc's name is never searched.
func TestIdentifySkipsResultsThatDontFit(t *testing.T) {
	s := newSetup(t)
	s.fake.SetLookup()
	s.fake.SetLookupFor("Tarantino Collection", map[string]any{"tmdbId": 496243, "title": "Parasite", "year": 2019})
	s.fake.SetLookupFor("The Whole Bloody Affair",
		map[string]any{"tmdbId": 414419, "title": "Kill Bill: The Whole Bloody Affair", "year": 2011})
	s.job.Disc.Volume, s.job.Disc.Name = "THE_WHOLE_BLOODY_AFFAIR_UHD_D2", "Tarantino Collection"
	if m, err := s.imp.Identify(ctx, s.job); err != nil || m == nil || m.TMDbID != 414419 {
		t.Errorf("identified %+v, %v; want Kill Bill (414419), not the unfitting first hit", m, err)
	}

	s = newSetup(t)
	s.fake.SetLookup(map[string]any{"tmdbId": 603, "title": "The Matrix", "year": 1999})
	s.job.Disc.Volume, s.job.Disc.Name = "THE_MATRIX", "Special Features"
	if m, err := s.imp.Identify(ctx, s.job); err != nil || m == nil || m.TMDbID != 603 {
		t.Errorf("identified %+v, %v; want The Matrix from the volume label", m, err)
	}
	if terms := s.fake.LookupTerms(); len(terms) != 1 || terms[0] != "The Matrix" {
		t.Errorf("searched %q; a bonus disc's name must not be searched", terms)
	}
}

// A failed search for the name doesn't stop the volume label being tried;
// the error is returned only when nothing fits.
func TestIdentifyTriesTheLabelWhenTheNameSearchFails(t *testing.T) {
	s := newSetup(t)
	s.fake.SetLookup()
	s.fake.FailLookup("Star Trek II The Wrath of Khan")
	s.fake.SetLookupFor("Star Trek Ii Twok",
		map[string]any{"tmdbId": 154, "title": "Star Trek II: The Wrath of Khan", "year": 1982})
	s.job.Disc.Volume, s.job.Disc.Name = "STAR_TREK_II_TWOK", "Star Trek II: The Wrath of Khan"
	if m, err := s.imp.Identify(ctx, s.job); err != nil || m == nil || m.TMDbID != 154 {
		t.Errorf("identified %+v, %v; want 154 from the volume label", m, err)
	}

	s = newSetup(t)
	s.fake.SetLookup()
	s.fake.FailLookup("Star Trek II The Wrath of Khan")
	s.job.Disc.Volume, s.job.Disc.Name = "STAR_TREK_II_TWOK", "Star Trek II: The Wrath of Khan"
	if m, err := s.imp.Identify(ctx, s.job); err == nil || m != nil {
		t.Errorf("got %+v, %v; want the failed search's error when nothing fits", m, err)
	}
}

// Import tells Radarr the file's audio languages, as Radarr's own language
// objects, each once. A language Radarr doesn't list is left out and logged.
func TestImportSendsTheFilesAudioLanguages(t *testing.T) {
	s := newSetup(t)
	var logged strings.Builder
	s.imp.Log = slog.New(slog.NewTextHandler(&logged, nil))
	s.imp.AudioLanguages = func(context.Context, string) ([]string, error) {
		// Thai isn't in the fake Radarr's list. Bengali isn't on the Settings
		// page, but Discern can name it. nob and nor are both Norwegian.
		return []string{"eng", "ger", "tha", "ben", "nob", "nor"}, nil
	}
	if err := s.imp.Import(ctx, s.job, s.staging); err != nil {
		t.Fatal(err)
	}
	file := s.fake.Imports()[0]["files"].([]any)[0].(map[string]any)
	langs, _ := file["languages"].([]any)
	var names []string
	for _, l := range langs {
		m, _ := l.(map[string]any)
		names = append(names, fmt.Sprint(m["id"], "=", m["name"]))
	}
	if got, want := strings.Join(names, ","), "1=English,4=German,34=Bengali,15=Norwegian"; got != want {
		t.Errorf("languages sent = %s; want %s", got, want)
	}
	if !strings.Contains(logged.String(), "tha") || strings.Contains(logged.String(), "ben") {
		t.Errorf("log = %q; want the language Radarr doesn't list (tha), and only that", logged.String())
	}
}

// A file whose languages can't be read is not imported with a guess, and
// nothing has been added to Radarr by then.
func TestImportFailsWhenTheLanguagesCantBeRead(t *testing.T) {
	s := newSetup(t)
	s.imp.AudioLanguages = func(context.Context, string) ([]string, error) { return nil, errors.New("mkvmerge couldn't read it") }
	err := s.imp.Import(ctx, s.job, s.staging)
	if err == nil || len(s.fake.Imports()) != 0 || len(s.fake.Added()) != 0 {
		t.Errorf("err = %v with %d imports and %d movies added; want an error and nothing sent or added",
			err, len(s.fake.Imports()), len(s.fake.Added()))
	}
}
