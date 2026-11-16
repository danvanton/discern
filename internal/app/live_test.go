package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/danvanton/discern/internal/drive"
	"github.com/danvanton/discern/internal/jobs"
	"github.com/danvanton/discern/internal/makemkv"
	"github.com/danvanton/discern/internal/poster"
)

// A rip's speeds are kept per drive while fresh, and forgotten when the run
// ends (decision D30).
func TestLiveIsKeptPerDrive(t *testing.T) {
	a := New(context.Background(), nil, nil, nil, nil)
	if _, ok := a.Live("/dev/sr0"); ok {
		t.Error("a drive that never ripped has speeds")
	}
	a.SetLive("/dev/sr0", makemkv.Live{WriteRate: 16e6})
	if l, ok := a.Live("/dev/sr0"); !ok || l.WriteRate != 16e6 {
		t.Errorf("Live = %+v, %v", l, ok)
	}
	if _, ok := a.Live("/dev/sr1"); ok {
		t.Error("another drive got sr0's speeds")
	}
	// The second title of the same run keeps the first title's start.
	first := time.Now().Add(-30 * time.Minute)
	a.SetLive("/dev/sr0", makemkv.Live{Started: first})
	a.SetLive("/dev/sr0", makemkv.Live{Started: time.Now()})
	if l, _ := a.Live("/dev/sr0"); !l.Started.Equal(first) {
		t.Errorf("start = %v; want the run's first, %v", l.Started, first)
	}
	a.endLive("/dev/sr0")
	if _, ok := a.Live("/dev/sr0"); ok {
		t.Error("speeds outlived the run")
	}
	// The next run starts its own clock.
	a.SetLive("/dev/sr0", makemkv.Live{Started: time.Now()})
	if l, _ := a.Live("/dev/sr0"); time.Since(l.Started) > time.Minute {
		t.Error("a new run kept the old run's start")
	}
	a.endLive("/dev/sr0")
	// A report older than a few samples belongs to a rip that has ended.
	a.live["/dev/sr0"] = liveEntry{Live: makemkv.Live{WriteRate: 1}, at: time.Now().Add(-liveFresh - time.Second)}
	if _, ok := a.Live("/dev/sr0"); ok {
		t.Error("a stale report was shown")
	}
	if s := a.DriveState("/dev/sr0"); s != drive.Unknown {
		t.Errorf("without a tray tracker: %v; want unknown", s)
	}
}

// tmdbImages answers every request, whatever its address, with a PNG.
type tmdbImages struct{ srv *httptest.Server }

func (t tmdbImages) RoundTrip(r *http.Request) (*http.Response, error) {
	u, _ := url.Parse(t.srv.URL)
	r = r.Clone(r.Context())
	r.URL.Scheme, r.URL.Host = u.Scheme, u.Host
	return t.srv.Client().Transport.RoundTrip(r)
}

