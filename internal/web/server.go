// Package web serves Discern's UI: server-rendered pages (html/template),
// with htmx refreshing the parts that change, such as rip progress
// (decision D4). Everything, templates and htmx included, is built into the
// binary.
package web

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/danvanton/discern/internal/app"
	"github.com/danvanton/discern/internal/jobs"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

// Backend is what the UI needs from the rest of Discern. *app.App provides
// it; tests use a fake.
type Backend interface {
	Job(ctx context.Context, id int64) (*jobs.Job, error)
	Events(ctx context.Context, id int64) ([]jobs.Event, error)
	ActiveJobs(ctx context.Context) ([]*jobs.Job, error)
	RecentJobs(ctx context.Context, n int) ([]*jobs.Job, error)
	DriveList() []string
	Pending(drive string) bool
	DiscInserted(ctx context.Context, drive string) (*jobs.Job, error)
	Approve(ctx context.Context, id int64, titles []int, movie *jobs.Movie) error
	Retry(ctx context.Context, id int64) error
	Cancel(ctx context.Context, id int64) error
	LookupMovie(ctx context.Context, tmdbID int) (*jobs.Movie, error)
	Health(ctx context.Context) []app.Check
}

// Options configures the server.
type Options struct {
	Username, Password string
	NoAuth             bool // development only
	Version            string
	Log                *slog.Logger
}

type server struct {
	b      Backend
	opt    Options
	pages  map[string]*template.Template
	log    *slog.Logger
	guards *loginGuard
}

// New returns the UI's handler.
func New(b Backend, opt Options) (http.Handler, error) {
	if !opt.NoAuth && (opt.Username == "" || opt.Password == "") {
		return nil, errors.New("web UI needs a username and password, or NoAuth for development")
	}
	if opt.Log == nil {
		opt.Log = slog.Default()
	}
	s := &server{b: b, opt: opt, pages: map[string]*template.Template{}, log: opt.Log, guards: newLoginGuard()}
	for _, page := range []string{"dashboard.html", "job.html"} {
		t, err := template.New("").Funcs(funcs).ParseFS(templateFS, "templates/layout.html", "templates/fragments.html", "templates/"+page)
		if err != nil {
			return nil, err
		}
		s.pages[page] = t
	}
	static, err := fs.Sub(staticFS, "static")
	if err != nil {
		return nil, err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.dashboard)
	mux.HandleFunc("GET /jobs/{id}", s.jobPage)
	mux.HandleFunc("GET /fragments/jobs", s.jobsFragment)
	mux.HandleFunc("GET /fragments/jobs/{id}", s.jobFragment)
	mux.HandleFunc("GET /fragments/health", s.healthFragment)
	mux.HandleFunc("POST /jobs/{id}/approve", s.approve)
	mux.HandleFunc("POST /jobs/{id}/retry", s.action(b.Retry))
	mux.HandleFunc("POST /jobs/{id}/cancel", s.action(b.Cancel))
	mux.HandleFunc("POST /drives/scan", s.scanDrive)
	mux.HandleFunc("GET /api/v1/jobs", s.apiJobs)
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))

	// Health endpoints sit outside the login, for container health checks.
	// They say whether Discern works, never anything secret.
	root := http.NewServeMux()
	root.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok\n")) })
	root.HandleFunc("GET /readyz", s.readyz)
	root.Handle("/", s.auth(sameOrigin(mux)))
	return securityHeaders(limitBody(root)), nil
}

// securityHeaders keeps the UI from being framed or loading anything from
// elsewhere. Nothing in it is inline, so the policy can be strict.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; "+
			"form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

// limitBody caps request bodies: the UI's forms are tiny.
func limitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		next.ServeHTTP(w, r)
	})
}

// auth is HTTP Basic authentication (v0.1; a login page comes later). Both
// values are hashed before comparing, so the comparison takes the same time
// whatever the input's length, and reveals nothing about the real values.
func (s *server) auth(next http.Handler) http.Handler {
	if s.opt.NoAuth {
		return next
	}
	wantUser := sha256.Sum256([]byte(s.opt.Username))
	wantPass := sha256.Sum256([]byte(s.opt.Password))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := clientIP(r)
		if wait := s.guards.blocked(ip); wait > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
			http.Error(w, "too many failed logins; try again later", http.StatusTooManyRequests)
			return
		}
		u, p, ok := r.BasicAuth()
		gotUser, gotPass := sha256.Sum256([]byte(u)), sha256.Sum256([]byte(p))
		userOK := subtle.ConstantTimeCompare(gotUser[:], wantUser[:]) == 1
		passOK := subtle.ConstantTimeCompare(gotPass[:], wantPass[:]) == 1
		if !ok || !userOK || !passOK {
			// A browser's first request carries no credentials at all; only
			// a wrong username or password counts as a failed attempt.
			if ok {
				n := s.guards.fail(ip)
				s.log.Warn("failed web login", "ip", ip, "user", u, "failures", n)
			}
			w.Header().Set("WWW-Authenticate", `Basic realm="Discern", charset="UTF-8"`)
			http.Error(w, "login required", http.StatusUnauthorized)
			return
		}
		s.guards.reset(ip)
		next.ServeHTTP(w, r)
	})
}

