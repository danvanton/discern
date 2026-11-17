package steps

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/danvanton/discern/internal/settings"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/danvanton/discern/internal/discdb"
	"github.com/danvanton/discern/internal/jobs"
	"github.com/danvanton/discern/internal/makemkv"
	"github.com/danvanton/discern/internal/makemkv/fake"
)

// TestMain lets the test binary play makemkvcon (see internal/makemkv).
func TestMain(m *testing.M) {
	if os.Getenv(fake.EnvVar) != "" {
		os.Exit(fake.Main(os.Args[1:], os.Stdout, os.Stderr))
	}
	os.Exit(m.Run())
}

var ctx = context.Background()

func titles(ts ...makemkv.Title) *makemkv.Disc { return &makemkv.Disc{Volume: "X", Titles: ts} }

// Select hands the selector TheDiscDB's index when there is one; without
// one it decides from the scan alone, and never automatically.
func TestSelectUsesTheIndexInUse(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "makemkv", "scan-star-trek-iii-uhd.json"))
	if err != nil {
		t.Fatal(err)
	}
	var d makemkv.Disc
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatal(err)
	}
	ix, _, err := discdb.Build(filepath.Join("..", "..", "testdata", "discdb"), "test", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var index *discdb.Index // none yet
	s := &Steps{Index: func() *discdb.Index { return index }}
	j := &jobs.Job{Disc: &d}

	sel, err := s.Select(ctx, j)
	if err != nil || sel.Confidence == jobs.High || slices.ContainsFunc(sel.Reasons, func(r string) bool {
		return strings.Contains(r, "TheDiscDB")
	}) {
		t.Errorf("without an index: %+v, %v", sel, err)
	}
	index = ix
	sel, err = s.Select(ctx, j)
	if err != nil || sel.Confidence != jobs.High || !slices.Equal(sel.Titles, []int{29}) || sel.Auto {
		t.Errorf("with the index: %s %v auto %v, %v", sel.Confidence, sel.Titles, sel.Auto, err)
	}
}

// Auto-import, by mode (decisions D60-D63): off changes nothing; a sure
// choice is Auto when on and Trial on trial; Radarr having the movie, or
// any doubt (no movie from the name, settings or Radarr that can't be
// read), leaves the job waiting with the reason.
func TestSelectAppliesAutoImport(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "makemkv", "scan-star-trek-iii-uhd.json"))
	if err != nil {
		t.Fatal(err)
	}
	var d makemkv.Disc
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatal(err)
	}
	ix, _, err := discdb.Build(filepath.Join("..", "..", "testdata", "discdb"), "test", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	spock := &jobs.Movie{TMDbID: 157, Title: "Star Trek III: The Search for Spock", Year: 1984}
	notHad := func(context.Context, int) (bool, error) { return false, nil }
	for _, c := range []struct {
		name        string
		mode        settings.AutoImport
		modeErr     error
		movie       *jobs.Movie
		has         func(context.Context, int) (bool, error)
		auto, trial bool
		wait        string
	}{
		{name: "off", mode: settings.AutoOff, movie: spock, has: notHad},
		{name: "on", mode: settings.AutoOn, movie: spock, has: notHad, auto: true},
		{name: "trial", mode: settings.AutoTrial, movie: spock, has: notHad, trial: true},
		{name: "in Radarr", mode: settings.AutoOn, movie: spock, wait: "Radarr already has Star Trek III: The Search for Spock (1984)",
			has: func(context.Context, int) (bool, error) { return true, nil }},
		{name: "Radarr down", mode: settings.AutoOn, movie: spock, wait: "Radarr couldn't be asked",
			has: func(context.Context, int) (bool, error) { return false, errors.New("connection refused") }},
		{name: "no Radarr", mode: settings.AutoOn, movie: spock, wait: "Radarr can't be asked"},
		{name: "no movie from the name", mode: settings.AutoOn, has: notHad, wait: "found no movie"},
		{name: "settings unreadable", modeErr: errors.New("bad row"), movie: spock, has: notHad, wait: "can't be read (bad row)"},
		{name: "unknown mode", mode: "always", movie: spock, has: notHad, wait: "can't be read (auto-import \"always\" isn't off, trial or on)"},
	} {
		s := &Steps{
			Index:      func() *discdb.Index { return ix },
			AutoImport: func(context.Context) (settings.AutoImport, error) { return c.mode, c.modeErr },
			HasMovie:   c.has,
		}
		sel, err := s.Select(ctx, &jobs.Job{Disc: &d, Movie: c.movie})
		if err != nil {
			t.Fatal(err)
		}
		if sel.Auto != c.auto || sel.Trial != c.trial || !strings.Contains(sel.Wait, c.wait) || (c.wait == "") != (sel.Wait == "") {
			t.Errorf("%s: auto %v, trial %v, wait %q", c.name, sel.Auto, sel.Trial, sel.Wait)
		}
	}
}

