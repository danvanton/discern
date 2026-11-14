// End-to-end tests: every real part of Discern (store, engine, steps,
// makemkvcon runner, Radarr importer, app), with two stand-ins: the fake
// makemkvcon (played by this test binary) and the fake Radarr.
package app

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danvanton/discern/internal/importer"
	"github.com/danvanton/discern/internal/jobs"
	"github.com/danvanton/discern/internal/makemkv"
	"github.com/danvanton/discern/internal/makemkv/fake"
	"github.com/danvanton/discern/internal/radarr"
	"github.com/danvanton/discern/internal/radarr/radarrtest"
	"github.com/danvanton/discern/internal/settings"
	"github.com/danvanton/discern/internal/steps"
	"github.com/danvanton/discern/internal/store"
)

func TestMain(m *testing.M) {
	if os.Getenv(fake.EnvVar) != "" {
		os.Exit(fake.Main(os.Args[1:], os.Stdout, os.Stderr))
	}
	os.Exit(m.Run())
}

// A small disc: a 1 MiB main feature (title 0) and an extra (title 1).
const scan = `TCOUNT:2
CINFO:2,0,"The Matrix"
CINFO:32,0,"MATRIX_THE"
TINFO:0,9,0,"2:16:17"
TINFO:0,11,0,"1048576"
TINFO:0,16,0,"00800.mpls"
TINFO:0,27,0,"The Matrix_t00.mkv"
SINFO:0,0,1,6201,"Video"
SINFO:0,0,19,0,"3840x2160"
SINFO:0,1,1,6202,"Audio"
SINFO:0,1,3,0,"eng"
SINFO:0,2,1,6202,"Audio"
SINFO:0,2,3,0,"fre"
TINFO:1,9,0,"0:05:00"
TINFO:1,11,0,"65536"
TINFO:1,16,0,"00801.mpls"
TINFO:1,27,0,"The Matrix_t01.mkv"
`

type world struct {
	app     *App
	store   *store.Store
	radarr  *radarrtest.Server
	staging string
	db      string
	cancel  context.CancelFunc
}

