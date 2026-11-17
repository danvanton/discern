package web

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/danvanton/discern/internal/app"
	"github.com/danvanton/discern/internal/discdb"
	"github.com/danvanton/discern/internal/drive"
	"github.com/danvanton/discern/internal/jobs"
	"github.com/danvanton/discern/internal/lang"
	"github.com/danvanton/discern/internal/makemkv"
	"github.com/danvanton/discern/internal/settings"
)

// fakeBackend holds a few jobs and records what the UI asked it to do.
type fakeBackend struct {
	jobs        map[int64]*jobs.Job
	approved    []int
	movie       *jobs.Movie
	retried     int64
	scanned     string
	health      []app.Check
	failNext    error
	settings    *settings.Settings
	trial       jobs.TrialTally
	settingsErr error
	posters     map[int]string // TMDb id → cached file
	live        map[string]makemkv.Live
	trays       map[string]drive.State
	events      map[int64][]jobs.Event
	active      []int64 // the active jobs; 1 and 2 if nil
	discLangs   *settings.Languages
	searched    string
	bundles     map[string]string // diagnostics bundle name → folder
	discIndex   *discdb.Index
}

func newBackend() *fakeBackend {
	disc := &makemkv.Disc{Name: "Sample Movie", Volume: "SAMPLE", Titles: []makemkv.Title{
		{Index: 0, Duration: 118 * time.Minute, SizeBytes: 33 << 30, SourceFile: "00800.mpls", Chapters: 24,
			Streams: []makemkv.Stream{{Type: "Video", Codec: "HEVC", VideoSize: "3840x2160"}, {Type: "Audio", Lang: "eng"}}},
		{Index: 1, Duration: 22 * time.Minute, SizeBytes: 4 << 30, SourceFile: "00801.mpls", MakeMKVMainFeature: true},
	}}
	now := time.Now()
	return &fakeBackend{
		jobs: map[int64]*jobs.Job{
			1: {ID: 1, Drive: "/dev/sr0", State: jobs.NeedsYou, Disc: disc, Titles: []int{0},
				Movie: &jobs.Movie{TMDbID: 603, Title: "The Matrix", Year: 1999}, Note: "title 0 is the longest", UpdatedAt: now},
			2: {ID: 2, Drive: "/dev/sr1", State: jobs.Ripping, Disc: disc, Titles: []int{0}, Progress: 0.42, UpdatedAt: now},
			3: {ID: 3, Drive: "/dev/sr0", State: jobs.Failed, FailedIn: jobs.Verifying, Note: "verify failed: <b>boom</b>", UpdatedAt: now},
			4: {ID: 4, Drive: "/dev/sr0", State: jobs.Done, Disc: disc, UpdatedAt: now},
		},
		health: []app.Check{{Name: "database", OK: true, Detail: "open"}, {Name: "radarr", OK: true, Detail: "version 5"}},
	}
}

func (f *fakeBackend) Job(_ context.Context, id int64) (*jobs.Job, error) {
	if j, ok := f.jobs[id]; ok {
		return j, nil
	}
	return nil, jobs.ErrNotFound
}
func (f *fakeBackend) Events(_ context.Context, id int64) ([]jobs.Event, error) {
	if e, ok := f.events[id]; ok {
		return e, nil
	}
	return []jobs.Event{{To: jobs.Detected}, {From: jobs.Detected, To: jobs.Scanning}}, nil
}
func (f *fakeBackend) ActiveJobs(context.Context) ([]*jobs.Job, error) {
	ids := f.active
	if ids == nil {
		ids = []int64{1, 2}
	}
	var out []*jobs.Job
	for _, id := range ids {
		out = append(out, f.jobs[id])
	}
	return out, nil
}
func (f *fakeBackend) RecentJobs(context.Context, int) ([]*jobs.Job, error) {
	return []*jobs.Job{f.jobs[3], f.jobs[4]}, nil
}
func (f *fakeBackend) DriveList() []string       { return []string{"/dev/sr0", "/dev/sr1"} }
func (f *fakeBackend) Pending(drive string) bool { return drive == "/dev/sr0" }
func (f *fakeBackend) DiscInserted(_ context.Context, d string) (*jobs.Job, error) {
	if f.failNext != nil {
		return nil, f.failNext
	}
	f.scanned = d
	return &jobs.Job{ID: 9}, nil
}
func (f *fakeBackend) Approve(_ context.Context, id int64, titles []int, m *jobs.Movie, l *settings.Languages) error {
	if f.failNext != nil {
		return f.failNext
	}
	f.approved, f.movie, f.discLangs = titles, m, l
	return nil
}
func (f *fakeBackend) SearchMovies(_ context.Context, term string) ([]jobs.Movie, error) {
	f.searched = term
	if term == "broken" {
		return nil, errors.New("radarr is down")
	}
	if term == "spider" {
		return []jobs.Movie{{TMDbID: 559, Title: "Spider-Man 3", Year: 2007}, {TMDbID: 557, Title: "Spider-Man", Year: 2002}}, nil
	}
	return nil, nil
}
func (f *fakeBackend) Retry(_ context.Context, id int64) error { f.retried = id; return f.failNext }
func (f *fakeBackend) Cancel(context.Context, int64) error     { return f.failNext }
func (f *fakeBackend) LookupMovie(_ context.Context, id int) (*jobs.Movie, error) {
	if id == 559 {
		return &jobs.Movie{TMDbID: 559, Title: "Spider-Man 3", Year: 2007}, nil
	}
	return nil, errors.New("TMDb has no such movie")
}
func (f *fakeBackend) Health(context.Context) []app.Check { return f.health }

func (f *fakeBackend) Settings(context.Context) (settings.Settings, error) {
	if f.settingsErr != nil {
		return settings.Defaults(), f.settingsErr
	}
	if f.settings == nil {
		return settings.Defaults(), nil
	}
	return *f.settings, nil
}

func (f *fakeBackend) SaveSettings(_ context.Context, st settings.Settings) error {
	if st.Languages != nil {
		if err := st.Languages.Validate(); err != nil {
			return err
		}
	}
	if st.AutoImport != "" {
		if err := st.AutoImport.Validate(); err != nil {
			return err
		}
	}
	f.settings = &st
	return nil
}

func (f *fakeBackend) Poster(tmdbID int) (string, string, bool) {
	p, ok := f.posters[tmdbID]
	return p, "image/png", ok
}

func (f *fakeBackend) Live(d string) (makemkv.Live, bool) {
	l, ok := f.live[d]
	return l, ok
}

func (f *fakeBackend) DriveState(d string) drive.State { return f.trays[d] }

func (f *fakeBackend) DiscIndex() *discdb.Index { return f.discIndex }