func TestSameDisc(t *testing.T) {
	a := titles(makemkv.Title{Index: 0, SourceFile: "00800.mpls", Duration: time.Hour, SizeBytes: 100})
	same := titles(makemkv.Title{Index: 0, SourceFile: "00800.mpls", Duration: time.Hour, SizeBytes: 100})
	if !SameDisc(a, same) {
		t.Error("identical scans differ")
	}
	changes := map[string]*makemkv.Disc{
		"label":       {Volume: "Y", Titles: a.Titles},
		"title count": titles(),
		"playlist":    titles(makemkv.Title{Index: 0, SourceFile: "00801.mpls", Duration: time.Hour, SizeBytes: 100}),
		"length":      titles(makemkv.Title{Index: 0, SourceFile: "00800.mpls", Duration: 2 * time.Hour, SizeBytes: 100}),
		"size":        titles(makemkv.Title{Index: 0, SourceFile: "00800.mpls", Duration: time.Hour, SizeBytes: 101}),
	}
	for what, b := range changes {
		if SameDisc(a, b) {
			t.Errorf("a different %s counted as the same disc", what)
		}
	}
	if SameDisc(nil, a) || SameDisc(a, nil) {
		t.Error("a missing scan counted as the same disc")
	}
}

func writeFile(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyDir(t *testing.T) {
	disc := titles(
		makemkv.Title{Index: 0, SizeBytes: 1000, OutputFile: "Movie_t00.mkv"},
		makemkv.Title{Index: 3, SizeBytes: 500, OutputFile: "Movie_t03.mkv"},
	)
	cases := []struct {
		name    string
		files   map[string]int
		titles  []int
		wantErr string // "" for success
	}{
		{"complete rip", map[string]int{"Movie_t00.mkv": 950}, []int{0}, ""},
		{"two titles", map[string]int{"Movie_t00.mkv": 950, "Movie_t03.mkv": 480}, []int{0, 3}, ""},
		{"stopped early (the Kill Bill case)", map[string]int{"Movie_t00.mkv": 9}, []int{0}, "1% of the size"},
		{"far too big", map[string]int{"Movie_t00.mkv": 2000}, []int{0}, "200% of the size"},
		{"empty file", map[string]int{"Movie_t00.mkv": 0}, []int{0}, "empty"},
		{"missing file", map[string]int{}, []int{0}, "0 files for 1"},
		{"wrong file", map[string]int{"Other_t05.mkv": 950}, []int{0}, "no file for title 0"},
		{"extra file", map[string]int{"Movie_t00.mkv": 950, "Movie_t03.mkv": 480}, []int{0}, "2 files for 1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, size := range c.files {
				writeFile(t, filepath.Join(dir, name), size)
			}
			err := VerifyDir(&jobs.Job{Disc: disc, Titles: c.titles}, dir)
			if c.wantErr == "" && err != nil {
				t.Fatalf("err = %v, want success", err)
			}
			if c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)) {
				t.Fatalf("err = %v, want one containing %q", err, c.wantErr)
			}
		})
	}
}

