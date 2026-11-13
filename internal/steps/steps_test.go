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

	"github.com/NotNad/trayarr/internal/jobs"
	"github.com/NotNad/trayarr/internal/makemkv"
	"github.com/NotNad/trayarr/internal/makemkv/fake"
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
