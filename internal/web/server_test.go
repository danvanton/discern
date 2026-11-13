package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/danvanton/discern/internal/app"
	"github.com/danvanton/discern/internal/jobs"
	"github.com/danvanton/discern/internal/makemkv"
)

// fakeBackend holds a few jobs and records what the UI asked it to do.
type fakeBackend struct {
	jobs     map[int64]*jobs.Job
	approved []int
	movie    *jobs.Movie
	retried  int64
	scanned  string
	health   []app.Check
	failNext error
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
func (f *fakeBackend) Events(context.Context, int64) ([]jobs.Event, error) {
	return []jobs.Event{{To: jobs.Detected}, {From: jobs.Detected, To: jobs.Scanning}}, nil
}
func (f *fakeBackend) ActiveJobs(context.Context) ([]*jobs.Job, error) {
	return []*jobs.Job{f.jobs[1], f.jobs[2]}, nil
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
func (f *fakeBackend) Approve(_ context.Context, id int64, titles []int, m *jobs.Movie) error {
	if f.failNext != nil {
		return f.failNext
	}
	f.approved, f.movie = titles, m
	return nil
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
		"Needs you", "The Matrix (1999)", `href="/jobs/1"`, // the waiting job
		`<progress class="bar" max="100" value="42">`, // the ripping job's progress
		"Scan disc", "/dev/sr1", "v0.1-test", "new disc waiting",
		`src="/static/htmx.min.js"`, `hx-get="/fragments/jobs"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard is missing %q", want)
		}
	}
	// Notes are escaped, never rendered as HTML.
	if strings.Contains(body, "<b>boom</b>") || !strings.Contains(body, "&lt;b&gt;boom&lt;/b&gt;") {
		t.Error("a job note was not escaped")
	}
}

func TestJobPageForApproval(t *testing.T) {
	srv := newServer(t, newBackend())
	_, body := get(t, srv, "/jobs/1")
	for _, want := range []string{
		`action="/jobs/1/approve"`, `name="tmdb_id"`, `value="603"`,
		`name="title" value="0" aria-label="Rip title 0" checked`, // the suggestion is pre-ticked
		`name="title" value="1" aria-label="Rip title 1">`,        // the other one isn't
		"1:58:00", "33.0 GiB", "00800.mpls", "HEVC 3840x2160", ">main<",
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
