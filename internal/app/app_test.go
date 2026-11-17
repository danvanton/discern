// End-to-end tests: every real part of Discern (store, engine, steps,
// makemkvcon runner, Radarr importer, app), with two stand-ins: the fake
// makemkvcon (played by this test binary) and the fake Radarr.
package app

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"github.com/danvanton/discern/internal/discdb"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
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
	if os.Getenv(fake.MkvmergeEnvVar) != "" {
		os.Exit(fake.Mkvmerge(os.Args[1:], os.Stdout, os.Stderr))
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
TINFO:0,26,0,"1-3"
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
TINFO:1,26,0,"4"
TINFO:1,27,0,"The Matrix_t01.mkv"
`

type world struct {
	app     *App
	runner  *makemkv.Runner
	steps   *steps.Steps
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
	a.ConnectSelect(jobSteps)
	return &world{app: a, runner: runner, steps: jobSteps, store: st, radarr: fr, staging: staging, db: db, cancel: cancel}
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

	if err := w.app.Approve(ctx, j.ID, waiting.Titles, nil, nil, jobs.Direct); err != nil {
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
	w.app.Approve(ctx, j.ID, waiting.Titles, nil, nil, jobs.Direct)
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
		w.app.Approve(ctx, j.ID, waiting.Titles, nil, nil, jobs.Direct)
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

// A disc that goes in while the drive's job is ripping is remembered, and
// gets its own job once the drive is free.
func TestADiscInsertedWhileBusyStartsLater(t *testing.T) {
	w := newWorld(t, 200, "") // a slow rip
	ctx := context.Background()
	first, _ := w.app.DiscInserted(ctx, "/dev/sr0")
	waiting := w.waitFor(t, first.ID, jobs.NeedsYou)
	if err := w.app.Approve(ctx, first.ID, waiting.Titles, nil, nil, jobs.Direct); err != nil {
		t.Fatal(err)
	}
	w.waitFor(t, first.ID, jobs.Ripping)

	w.app.OnDisc(ctx, "/dev/sr0") // the next disc goes in
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

// A drive with no disc cancels its job waiting for approval, with the
// reason as the job's note, and forgets a disc that was waiting for the
// drive; the next disc in starts afresh (decision D58).
func TestARemovedDiscCancelsAWaitingJob(t *testing.T) {
	w := newWorld(t, 0, "")
	ctx := context.Background()
	first, _ := w.app.DiscInserted(ctx, "/dev/sr0")
	w.waitFor(t, first.ID, jobs.NeedsYou)

	w.app.OnDiscRemoved(ctx, "/dev/sr0")
	j, _ := w.store.Job(ctx, first.ID)
	if j.State != jobs.Cancelled || j.Note != "the disc was taken out" {
		t.Errorf("the waiting job is %s, note %q", j.State, j.Note)
	}
	if active, _ := w.store.ActiveJobs(ctx); len(active) != 0 {
		t.Fatalf("%d active jobs after the disc came out, want none", len(active))
	}

	w.app.OnDisc(ctx, "/dev/sr0") // the disc goes back in
	active, _ := w.store.ActiveJobs(ctx)
	if len(active) != 1 || active[0].ID == first.ID {
		t.Fatalf("active jobs = %v, want one new job", active)
	}
	w.waitFor(t, active[0].ID, jobs.NeedsYou)

	// Another drive's removal doesn't touch this job.
	w.app.OnDiscRemoved(ctx, "/dev/sr1")
	if j, _ := w.store.Job(ctx, active[0].ID); j.State != jobs.NeedsYou {
		t.Errorf("another drive's removal changed the job to %s", j.State)
	}
}

// A disc swapped between two polls (the tray never read as open) replaces
// the job waiting for approval at once (decision D58).
func TestASwappedDiscReplacesAWaitingJob(t *testing.T) {
	w := newWorld(t, 0, "")
	ctx := context.Background()
	first, _ := w.app.DiscInserted(ctx, "/dev/sr0")
	w.waitFor(t, first.ID, jobs.NeedsYou)

	w.app.OnDisc(ctx, "/dev/sr0")
	if j, _ := w.store.Job(ctx, first.ID); j.State != jobs.Cancelled || j.Note != "another disc went in" {
		t.Errorf("the waiting job is %s, note %q", j.State, j.Note)
	}
	if w.app.Pending("/dev/sr0") {
		t.Error("the new disc was queued instead of started")
	}
	active, _ := w.store.ActiveJobs(ctx)
	if len(active) != 1 || active[0].ID == first.ID {
		t.Fatalf("active jobs = %v, want one new job", active)
	}
	w.waitFor(t, active[0].ID, jobs.NeedsYou)
}

// A disc queued behind a ripping job is forgotten when the drive reads
// empty: it came out with the tray.
func TestARemovedDiscForgetsAQueuedDisc(t *testing.T) {
	w := newWorld(t, 200, "") // a slow rip
	ctx := context.Background()
	first, _ := w.app.DiscInserted(ctx, "/dev/sr0")
	waiting := w.waitFor(t, first.ID, jobs.NeedsYou)
	if err := w.app.Approve(ctx, first.ID, waiting.Titles, nil, nil, jobs.Direct); err != nil {
		t.Fatal(err)
	}
	w.waitFor(t, first.ID, jobs.Ripping)
	w.app.OnDisc(ctx, "/dev/sr0")
	if !w.app.Pending("/dev/sr0") {
		t.Fatal("the disc wasn't queued behind the rip")
	}
	w.app.OnDiscRemoved(ctx, "/dev/sr0")
	if w.app.Pending("/dev/sr0") {
		t.Error("the disc that came out is still queued")
	}
	if j, _ := w.store.Job(ctx, first.ID); j.State != jobs.Ripping {
		t.Errorf("the ripping job is %s; it is left to its own disc check", j.State)
	}
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
	if err := w.app.Approve(ctx, j.ID, waiting.Titles, nil, nil, jobs.Direct); err != nil {
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

// A disc's own languages (decision D34) win over the Settings page's, are
// used exactly (no foreign-film fallback), and tell Verify the rip is
// trimmed even with MakeMKV's default on the Settings page.
func TestADiscsOwnLanguagesAreUsedForItsRip(t *testing.T) {
	w := newWorld(t, 0, "")
	ctx := context.Background()
	if err := w.app.SaveSettings(ctx, settings.Settings{Eject: true, Languages: &settings.Languages{Audio: []string{"ger"}}}); err != nil {
		t.Fatal(err)
	}
	j, err := w.app.DiscInserted(ctx, "/dev/sr0")
	if err != nil {
		t.Fatal(err)
	}
	waiting := w.waitFor(t, j.ID, jobs.NeedsYou)
	disc := &settings.Languages{Audio: []string{"jpn"}, Subtitles: []string{"eng"}, ForcedOnly: true}
	if err := w.app.Approve(ctx, j.ID, waiting.Titles, nil, disc, jobs.Direct); err != nil {
		t.Fatal(err)
	}
	done := w.waitFor(t, j.ID, jobs.Done)
	w.app.Wait(5 * time.Second)
	b, _ := os.ReadFile(w.app.MakeMKVSettings)
	if !strings.Contains(string(b), "+sel:(audio&(jpn|nolang|single)),+sel:(subtitle&(eng)&forced)") {
		t.Errorf("settings.conf = %q; want exactly the disc's choice: jpn audio, forced eng subtitles", b)
	}
	if !reflect.DeepEqual(done.Languages, disc) {
		t.Errorf("job's languages = %+v; want them saved with it", done.Languages)
	}

	// Verify expects a trimmed file for this job, whatever Settings say.
	if err := w.app.SaveSettings(ctx, settings.Settings{Eject: true}); err != nil {
		t.Fatal(err)
	}
	if trimmed, err := w.app.LanguagesChosen(ctx, done); err != nil || !trimmed {
		t.Errorf("LanguagesChosen = %v, %v; want true for a job with its own languages", trimmed, err)
	}
	if trimmed, _ := w.app.LanguagesChosen(ctx, &jobs.Job{}); trimmed {
		t.Error("a job without its own languages, with MakeMKV's default: trimmed")
	}
}

func TestApproveRefusesBadDiscLanguages(t *testing.T) {
	w := newWorld(t, 0, "")
	ctx := context.Background()
	j, _ := w.app.DiscInserted(ctx, "/dev/sr0")
	waiting := w.waitFor(t, j.ID, jobs.NeedsYou)
	for _, bad := range []*settings.Languages{{}, {Audio: []string{"xx"}}, {Audio: []string{"eng", "eng"}}} {
		if err := w.app.Approve(ctx, j.ID, waiting.Titles, nil, bad, jobs.Direct); err == nil {
			t.Errorf("languages %+v were accepted", bad)
		}
	}
	if got, _ := w.store.Job(ctx, j.ID); got.State != jobs.NeedsYou {
		t.Errorf("state = %s; a refused approval must leave the job waiting", got.State)
	}
}

func TestSaveSettingsRefusesBadLanguages(t *testing.T) {
	w := newWorld(t, 0, "")
	err := w.app.SaveSettings(context.Background(), settings.Settings{Languages: &settings.Languages{}})
	if err == nil {
		t.Fatal("languages without audio were saved")
	}
}

// The scan and the Settings page use different codes for the same language:
// a real UHD scan says "deu", "fra" where the page stores "ger", "fre".
// (Star Trek III's main title, job 6 on the homelab.)
func TestOwnAudioComparesBothCodeForms(t *testing.T) {
	var streams []makemkv.Stream
	for _, l := range []string{"eng", "eng", "deu", "deu", "spa", "fra", "jpn"} {
		streams = append(streams, makemkv.Stream{Type: "Audio", Lang: l})
	}
	j := &jobs.Job{Titles: []int{29}, Disc: &makemkv.Disc{Titles: []makemkv.Title{{Index: 29, Streams: streams}}}}
	for _, chosen := range [][]string{{"ger"}, {"fre"}, {"eng"}, {"fre", "ger"}} {
		if own := ownAudio(j, chosen); len(own) != 0 {
			t.Errorf("chosen %v: the title was treated as foreign, keeping %v; it has audio in a chosen language", chosen, own)
		}
	}
	// A language the title really lacks: its own are kept, each once, in /B form.
	if own := ownAudio(j, []string{"ita"}); strings.Join(own, ",") != "eng,ger,spa,fre,jpn" {
		t.Errorf("chosen ita: own audio = %v; want eng,ger,spa,fre,jpn", own)
	}
}

// "und" is audio without a language, like an empty code: the title isn't
// foreign, and "und" never goes into the rule.
func TestOwnAudioCountsUndAsNoLanguage(t *testing.T) {
	streams := []makemkv.Stream{{Type: "Audio", Lang: "und"}, {Type: "Audio", Lang: "jpn"}}
	j := &jobs.Job{Titles: []int{0}, Disc: &makemkv.Disc{Titles: []makemkv.Title{{Index: 0, Streams: streams}}}}
	if own := ownAudio(j, []string{"eng"}); len(own) != 0 {
		t.Errorf("own audio = %v; want none: the und track is kept by nolang", own)
	}
	j.Disc.Titles[0].Streams = []makemkv.Stream{{Type: "Audio", Lang: "UND"}}
	if own := ownAudio(j, []string{"eng"}); len(own) != 0 {
		t.Errorf("own audio = %v; want none", own)
	}
}

// A code from the disc that isn't three letters is left out, so one odd code
// doesn't make MakeMKV's rule invalid and fail the rip.
func TestOwnAudioSkipsOddCodes(t *testing.T) {
	streams := []makemkv.Stream{{Type: "Audio", Lang: "jpn"}, {Type: "Audio", Lang: "en"}, {Type: "Audio", Lang: "j,p"}}
	j := &jobs.Job{Titles: []int{0}, Disc: &makemkv.Disc{Titles: []makemkv.Title{{Index: 0, Streams: streams}}}}
	own := ownAudio(j, []string{"eng"})
	if strings.Join(own, ",") != "jpn" {
		t.Fatalf("own audio = %v; want only jpn", own)
	}
	if _, err := makemkv.SelectionRule(append([]string{"eng"}, own...), nil, false, false); err != nil {
		t.Errorf("the rule for this title is refused: %v", err)
	}
}

// knownMatrix gives the app a TheDiscDB index that knows the world's disc
// as The Matrix, signed with a test key the catalog trusts.
func knownMatrix(t *testing.T, a *App) {
	t.Helper()
	d, err := makemkv.ParseInfo(strings.NewReader(scan))
	if err != nil {
		t.Fatal(err)
	}
	known := discdb.Disc{Movie: discdb.Movie{TMDbID: 603, Title: "The Matrix", Year: 1999}, Release: "test", Name: "The Matrix", Format: "UHD"}
	for _, ti := range d.Titles {
		kind := "Extra"
		if ti.Index == 0 {
			kind = discdb.MainMovie
		}
		known.Titles = append(known.Titles, discdb.Title{Index: ti.Index, File: ti.SourceFile, Segments: ti.SegmentMap,
			Seconds: int(ti.Duration / time.Second), Size: ti.SizeBytes, Type: kind, Name: "The Matrix"})
	}
	var b bytes.Buffer
	if err := (&discdb.Index{Version: 1, Built: time.Now(), Source: "test", Discs: []discdb.Disc{known}}).Write(&b); err != nil {
		t.Fatal(err)
	}
	seed := bytes.Repeat([]byte{7}, ed25519.SeedSize)
	sig, _ := discdb.Sign(seed, b.Bytes())
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "index.json.gz"), b.Bytes(), 0o644)
	os.WriteFile(filepath.Join(dir, "index.json.gz.sig"), sig, 0o644)
	c := discdb.NewCatalog("", dir, nil)
	c.Key = ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
	if err := c.LoadLocal(); err != nil || c.Index() == nil {
		t.Fatalf("the test index didn't load: %v", err)
	}
	a.Catalog = c
}

// Auto-import through the app's own wiring (decisions D60-D63): with a
// disc TheDiscDB knows, auto-import on goes from tray to Radarr without an
// approval; on trial, or once Radarr has the movie, the disc waits, saying
// why.
func TestAutoImportEndToEnd(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t, 0, "")
	knownMatrix(t, w.app)
	if err := w.app.SaveSettings(ctx, settings.Settings{AutoImport: settings.AutoTrial}); err != nil {
		t.Fatal(err)
	}
	trial, _ := w.app.DiscInserted(ctx, "/dev/sr0")
	got := w.waitFor(t, trial.ID, jobs.NeedsYou)
	if got.Selection == nil || !got.Selection.Trial || got.Selection.Auto {
		t.Fatalf("on trial: %+v", got.Selection)
	}
	w.app.Cancel(ctx, trial.ID)

	if err := w.app.SaveSettings(ctx, settings.Settings{AutoImport: settings.AutoOn}); err != nil {
		t.Fatal(err)
	}
	on, _ := w.app.DiscInserted(ctx, "/dev/sr0")
	got = w.waitFor(t, on.ID, jobs.Done)
	if !strings.HasPrefix(got.Note, "ripped without asking (auto-import)") || len(w.radarr.Imports()) != 1 {
		t.Errorf("on: note %q, %d imports", got.Note, len(w.radarr.Imports()))
	}

	again, _ := w.app.DiscInserted(ctx, "/dev/sr0")
	got = w.waitFor(t, again.ID, jobs.NeedsYou)
	if got.Selection == nil || got.Selection.Wait != "Radarr already has The Matrix (1999)" {
		t.Errorf("Radarr has it: %+v", got.Selection)
	}
}

// fallbackScenario is the fake makemkvcon for the Backup + remux route:
// the world's scan, a rip that stalls at 256 KiB while it keeps reading,
// and a backup of the world disc's size.
func fallbackScenario(t *testing.T, w *world) {
	t.Helper()
	scanFile := filepath.Join(t.TempDir(), "scan.txt")
	os.WriteFile(scanFile, []byte(scan), 0o644)
	sc, _ := json.Marshal(fake.Scenario{
		Info:   fake.Info{File: scanFile},
		Mkv:    fake.Mkv{TitleName: "The Matrix", TotalBytes: 64 << 20, StallAtBytes: 256 << 10, ChunkDelayMs: 2},
		Backup: fake.Backup{Mkv: fake.Mkv{TotalBytes: 1 << 20, ChunkBytes: 64 << 10}, Playlists: []string{"00800.mpls", "00801.mpls"}},
	})
	w.runner.Env = []string{fake.EnvVar + "=" + string(sc)}
	exe, _ := os.Executable()
	w.steps.Mkvmerge = exe
	// The test machine's disk needn't hold a real disc's backup.
	w.steps.FreeSpace = func(string) (int64, error) { return 1 << 50, nil }
	t.Setenv(fake.MkvmergeEnvVar, "on")
}

// The manual route through the real steps (decision D69): approved with
// backup + remux, the disc is backed up by makemkvcon and remuxed by
// mkvmerge (both fakes), verified and imported, with no Direct rip.
func TestBackupAndRemuxEndToEnd(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t, 0, "")
	fallbackScenario(t, w)
	j, _ := w.app.DiscInserted(ctx, "/dev/sr0")
	waiting := w.waitFor(t, j.ID, jobs.NeedsYou)
	if err := w.app.Approve(ctx, j.ID, waiting.Titles, nil, nil, jobs.Backup); err != nil {
		t.Fatal(err)
	}
	w.waitFor(t, j.ID, jobs.Done)
	events, _ := w.store.Events(ctx, j.ID)
	var states []jobs.State
	for _, e := range events {
		states = append(states, e.To)
	}
	if !slices.Contains(states, jobs.BackingUp) || !slices.Contains(states, jobs.Remuxing) || len(w.radarr.Imports()) != 1 {
		t.Errorf("states %v, %d imports", states, len(w.radarr.Imports()))
	}
}