// Once identification has found the movie, its poster is fetched without
// anyone asking (decision D31).
func TestThePosterIsFetchedWhenTheMovieIsKnown(t *testing.T) {
	w := newWorld(t, 0, "")
	w.radarr.SetLookup(map[string]any{"tmdbId": 603, "title": "The Matrix", "year": 1999, "images": []map[string]any{
		{"coverType": "fanart", "remoteUrl": "https://image.tmdb.org/t/p/original/fan.jpg"},
		{"coverType": "poster", "remoteUrl": "https://image.tmdb.org/t/p/original/matrix.png"}}})
	var img bytes.Buffer
	png.Encode(&img, image.NewGray(image.Rect(0, 0, 2, 3)))
	var (
		mu    sync.Mutex
		asked string
	)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = r.URL.Path
		mu.Unlock()
		rw.Write(img.Bytes())
	}))
	defer srv.Close()
	w.app.Posters = poster.New(t.TempDir(), w.app.PosterURL, w.app.log)
	w.app.Posters.Client.Transport = tmdbImages{srv}

	j, err := w.app.DiscInserted(context.Background(), "/dev/sr0")
	if err != nil {
		t.Fatal(err)
	}
	w.waitFor(t, j.ID, jobs.NeedsYou)
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, ct, ok := w.app.Poster(603); ok {
			mu.Lock()
			defer mu.Unlock()
			if ct != "image/png" || asked != "/t/p/w342/matrix.png" {
				t.Errorf("poster %s, fetched %s; want the PNG poster at 342 pixels", ct, asked)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("no poster 10 s after the movie was identified")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A page asking for a missing poster starts a fetch in the background when
// one is due, and doesn't start another while it was tried lately.
func TestAPageViewFetchesAMissingPoster(t *testing.T) {
	a := New(context.Background(), nil, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	var asked atomic.Int32
	a.Posters = poster.New(t.TempDir(), func(context.Context, int) (string, error) {
		asked.Add(1)
		return "", errors.New("radarr is down")
	}, nil)
	if _, _, ok := a.Poster(603); ok {
		t.Fatal("a poster that was never fetched")
	}
	deadline := time.Now().Add(5 * time.Second)
	for asked.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	a.Wait(5 * time.Second)
	if asked.Load() != 1 {
		t.Fatalf("Radarr was asked %d times; want 1", asked.Load())
	}
	a.Poster(603) // tried a moment ago: not again
	a.Wait(5 * time.Second)
	if asked.Load() != 1 {
		t.Errorf("Radarr was asked %d times; want no second try within the hour", asked.Load())
	}
}

// The review page's movie search (decision D33): up to 8 matches with a
// TMDb id, their posters fetched from the addresses Radarr gave, before the
// page is shown.
func TestSearchMovies(t *testing.T) {
	w := newWorld(t, 0, "")
	var results []map[string]any
	results = append(results, map[string]any{"tmdbId": 0, "title": "No id", "year": 2000})
	for i := 1; i <= 10; i++ {
		m := map[string]any{"tmdbId": 1000 + i, "title": fmt.Sprintf("Star Trek %d", i), "year": 1978 + i}
		if i == 1 {
			m["images"] = []map[string]any{{"coverType": "poster", "remoteUrl": "https://image.tmdb.org/t/p/original/st1.png"}}
		}
		results = append(results, m)
	}
	w.radarr.SetLookupFor("star trek", results...)
	var img bytes.Buffer
	png.Encode(&img, image.NewGray(image.Rect(0, 0, 2, 3)))
	srv := httptest.NewTLSServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) { rw.Write(img.Bytes()) }))
	defer srv.Close()
	w.app.Posters = poster.New(t.TempDir(), func(context.Context, int) (string, error) {
		return "", errors.New("the search's own address should be used")
	}, w.app.log)
	w.app.Posters.Client.Transport = tmdbImages{srv}

	got, err := w.app.SearchMovies(context.Background(), "  star trek ")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 8 || got[0].TMDbID != 1001 || got[0].Title != "Star Trek 1" || got[0].Year != 1979 {
		t.Errorf("got %d results starting %+v; want 8, starting with Star Trek 1 (1979), without the one with no id", len(got), got[0])
	}
	if _, _, ok := w.app.Poster(1001); !ok {
		t.Error("the first result's poster wasn't fetched before the results were shown")
	}
	for _, bad := range []string{"", "   ", strings.Repeat("x", 101)} {
		if _, err := w.app.SearchMovies(context.Background(), bad); err == nil {
			t.Errorf("search %q was sent", bad)
		}
	}
}

