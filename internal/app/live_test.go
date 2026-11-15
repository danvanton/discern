package app

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
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

func TestPosterURLWithoutAPoster(t *testing.T) {
	w := newWorld(t, 0, "")
	if _, err := w.app.PosterURL(context.Background(), 603); err == nil || !strings.Contains(err.Error(), "no poster") {
		t.Errorf("err = %v; want no poster for The Matrix", err)
	}
	if _, _, ok := w.app.Poster(603); ok {
		t.Error("a poster without a cache")
	}
}