// fakeRunner is a makemkv.Runner whose makemkvcon is this test binary
// playing the fake.
func fakeRunner(t *testing.T, sc fake.Scenario) *makemkv.Runner {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(sc)
	r := makemkv.NewRunner(exe)
	r.Env = []string{fake.EnvVar + "=" + string(raw)}
	return r
}

func TestRipMovesEachTitleIntoTheFolderAndReportsProgress(t *testing.T) {
	r := fakeRunner(t, fake.Scenario{Mkv: fake.Mkv{TitleName: "Movie", TotalBytes: 256 << 10, ChunkBytes: 16 << 10}})
	s := New(r, nil)
	j := &jobs.Job{Drive: "/dev/sr0", Titles: []int{0, 3}, Disc: titles(
		makemkv.Title{Index: 0, SizeBytes: 256 << 10, OutputFile: "Movie_t00.mkv"},
		makemkv.Title{Index: 3, SizeBytes: 256 << 10, OutputFile: "Movie_t03.mkv"},
	)}
	dir := filepath.Join(t.TempDir(), "job")
	var last float64
	var backwards bool
	err := s.Rip(ctx, j, dir, func(p float64) {
		if p < last {
			backwards = true
		}
		last = p
	})
	if err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if strings.Join(names, ",") != "Movie_t00.mkv,Movie_t03.mkv" {
		t.Errorf("folder holds %v, want the two rips and nothing else", names)
	}
	if backwards || last < 0.99 {
		t.Errorf("progress ended at %v (went backwards: %v), want it to rise to 1", last, backwards)
	}
	if err := VerifyDir(j, dir); err != nil {
		t.Errorf("the rip doesn't verify: %v", err)
	}
}

func TestRipNamesTheTitleThatFailed(t *testing.T) {
	r := fakeRunner(t, fake.Scenario{Mkv: fake.Mkv{TotalBytes: 1 << 10, ExitCode: 3}})
	j := &jobs.Job{Drive: "/dev/sr0", Titles: []int{4}, Disc: titles(makemkv.Title{Index: 4})}
	err := New(r, nil).Rip(ctx, j, t.TempDir(), nil)
	var ee *makemkv.ExitError
	if !errors.As(err, &ee) || !strings.Contains(err.Error(), "title 4") {
		t.Fatalf("err = %v, want an ExitError naming title 4", err)
	}
}