// A poster slower than the search's wait isn't cut off: the results come
// back without it, and it arrives in the background.
func TestASlowSearchPosterStillArrives(t *testing.T) {
	w := newWorld(t, 0, "")
	w.radarr.SetLookupFor("slow", map[string]any{"tmdbId": 559, "title": "Spider-Man 3", "year": 2007,
		"images": []map[string]any{{"coverType": "poster", "remoteUrl": "https://image.tmdb.org/t/p/original/s.png"}}})
	var img bytes.Buffer
	png.Encode(&img, image.NewGray(image.Rect(0, 0, 2, 3)))
	release := make(chan struct{})
	srv := httptest.NewTLSServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		<-release
		rw.Write(img.Bytes())
	}))
	defer srv.Close()
	defer close(release)
	w.app.Posters = poster.New(t.TempDir(), w.app.PosterURL, w.app.log)
	w.app.Posters.Client.Transport = tmdbImages{srv}

	start := time.Now()
	if got, err := w.app.SearchMovies(context.Background(), "slow"); err != nil || len(got) != 1 {
		t.Fatalf("results %v, %v", got, err)
	}
	if took := time.Since(start); took > searchPosterWait+2*time.Second {
		t.Errorf("the search waited %v for a slow poster; the page waits %v at most", took, searchPosterWait)
	}
	release <- struct{}{} // the image server answers now
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, _, ok := w.app.Poster(559); ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the slow poster never arrived: it was cut off with the page's wait")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// At start, posters no listed job uses (search results nobody picked) are
// removed, and those of listed jobs kept.
func TestStartPrunesUnusedPosters(t *testing.T) {
	w := newWorld(t, 0, "")
	ctx := context.Background()
	j, err := w.app.DiscInserted(ctx, "/dev/sr0")
	if err != nil {
		t.Fatal(err)
	}
	w.waitFor(t, j.ID, jobs.NeedsYou) // identified as The Matrix (603)
	dir := t.TempDir()
	w.app.Posters = poster.New(dir, func(context.Context, int) (string, error) { return "", errors.New("offline") }, w.app.log)
	for _, name := range []string{"603.jpg", "559.jpg"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.app.Resume(ctx); err != nil {
		t.Fatal(err)
	}
	w.app.Wait(5 * time.Second)
	if _, err := os.Stat(filepath.Join(dir, "603.jpg")); err != nil {
		t.Error("the waiting job's poster was removed")
	}
	if _, err := os.Stat(filepath.Join(dir, "559.jpg")); !os.IsNotExist(err) {
		t.Error("a poster no job uses was kept")
	}
}

// A diagnostics bundle is found only by a name of the rip step's form, and
// only if its folder exists (decision D37).
func TestDiagnosticsFolder(t *testing.T) {
	a := New(context.Background(), nil, nil, nil, nil)
	a.DiagnosticsDir = t.TempDir()
	good := "job-3-20261116-010203"
	if _, ok := a.Diagnostics(good); ok {
		t.Error("a bundle that was never saved was found")
	}
	os.Mkdir(filepath.Join(a.DiagnosticsDir, good), 0o755)
	if dir, ok := a.Diagnostics(good); !ok || dir != filepath.Join(a.DiagnosticsDir, good) {
		t.Errorf("Diagnostics = %q, %v", dir, ok)
	}
	os.Mkdir(filepath.Join(a.DiagnosticsDir, "secrets"), 0o755)
	for _, bad := range []string{"", "..", "../config", "secrets", "job-3-20261116-010203/..", "job-x-20261116-010203"} {
		if _, ok := a.Diagnostics(bad); ok {
			t.Errorf("%q was accepted", bad)
		}
	}
	os.WriteFile(filepath.Join(a.DiagnosticsDir, "job-4-20261116-010203"), []byte("x"), 0o644)
	if _, ok := a.Diagnostics("job-4-20261116-010203"); ok {
		t.Error("a file was taken for a bundle folder")
	}
}

// At start, diagnostics bundles no listed job refers to are removed; a
// listed job's bundle and anything not named like a bundle stay.
func TestStartPrunesUnusedDiagnostics(t *testing.T) {
	w := newWorld(t, 0, "")
	ctx := context.Background()
	j, err := w.app.DiscInserted(ctx, "/dev/sr0")
	if err != nil {
		t.Fatal(err)
	}
	waiting := w.waitFor(t, j.ID, jobs.NeedsYou)
	waiting.Failure = &jobs.Failure{Diagnostics: "job-1-20261116-010203"} // an earlier stall's
	if err := w.store.Move(ctx, waiting, jobs.Cancelled, ""); err != nil {
		t.Fatal(err)
	}
	w.app.DiagnosticsDir = t.TempDir()
	for _, name := range []string{"job-1-20261116-010203", "job-9-20251231-235959", "notes"} {
		os.Mkdir(filepath.Join(w.app.DiagnosticsDir, name), 0o755)
	}
	if err := w.app.Resume(ctx); err != nil {
		t.Fatal(err)
	}
	w.app.Wait(5 * time.Second)
	for name, want := range map[string]bool{"job-1-20261116-010203": true, "job-9-20251231-235959": false, "notes": true} {
		_, err := os.Stat(filepath.Join(w.app.DiagnosticsDir, name))
		if (err == nil) != want {
			t.Errorf("%s: kept %v; want %v", name, err == nil, want)
		}
	}
}

func TestPosterURLWithoutAPoster(t *testing.T) {
	w := newWorld(t, 0, "")
	if _, err := w.app.PosterURL(context.Background(), 603); err == nil || !strings.Contains(err.Error(), "no poster") {
		t.Errorf("err = %v; want no poster for The Matrix", err)
	}
	if _, _, ok := w.app.Poster(603); ok {
		t.Error("a poster without a cache")
	}
}