func (f *fakeBackend) TrialTally(context.Context) (jobs.TrialTally, error) { return f.trial, nil }

func (f *fakeBackend) Diagnostics(name string) (string, bool) {
	dir, ok := f.bundles[name]
	return dir, ok
}

const user, pass = "dan", "correct horse battery"

func newServer(t *testing.T, b *fakeBackend) *httptest.Server {
	t.Helper()
	h, err := New(b, Options{Username: user, Password: pass, Version: "v0.1-test",
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

// client never follows redirects, so tests can check where they point.
var client = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func do(t *testing.T, srv *httptest.Server, method, path string, form url.Values, auth bool, headers map[string]string) (*http.Response, string) {
	t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, _ := http.NewRequest(method, srv.URL+path, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if auth {
		req.SetBasicAuth(user, pass)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func get(t *testing.T, srv *httptest.Server, path string) (*http.Response, string) {
	return do(t, srv, "GET", path, nil, true, nil)
}

func TestLoginIsRequired(t *testing.T) {
	srv := newServer(t, newBackend())
	resp, _ := do(t, srv, "GET", "/", nil, false, nil)
	if resp.StatusCode != 401 || !strings.Contains(resp.Header.Get("WWW-Authenticate"), "Basic") {
		t.Errorf("no login: %d %q", resp.StatusCode, resp.Header.Get("WWW-Authenticate"))
	}
	req, _ := http.NewRequest("GET", srv.URL+"/", nil)
	req.SetBasicAuth(user, "wrong password")
	if resp, _ := client.Do(req); resp.StatusCode != 401 {
		t.Errorf("wrong password: %d", resp.StatusCode)
	}
	if resp, _ := get(t, srv, "/"); resp.StatusCode != 200 {
		t.Errorf("right login: %d", resp.StatusCode)
	}
	// Health endpoints work without a login, for container health checks.
	if resp, _ := do(t, srv, "GET", "/healthz", nil, false, nil); resp.StatusCode != 200 {
		t.Errorf("/healthz: %d", resp.StatusCode)
	}
}

func TestNoAuthNeedsToBeExplicit(t *testing.T) {
	if _, err := New(newBackend(), Options{}); err == nil {
		t.Error("built a UI with no login and no NoAuth")
	}
	if _, err := New(newBackend(), Options{NoAuth: true}); err != nil {
		t.Error(err)
	}
}

func TestDashboardShowsTheJobs(t *testing.T) {
	srv := newServer(t, newBackend())
	resp, body := get(t, srv, "/")
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	for _, want := range []string{
		// The waiting job, named by its movie, with its year and quality.
		"Needs you: check and approve", "The Matrix", "1999 · 4K", `href="/jobs/1"`,
		// The ripping job, named by its disc until the movie is known.
		"Ripping 42%", "Sample Movie", `<progress class="bar" max="100" value="42">`,
		"Drive sr0", "Drive sr1", "new disc waiting", "v0.1-test",
		// Recent jobs say how they ended, and a failure why, escaped.
		"In Radarr", "Failed: verify failed: &lt;b&gt;boom&lt;/b&gt;",
		`src="/static/htmx.min.js"`, `hx-get="/fragments/jobs"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard is missing %q", want)
		}
	}
	if strings.Contains(body, "<b>boom</b>") {
		t.Error("a job note was not escaped")
	}
}

// A drive without a job shows what its tray is doing, and the Scan button.
func TestDashboardShowsAnIdleDrivesTray(t *testing.T) {
	for state, want := range map[drive.State]string{
		drive.TrayOpen: "Tray open", drive.NoDisc: "Empty", drive.NotReady: "Disc loading",
		drive.DiscOK: "Disc in, not started", drive.Unknown: "Waiting for a disc",
	} {
		b := newBackend()
		b.active = []int64{2} // only sr1 is busy
		b.trays = map[string]drive.State{"/dev/sr0": state}
		_, body := get(t, newServer(t, b), "/")
		if !strings.Contains(body, want) || !strings.Contains(body, "Scan disc") || !strings.Contains(body, `value="/dev/sr0"`) {
			t.Errorf("tray %v: the idle drive doesn't say %q with a Scan button", state, want)
		}
	}
}

// Jobs are named, never numbered (decision D22). The number stays in the
// address only.
func TestNoJobNumbersShown(t *testing.T) {
	srv := newServer(t, newBackend())
	for _, page := range []string{"/", "/jobs/1", "/jobs/2", "/jobs/3"} {
		_, body := get(t, srv, page)
		text := html.UnescapeString(regexp.MustCompile(`<[^>]*>`).ReplaceAllString(body, " "))
		if regexp.MustCompile(`#\s*\d`).MatchString(text) || strings.Contains(text, "Job ") {
			t.Errorf("%s shows a job number", page)
		}
	}
}

// The ripping page shows the steps, the time so far and left, and the speeds
// the watchdog measured (decisions D29, D30).
func TestRippingPageShowsTimeAndSpeeds(t *testing.T) {
	b := newBackend()
	now := time.Now()
	b.events = map[int64][]jobs.Event{2: {
		{At: now.Add(-25 * time.Minute), To: jobs.Detected},
		{At: now.Add(-25 * time.Minute), From: jobs.Detected, To: jobs.Scanning},
		{At: now.Add(-22 * time.Minute), From: jobs.Scanning, To: jobs.Identifying},
		{At: now.Add(-22 * time.Minute), From: jobs.Identifying, To: jobs.Selecting},
		{At: now.Add(-22 * time.Minute), From: jobs.Selecting, To: jobs.NeedsYou},
		{At: now.Add(-18 * time.Minute), From: jobs.NeedsYou, To: jobs.Ripping},
	}}
	b.live = map[string]makemkv.Live{"/dev/sr1": {ReadRate: 27e6, WriteRate: 26.4e6, ReadKnown: true, Verdict: makemkv.Healthy}}
	_, body := get(t, newServer(t, b), "/jobs/2")
	for _, want := range []string{
		`<li class="done"><span>Read the disc</span> <span class="muted small">3 min</span>`, // the scan, not the wait
		`<li class="now"><span>Rip</span>`, `<li class="next"><span>Check the rip</span>`,
		"<dt>Elapsed</dt><dd>18 min</dd>",
		"<dt>Left</dt><dd>about 25 min</dd>", // 18 min for 42%: 25 more for 58%
		"<dt>Reading</dt><dd>27 MB/s</dd>", "<dt>Writing</dt><dd>26 MB/s</dd>",
		`<dd class="verdict healthy">healthy</dd>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("ripping page is missing %q", want)
		}
	}

	// Before the watchdog's first report: no made-up speeds.
	b.live = nil
	if _, body := get(t, newServer(t, b), "/jobs/2"); !strings.Contains(body, "measuring") || strings.Contains(body, "MB/s") {
		t.Error("without a report, the page should say the speed is being measured")
	}
}

// A failed job shows where it failed and its note, escaped.
func TestFailedJobPage(t *testing.T) {
	_, body := get(t, newServer(t, newBackend()), "/jobs/3")
	if !strings.Contains(body, `<li class="failed"><span>Check the rip</span>`) || !strings.Contains(body, "Retry") {
		t.Error("the failed job doesn't show the failed step and Retry")
	}
	if strings.Contains(body, "<b>boom</b>") || !strings.Contains(body, "&lt;b&gt;boom&lt;/b&gt;") {
		t.Error("a job note was not escaped")
	}
}

// A failure is said in plain words with what to do, and the details are
// folded under it: the note, MakeMKV's messages with known errors marked,
// and the diagnostics download (decisions D35-D37).
func TestFailurePage(t *testing.T) {
	b := newBackend()
	b.jobs[3].Failure = &jobs.Failure{Kind: jobs.KindRipStalled, Diagnostics: "job-3-20261116-010203",
		Messages: []makemkv.Message{
			{Code: 1005, Text: "MakeMKV v2.0.0 started"},
			{Code: 2003, Text: "Error 'Scsi error - <NOT READY>' occurred"},
		}}
	srv := newServer(t, b)
	_, body := get(t, srv, "/jobs/3")
	// A job retried after a stall still links the stall's diagnostics.
	b.jobs[2].Failure = &jobs.Failure{Diagnostics: "job-2-20261116-010203"}
	if _, body := get(t, srv, "/jobs/2"); !strings.Contains(body, `The watchdog stopped an earlier attempt: <a href="/jobs/2/diagnostics.zip" download>`) {
		t.Error("a retried job doesn't link its earlier stall's diagnostics")
	}
	for _, want := range []string{
		`<p class="failure-what">The rip stalled: MakeMKV kept reading the disc but stopped writing.</p>`,
		"the diagnostics under Details are what a MakeMKV bug report needs",
		`<details class="group details">`, "&lt;b&gt;boom&lt;/b&gt;", // the note, escaped
		`<li><code>1005</code> MakeMKV v2.0.0 started</li>`,
		`<li class="problem"><code>2003</code> Error &#39;Scsi error - &lt;NOT READY&gt;&#39; occurred</li>`,
		`<a href="/jobs/3/diagnostics.zip" download>Download the diagnostics (.zip)</a>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("failure page is missing %q", want)
		}
	}
	if _, body := get(t, srv, "/"); !strings.Contains(body, "Failed: The rip stalled") {
		t.Error("the dashboard doesn't say the failure in plain words")
	}

	// A kind named by its step, and an old failure with no kind recorded.
	b.jobs[3].Failure = &jobs.Failure{Kind: "import"}
	if _, body := get(t, srv, "/jobs/3"); !strings.Contains(body, "Radarr couldn&#39;t import the rip.") || strings.Contains(body, "diagnostics.zip") {
		t.Error("an import failure isn't explained, or offers diagnostics it doesn't have")
	}
	b.jobs[3].Failure = nil
	if _, body := get(t, srv, "/jobs/3"); !strings.Contains(body, `<p class="failure-what">verify failed: &lt;b&gt;boom&lt;/b&gt;</p>`) {
		t.Error("a failure without a kind should show its note's first line")
	}
}

// The diagnostics bundle comes as one .zip of the bundle's files, behind
// the login, and only for a job that has one.
func TestDiagnosticsDownload(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "summary.txt"), []byte("verdict: stalled"), 0o644)
	os.WriteFile(filepath.Join(dir, "debug-fd5.log"), []byte("MakeMKV debug"), 0o644)
	os.Mkdir(filepath.Join(dir, "sub"), 0o755) // not a file: left out
	b := newBackend()
	b.jobs[3].Failure = &jobs.Failure{Kind: jobs.KindRipStalled, Diagnostics: "job-3-20261116-010203"}
	b.bundles = map[string]string{"job-3-20261116-010203": dir}
	srv := newServer(t, b)

	resp, body := get(t, srv, "/jobs/3/diagnostics.zip")
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "application/zip" ||
		resp.Header.Get("Content-Disposition") != `attachment; filename="discern-job-3-20261116-010203.zip"` {
		t.Fatalf("download: %d %q %q", resp.StatusCode, resp.Header.Get("Content-Type"), resp.Header.Get("Content-Disposition"))
	}
	zr, err := zip.NewReader(strings.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		got[f.Name] = string(b)
	}
	if len(got) != 2 || got["summary.txt"] != "verdict: stalled" || got["debug-fd5.log"] != "MakeMKV debug" {
		t.Errorf("zip holds %v", got)
	}

	// A failure without a bundle is refused before the backend is asked,
	// even if it had something under an empty name.
	b.bundles[""] = dir
	b.jobs[4].Failure = &jobs.Failure{Kind: "import"}
	for path, want := range map[string]int{"/jobs/1/diagnostics.zip": 404, "/jobs/4/diagnostics.zip": 404, "/jobs/99/diagnostics.zip": 404} {
		if resp, _ := get(t, srv, path); resp.StatusCode != want {
			t.Errorf("%s: %d; want %d", path, resp.StatusCode, want)
		}
	}
	b.bundles = nil // the folder is gone
	if resp, _ := get(t, srv, "/jobs/3/diagnostics.zip"); resp.StatusCode != 404 {
		t.Errorf("a missing bundle: %d; want 404", resp.StatusCode)
	}
	if resp, _ := do(t, srv, "GET", "/jobs/3/diagnostics.zip", nil, false, nil); resp.StatusCode != 401 {
		t.Errorf("without the login: %d; want 401", resp.StatusCode)
	}
}

// Settings and the way back to all jobs are buttons, not bare links.
func TestNavigationLooksLikeButtons(t *testing.T) {
	srv := newServer(t, newBackend())
	for _, page := range []string{"/", "/jobs/1", "/settings"} {
		_, body := get(t, srv, page)
		if !strings.Contains(body, `<a href="/settings" class="nav button">Settings</a>`) {
			t.Errorf("%s: the Settings link isn't a button", page)
		}
		if page != "/" && !strings.Contains(body, `<a href="/" class="button">← All jobs</a>`) {
			t.Errorf("%s: the way back isn't a button", page)
		}
	}
}

// Posters come from Discern's cache, behind the login (decision D23).
func TestPosters(t *testing.T) {
	file := filepath.Join(t.TempDir(), "603.png")
	if err := os.WriteFile(file, []byte("\x89PNG\r\n\x1a\nfake"), 0o644); err != nil {
		t.Fatal(err)
	}
	b := newBackend()
	// 0 and -1 are there to show the handler refuses such ids before asking.
	b.posters = map[int]string{603: file, 0: file, -1: file}
	srv := newServer(t, b)

	if _, body := get(t, srv, "/"); !strings.Contains(body, `<img class="poster" src="/posters/603"`) {
		t.Error("the dashboard doesn't show The Matrix's poster")
	}
	if _, body := get(t, srv, "/jobs/2"); !strings.Contains(body, `<div class="poster none" aria-hidden="true">S</div>`) {
		t.Error("a job without a poster should show its name's first letter")
	}
	resp, body := get(t, srv, "/posters/603")
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/png" ||
		resp.Header.Get("Cache-Control") != "private, max-age=86400" || !strings.HasSuffix(body, "fake") {
		t.Errorf("poster: %d %q %q", resp.StatusCode, resp.Header.Get("Content-Type"), resp.Header.Get("Cache-Control"))
	}
	for _, bad := range []string{"/posters/604", "/posters/abc", "/posters/-1", "/posters/0"} {
		if resp, _ := get(t, srv, bad); resp.StatusCode != 404 {
			t.Errorf("%s: %d; want 404", bad, resp.StatusCode)
		}
	}
	if resp, _ := do(t, srv, "GET", "/posters/603", nil, false, nil); resp.StatusCode != 401 {
		t.Errorf("a poster without the login: %d; want 401", resp.StatusCode)
	}
}

func TestJobPageForApproval(t *testing.T) {
	srv := newServer(t, newBackend())
	_, body := get(t, srv, "/jobs/1")
	for _, want := range []string{
		`action="/jobs/1/approve"`, `<input type="hidden" name="tmdb_id" value="603">`,
		"The Matrix (1999)", "Wrong movie? Search",
		`<input type="checkbox" name="title" value="0" checked>`, // the suggestion is pre-ticked
		`<input type="checkbox" name="title" value="1">`,         // the other one isn't
		"1:58:00", "33.0 GiB", "00800.mpls", "4K", ">main<",
		"Suggested because title 0 is the longest.",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("job page is missing %q", want)
		}
	}
	if resp, _ := get(t, srv, "/jobs/99"); resp.StatusCode != 404 {
		t.Errorf("missing job: %d", resp.StatusCode)
	}
	if resp, _ := get(t, srv, "/jobs/abc"); resp.StatusCode != 404 {
		t.Errorf("bad id: %d", resp.StatusCode)
	}
}

// The selector's decision is shown: how sure it is, what holds it, its
// reasons, TheDiscDB's names for titles, and TheDiscDB's movie when it
// isn't the job's (decisions D43, D47, D49). Two cuts start unticked.
func TestJobPageShowsTheDecision(t *testing.T) {
	b := newBackend()
	j := b.jobs[1]
	j.Titles = nil
	j.Selection = &jobs.Selection{Titles: []int{}, Choices: []int{0, 1}, Confidence: jobs.Low,
		Reason: "two cuts", Holds: []string{"TheDiscDB lists 2 main titles: choose the one to keep"},
		Reasons: []string{"TheDiscDB knows this disc: Alien (1979)"},
		Labels:  map[int]jobs.TitleLabel{0: {Type: "MainMovie", Name: "Alien <Director's Cut>"}, 1: {Type: "DeletedScene"}},
		Movie:   &jobs.Movie{TMDbID: 348, Title: "Alien", Year: 1979}}
	srv := newServer(t, b)
	_, body := get(t, srv, "/jobs/1")
	for _, want := range []string{
		`<span class="badge ask">Check</span>`,
		"<li>TheDiscDB lists 2 main titles: choose the one to keep.</li>",
		"How Discern chose", "<li>TheDiscDB knows this disc: Alien (1979).</li>",
		`>Main movie</span> Alien &lt;Director&#39;s Cut&gt;</span>`, // escaped: names come from TheDiscDB
		">Deleted scene</span>",
		`<input type="checkbox" name="title" value="0">`, `<input type="checkbox" name="title" value="1">`,
		`TheDiscDB says this disc is <strong>Alien (1979)</strong>. <a href="/jobs/1?tmdb=348">Use it</a>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("job page is missing %q", want)
		}
	}
	if strings.Contains(body, "Suggested because") || strings.Contains(body, `name="title" value="0" checked`) {
		t.Error("the old note, or a pre-ticked cut")
	}
	b.failNext = jobs.ErrNoTitles
	if _, body := do(t, srv, "POST", "/jobs/1/approve", url.Values{}, true, nil); !strings.Contains(body, "Choose a cut: tick one of the main titles TheDiscDB names.") {
		t.Error("approving no cut didn't say to choose one")
	}

	// TheDiscDB agreeing with the job's movie is said, without the offer.
	j.Selection.Movie = &jobs.Movie{TMDbID: 603, Title: "The Matrix", Year: 1999}
	if _, body := get(t, srv, "/jobs/1"); !strings.Contains(body, "TheDiscDB knows this disc as this movie.") || strings.Contains(body, "Use it") {
		t.Error("TheDiscDB's agreement isn't shown, or the offer still is")
	}
}

func TestSentenceAndClassName(t *testing.T) {
	for in, want := range map[string]string{"title 3 is it": "Title 3 is it.", "Done.": "Done.", "": "", "é": "É."} {
		if got := sentence(in); got != want {
			t.Errorf("sentence(%q) = %q; want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{"MainMovie": "Main movie", "DeletedScene": "Deleted scene", "Extra": "Extra", "": ""} {
		if got := className(in); got != want {
			t.Errorf("className(%q) = %q; want %q", in, got, want)
		}
	}
}

func TestApprove(t *testing.T) {
	b := newBackend()
	srv := newServer(t, b)
	resp, _ := do(t, srv, "POST", "/jobs/1/approve", url.Values{"title": {"0", "1"}, "tmdb_id": {"603"}}, true, nil)
	if resp.StatusCode != 303 || resp.Header.Get("Location") != "/jobs/1" {
		t.Fatalf("approve: %d to %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if !slices.Equal(b.approved, []int{0, 1}) || b.movie != nil {
		t.Errorf("approved %v with movie %+v; want [0 1] and the movie unchanged", b.approved, b.movie)
	}

	// A different TMDb id looks the movie up and passes it on.
	do(t, srv, "POST", "/jobs/1/approve", url.Values{"title": {"0"}, "tmdb_id": {"559"}}, true, nil)
	if b.movie == nil || b.movie.Title != "Spider-Man 3" {
		t.Errorf("corrected movie = %+v", b.movie)
	}

	// Problems come back on the page, not as a crash.
	for _, form := range []url.Values{
		{"title": {"x"}},
		{"title": {"0"}, "tmdb_id": {"abc"}},
		{"title": {"0"}, "tmdb_id": {"12345"}}, // unknown to TMDb
	} {
		resp, body := do(t, srv, "POST", "/jobs/1/approve", form, true, nil)
		if resp.StatusCode < 400 || !strings.Contains(body, `class="alert"`) {
			t.Errorf("%v: status %d, no error shown", form, resp.StatusCode)
		}
	}
	b.failNext = errors.New("choose at least one title")
	if _, body := do(t, srv, "POST", "/jobs/1/approve", url.Values{}, true, nil); !strings.Contains(body, "choose at least one title") {
		t.Error("the engine's refusal wasn't shown")
	}
}

// A wrong movie is found by name (decision D33): the results link back to
// the page with the pick, which the approve form then sends. Nothing is
// saved by searching or picking.
func TestReviewMovieSearch(t *testing.T) {
	b := newBackend()
	srv := newServer(t, b)
	_, body := get(t, srv, "/jobs/1?q=+spider+")
	if b.searched != "spider" {
		t.Errorf("searched %q; want the trimmed name", b.searched)
	}
	for _, want := range []string{
		`<details class="search" open>`, `value="spider"`,
		`<a href="/jobs/1?tmdb=559">`, "<strong>Spider-Man 3</strong>", "2007",
		`<a href="/jobs/1?tmdb=557">`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("search results are missing %q", want)
		}
	}
	if b.movie != nil || b.approved != nil {
		t.Error("searching changed the job")
	}

	_, body = get(t, srv, "/jobs/1?tmdb=559")
	for _, want := range []string{
		"Spider-Man 3", "Chosen here: it is saved when you rip.", "Keep The Matrix (1999)",
		`<input type="hidden" name="tmdb_id" value="559">`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the picked movie: page is missing %q", want)
		}
	}

	if _, body := get(t, srv, "/jobs/1?q=broken"); !strings.Contains(body, "The search failed: radarr is down") {
		t.Error("a failed search isn't explained")
	}
	if _, body := get(t, srv, "/jobs/1?q=nothing"); !strings.Contains(body, "No movie matches “nothing”.") {
		t.Error("an empty search isn't explained")
	}
	if _, body := get(t, srv, "/jobs/1?tmdb=12345"); !strings.Contains(body, "TMDb 12345 couldn&#39;t be looked up") ||
		!strings.Contains(body, `name="tmdb_id" value="603"`) {
		t.Error("an unknown id must be explained, keeping the movie Discern found")
	}
	// A rejected approval shows the page again with the pick still there.
	b.failNext = errors.New("choose at least one title")
	_, body = do(t, srv, "POST", "/jobs/1/approve", url.Values{"tmdb_id": {"559"}}, true, nil)
	if !strings.Contains(body, `name="tmdb_id" value="559"`) {
		t.Error("a rejected approval lost the movie picked on the page")
	}
}

// A search or pick sent from another website (an image tag, say) isn't
// run: it would make Discern ask Radarr and save posters.
func TestReviewSearchFromAnotherSiteIsIgnored(t *testing.T) {
	b := newBackend()
	srv := newServer(t, b)
	cross := map[string]string{"Sec-Fetch-Site": "cross-site"}
	_, body := do(t, srv, "GET", "/jobs/1?q=spider&tmdb=559", nil, true, cross)
	if b.searched != "" || strings.Contains(body, "Spider-Man") {
		t.Errorf("a cross-site search ran (searched %q)", b.searched)
	}
	if !strings.Contains(body, "The Matrix (1999)") {
		t.Error("the page itself should still show")
	}
	if do(t, srv, "GET", "/jobs/1?q=spider", nil, true, map[string]string{"Sec-Fetch-Site": "same-origin"}); b.searched != "spider" {
		t.Error("a search from Discern's own page didn't run")
	}
}

// A refused approval comes back as the user left it: their titles, this
// disc's languages and the switch, not the saved job's.
func TestARefusedApprovalKeepsTheForm(t *testing.T) {
	b := newBackend()
	srv := newServer(t, b)
	b.failNext = errors.New("choose at least one audio language")
	_, body := do(t, srv, "POST", "/jobs/1/approve", url.Values{"title": {"1"}, "disc_langs": {"on"}, "disc_commentary": {"on"}}, true, nil)
	for _, want := range []string{
		"choose at least one audio language",
		`<input type="checkbox" name="title" value="1" checked>`, `<input type="checkbox" name="title" value="0">`,
		`id="disc-langs" checked`, `name="disc_commentary" value="on" checked`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the page shown again is missing %q", want)
		}
	}
}

// A disc's own saved choice (before a Retry) is shown, switch on, so
// approving as shown keeps it.
func TestReviewShowsTheDiscsSavedLanguages(t *testing.T) {
	b := newBackend()
	b.jobs[1].Languages = &lang.Choice{Audio: []string{"eng"}, Commentary: true}
	_, body := get(t, newServer(t, b), "/jobs/1")
	if !strings.Contains(body, `<details class="group disc-langs" open>`) || !strings.Contains(body, `id="disc-langs" checked`) ||
		!strings.Contains(body, `name="disc_commentary" value="on" checked`) {
		t.Error("the disc's saved languages aren't shown")
	}
}

// Unreadable Settings are said on the review page, not shown as "All
// tracks": the rip would fail on them.
func TestReviewWithUnreadableSettings(t *testing.T) {
	b := newBackend()
	b.settingsErr = errors.New("settings row doesn't parse")
	_, body := get(t, newServer(t, b), "/jobs/1")
	if !strings.Contains(body, `<p class="alert">The Settings page&#39;s languages can&#39;t be read`) || strings.Contains(body, "All tracks") {
		t.Error("unreadable Settings aren't reported on the review page")
	}
}

// With no movie found, the search is offered as finding it, open.
func TestReviewWithoutAMovie(t *testing.T) {
	b := newBackend()
	b.jobs[1].Movie = nil
	_, body := get(t, newServer(t, b), "/jobs/1")
	if !strings.Contains(body, `<details class="search" open>`) || !strings.Contains(body, "<summary>Find the movie</summary>") ||
		strings.Contains(body, "Wrong movie?") || !strings.Contains(body, `name="tmdb_id" value=""`) {
		t.Error("without a movie, the page should offer to find it")
	}
}

// This disc's languages (decision D34): only with "Choose languages for this
// disc" ticked are they sent with the approval; otherwise Settings apply.
func TestReviewDiscLanguages(t *testing.T) {
	b := newBackend()
	b.settings = &settings.Settings{Languages: &settings.Languages{Audio: []string{"eng"}, Subtitles: []string{"eng"}, ForcedOnly: true}}
	srv := newServer(t, b)
	_, body := get(t, srv, "/jobs/1")
	for _, want := range []string{
		"Audio English · Subtitles English, forced only", "Change for this disc",
		`<fieldset class="disc-only" data-enabled-by="disc-langs">`, `<script src="/static/toggle.js" defer></script>`,
		`name="disc_audio" value="eng" checked`, // the title's own English, ticked as Settings have it
	} {
		if !strings.Contains(body, want) {
			t.Errorf("review page is missing %q", want)
		}
	}

	do(t, srv, "POST", "/jobs/1/approve", url.Values{"title": {"0"}, "disc_audio": {"eng"}}, true, nil)
	if b.discLangs != nil {
		t.Errorf("languages sent without the switch: %+v", b.discLangs)
	}
	do(t, srv, "POST", "/jobs/1/approve", url.Values{"title": {"0"}, "disc_langs": {"on"},
		"disc_audio": {"jpn", "eng"}, "disc_subtitles": {"eng"}, "disc_forced": {"on"}}, true, nil)
	want := &settings.Languages{Audio: []string{"jpn", "eng"}, Subtitles: []string{"eng"}, ForcedOnly: true}
	if !reflect.DeepEqual(b.discLangs, want) {
		t.Errorf("disc languages = %+v; want %+v", b.discLangs, want)
	}
}

// Another website can't make the user's browser press Discern's buttons.
func TestCrossSiteFormsAreRefused(t *testing.T) {
	b := newBackend()
	srv := newServer(t, b)
	evil := map[string]string{"Origin": "https://evil.example"}
	if resp, _ := do(t, srv, "POST", "/jobs/3/retry", url.Values{}, true, evil); resp.StatusCode != 403 {
		t.Errorf("cross-site POST: %d, want 403", resp.StatusCode)
	}
	if b.retried != 0 {
		t.Error("the cross-site retry went through")
	}
	if resp, _ := do(t, srv, "POST", "/jobs/3/retry", url.Values{}, true, map[string]string{"Sec-Fetch-Site": "cross-site"}); resp.StatusCode != 403 {
		t.Errorf("cross-site by Sec-Fetch-Site: %d", resp.StatusCode)
	}
	same := map[string]string{"Origin": srv.URL}
	if resp, _ := do(t, srv, "POST", "/jobs/3/retry", url.Values{}, true, same); resp.StatusCode != 303 || b.retried != 3 {
		t.Errorf("same-site POST: %d, retried %d", resp.StatusCode, b.retried)
	}
}

func TestActionsRedirectWithErrors(t *testing.T) {
	b := newBackend()
	srv := newServer(t, b)
	b.failNext = errors.New("the drive already has an active job")
	resp, _ := do(t, srv, "POST", "/jobs/3/retry", url.Values{}, true, nil)
	if loc := resp.Header.Get("Location"); !strings.HasPrefix(loc, "/jobs/3?error=") {
		t.Errorf("redirect = %q", loc)
	}
	resp, _ = do(t, srv, "POST", "/drives/scan", url.Values{"drive": {"/dev/sr0"}}, true, nil)
	if loc := resp.Header.Get("Location"); !strings.HasPrefix(loc, "/?error=") {
		t.Errorf("scan error redirect = %q", loc)
	}
	b.failNext = nil
	resp, _ = do(t, srv, "POST", "/drives/scan", url.Values{"drive": {"/dev/sr1"}}, true, nil)
	if resp.Header.Get("Location") != "/jobs/9" || b.scanned != "/dev/sr1" {
		t.Errorf("scan: %q, scanned %q", resp.Header.Get("Location"), b.scanned)
	}
}

func TestStatusFragmentReloadsOnceTheJobStops(t *testing.T) {
	srv := newServer(t, newBackend())
	resp, body := get(t, srv, "/fragments/jobs/2") // ripping
	if resp.Header.Get("HX-Refresh") != "" || !strings.Contains(body, `hx-trigger="every 2s"`) {
		t.Error("a running job's status should keep polling")
	}
	resp, _ = get(t, srv, "/fragments/jobs/1") // waiting for the user
	if resp.Header.Get("HX-Refresh") != "true" {
		t.Error("a job waiting for the user should reload the page to show the form")
	}
}

func TestReadyz(t *testing.T) {
	b := newBackend()
	srv := newServer(t, b)
	resp, body := do(t, srv, "GET", "/readyz", nil, false, nil)
	if resp.StatusCode != 200 || !strings.Contains(body, `"ok":true`) {
		t.Errorf("healthy: %d %s", resp.StatusCode, body)
	}
	b.health = append(b.health, app.Check{Name: "makemkvcon", OK: false, Detail: "not found"})
	if resp, _ := do(t, srv, "GET", "/readyz", nil, false, nil); resp.StatusCode != 503 {
		t.Errorf("unhealthy: %d, want 503", resp.StatusCode)
	}
}

func TestAPIJobs(t *testing.T) {
	srv := newServer(t, newBackend())
	resp, body := get(t, srv, "/api/v1/jobs")
	var got struct {
		Active []apiJob `json:"active"`
		Recent []apiJob `json:"recent"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil || resp.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("%v: %s", err, body)
	}
	if len(got.Active) != 2 || got.Active[1].Progress != 0.42 || len(got.Recent) != 2 {
		t.Errorf("api = %+v", got)
	}
}

func TestSecurityHeadersAndStaticFiles(t *testing.T) {
	srv := newServer(t, newBackend())
	resp, _ := get(t, srv, "/")
	csp := resp.Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "script-src 'self'") || !strings.Contains(csp, "frame-ancestors 'none'") ||
		resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("headers: CSP %q, nosniff %q", csp, resp.Header.Get("X-Content-Type-Options"))
	}
	for _, f := range []string{"/static/htmx.min.js", "/static/style.css"} {
		if resp, body := get(t, srv, f); resp.StatusCode != 200 || len(body) < 100 {
			t.Errorf("%s: %d, %d bytes", f, resp.StatusCode, len(body))
		}
	}
}

// Nothing in the pages may be inline (a script or a style attribute): the
// Content-Security-Policy would block it.
func TestPagesHaveNoInlineScriptOrStyle(t *testing.T) {
	srv := newServer(t, newBackend())
	for _, p := range []string{"/", "/jobs/1", "/jobs/2", "/jobs/3"} {
		_, body := get(t, srv, p)
		if strings.Contains(body, "style=") || strings.Contains(body, "<script>") || strings.Contains(body, "onclick=") {
			t.Errorf("%s has inline style or script", p)
		}
	}
}

// /readyz is outside the login, so it must not show error details.
func TestReadyzHidesDetails(t *testing.T) {
	b := newBackend()
	b.health = []app.Check{{Name: "radarr", OK: false, Detail: "dial tcp 192.0.2.10:7878: connection refused"}}
	srv := newServer(t, b)
	resp, body := do(t, srv, "GET", "/readyz", nil, false, nil)
	if resp.StatusCode != 503 || !strings.Contains(body, `"name":"radarr"`) {
		t.Errorf("readyz: %d %s", resp.StatusCode, body)
	}
	if strings.Contains(body, "192.0.2.10") || strings.Contains(body, "detail") {
		t.Errorf("readyz leaks details: %s", body)
	}
	// The logged-in dashboard still shows them.
	if _, body := get(t, srv, "/fragments/health"); !strings.Contains(body, "connection refused") {
		t.Error("the dashboard's health fragment lost the details")
	}
}

// The System list says whether TheDiscDB's index is there; it isn't a
// readiness check, since Discern works without it.
func TestHealthShowsTheDiscIndex(t *testing.T) {
	b := newBackend()
	srv := newServer(t, b)
	if _, body := get(t, srv, "/fragments/health"); !strings.Contains(body, "TheDiscDB: not downloaded yet") {
		t.Error("a missing index isn't said")
	}
	if resp, _ := do(t, srv, "GET", "/readyz", nil, false, nil); resp.StatusCode != 200 {
		t.Errorf("readyz without an index: %d; want 200", resp.StatusCode)
	}
	// Built at midday UTC: the same date in nearly every time zone.
	b.discIndex = &discdb.Index{Built: time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC), Discs: make([]discdb.Disc, 3112)}
	if _, body := get(t, srv, "/fragments/health"); !strings.Contains(body, "TheDiscDB: 3112 discs known, from 16 Nov 2026.") {
		t.Errorf("the index isn't shown")
	}
}

// The System list says the auto-import mode and how its trial went, with
// links to the jobs where the user chose differently (decision D62).
func TestHealthShowsAutoImport(t *testing.T) {
	b := newBackend()
	srv := newServer(t, b)
	if _, body := get(t, srv, "/fragments/health"); !strings.Contains(body, "Auto-import: off.") {
		t.Error("auto-import off isn't said")
	}
	b.settings = &settings.Settings{AutoImport: settings.AutoTrial}
	b.trial = jobs.TrialTally{Agreed: 6, Differed: []int64{12, 9}}
	_, body := get(t, srv, "/fragments/health")
	for _, want := range []string{"Auto-import: on trial.", "on 6 of 8 discs",
		`you chose differently on <a href="/jobs/12">job 12</a>, <a href="/jobs/9">job 9</a>.`} {
		if !strings.Contains(body, want) {
			t.Errorf("trial: missing %q in %s", want, body)
		}
	}
	// Only the newest five are linked.
	b.trial = jobs.TrialTally{Differed: []int64{20, 19, 18, 17, 16, 15, 14}}
	_, body = get(t, srv, "/fragments/health")
	if !strings.Contains(body, `<a href="/jobs/16">job 16</a> and 2 older.`) || strings.Contains(body, "/jobs/15") {
		t.Errorf("the differing jobs aren't capped: %s", body)
	}
	b.settings = &settings.Settings{AutoImport: settings.AutoOn}
	b.trial = jobs.TrialTally{Agreed: 1}
	if _, body := get(t, srv, "/fragments/health"); !strings.Contains(body, "Auto-import: on.") || !strings.Contains(body, "on 1 of 1 disc.") {
		t.Errorf("on: %s", body)
	}
}

func TestRepeatedFailedLoginsAreLockedOut(t *testing.T) {
	srv := newServer(t, newBackend())
	try := func(password string) int {
		req, _ := http.NewRequest("GET", srv.URL+"/", nil)
		req.SetBasicAuth(user, password)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	// A first request without credentials doesn't count.
	do(t, srv, "GET", "/", nil, false, nil)
	for i := 1; i < maxFailures; i++ {
		if got := try("guess"); got != 401 {
			t.Fatalf("attempt %d: %d, want 401", i, got)
		}
	}
	try("guess") // the 10th failure starts the lockout
	if got := try(pass); got != 429 {
		t.Errorf("right password during lockout: %d, want 429", got)
	}
}

func TestLoginGuardLockoutEnds(t *testing.T) {
	g := newLoginGuard()
	now := time.Now()
	g.now = func() time.Time { return now }
	for range maxFailures {
		g.fail("1.2.3.4")
	}
	if g.blocked("1.2.3.4") == 0 || g.blocked("5.6.7.8") != 0 {
		t.Fatal("lockout should apply to the failing address only")
	}
	now = now.Add(lockout + time.Second)
	if g.blocked("1.2.3.4") != 0 {
		t.Error("lockout didn't end")
	}
	if n := g.fail("1.2.3.4"); n != 1 {
		t.Errorf("count after the lockout = %d, want a fresh start", n)
	}
}

func TestSettingsPage(t *testing.T) {
	b := newBackend()
	srv := newServer(t, b)
	same := map[string]string{"Origin": srv.URL}

	resp, body := do(t, srv, "GET", "/settings", nil, true, nil)
	if resp.StatusCode != 200 || !strings.Contains(body, `value="default" checked`) || !strings.Contains(body, `name="eject" value="on" checked`) {
		t.Fatalf("settings page: %d; want MakeMKV's default and eject on", resp.StatusCode)
	}

	form := url.Values{"eject": nil, "languages": {"chosen"}, "audio": {"fre", "eng"}, "subtitles": {"eng"}, "forced_only": {"on"}}
	if resp, _ := do(t, srv, "POST", "/settings", form, true, same); resp.StatusCode != 303 {
		t.Fatalf("save: %d", resp.StatusCode)
	}
	got := b.settings
	if got == nil || got.Eject || got.Languages == nil || strings.Join(got.Languages.Audio, ",") != "eng,fre" || !got.Languages.ForcedOnly {
		t.Fatalf("saved %+v; want eject off, audio in the page's order (eng, fre), forced only", got)
	}

	// No audio chosen: refused, with the boxes kept as submitted.
	form = url.Values{"languages": {"chosen"}, "subtitles": {"eng"}}
	resp, body = do(t, srv, "POST", "/settings", form, true, same)
	if resp.StatusCode != 422 || !strings.Contains(body, "at least one audio language") ||
		!strings.Contains(body, `name="subtitles" value="eng" checked`) {
		t.Errorf("bad languages: %d; want 422 with the error and the form kept", resp.StatusCode)
	}

	// Auto-import: off is checked at first; trial saves and shows; an
	// unknown mode is refused.
	if _, body := do(t, srv, "GET", "/settings", nil, true, nil); !strings.Contains(body, `name="auto_import" value="off" checked`) {
		t.Error("auto-import isn't off at first")
	}
	if resp, _ := do(t, srv, "POST", "/settings", url.Values{"languages": {"default"}, "auto_import": {"trial"}}, true, same); resp.StatusCode != 303 || b.settings.AutoImport != settings.AutoTrial {
		t.Errorf("save trial: %d, saved %q", resp.StatusCode, b.settings.AutoImport)
	}
	if _, body := do(t, srv, "GET", "/settings", nil, true, nil); !strings.Contains(body, `name="auto_import" value="trial" checked`) {
		t.Error("the page doesn't show trial as saved")
	}
	if resp, _ := do(t, srv, "POST", "/settings", url.Values{"languages": {"default"}, "auto_import": {"always"}}, true, same); resp.StatusCode != 422 || b.settings.AutoImport != settings.AutoTrial {
		t.Errorf("an unknown mode: %d, saved %q", resp.StatusCode, b.settings.AutoImport)
	}

	// Back to MakeMKV's default.
	if resp, _ := do(t, srv, "POST", "/settings", url.Values{"languages": {"default"}, "eject": {"on"}}, true, same); resp.StatusCode != 303 {
		t.Fatalf("save default: %d", resp.StatusCode)
	}
	if b.settings.Languages != nil || !b.settings.Eject {
		t.Errorf("saved %+v; want no languages and eject on", b.settings)
	}

	// The preferred language goes first, whatever the page's order, and
	// counts as ticked.
	form = url.Values{"languages": {"chosen"}, "audio": {"eng", "fre"}, "first_audio": {"ger"}}
	do(t, srv, "POST", "/settings", form, true, same)
	if got := strings.Join(b.settings.Languages.Audio, ","); got != "ger,eng,fre" {
		t.Errorf("audio = %s; want the preferred language first: ger,eng,fre", got)
	}
	if _, body := do(t, srv, "GET", "/settings", nil, true, nil); !strings.Contains(body, `<option value="ger" selected>`) {
		t.Error("the page doesn't show the saved preferred language")
	}

	// With MakeMKV's default on, the page disables the language choices, so
	// they aren't sent. A browser that sends them anyway (the script didn't
	// run) gets the default saved, and the page says the ticks weren't.
	form = url.Values{"languages": {"default"}, "audio": {"eng"}}
	resp, _ = do(t, srv, "POST", "/settings", form, true, same)
	if loc := resp.Header.Get("Location"); resp.StatusCode != 303 || loc != "/settings?saved=ignored" || b.settings.Languages != nil {
		t.Errorf("default with ticked languages: %d to %q, saved %+v", resp.StatusCode, loc, b.settings.Languages)
	}
	if _, body := do(t, srv, "GET", "/settings?saved=ignored", nil, true, nil); !strings.Contains(body, "the ticked languages were not saved") {
		t.Error("the page doesn't say the ticked languages were ignored")
	}
	form = url.Values{"languages": {"default"}}
	if resp, _ = do(t, srv, "POST", "/settings", form, true, same); resp.Header.Get("Location") != "/settings?saved=1" {
		t.Errorf("default with nothing ticked: to %q; want the ordinary notice", resp.Header.Get("Location"))
	}

	// The script that disables the group is a file of Discern's own: the
	// Content-Security-Policy allows no inline script.
	_, page := do(t, srv, "GET", "/settings", nil, true, nil)
	if !strings.Contains(page, `<script src="/static/toggle.js" defer></script>`) ||
		!strings.Contains(page, `<fieldset class="chosen-only" data-enabled-by="languages-chosen">`) ||
		!strings.Contains(page, `id="languages-chosen"`) {
		t.Error("the page doesn't load toggle.js or has no group for it to disable")
	}
	if resp, js := do(t, srv, "GET", "/static/toggle.js", nil, true, nil); resp.StatusCode != 200 || !strings.Contains(js, "data-enabled-by") {
		t.Errorf("/static/toggle.js: %d", resp.StatusCode)
	}

	// Like every change, a cross-site POST is refused.
	if resp, _ := do(t, srv, "POST", "/settings", url.Values{"languages": {"default"}}, true, map[string]string{"Origin": "https://evil.example"}); resp.StatusCode != 403 {
		t.Errorf("cross-site save: %d, want 403", resp.StatusCode)
	}
}

// Settings that can't be read don't lock the page: it opens with the
// defaults and the reason, and saving it replaces them.
func TestSettingsPageOpensWhenTheSettingsCantBeRead(t *testing.T) {
	b := newBackend()
	b.settingsErr = errors.New("setting eject: invalid character 'y'")
	srv := newServer(t, b)
	resp, body := do(t, srv, "GET", "/settings", nil, true, nil)
	if resp.StatusCode != 200 || !strings.Contains(body, "can&#39;t be read") || !strings.Contains(body, `name="eject" value="on" checked`) {
		t.Fatalf("settings page: %d; want the page with the defaults and the reason", resp.StatusCode)
	}
	b.settingsErr = nil
	if resp, _ := do(t, srv, "POST", "/settings", url.Values{"languages": {"default"}}, true, map[string]string{"Origin": srv.URL}); resp.StatusCode != 303 || b.settings == nil {
		t.Errorf("saving over them: %d", resp.StatusCode)
	}
}

// The title list names the audio languages, each once, whichever code form
// the scan used; a code Discern can't name is shown as it is.
func TestAudioNamesTheLanguages(t *testing.T) {
	audio := funcs["audio"].(func(makemkv.Title) string)
	var streams []makemkv.Stream
	for _, l := range []string{"eng", "deu", "ger", "fra", "und", "", "qaa"} {
		streams = append(streams, makemkv.Stream{Type: "Audio", Lang: l})
	}
	streams = append(streams, makemkv.Stream{Type: "Subtitles", Lang: "spa"})
	if got, want := audio(makemkv.Title{Streams: streams}), "English, German, French, qaa"; got != want {
		t.Errorf("audio = %q; want %q", got, want)
	}
}

// The engine says only that the job has no movie; the page, where the form
// is, says how to choose one.
func TestApproveWithoutAMovieSaysHowToChooseOne(t *testing.T) {
	b := newBackend()
	b.failNext = fmt.Errorf("approving: %w", jobs.ErrNoMovie)
	srv := newServer(t, b)
	resp, body := do(t, srv, "POST", "/jobs/1/approve", url.Values{"title": {"0"}}, true, map[string]string{"Origin": srv.URL})
	if resp.StatusCode != 422 || !strings.Contains(body, "search for it by name above the titles") {
		t.Errorf("approve without a movie: %d; want 422 and how to choose the movie", resp.StatusCode)
	}
}
