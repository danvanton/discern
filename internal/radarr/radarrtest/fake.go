// Package radarrtest is an in-memory stand-in for Radarr, for tests and for
// running Discern locally without a real Radarr (cmd/fake-radarr). It answers
// the API calls Discern makes. Its Manual Import really moves the file, from
// the staging folder (mapped back from Radarr's path) into a library folder,
// so checks run against real files.
package radarrtest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Server is the fake Radarr.
type Server struct {
	APIKey  string
	Local   string // the staging folder as Discern sees it
	Remote  string // the same folder as Radarr sees it
	Library string // where imported files go

	mu         sync.Mutex
	b          Behaviour
	movies     []map[string]any
	nextID     int
	lookup     []map[string]any
	lookupFor  map[string][]map[string]any // per-term results, from SetLookupFor
	terms      []string                    // every search term, in order
	lookupFail map[string]bool             // terms whose search fails, from FailLookup
	commands   map[int]*command
	added      []map[string]any
	imports    []map[string]any
	badKeys    int
	lookups    int
	errs       []error
	handler    http.Handler
}

// Behaviour switches the fake into a failure mode.
type Behaviour struct {
	FailImport   bool // the import command ends "failed"
	KeepFile     bool // "complete" without moving the file
	NeverFinish  bool // the command stays "started"
	EmptyScan    bool // the Manual Import scan finds nothing
	WrongQuality bool // file the movie with another quality
}

// SetBehaviour sets the failure mode. It takes the fake's lock, so it is safe
// while requests are being served.
func (s *Server) SetBehaviour(b Behaviour) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.b = b
}

type command struct {
	status string
	polls  int
	body   map[string]any
}

// New returns a fake Radarr for the given staging mapping, filing imports
// into library.
func New(apiKey, local, remote, library string) *Server {
	s := &Server{APIKey: apiKey, Local: local, Remote: remote, Library: library,
		nextID: 1, commands: map[int]*command{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v3/system/status", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"version": "5.99.0-fake"})
	})
	mux.HandleFunc("GET /api/v3/movie", s.listMovies)
	mux.HandleFunc("GET /api/v3/movie/{id}", s.getMovie)
	mux.HandleFunc("POST /api/v3/movie", s.addMovie)
	mux.HandleFunc("GET /api/v3/movie/lookup", s.doLookup)
	mux.HandleFunc("GET /api/v3/movie/lookup/tmdb", s.lookupTMDb)
	mux.HandleFunc("GET /api/v3/language", func(w http.ResponseWriter, _ *http.Request) {
		// Part of Radarr's own list (src/NzbDrone.Core/Languages/Language.cs
		// at v6.4.4.10685). It has no Malay or Thai entry here on purpose: the
		// real list lacks Malay, and a test needs a language it doesn't list.
		writeJSON(w, []map[string]any{
			{"id": 0, "name": "Unknown"}, {"id": 1, "name": "English"}, {"id": 2, "name": "French"},
			{"id": 4, "name": "German"}, {"id": 8, "name": "Japanese"}, {"id": 10, "name": "Chinese"},
			{"id": 15, "name": "Norwegian"}, {"id": 34, "name": "Bengali"},
		})
	})
	mux.HandleFunc("GET /api/v3/qualityprofile", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, []map[string]any{{"id": 1, "name": "HD-1080p"}, {"id": 4, "name": "Any"}})
	})
	mux.HandleFunc("GET /api/v3/qualitydefinition", func(w http.ResponseWriter, _ *http.Request) {
		var defs []map[string]any
		for i, n := range []string{"DVD", "Bluray-720p", "Remux-1080p", "Remux-2160p"} {
			defs = append(defs, map[string]any{"quality": map[string]any{"id": 20 + i, "name": n, "source": "bluray"}})
		}
		writeJSON(w, defs)
	})
	mux.HandleFunc("GET /api/v3/manualimport", s.scan)
	mux.HandleFunc("POST /api/v3/command", s.startCommand)
	mux.HandleFunc("GET /api/v3/command/{id}", s.getCommand)
	mux.HandleFunc("GET /api/v3/command", s.listCommands)
	s.handler = s.checkKey(mux)
	return s
}

// Handler serves the fake API.
func (s *Server) Handler() http.Handler { return s.handler }

// NewTest starts a fake Radarr for a test and returns it with its URL. It is
// closed when the test ends, and the test fails if the fake hit a problem
// moving files.
func NewTest(t *testing.T, local, remote string) (*Server, string) {
	t.Helper()
	s := New("test-key", local, remote, t.TempDir())
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(func() {
		srv.Close()
		for _, err := range s.Errors() {
			t.Errorf("fake radarr: %v", err)
		}
	})
	return s, srv.URL
}

