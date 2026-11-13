package radarr

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var ctx = context.Background()

// server starts a test server that runs h, and a client for it with the
// given base path (as when Radarr sits behind a reverse proxy at /radarr).
func server(t *testing.T, basePath string, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := New(srv.URL+basePath, "secret-key")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestRequestsUseTheBasePathAndKeyHeader(t *testing.T) {
	var gotPath, gotKey, gotQuery string
	c := server(t, "/radarr/", func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotKey, gotQuery = r.URL.Path, r.Header.Get("X-Api-Key"), r.URL.RawQuery
		w.Write([]byte(`[{"tmdbId":603,"title":"The Matrix","year":1999}]`))
	})
	movies, err := c.Lookup(ctx, "the matrix & co")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/radarr/api/v3/movie/lookup" {
		t.Errorf("path = %q", gotPath)
	}
	if gotKey != "secret-key" || strings.Contains(gotQuery, "secret") {
		t.Errorf("key header %q, query %q: the key must be in the header only", gotKey, gotQuery)
	}
	if gotQuery != "term=the+matrix+%26+co" {
		t.Errorf("query = %q, want the term encoded", gotQuery)
	}
	if len(movies) != 1 || movies[0].TMDbID != 603 {
		t.Errorf("movies = %+v", movies)
	}
}

func TestErrorsCarryRadarrsMessageButNotTheKey(t *testing.T) {
	long := strings.Repeat("x", 2000)
	c := server(t, "", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"Movie already exists"} `+long, http.StatusBadRequest)
	})
	_, err := c.AddMovie(ctx, NewMovie{TMDbID: 1, Title: "X"})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 400 || apiErr.Method != "POST" || apiErr.Path != "/movie" {
		t.Fatalf("err = %#v", err)
	}
	if !strings.Contains(apiErr.Body, "Movie already exists") || len(apiErr.Body) > 500 {
		t.Errorf("body = %d bytes, want Radarr's message cut to at most 500", len(apiErr.Body))
	}
	if strings.Contains(err.Error(), "secret-key") {
		t.Error("the API key appears in the error")
	}
}

func TestEmptyBodies(t *testing.T) {
	c := server(t, "", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	// A list call with no body is just an empty list.
	if movies, err := c.Lookup(ctx, "x"); err != nil || len(movies) != 0 {
		t.Errorf("Lookup on 204 = %v, %v", movies, err)
	}
	// Calls that need an answer say so clearly.
	if _, err := c.AddMovie(ctx, NewMovie{TMDbID: 1, Title: "X"}); err == nil || !strings.Contains(err.Error(), "no movie id") {
		t.Errorf("AddMovie on 204: err = %v", err)
	}
	if _, err := c.StartManualImport(ctx, nil); err == nil || !strings.Contains(err.Error(), "no command id") {
		t.Errorf("StartManualImport on 204: err = %v", err)
	}
	if _, err := c.Movie(ctx, 5); err == nil {
		t.Error("Movie on 204 succeeded")
	}
}

func TestGarbledAnswerIsAnError(t *testing.T) {
	c := server(t, "", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("<html>login</html>")) })
	if _, err := c.Lookup(ctx, "x"); err == nil || !strings.Contains(err.Error(), "reading the answer") {
		t.Errorf("err = %v", err)
	}
}

func TestAddMovieIsAlwaysUnmonitoredWithoutSearch(t *testing.T) {
	var body string
	c := server(t, "", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		w.Write([]byte(`{"id":7,"tmdbId":1,"title":"X"}`))
	})
	if _, err := c.AddMovie(ctx, NewMovie{TMDbID: 1, Title: "X", Year: 2000, QualityProfileID: 4, RootFolderPath: "/movies"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"monitored":false`, `"searchForMovie":false`, `"monitor":"none"`} {
		if !strings.Contains(body, want) {
			t.Errorf("body %s is missing %s", body, want)
		}
	}
}

func TestWaitForCommandToleratesAFewFailedPolls(t *testing.T) {
	var polls atomic.Int32
	c := server(t, "", func(w http.ResponseWriter, r *http.Request) {
		switch polls.Add(1) {
		case 1, 2, 3: // Radarr restarting
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
		case 4:
			w.Write([]byte(`{"id":9,"status":"started"}`))
		default:
			w.Write([]byte(`{"id":9,"status":"completed"}`))
		}
	})
	cmd, err := c.WaitForCommand(ctx, 9, time.Millisecond)
	if err != nil || cmd.Status != "completed" {
		t.Fatalf("got %+v, %v; want completed despite three failed polls", cmd, err)
	}
}

func TestWaitForCommandGivesUpAfterRepeatedFailures(t *testing.T) {
	c := server(t, "", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gone", http.StatusNotFound)
	})
	_, err := c.WaitForCommand(ctx, 9, time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "5 status checks in a row failed") {
		t.Fatalf("err = %v", err)
	}
}

func TestWaitForCommandStopsAtTheDeadline(t *testing.T) {
	c := server(t, "", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":9,"status":"started"}`))
	})
	dctx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	_, err := c.WaitForCommand(dctx, 9, 5*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "still started") {
		t.Fatalf("err = %v", err)
	}
}

func TestCommandImportsPath(t *testing.T) {
	cmd := Command{Name: "ManualImport", Body: []byte(`{"files":[{"path":"/data/staging/42/a.mkv"}]}`)}
	if !cmd.ImportsPath("/data/staging/42/a.mkv") || cmd.ImportsPath("/data/staging/42/b.mkv") {
		t.Error("ImportsPath matched the wrong file")
	}
	other := Command{Name: "RefreshMovie", Body: cmd.Body}
	if other.ImportsPath("/data/staging/42/a.mkv") {
		t.Error("a non-import command counted as an import")
	}
}

func TestMovieByTMDbFiltersTheAnswer(t *testing.T) {
	c := server(t, "", func(w http.ResponseWriter, r *http.Request) {
		// Ignores the tmdbId parameter and lists the whole library.
		w.Write([]byte(`[{"id":1,"tmdbId":11},{"id":2,"tmdbId":603},{"id":3,"tmdbId":12}]`))
	})
	m, err := c.MovieByTMDb(ctx, 603)
	if err != nil || m == nil || m.ID != 2 {
		t.Errorf("got %+v, %v; want id 2", m, err)
	}
	if m, err := c.MovieByTMDb(ctx, 999); err != nil || m != nil {
		t.Errorf("missing movie: got %+v, %v", m, err)
	}
}