func TestCheckDiscComparesAFreshScan(t *testing.T) {
	scan := "../../testdata/makemkv/info-sample-movie.txt"
	r := fakeRunner(t, fake.Scenario{Info: fake.Info{File: abs(t, scan)}})
	s := New(r, nil)
	disc, err := s.Scan(ctx, &jobs.Job{Drive: "/dev/sr0"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CheckDisc(ctx, &jobs.Job{Drive: "/dev/sr0", Disc: disc}); err != nil {
		t.Errorf("same disc: %v", err)
	}
	other := *disc
	other.Volume = "SOMETHING_ELSE"
	if err := s.CheckDisc(ctx, &jobs.Job{Drive: "/dev/sr0", Disc: &other}); !errors.Is(err, jobs.ErrDiscChanged) {
		t.Errorf("different disc: err = %v, want ErrDiscChanged", err)
	}
}

func abs(t *testing.T, p string) string {
	t.Helper()
	a, err := filepath.Abs(p)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// fakeTray reports a tray generation the test controls.
type fakeTray struct{ gen uint64 }

func (f *fakeTray) Generation(string) (uint64, bool) { return f.gen, true }

// With an unchanged tray, CheckDisc trusts the scan and reads nothing; after
// a tray change it rescans.
func TestCheckDiscSkipsTheRescanWhenTheTrayHasNotChanged(t *testing.T) {
	scanFile := filepath.Join(t.TempDir(), "scan.txt")
	data, _ := os.ReadFile("../../testdata/makemkv/info-sample-movie.txt")
	os.WriteFile(scanFile, data, 0o644)
	r := fakeRunner(t, fake.Scenario{Info: fake.Info{File: scanFile}})
	s := New(r, nil)
	tray := &fakeTray{gen: 7}
	s.Tray = tray

	j := &jobs.Job{ID: 1, Drive: "/dev/sr0"}
	disc, err := s.Scan(ctx, j)
	if err != nil {
		t.Fatal(err)
	}
	j.Disc = disc

	// Make any further scan fail: if CheckDisc scanned, it would error.
	os.Remove(scanFile)
	if err := s.CheckDisc(ctx, j); err != nil {
		t.Fatalf("unchanged tray: %v (it should not have rescanned)", err)
	}
	tray.gen = 8 // the tray opened and closed
	if err := s.CheckDisc(ctx, j); err == nil {
		t.Error("after a tray change CheckDisc must rescan (and here fail, as the scan file is gone)")
	}
}

// scans returns an info seam that plays results in order and counts calls.
func scans(results ...error) (func(context.Context, string) (*makemkv.Disc, error), *int) {
	calls := 0
	return func(context.Context, string) (*makemkv.Disc, error) {
		err := results[min(calls, len(results)-1)]
		calls++
		if err != nil {
			return nil, err
		}
		return &makemkv.Disc{Volume: "DISC"}, nil
	}, &calls
}

var notReady = &makemkv.IncompleteScanError{ReadErrors: 1204, Skipped: 35, First: "Error 'Scsi error - NOT READY'"}

func TestScanRetriesAnIncompleteScan(t *testing.T) {
	s := New(nil, nil)
	s.ScanRetryDelay = time.Millisecond
	var calls *int
	s.info, calls = scans(notReady, notReady, nil)
	d, err := s.Scan(context.Background(), &jobs.Job{Drive: "/dev/sr0"})
	if err != nil || d == nil || *calls != 3 {
		t.Fatalf("Scan = %v, %v after %d scans; want the third scan's disc", d, err, *calls)
	}
}

func TestScanGivesUpAfterTheLastAttempt(t *testing.T) {
	s := New(nil, nil)
	s.ScanRetryDelay = time.Millisecond
	var calls *int
	s.info, calls = scans(notReady)
	_, err := s.Scan(context.Background(), &jobs.Job{Drive: "/dev/sr0"})
	var inc *makemkv.IncompleteScanError
	if !errors.As(err, &inc) || *calls != 3 || !strings.Contains(err.Error(), "after 3 scans") {
		t.Fatalf("err = %v after %d scans; want an IncompleteScanError after 3", err, *calls)
	}
}

func TestScanDoesNotRetryOtherErrors(t *testing.T) {
	s := New(nil, nil)
	s.ScanRetryDelay = time.Millisecond
	var calls *int
	s.info, calls = scans(errors.New("makemkvcon exited with status 1"))
	if _, err := s.Scan(context.Background(), &jobs.Job{Drive: "/dev/sr0"}); err == nil || *calls != 1 {
		t.Fatalf("err = %v after %d scans; want the error after 1", err, *calls)
	}
}

func TestScanStopsWaitingOnShutdown(t *testing.T) {
	s := New(nil, nil)
	s.ScanRetryDelay = time.Hour
	s.info, _ = scans(notReady)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	if _, err := s.Scan(ctx, &jobs.Job{Drive: "/dev/sr0"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v; want context.Canceled, so the job counts as interrupted", err)
	}
}

func TestCheckDiscRetriesAnIncompleteRescan(t *testing.T) {
	s := New(nil, nil)
	s.ScanRetryDelay = time.Millisecond
	var calls *int
	s.info, calls = scans(notReady, nil)
	j := &jobs.Job{ID: 9, Drive: "/dev/sr0", Disc: &makemkv.Disc{Volume: "DISC"}}
	if err := s.CheckDisc(context.Background(), j); err != nil || *calls != 2 {
		t.Fatalf("CheckDisc = %v after %d scans; want nil after 2", err, *calls)
	}
}

func TestScanWaitsForTheDriveBeforeEveryAttempt(t *testing.T) {
	s := New(nil, nil)
	s.ScanRetryDelay = time.Millisecond
	waits := 0
	s.WaitReady = func(context.Context, string) error { waits++; return nil }
	s.info, _ = scans(notReady, nil)
	if _, err := s.Scan(context.Background(), &jobs.Job{Drive: "/dev/sr0"}); err != nil || waits != 2 {
		t.Fatalf("Scan = %v with %d waits; want nil with one wait per scan (2)", err, waits)
	}

	s.WaitReady = func(context.Context, string) error { return errors.New("/dev/sr0 has no readable disc (tray open)") }
	var calls *int
	s.info, calls = scans(nil)
	if _, err := s.Scan(context.Background(), &jobs.Job{Drive: "/dev/sr0"}); err == nil || *calls != 0 {
		t.Fatalf("err = %v after %d scans; want the drive's error and no scan", err, *calls)
	}
}

// With chosen languages the file is smaller by the tracks left out, so the
// size floor is lower; an early stop is still far below it.
func TestVerifyAllowsSmallerFilesWhenLanguagesTrimThem(t *testing.T) {
	disc := titles(makemkv.Title{Index: 0, SizeBytes: 1000, OutputFile: "Movie_t00.mkv"})
	j := &jobs.Job{Disc: disc, Titles: []int{0}}
	for size, c := range map[int]struct {
		trimmed bool
		wantErr string
	}{
		520: {false, "52% of the size"}, // five lossless tracks cut to one
		521: {true, ""},
		9:   {true, "1% of the size"}, // the Kill Bill stall
	} {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "Movie_t00.mkv"), size)
		s := New(nil, nil)
		s.Trimmed = func(context.Context, *jobs.Job) (bool, error) { return c.trimmed, nil }
		err := s.Verify(context.Background(), j, dir)
		if c.wantErr == "" && err != nil || c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)) {
			t.Errorf("%d bytes, trimmed %v: err = %v; want %q", size, c.trimmed, err, c.wantErr)
		}
	}

	// A settings read that fails is an error, not the default floor.
	s := New(nil, nil)
	s.Trimmed = func(context.Context, *jobs.Job) (bool, error) { return false, errors.New("database is closed") }
	if err := s.Verify(context.Background(), j, t.TempDir()); err == nil {
		t.Error("Verify ignored a failed settings read")
	}
}