// SetLookup sets what a movie lookup returns: maps with tmdbId, title and
// year.
func (s *Server) SetLookup(results ...map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lookup = results
}

// SetLookupFor sets the results of a movie search for one term only, as
// Radarr's answers depend on the term. Other terms get SetLookup's results.
func (s *Server) SetLookupFor(term string, results ...map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lookupFor == nil {
		s.lookupFor = map[string][]map[string]any{}
	}
	if results == nil {
		results = []map[string]any{}
	}
	s.lookupFor[term] = results
}

// FailLookup makes the movie search for term fail, as Radarr does when TMDb
// doesn't answer.
func (s *Server) FailLookup(term string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lookupFail == nil {
		s.lookupFail = map[string]bool{}
	}
	s.lookupFail[term] = true
}

// LookupTerms returns the movie searches made, in order.
func (s *Server) LookupTerms() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.terms)
}

// AddLibraryMovie puts a movie in the library, with a file the user had long
// before (dateAdded 2020) if hasFile.
func (s *Server) AddLibraryMovie(tmdbID int, title string, year int, hasFile bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := map[string]any{"id": s.nextID, "tmdbId": tmdbID, "title": title, "year": year, "hasFile": hasFile}
	if hasFile {
		m["movieFile"] = map[string]any{"relativePath": title + ".mkv", "dateAdded": "2020-01-01T00:00:00Z",
			"quality": qualityModel("Remux-1080p")}
	}
	s.nextID++
	s.movies = append(s.movies, m)
}

// SetFileQuality changes the quality of a movie's file, as the user does
// under Manage Files in Radarr.
func (s *Server) SetFileQuality(tmdbID int, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.movies {
		if asInt(m["tmdbId"]) != tmdbID {
			continue
		}
		if f, ok := m["movieFile"].(map[string]any); ok {
			f["quality"] = qualityModel(name)
		}
	}
}

// asInt reads a number stored either by the fake (int) or decoded from a
// request's JSON (float64).
func asInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case float64:
		return int(n)
	}
	return -1
}

// RunningImport queues a Manual Import of remotePath for movieID, as if an
// earlier attempt had started it.
func (s *Server) RunningImport(remotePath string, movieID int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	body := map[string]any{"name": "ManualImport", "importMode": "move", "files": []any{
		map[string]any{"path": remotePath, "movieId": float64(movieID),
			"quality": qualityModel("Remux-2160p")},
	}}
	s.commands[7] = &command{status: "started", body: body}
}

// Added returns the bodies of POST /movie, in order.
func (s *Server) Added() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.added)
}

// Imports returns the bodies of POST /command, in order.
func (s *Server) Imports() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.imports)
}

// BadKeys counts requests without the right API key header.
func (s *Server) BadKeys() int { s.mu.Lock(); defer s.mu.Unlock(); return s.badKeys }

// Lookups counts movie searches.
func (s *Server) Lookups() int { s.mu.Lock(); defer s.mu.Unlock(); return s.lookups }

// Errors returns problems the fake hit, such as a file it couldn't move.
func (s *Server) Errors() []error { s.mu.Lock(); defer s.mu.Unlock(); return s.errs }

