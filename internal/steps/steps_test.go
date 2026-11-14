package steps

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestSuggest(t *testing.T) {
	h := time.Hour
	cases := []struct {
		name string
		disc *makemkv.Disc
		want []int
		why  string
	}{
		{"longest wins", titles(
			makemkv.Title{Index: 0, Duration: 20 * time.Minute},
			makemkv.Title{Index: 1, Duration: 2 * h},
			makemkv.Title{Index: 2, Duration: 40 * time.Minute}), []int{1}, "longest"},
		{"equal length: larger wins", titles(
			makemkv.Title{Index: 0, Duration: 2 * h, SizeBytes: 10},
			makemkv.Title{Index: 1, Duration: 2 * h, SizeBytes: 20}), []int{1}, "longest"},
		{"MakeMKV's mark beats length", titles(
			makemkv.Title{Index: 0, Duration: 3 * h},
			makemkv.Title{Index: 5, Duration: 2 * h, MakeMKVMainFeature: true}), []int{5}, "MakeMKV marked"},
		{"two marks: fall back to length", titles(
			makemkv.Title{Index: 0, Duration: 3 * h, MakeMKVMainFeature: true},
			makemkv.Title{Index: 1, Duration: 2 * h, MakeMKVMainFeature: true}), []int{0}, "longest"},
		{"no titles", titles(), nil, "no titles"},
		{"no scan", nil, nil, "no titles"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sel := Suggest(c.disc)
			if sel.Auto {
				t.Error("v0.1 must never select automatically")
			}
			if len(sel.Titles) != len(c.want) || (len(c.want) == 1 && sel.Titles[0] != c.want[0]) {
				t.Errorf("titles = %v, want %v", sel.Titles, c.want)
			}
			if !strings.Contains(sel.Reason, c.why) {
				t.Errorf("reason = %q, want it to mention %q", sel.Reason, c.why)
			}
		})
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
		s.Trimmed = func(context.Context) (bool, error) { return c.trimmed, nil }
		err := s.Verify(context.Background(), j, dir)
		if c.wantErr == "" && err != nil || c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)) {
			t.Errorf("%d bytes, trimmed %v: err = %v; want %q", size, c.trimmed, err, c.wantErr)
		}
	}

	// A settings read that fails is an error, not the default floor.
	s := New(nil, nil)
	s.Trimmed = func(context.Context) (bool, error) { return false, errors.New("database is closed") }
	if err := s.Verify(context.Background(), j, t.TempDir()); err == nil {
		t.Error("Verify ignored a failed settings read")
	}
}

// Verify reads the finished file's tracks: a rip without audio, from a title
// that has audio, is not imported.
func TestVerifyChecksTheFilesTracks(t *testing.T) {
	audio := []makemkv.Stream{{Type: "Video"}, {Type: "Audio", Lang: "jpn"}}
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
		"not a file mkvmerge can read": {audio, FileTracks{}, errors.New("mkvmerge doesn't recognise it"), "doesn't recognise"},
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
	}
}

func TestParseIdentification(t *testing.T) {
	// The shape of "mkvmerge -J" output, cut down to what Discern reads.
	out := `{"container":{"recognized":true,"supported":true,"type":"Matroska"},"errors":[],"tracks":[
		{"codec":"HEVC/H.265/MPEG-H","id":0,"type":"video"},
		{"codec":"TrueHD Atmos","id":1,"type":"audio"},{"codec":"AC-3","id":2,"type":"audio"},
		{"codec":"HDMV PGS","id":3,"type":"subtitles"}],"warnings":[]}`
	got, err := parseIdentification([]byte(out))
	if err != nil || got != (FileTracks{Video: 1, Audio: 2, Subtitles: 1}) {
		t.Errorf("parseIdentification = %+v, %v", got, err)
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