// Verify reads the finished file's tracks: a rip without audio, from a title
// that has audio, is not imported.
func TestVerifyChecksTheFilesTracks(t *testing.T) {
	audio := []makemkv.Stream{{Type: "Video"}, {Type: "Audio", Lang: "jpn"}}
	startrek3 := []makemkv.Stream{{Type: "Video"},
		{Type: "Audio", Lang: "eng", Codec: "TrueHD"}, {Type: "Audio", Lang: "eng", Codec: "AC3"},
		{Type: "Audio", Lang: "deu", Codec: "TrueHD"}, {Type: "Audio", Lang: "deu", Codec: "AC3"}}
	for name, c := range map[string]struct {
		streams []makemkv.Stream
		got     FileTracks
		readErr error
		wantErr string
	}{
		"video and audio":              {audio, FileTracks{Video: 1, Audio: 2}, nil, ""},
		"silent rip":                   {audio, FileTracks{Video: 1}, nil, "has no audio track"},
		"title without audio":          {[]makemkv.Stream{{Type: "Video"}}, FileTracks{Video: 1}, nil, ""},
		"no video":                     {audio, FileTracks{Audio: 1}, nil, "has no video track"},
		"not a file mkvmerge can read": {audio, FileTracks{}, errors.New("mkvmerge doesn't recognise it"), "doesn't recognise"}, // kind "verify": the check is retried
		// Star Trek III, v0.1.3: TrueHD 7.1 and its AC-3 copy on the disc,
		// only the copy in the file.
		"lossless track lost": {startrek3, FileTracks{Video: 1, Audio: 1, AudioLanguages: []string{"eng"}}, nil,
			"kept English audio but not its lossless track"},
		"lossless track kept": {startrek3, FileTracks{Video: 1, Audio: 2, AudioLanguages: []string{"eng"}, LosslessLanguages: []string{"eng"}}, nil, ""},
		// German was left out on purpose: its lossless track isn't missed.
		"language left out": {startrek3, FileTracks{Video: 1, Audio: 2, AudioLanguages: []string{"eng"}, LosslessLanguages: []string{"eng"}}, nil, ""},
		// The scan's long codec name is enough to know.
		"lossless by long name": {[]makemkv.Stream{{Type: "Video"}, {Type: "Audio", Lang: "fra", Codec: "DTS-HD",
			Attrs: map[makemkv.Attr]string{makemkv.AttrCodecLong: "DTS-HD Master Audio"}}},
			FileTracks{Video: 1, Audio: 1, AudioLanguages: []string{"fre"}}, nil, "kept French audio but not its lossless track"},
	} {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "Movie_t00.mkv"), 950)
		j := &jobs.Job{Titles: []int{0}, Disc: titles(makemkv.Title{Index: 0, SizeBytes: 1000, OutputFile: "Movie_t00.mkv", Streams: c.streams})}
		s := New(nil, nil)
		s.Tracks = func(context.Context, string) (FileTracks, error) { return c.got, c.readErr }
		err := s.Verify(context.Background(), j, dir)
		if c.wantErr == "" && err != nil || c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)) {
			t.Errorf("%s: err = %v; want %q", name, err, c.wantErr)
		}
		// Every failed check says its kind, for the failure page.
		var k interface{ FailKind() string }
		if err != nil && (!errors.As(err, &k) || !strings.HasPrefix(k.FailKind(), "verify")) {
			t.Errorf("%s: %v has no verify failure kind", name, err)
		}
	}
}