// loginGuard counts failed logins per client address and blocks an address
// after maxFailures in a row, for lockout. It makes guessing the password
// slow: 10 tries every 15 minutes instead of thousands a second.
//
// Behind a reverse proxy every request comes from the proxy's address, so a
// lockout then blocks everyone using that proxy for a while; that is the
// price of not trusting forwarded headers, which a client could fake.
type loginGuard struct {
	mu      sync.Mutex
	entries map[string]*failures
	now     func() time.Time
}

type failures struct {
	count int
	until time.Time // blocked until then
}

const (
	maxFailures = 10
	lockout     = 15 * time.Minute
)

func newLoginGuard() *loginGuard {
	return &loginGuard{entries: map[string]*failures{}, now: time.Now}
}

// blocked returns how long ip must still wait, or 0.
func (g *loginGuard) blocked(ip string) time.Duration {
	g.mu.Lock()
	defer g.mu.Unlock()
	if f := g.entries[ip]; f != nil {
		if wait := f.until.Sub(g.now()); wait > 0 {
			return wait
		}
	}
	return 0
}

// fail records a failed login and returns the count so far.
func (g *loginGuard) fail(ip string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	f := g.entries[ip]
	if f == nil || (!f.until.IsZero() && g.now().After(f.until)) {
		f = &failures{}
		g.entries[ip] = f
	}
	f.count++
	if f.count >= maxFailures {
		f.until = g.now().Add(lockout)
	}
	return f.count
}

func (g *loginGuard) reset(ip string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.entries, ip)
}

