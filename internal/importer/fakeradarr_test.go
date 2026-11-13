package importer

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeRadarr answers the Radarr API calls the importer makes, from memory.
// Its Manual Import really moves the file, from the staging folder (mapped
// back from Radarr's path) into a library folder, so the importer's checks
// run against real files.
type fakeRadarr struct {
	t       *testing.T
	srv     *httptest.Server
	apiKey  string
	paths   PathMap // to turn Radarr's paths back into local ones
	library string  // where imported files go

	mu       sync.Mutex
	movies   []map[string]any
	nextID   int
	lookup   []map[string]any
	commands map[int]*fakeCommand
	added    []map[string]any // bodies of POST /movie
	imports  []map[string]any // bodies of POST /command
	badKeys  int              // requests without the right API key
	lookups  int              // movie lookups made

	// Behaviour switches.
	failImport   bool // the command ends "failed"
	keepFile     bool // "complete" without moving the file
	neverFinish  bool // the command stays "started"
	emptyScan    bool // Manual Import scan finds nothing
	wrongQuality bool // file the movie with another quality
}

type fakeCommand struct {
	status string
	polls  int
	body   map[string]any
}

func newFakeRadarr(t *testing.T, paths PathMap) *fakeRadarr {
	f := &fakeRadarr{
		t: t, apiKey: "test-key", paths: paths, library: t.TempDir(),
		nextID: 1, commands: map[int]*fakeCommand{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v3/movie", f.listMovies)
	mux.HandleFunc("GET /api/v3/movie/{id}", f.getMovie)
	mux.HandleFunc("POST /api/v3/movie", f.addMovie)
	mux.HandleFunc("GET /api/v3/movie/lookup", f.doLookup)
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
	mux.HandleFunc("GET /api/v3/manualimport", f.scan)
	mux.HandleFunc("POST /api/v3/command", f.startCommand)
	mux.HandleFunc("GET /api/v3/command/{id}", f.getCommand)
	mux.HandleFunc("GET /api/v3/command", f.listCommands)
	f.srv = httptest.NewServer(f.checkKey(mux))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeRadarr) checkKey(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != f.apiKey || r.URL.Query().Has("apikey") {
			f.mu.Lock()
			f.badKeys++
			f.mu.Unlock()
			http.Error(w, `{"error":"Unauthorized"}`, http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (f *fakeRadarr) addLibraryMovie(tmdbID int, title string, year int, hasFile bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := map[string]any{"id": f.nextID, "tmdbId": tmdbID, "title": title, "year": year, "hasFile": hasFile}
	if hasFile {
		// A file the user had long before the job.
		m["movieFile"] = map[string]any{"relativePath": title + ".mkv", "dateAdded": "2020-01-01T00:00:00Z",
			"quality": map[string]any{"quality": map[string]any{"name": "Remux-1080p"}}}
	}
	f.nextID++
	f.movies = append(f.movies, m)
}

func (f *fakeRadarr) listMovies(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	// Like some Radarr versions, ignore the tmdbId filter and list everything;
	// the client must filter itself.
	writeJSON(w, f.movies)
}

func (f *fakeRadarr) getMovie(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id, _ := strconv.Atoi(r.PathValue("id"))
	for _, m := range f.movies {
		if m["id"] == id {
			writeJSON(w, m)
			return
		}
	}
	http.NotFound(w, r)
}

func (f *fakeRadarr) addMovie(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	json.NewDecoder(r.Body).Decode(&body)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.added = append(f.added, body)
	m := map[string]any{"id": f.nextID, "tmdbId": int(body["tmdbId"].(float64)), "title": body["title"],
		"year": int(body["year"].(float64)), "hasFile": false}
	f.nextID++
	f.movies = append(f.movies, m)
	writeJSON(w, m)
}

func (f *fakeRadarr) doLookup(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lookups++
	writeJSON(w, f.lookup)
}

// toLocal turns a path as Radarr sees it back into the local one.
func (f *fakeRadarr) toLocal(remote string) string {
	rel := strings.TrimPrefix(strings.TrimPrefix(remote, f.paths.Remote), "/")
	return filepath.Join(f.paths.Local, filepath.FromSlash(rel))
}

func (f *fakeRadarr) scan(w http.ResponseWriter, r *http.Request) {
	if f.emptyScan {
		writeJSON(w, []any{})
		return
	}
	folder := r.URL.Query().Get("folder")
	entries, _ := os.ReadDir(f.toLocal(folder))
	var out []map[string]any
	for _, e := range entries {
		out = append(out, map[string]any{
			"path":       path.Join(folder, e.Name()),
			"name":       e.Name(),
			"languages":  []any{map[string]any{"id": 1, "name": "English"}},
			"rejections": []any{},
		})
	}
	writeJSON(w, out)
}

func (f *fakeRadarr) startCommand(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	json.NewDecoder(r.Body).Decode(&body)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.imports = append(f.imports, body)
	id := 100 + len(f.imports)
	f.commands[id] = &fakeCommand{status: "queued", body: body}
	writeJSON(w, map[string]any{"id": id, "name": body["name"], "status": "queued"})
}

func (f *fakeRadarr) getCommand(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.PathValue("id"))
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.commands[id]
	if c == nil {
		http.NotFound(w, r)
		return
	}
	c.polls++
	switch {
	case f.neverFinish || c.polls < 2:
		c.status = "started"
	case f.failImport:
		c.status = "failed"
	case c.status != "completed":
		c.status = "completed"
		f.runImport(c.body)
	}
	resp := map[string]any{"id": id, "name": c.body["name"], "status": c.status}
	if c.status == "failed" {
		resp["message"] = "Import failed, path does not exist or is not accessible by Radarr"
	}
	writeJSON(w, resp)
}

// runImport does what Radarr's Manual Import does: move each file into the
// movie's folder and record the file on the movie.
func (f *fakeRadarr) runImport(body map[string]any) {
	for _, raw := range body["files"].([]any) {
		file := raw.(map[string]any)
		id := int(file["movieId"].(float64))
		q := file["quality"].(map[string]any)["name"]
		if f.wrongQuality {
			q = "DVD"
		}
		for _, m := range f.movies {
			if m["id"] != id {
				continue
			}
			src := f.toLocal(file["path"].(string))
			dst := filepath.Join(f.library, path.Base(file["path"].(string)))
			if !f.keepFile {
				if err := os.Rename(src, dst); err != nil {
					f.t.Errorf("fake import: %v", err)
				}
			}
			m["hasFile"] = true
			m["movieFile"] = map[string]any{"relativePath": path.Base(dst),
				"dateAdded": time.Now().UTC().Format(time.RFC3339Nano),
				"quality":   map[string]any{"quality": map[string]any{"name": q}}}
		}
	}
}

func (f *fakeRadarr) listCommands(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []map[string]any
	for id, c := range f.commands {
		out = append(out, map[string]any{"id": id, "name": c.body["name"], "status": c.status, "body": c.body})
	}
	writeJSON(w, out)
}

// runningImport puts a Manual Import for remotePath into Radarr's queue as if
// an earlier attempt had started it, for movie id movieID.
func (f *fakeRadarr) runningImport(remotePath string, movieID int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body := map[string]any{"name": "ManualImport", "importMode": "move", "files": []any{
		map[string]any{"path": remotePath, "movieId": float64(movieID),
			"quality": map[string]any{"name": "Remux-2160p"}},
	}}
	f.commands[7] = &fakeCommand{status: "started", body: body}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