func (s *Server) checkKey(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != s.APIKey || r.URL.Query().Has("apikey") {
			s.mu.Lock()
			s.badKeys++
			s.mu.Unlock()
			http.Error(w, `{"error":"Unauthorized"}`, http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) listMovies(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Like some Radarr versions, ignore the tmdbId filter and list everything;
	// the client must filter itself.
	writeJSON(w, s.movies)
}

func (s *Server) getMovie(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, _ := strconv.Atoi(r.PathValue("id"))
	for _, m := range s.movies {
		if m["id"] == id {
			writeJSON(w, m)
			return
		}
	}
	http.NotFound(w, r)
}

func (s *Server) addMovie(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	json.NewDecoder(r.Body).Decode(&body)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.added = append(s.added, body)
	tmdb, _ := body["tmdbId"].(float64)
	year, _ := body["year"].(float64)
	m := map[string]any{"id": s.nextID, "tmdbId": int(tmdb), "title": body["title"], "year": int(year), "hasFile": false}
	s.nextID++
	s.movies = append(s.movies, m)
	writeJSON(w, m)
}

func (s *Server) doLookup(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lookups++
	term := r.URL.Query().Get("term")
	s.terms = append(s.terms, term)
	if s.lookupFail[term] {
		http.Error(w, "lookup failed", http.StatusServiceUnavailable)
		return
	}
	if results, ok := s.lookupFor[term]; ok {
		writeJSON(w, results)
		return
	}
	writeJSON(w, s.lookup)
}

func (s *Server) lookupTMDb(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, _ := strconv.Atoi(r.URL.Query().Get("tmdbId"))
	for _, m := range s.lookup {
		if v, _ := m["tmdbId"].(int); v == id {
			writeJSON(w, m)
			return
		}
	}
	http.NotFound(w, r)
}

// toLocal turns a path as Radarr sees it back into the local one.
func (s *Server) toLocal(remote string) string {
	rel := strings.TrimPrefix(strings.TrimPrefix(remote, s.Remote), "/")
	return filepath.Join(s.Local, filepath.FromSlash(rel))
}

func (s *Server) scan(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	empty := s.b.EmptyScan
	s.mu.Unlock()
	if empty {
		writeJSON(w, []any{})
		return
	}
	folder := r.URL.Query().Get("folder")
	entries, _ := os.ReadDir(s.toLocal(folder))
	out := []map[string]any{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		// As Radarr's ManualImportResource: quality is a QualityModel
		// (quality plus revision), and the release group is Radarr's guess
		// from the folder name.
		out = append(out, map[string]any{
			"path":         path.Join(folder, e.Name()),
			"name":         e.Name(),
			"quality":      qualityModel("Unknown"),
			"releaseGroup": path.Base(folder),
			"languages":    []any{map[string]any{"id": 1, "name": "English"}},
			"rejections":   []any{},
		})
	}
	writeJSON(w, out)
}

func (s *Server) startCommand(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	json.NewDecoder(r.Body).Decode(&body)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.imports = append(s.imports, body)
	id := 100 + len(s.imports)
	s.commands[id] = &command{status: "queued", body: body}
	writeJSON(w, map[string]any{"id": id, "name": body["name"], "status": "queued"})
}

func (s *Server) getCommand(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.PathValue("id"))
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.commands[id]
	if c == nil {
		http.NotFound(w, r)
		return
	}
	c.polls++
	switch {
	case s.b.NeverFinish || c.polls < 2:
		c.status = "started"
	case s.b.FailImport:
		c.status = "failed"
	case c.status != "completed":
		c.status = "completed"
		s.runImport(c.body)
	}
	resp := map[string]any{"id": id, "name": c.body["name"], "status": c.status}
	if c.status == "failed" {
		resp["message"] = "Import failed, path does not exist or is not accessible by Radarr"
	}
	writeJSON(w, resp)
}

// runImport does what Radarr's Manual Import does: move each file into the
// movie's folder and record the file on the movie.
func (s *Server) runImport(body map[string]any) {
	files, _ := body["files"].([]any)
	for _, raw := range files {
		file, _ := raw.(map[string]any)
		id, _ := file["movieId"].(float64)
		p, _ := file["path"].(string)
		// Radarr reads quality.quality; anything else leaves it Unknown,
		// which is what the real Radarr did with a bare quality object.
		name := "Unknown"
		if model, ok := file["quality"].(map[string]any); ok {
			if q, ok := model["quality"].(map[string]any); ok {
				if n, ok := q["name"].(string); ok {
					name = n
				}
			}
		}
		group, ok := file["releaseGroup"].(string)
		if !ok {
			group = path.Base(path.Dir(p))
		}
		if s.b.WrongQuality {
			name = "DVD"
		}
		for _, m := range s.movies {
			if m["id"] != int(id) {
				continue
			}
			dst := filepath.Join(s.Library, path.Base(p))
			if !s.b.KeepFile {
				if err := os.Rename(s.toLocal(p), dst); err != nil {
					s.errs = append(s.errs, fmt.Errorf("moving %s: %w", p, err))
				}
			}
			m["hasFile"] = true
			m["movieFile"] = map[string]any{"relativePath": path.Base(dst),
				"dateAdded":    time.Now().UTC().Format(time.RFC3339Nano),
				"quality":      qualityModel(name),
				"releaseGroup": group,
				"languages":    file["languages"]}
		}
	}
}

func (s *Server) listCommands(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []map[string]any{}
	for id, c := range s.commands {
		out = append(out, map[string]any{"id": id, "name": c.body["name"], "status": c.status, "body": c.body})
	}
	writeJSON(w, out)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// qualityModel is Radarr's QualityModel: a quality and its revision.
func qualityModel(name string) map[string]any {
	return map[string]any{
		"quality":  map[string]any{"name": name},
		"revision": map[string]any{"version": 1, "real": 0, "isRepack": false},
	}
}