// clientIP is the address the connection came from, without the port.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// sameOrigin blocks cross-site request forgery: a browser sends Basic auth
// with any request to this host, including a form another website submits.
// Changes are only accepted from Discern's own pages, which a browser marks
// with an Origin header of this host (or Sec-Fetch-Site: same-origin).
// Requests with neither header come from scripts like curl, which don't send
// stored credentials by themselves.
func sameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && !fromThisSite(r) {
			http.Error(w, "this request came from another website and was refused", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func fromThisSite(r *http.Request) bool {
	if o := r.Header.Get("Origin"); o != "" {
		u, err := url.Parse(o)
		return err == nil && u.Host == r.Host
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" {
		return site == "same-origin" || site == "none"
	}
	return true
}

// Pages.

type driveView struct {
	Path    string
	Pending bool // a new disc is waiting for the drive's current job to end
}

type dashboardData struct {
	Version string
	Drives  []driveView
	Active  []*jobs.Job
	Waiting []*jobs.Job
	Recent  []*jobs.Job
	Error   string
}

func (s *server) dashboardData(ctx context.Context) (dashboardData, error) {
	active, err := s.b.ActiveJobs(ctx)
	if err != nil {
		return dashboardData{}, err
	}
	recent, err := s.b.RecentJobs(ctx, 20)
	if err != nil {
		return dashboardData{}, err
	}
	d := dashboardData{Version: s.opt.Version, Recent: recent}
	for _, p := range s.b.DriveList() {
		d.Drives = append(d.Drives, driveView{Path: p, Pending: s.b.Pending(p)})
	}
	for _, j := range active {
		if j.State == jobs.NeedsYou {
			d.Waiting = append(d.Waiting, j)
		} else {
			d.Active = append(d.Active, j)
		}
	}
	return d, nil
}

func (s *server) dashboard(w http.ResponseWriter, r *http.Request) {
	d, err := s.dashboardData(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	d.Error = r.URL.Query().Get("error")
	s.render(w, http.StatusOK, "dashboard.html", "layout", d)
}

func (s *server) jobsFragment(w http.ResponseWriter, r *http.Request) {
	d, err := s.dashboardData(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	s.render(w, http.StatusOK, "dashboard.html", "jobs", d)
}

type jobData struct {
	Version string
	Job     *jobs.Job
	Events  []jobs.Event
	Error   string
}

func (s *server) loadJob(w http.ResponseWriter, r *http.Request) (*jobs.Job, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return nil, false
	}
	j, err := s.b.Job(r.Context(), id)
	if errors.Is(err, jobs.ErrNotFound) {
		http.NotFound(w, r)
		return nil, false
	}
	if err != nil {
		s.fail(w, err)
		return nil, false
	}
	return j, true
}

func (s *server) jobPage(w http.ResponseWriter, r *http.Request) {
	s.showJob(w, r, http.StatusOK, r.URL.Query().Get("error"))
}

func (s *server) showJob(w http.ResponseWriter, r *http.Request, status int, msg string) {
	j, ok := s.loadJob(w, r)
	if !ok {
		return
	}
	events, err := s.b.Events(r.Context(), j.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.render(w, status, "job.html", "layout", jobData{Version: s.opt.Version, Job: j, Events: events, Error: msg})
}

func (s *server) jobFragment(w http.ResponseWriter, r *http.Request) {
	j, ok := s.loadJob(w, r)
	if !ok {
		return
	}
	// Once the job stops moving (it waits for the user, or ended), reload the
	// whole page: it then shows the approve form or the result, and stops
	// polling.
	if !j.State.Active() || j.State == jobs.NeedsYou {
		w.Header().Set("HX-Refresh", "true")
	}
	s.render(w, http.StatusOK, "job.html", "status", jobData{Job: j})
}

func (s *server) healthFragment(w http.ResponseWriter, r *http.Request) {
	s.render(w, http.StatusOK, "dashboard.html", "health", s.b.Health(r.Context()))
}

// Actions. Each one answers with a redirect (post, redirect, get), so the
// browser's back and reload buttons never repeat it.

func (s *server) approve(w http.ResponseWriter, r *http.Request) {
	j, ok := s.loadJob(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		s.showJob(w, r, http.StatusBadRequest, err.Error())
		return
	}
	var titles []int
	for _, v := range r.PostForm["title"] {
		n, err := strconv.Atoi(v)
		if err != nil {
			s.showJob(w, r, http.StatusBadRequest, fmt.Sprintf("%q is not a title number", v))
			return
		}
		titles = append(titles, n)
	}
	var movie *jobs.Movie
	if v := strings.TrimSpace(r.PostForm.Get("tmdb_id")); v != "" && (j.Movie == nil || v != strconv.Itoa(j.Movie.TMDbID)) {
		id, err := strconv.Atoi(v)
		if err != nil || id <= 0 {
			s.showJob(w, r, http.StatusUnprocessableEntity, fmt.Sprintf("%q is not a TMDb id", v))
			return
		}
		if movie, err = s.b.LookupMovie(r.Context(), id); err != nil {
			s.showJob(w, r, http.StatusUnprocessableEntity, fmt.Sprintf("looking up TMDb %d: %v", id, err))
			return
		}
	}
	if err := s.b.Approve(r.Context(), j.ID, titles, movie); err != nil {
		s.showJob(w, r, http.StatusUnprocessableEntity, err.Error())
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/jobs/%d", j.ID), http.StatusSeeOther)
}

func (s *server) action(do func(context.Context, int64) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		j, ok := s.loadJob(w, r)
		if !ok {
			return
		}
		target := fmt.Sprintf("/jobs/%d", j.ID)
		if err := do(r.Context(), j.ID); err != nil {
			target += "?error=" + url.QueryEscape(err.Error())
		}
		http.Redirect(w, r, target, http.StatusSeeOther)
	}
}

func (s *server) scanDrive(w http.ResponseWriter, r *http.Request) {
	drive := r.PostFormValue("drive")
	j, err := s.b.DiscInserted(r.Context(), drive)
	if err != nil {
		http.Redirect(w, r, "/?error="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/jobs/%d", j.ID), http.StatusSeeOther)
}

// API and health.

type apiJob struct {
	ID       int64       `json:"id"`
	Drive    string      `json:"drive"`
	State    jobs.State  `json:"state"`
	Movie    *jobs.Movie `json:"movie"`
	Titles   []int       `json:"titles"`
	Note     string      `json:"note"`
	Progress float64     `json:"progress"`
	Updated  time.Time   `json:"updated"`
}

func toAPI(js []*jobs.Job) []apiJob {
	out := make([]apiJob, 0, len(js))
	for _, j := range js {
		out = append(out, apiJob{j.ID, j.Drive, j.State, j.Movie, j.Titles, j.Note, j.Progress, j.UpdatedAt})
	}
	return out
}

func (s *server) apiJobs(w http.ResponseWriter, r *http.Request) {
	active, err := s.b.ActiveJobs(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	recent, err := s.b.RecentJobs(r.Context(), 50)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"active": toAPI(active), "recent": toAPI(recent)})
}

// readyz answers 200 when everything Discern needs works, 503 otherwise. It
// is outside the login (for container health checks), so it says only which
// check failed: the details (error text with internal addresses and paths)
// are on the logged-in dashboard.
func (s *server) readyz(w http.ResponseWriter, r *http.Request) {
	type check struct {
		Name string `json:"name"`
		OK   bool   `json:"ok"`
	}
	var out []check
	status := http.StatusOK
	for _, c := range s.b.Health(r.Context()) {
		out = append(out, check{c.Name, c.OK})
		if !c.OK {
			status = http.StatusServiceUnavailable
		}
	}
	writeJSON(w, status, map[string]any{"ok": status == http.StatusOK, "checks": out})
}

// Rendering.

func (s *server) render(w http.ResponseWriter, status int, page, name string, data any) {
	var buf strings.Builder
	if err := s.pages[page].ExecuteTemplate(&buf, name, data); err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	w.Write([]byte(buf.String()))
}

// fail logs the real error and shows the user a short one.
func (s *server) fail(w http.ResponseWriter, err error) {
	s.log.Error("web request failed", "err", err)
	http.Error(w, "something went wrong; see Discern's log", http.StatusInternalServerError)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