// newWorld builds Discern with the fakes. ripDelay slows the fake rip so a
// test can interrupt it.
func newWorld(t *testing.T, ripDelay int, db string) *world {
	t.Helper()
	dir := t.TempDir()
	scanFile := filepath.Join(dir, "scan.txt")
	os.WriteFile(scanFile, []byte(scan), 0o644)
	staging := filepath.Join(dir, "staging")
	if db == "" {
		db = filepath.Join(dir, "discern.db")
	}

	ctx, cancel := context.WithCancel(context.Background())
	st, err := store.Open(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	fr, url := radarrtest.NewTest(t, staging, "/data/staging")
	fr.SetLookup(map[string]any{"tmdbId": 603, "title": "The Matrix", "year": 1999})
	rc, _ := radarr.New(url, fr.APIKey)
	imp, err := importer.NewRadarr(rc, importer.RadarrConfig{
		RootFolder: "/movies", Staging: importer.PathMap{Local: staging, Remote: "/data/staging"},
		PollEvery: 5 * time.Millisecond, CommandTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	exe, _ := os.Executable()
	sc, _ := json.Marshal(fake.Scenario{
		Info: fake.Info{File: scanFile},
		Mkv:  fake.Mkv{TitleName: "The Matrix", TotalBytes: 1 << 20, ChunkBytes: 64 << 10, ChunkDelayMs: ripDelay},
	})
	runner := makemkv.NewRunner(exe)
	runner.Env = []string{fake.EnvVar + "=" + string(sc)}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	jobSteps := steps.New(runner, imp)
	engine := jobs.NewEngine(st, jobSteps, staging, log)
	a := New(ctx, st, engine, rc, log)
	a.Drives, a.MakeMKVCon = []string{"/dev/sr0"}, exe
	a.MakeMKVSettings = filepath.Join(dir, "settings.conf")
	jobSteps.BeforeRip = a.ApplyLanguages
	return &world{app: a, store: st, radarr: fr, staging: staging, db: db, cancel: cancel}
}

// waitFor polls the job until it reaches state, or fails the test.
func (w *world) waitFor(t *testing.T, id int64, want jobs.State) *jobs.Job {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		j, err := w.store.Job(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if j.State == want {
			return j
		}
		if j.State == jobs.Failed && want != jobs.Failed {
			t.Fatalf("job failed: %s", j.Note)
		}
		time.Sleep(10 * time.Millisecond)
	}
	j, _ := w.store.Job(context.Background(), id)
	t.Fatalf("job is %s after 20 s, want %s", j.State, want)
	return nil
}

func TestADiscGoesFromTrayToRadarr(t *testing.T) {
	w := newWorld(t, 0, "")
	ctx := context.Background()

	j, err := w.app.DiscInserted(ctx, "/dev/sr0")
	if err != nil {
		t.Fatal(err)
	}
	waiting := w.waitFor(t, j.ID, jobs.NeedsYou)
	if waiting.Movie == nil || waiting.Movie.TMDbID != 603 {
		t.Errorf("identified %+v, want The Matrix (603)", waiting.Movie)
	}
	if len(waiting.Titles) != 1 || waiting.Titles[0] != 0 {
		t.Errorf("suggested titles %v, want [0] (the longest)", waiting.Titles)
	}

	if err := w.app.Approve(ctx, j.ID, waiting.Titles, nil); err != nil {
		t.Fatal(err)
	}
	w.waitFor(t, j.ID, jobs.Done)

	if n := len(w.radarr.Imports()); n != 1 {
		t.Errorf("Radarr got %d imports, want 1", n)
	}
	if _, err := os.Stat(filepath.Join(w.radarr.Library, "The Matrix_t00.mkv")); err != nil {
		t.Errorf("the rip isn't in the library: %v", err)
	}
	if entries, _ := os.ReadDir(w.staging); len(entries) != 0 {
		t.Errorf("staging still holds %d entries", len(entries))
	}
	if checks := w.app.Health(ctx); !allOK(checks) {
		t.Errorf("health: %+v", checks)
	}
}

// Shutting Discern down mid-rip and starting it again finishes the job.
func TestShutdownMidRipThenRestartFinishesTheJob(t *testing.T) {
	w := newWorld(t, 50, "") // 16 chunks x 50 ms: under a second of ripping
	ctx := context.Background()
	j, _ := w.app.DiscInserted(ctx, "/dev/sr0")
	waiting := w.waitFor(t, j.ID, jobs.NeedsYou)
	w.app.Approve(ctx, j.ID, waiting.Titles, nil)
	w.waitFor(t, j.ID, jobs.Ripping)
	time.Sleep(150 * time.Millisecond) // part-way through

	w.cancel() // SIGTERM
	if !w.app.Wait(10 * time.Second) {
		t.Fatal("jobs didn't stop")
	}
	if got, _ := w.store.Job(ctx, j.ID); got.State != jobs.Ripping {
		t.Fatalf("after shutdown: %s, want ripping (interrupted, not failed)", got.State)
	}
	w.store.Close()

	// Restart on the same database.
	w2 := newWorld(t, 0, w.db)
	if err := w2.app.Resume(ctx); err != nil {
		t.Fatal(err)
	}
	// The restarted Discern has its own fake Radarr and staging folder: the
	// job must rip again from scratch and finish.
	w2.waitFor(t, j.ID, jobs.Done)
}

func allOK(cs []Check) bool {
	for _, c := range cs {
		if !c.OK {
			return false
		}
	}
	return true
}

// A finished job ejects its disc; a failed one doesn't.
func TestEjectOnlyWhenDone(t *testing.T) {
	for _, fail := range []bool{false, true} {
		w := newWorld(t, 0, "")
		var mu sync.Mutex
		var ejected []string
		w.app.Eject = func(d string) error { mu.Lock(); ejected = append(ejected, d); mu.Unlock(); return nil }
		if fail {
			w.radarr.SetBehaviour(radarrtest.Behaviour{FailImport: true})
		}
		ctx := context.Background()
		j, _ := w.app.DiscInserted(ctx, "/dev/sr0")
		waiting := w.waitFor(t, j.ID, jobs.NeedsYou)
		w.app.Approve(ctx, j.ID, waiting.Titles, nil)
		want := jobs.Done
		if fail {
			want = jobs.Failed
		}
		w.waitFor(t, j.ID, want)
		w.app.Wait(5 * time.Second) // the run's goroutine ejects after the last state
		mu.Lock()
		got := len(ejected)
		mu.Unlock()
		if fail && got != 0 {
			t.Errorf("a failed job ejected the disc")
		}
		if !fail && got != 1 {
			t.Errorf("a finished job ejected %d times, want once", got)
		}
	}
}

// A disc that goes in while the drive still has a job is remembered, and gets
// its own job once the drive is free.
func TestADiscInsertedWhileBusyStartsLater(t *testing.T) {
	w := newWorld(t, 0, "")
	ctx := context.Background()
	first, _ := w.app.DiscInserted(ctx, "/dev/sr0")
	w.waitFor(t, first.ID, jobs.NeedsYou)

	w.app.OnDisc(ctx, "/dev/sr0") // the user swaps in the next disc
	if !w.app.Pending("/dev/sr0") {
		t.Fatal("the new disc wasn't remembered")
	}
	if active, _ := w.store.ActiveJobs(ctx); len(active) != 1 {
		t.Fatalf("%d active jobs, want only the first", len(active))
	}

	if err := w.app.Cancel(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	if w.app.Pending("/dev/sr0") {
		t.Error("the waiting disc is still pending after the drive freed up")
	}
	active, _ := w.store.ActiveJobs(ctx)
	if len(active) != 1 || active[0].ID == first.ID {
		t.Fatalf("active jobs = %v, want one new job", active)
	}
	w.waitFor(t, active[0].ID, jobs.NeedsYou)
}

func TestHealthReportsWhatIsBroken(t *testing.T) {
	w := newWorld(t, 0, "")
	down := httptest.NewServer(http.NotFoundHandler())
	down.Close() // nothing answers at this address any more
	rc, _ := radarr.New(down.URL, "k")
	a := New(context.Background(), w.store, nil, rc, slog.New(slog.NewTextHandler(io.Discard, nil)))
	a.MakeMKVCon = filepath.Join(t.TempDir(), "no-makemkvcon")

	got := map[string]bool{}
	for _, c := range a.Health(context.Background()) {
		got[c.Name] = c.OK
	}
	if !got["database"] || got["makemkvcon"] || got["radarr"] {
		t.Errorf("health = %v, want database ok, makemkvcon and radarr failing", got)
	}
}

// rip runs one disc through to state want, approving the suggested titles.
func (w *world) rip(t *testing.T, want jobs.State) *jobs.Job {
	t.Helper()
	ctx := context.Background()
	j, err := w.app.DiscInserted(ctx, "/dev/sr0")
	if err != nil {
		t.Fatal(err)
	}
	waiting := w.waitFor(t, j.ID, jobs.NeedsYou)
	if err := w.app.Approve(ctx, j.ID, waiting.Titles, nil); err != nil {
		t.Fatal(err)
	}
	done := w.waitFor(t, j.ID, want)
	w.app.Wait(5 * time.Second)
	return done
}

// Eject follows the Settings page (decision D25).
func TestEjectFollowsTheSettingsPage(t *testing.T) {
	w := newWorld(t, 0, "")
	ejected := 0
	var mu sync.Mutex
	w.app.Eject = func(string) error { mu.Lock(); ejected++; mu.Unlock(); return nil }
	if err := w.app.SaveSettings(context.Background(), settings.Settings{Eject: false}); err != nil {
		t.Fatal(err)
	}
	w.rip(t, jobs.Done)
	mu.Lock()
	defer mu.Unlock()
	if ejected != 0 {
		t.Errorf("ejected %d times with eject off", ejected)
	}
}

// The saved languages reach MakeMKV's settings before the rip; clearing them
// removes Discern's rule again (decisions D20, D24).
func TestLanguagesReachMakeMKVBeforeTheRip(t *testing.T) {
	w := newWorld(t, 0, "")
	ctx := context.Background()
	conf := w.app.MakeMKVSettings
	langs := &settings.Languages{Audio: []string{"eng"}, Subtitles: []string{"eng"}}
	if err := w.app.SaveSettings(ctx, settings.Settings{Eject: true, Languages: langs}); err != nil {
		t.Fatal(err)
	}
	w.rip(t, jobs.Done)
	b, _ := os.ReadFile(conf)
	if !strings.Contains(string(b), `app_DefaultSelectionString = "-sel:all,+sel:video,+sel:(audio&(eng|nolang|single))`) {
		t.Errorf("settings.conf = %q; want the English rule", b)
	}

	// Clearing them; the next rip's hook removes the rule. (A second real rip
	// would re-import The Matrix, which Radarr already has.)
	if err := w.app.SaveSettings(ctx, settings.Settings{Eject: true}); err != nil {
		t.Fatal(err)
	}
	if err := w.app.ApplyLanguages(ctx, &jobs.Job{}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(conf); strings.Contains(string(b), "app_DefaultSelectionString") {
		t.Errorf("settings.conf = %q; want Discern's rule removed", b)
	}
}

// A title with no audio in the chosen languages (a foreign film) keeps its
// own audio for that rip, after the chosen languages.
func TestAForeignTitleKeepsItsOwnAudio(t *testing.T) {
	w := newWorld(t, 0, "")
	langs := &settings.Languages{Audio: []string{"jpn"}, Subtitles: []string{"jpn"}}
	if err := w.app.SaveSettings(context.Background(), settings.Settings{Eject: true, Languages: langs}); err != nil {
		t.Fatal(err)
	}
	w.rip(t, jobs.Done)
	b, _ := os.ReadFile(w.app.MakeMKVSettings)
	if !strings.Contains(string(b), "+sel:(audio&(jpn|eng|fre|nolang|single))") || !strings.Contains(string(b), "+sel:(subtitle&(jpn))") {
		t.Errorf("settings.conf = %q; want the title's own eng and fre audio added after jpn, subtitles as chosen", b)
	}
}

func TestSaveSettingsRefusesBadLanguages(t *testing.T) {
	w := newWorld(t, 0, "")
	err := w.app.SaveSettings(context.Background(), settings.Settings{Languages: &settings.Languages{}})
	if err == nil {
		t.Fatal("languages without audio were saved")
	}
}