func TestParseIdentification(t *testing.T) {
	// "mkvmerge -J" output as a real rip gave it on the homelab (Star Trek
	// II, MKVToolNix in the v0.1.3 image), cut down to what Discern reads.
	// The file carries ISO 639-2/B codes: "ger", "fre".
	out := `{"attachments":[],"chapters":[],"container":{"recognized":true,"supported":true,"type":"Matroska"},"errors":[],
		"identification_format_version":20,"tracks":[
		{"codec":"HEVC/H.265/MPEG-H","id":0,"properties":{"language":"eng"},"type":"video"},
		{"codec":"TrueHD","id":1,"properties":{"language":"eng","default_track":true},"type":"audio"},
		{"codec":"AC-3 Dolby Surround EX","id":2,"properties":{"language":"eng"},"type":"audio"},
		{"codec":"TrueHD","id":3,"properties":{"language":"ger"},"type":"audio"},
		{"codec":"AC-3","id":6,"properties":{"language":"fre"},"type":"audio"},
		{"codec":"AC-3","id":7,"properties":{"language":"und"},"type":"audio"},
		{"codec":"TrueHD","id":8,"properties":{"language":"und"},"type":"audio"},
		{"codec":"HDMV PGS","id":9,"properties":{"language":"eng"},"type":"subtitles"}],"warnings":[]}`
	got, err := parseIdentification([]byte(out))
	want := FileTracks{Video: 1, Audio: 6, Subtitles: 1, AudioLanguages: []string{"eng", "ger", "fre"},
		LosslessLanguages: []string{"eng", "ger"}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("parseIdentification = %+v, %v; want %+v", got, err, want)
	}
	for name, bad := range map[string]string{
		"not recognised": `{"container":{"recognized":false,"supported":false},"tracks":[]}`,
		"not JSON":       `Error: The file could not be opened`,
		"empty":          ``,
	} {
		if _, err := parseIdentification([]byte(bad)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// Verify and Import read the same rip one after the other; mkvmerge runs once.
func TestRememberedReadsAnUnchangedFileOnce(t *testing.T) {
	file := filepath.Join(t.TempDir(), "Movie_t00.mkv")
	writeFile(t, file, 100)
	calls, fail := 0, false
	read := Remembered(func(context.Context, string) (FileTracks, error) {
		calls++
		if fail {
			return FileTracks{}, errors.New("mkvmerge couldn't read it")
		}
		return FileTracks{Video: 1, Audio: calls}, nil
	})
	ctx := context.Background()
	first, _ := read(ctx, file)
	second, err := read(ctx, file)
	if err != nil || calls != 1 || !reflect.DeepEqual(first, second) {
		t.Fatalf("two reads of one file: %d calls, %+v then %+v, %v; want 1 call and the same answer", calls, first, second, err)
	}

	writeFile(t, file, 200) // the file changed: read it again
	if third, _ := read(ctx, file); calls != 2 || third.Audio != 2 {
		t.Errorf("after the file changed: %d calls, %+v; want a fresh read", calls, third)
	}

	// A failed read isn't remembered as the answer.
	writeFile(t, file, 300)
	fail = true
	if _, err := read(ctx, file); err == nil {
		t.Error("a failed read returned no error")
	}
	fail = false
	if _, err := read(ctx, file); err != nil || calls != 4 {
		t.Errorf("after a failed read: err %v, %d calls; want a fresh, good read", err, calls)
	}
	if _, err := read(ctx, filepath.Join(t.TempDir(), "missing.mkv")); err == nil {
		t.Error("a missing file returned no error")
	}
}

// A caller that changes the languages it was given doesn't change what the
// next caller gets.
func TestRememberedGivesEachCallerItsOwnCopy(t *testing.T) {
	file := filepath.Join(t.TempDir(), "Movie_t00.mkv")
	writeFile(t, file, 100)
	read := Remembered(func(context.Context, string) (FileTracks, error) {
		return FileTracks{Video: 1, Audio: 2, AudioLanguages: []string{"eng", "ger"}, LosslessLanguages: []string{"eng"}}, nil
	})
	ctx := context.Background()
	first, _ := read(ctx, file)
	first.AudioLanguages[0], first.LosslessLanguages[0] = "xxx", "xxx"
	second, _ := read(ctx, file)
	second.AudioLanguages[1] = "yyy"
	third, _ := read(ctx, file)
	if !reflect.DeepEqual(third.AudioLanguages, []string{"eng", "ger"}) || !reflect.DeepEqual(third.LosslessLanguages, []string{"eng"}) {
		t.Errorf("after callers changed their copies: %+v; want the file's own languages", third)
	}
}

// A read doesn't hold up another: Remembered's lock isn't held while
// mkvmerge runs.
func TestRememberedDoesntBlockOnASlowRead(t *testing.T) {
	dir := t.TempDir()
	slow, quick := filepath.Join(dir, "slow.mkv"), filepath.Join(dir, "quick.mkv")
	writeFile(t, slow, 100)
	writeFile(t, quick, 100)
	release := make(chan struct{})
	read := Remembered(func(ctx context.Context, file string) (FileTracks, error) {
		if file == slow {
			<-release
		}
		return FileTracks{Video: 1}, nil
	})
	done := make(chan struct{})
	go func() {
		read(context.Background(), slow)
		close(done)
	}()
	time.Sleep(20 * time.Millisecond) // let the slow read start
	got := make(chan error, 1)
	go func() {
		_, err := read(context.Background(), quick)
		got <- err
	}()
	select {
	case err := <-got:
		if err != nil {
			t.Errorf("the quick read: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Error("the quick read waited for the slow one")
	}
	close(release)
	<-done
}

func TestLossless(t *testing.T) {
	for codec, want := range map[string]bool{
		// MakeMKV's scan (short and long names) and mkvmerge's names.
		"TrueHD": true, "TrueHD Atmos": true, "DTS-HD MA": true, "DTS-HD Master Audio": true,
		"LPCM": true, "PCM": true, "FLAC": true,
		"AC3": false, "AC-3 Dolby Surround EX": false, "E-AC3": false, "DTS": false,
		"DTS-HD HR": false, "DTS-HD High Resolution Audio": false, "AAC": false, "": false,
	} {
		if lossless(codec) != want {
			t.Errorf("lossless(%q) = %v; want %v", codec, !want, want)
		}
	}
}
