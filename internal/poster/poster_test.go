package poster

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// toServer sends every request to srv, whatever its address, so the real
// host check runs while nothing leaves the test.
type toServer struct{ srv *httptest.Server }

func (t toServer) RoundTrip(r *http.Request) (*http.Response, error) {
	u, _ := url.Parse(t.srv.URL)
	r = r.Clone(r.Context())
	r.URL.Scheme, r.URL.Host = u.Scheme, u.Host
	return t.srv.Client().Transport.RoundTrip(r)
}

func pngBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 3))
	img.Set(0, 0, color.White)
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// newCache returns a cache whose image server is handler, and counts the
// requests it gets, keeping the last path asked for.
func newCache(t *testing.T, poster string, handler http.HandlerFunc) (*Cache, *atomic.Int32, *atomic.Value) {
	t.Helper()
	var hits atomic.Int32
	var path atomic.Value
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		path.Store(r.URL.Path)
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	c := New(t.TempDir(), func(context.Context, int) (string, error) { return poster, nil }, nil)
	c.Client.Transport = toServer{srv}
	return c, &hits, &path
}

func TestFetchKeepsASmallPoster(t *testing.T) {
	img := pngBytes(t)
	c, hits, path := newCache(t, "https://image.tmdb.org/t/p/original/abc123.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html") // the bytes decide, not this
		w.Write(img)
	})
	if err := c.Fetch(context.Background(), 603); err != nil {
		t.Fatal(err)
	}
	if p, _ := path.Load().(string); p != "/t/p/w342/abc123.png" {
		t.Errorf("fetched %s; want the 342-pixel width", p)
	}
	p, ct, ok := c.File(603)
	if !ok || ct != "image/png" || filepath.Base(p) != "603.png" {
		t.Fatalf("File = %q, %q, %v", p, ct, ok)
	}
	if b, _ := os.ReadFile(p); !bytes.Equal(b, img) {
		t.Error("the saved poster differs from the one served")
	}
	// Cached: no second download.
	if err := c.Fetch(context.Background(), 603); err != nil || hits.Load() != 1 {
		t.Errorf("second fetch: %v, %d downloads; want 1", err, hits.Load())
	}
}

func TestFetchRefusesWhatIsntAPoster(t *testing.T) {
	for name, c := range map[string]struct {
		poster  string
		status  int
		body    []byte
		wantErr string
	}{
		"html, not an image": {"https://image.tmdb.org/t/p/original/a.jpg", 200, []byte("<html>hello</html>"), "not a JPEG or PNG"},
		"too big":            {"https://image.tmdb.org/t/p/original/a.jpg", 200, append([]byte("\xff\xd8\xff"), make([]byte, maxBytes)...), "over"},
		"not found":          {"https://image.tmdb.org/t/p/original/a.jpg", 404, nil, "404"},
		"another host":       {"https://example.com/t/p/original/a.jpg", 200, nil, "not a poster on TMDb"},
		"plain http":         {"http://image.tmdb.org/t/p/original/a.jpg", 200, nil, "not a poster on TMDb"},
		"a path outside":     {"https://image.tmdb.org/../etc/passwd", 200, nil, "not a poster on TMDb"},
		"no poster":          {"", 200, nil, "not a poster on TMDb"},
	} {
		cache, hits, _ := newCache(t, c.poster, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(c.status)
			w.Write(c.body)
		})
		err := cache.Fetch(context.Background(), 603)
		if err == nil || !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("%s: err = %v; want %q", name, err, c.wantErr)
		}
		if _, _, ok := cache.File(603); ok {
			t.Errorf("%s: a poster was kept", name)
		}
		if strings.Contains(c.wantErr, "TMDb") && hits.Load() != 0 {
			t.Errorf("%s: fetched from a refused address", name)
		}
		// Tried once since start: no new download at the next page view.
		before := hits.Load()
		if err := cache.Fetch(context.Background(), 603); err != nil || hits.Load() != before {
			t.Errorf("%s: a failed poster was fetched again (%v)", name, err)
		}
	}
}

// One redirect to another host, which would answer with an image: it must
// not be followed.
// A failed fetch is tried again after an hour, not at every page view and
// not only at the next start.
func TestAFailedFetchIsRetriedAfterAnHour(t *testing.T) {
	img := pngBytes(t)
	var up atomic.Bool
	c, hits, _ := newCache(t, "https://image.tmdb.org/t/p/original/a.png", func(w http.ResponseWriter, r *http.Request) {
		if !up.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Write(img)
	})
	clock := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return clock }

	if !c.Due(603) {
		t.Fatal("a poster never tried isn't due")
	}
	if err := c.Fetch(context.Background(), 603); err == nil {
		t.Fatal("the outage gave no error")
	}
	up.Store(true)
	clock = clock.Add(59 * time.Minute)
	if c.Due(603) || c.Fetch(context.Background(), 603) != nil || hits.Load() != 1 {
		t.Errorf("tried again after 59 min (%d downloads); want to wait the hour", hits.Load())
	}
	clock = clock.Add(time.Minute)
	if !c.Due(603) {
		t.Fatal("not due after an hour")
	}
	if err := c.Fetch(context.Background(), 603); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := c.File(603); !ok || c.Due(603) {
		t.Error("the retry didn't keep the poster, or it is still due")
	}
}

// A poster found once is remembered, so a page refresh doesn't stat it.
func TestFoundPostersAreRemembered(t *testing.T) {
	c := New(t.TempDir(), nil, nil)
	p := filepath.Join(c.Dir, "603.png")
	if err := os.WriteFile(p, pngBytes(t), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := c.File(603); !ok {
		t.Fatal("the poster on disk wasn't found")
	}
	os.Remove(p) // only to show the second answer comes from memory
	if got, _, ok := c.File(603); !ok || got != p {
		t.Errorf("File = %q, %v; want the remembered poster", got, ok)
	}
}

// Prune keeps the posters asked for, removes the rest and any half-written
// file, and leaves other files alone.
func TestPrune(t *testing.T) {
	c := New(t.TempDir(), nil, nil)
	for _, name := range []string{"603.jpg", "559.png", "157.jpg", ".poster-123", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(c.Dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	c.File(559) // remembered: must be forgotten when pruned
	if err := c.Prune(map[int]bool{603: true, 157: true}); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(c.Dir)
	var left []string
	for _, e := range entries {
		left = append(left, e.Name())
	}
	if want := []string{"157.jpg", "603.jpg", "notes.txt"}; !slices.Equal(left, want) {
		t.Errorf("left %v; want %v", left, want)
	}
	if _, _, ok := c.File(559); ok {
		t.Error("a pruned poster is still remembered")
	}
	if err := New(filepath.Join(t.TempDir(), "none"), nil, nil).Prune(nil); err != nil {
		t.Errorf("pruning a cache never written: %v", err)
	}
}

func TestFetchRefusesARedirectAway(t *testing.T) {
	img := pngBytes(t)
	c, _, _ := newCache(t, "https://image.tmdb.org/t/p/original/a.jpg", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/elsewhere.png" {
			w.Write(img)
			return
		}
		http.Redirect(w, r, "https://example.com/elsewhere.png", http.StatusFound)
	})
	if err := c.Fetch(context.Background(), 603); err == nil || !strings.Contains(err.Error(), "redirected away") {
		t.Errorf("err = %v; want the redirect refused", err)
	}
	if _, _, ok := c.File(603); ok {
		t.Error("the image from the other host was kept")
	}
}

// A search result's poster address is used as given, without asking Radarr,
// and checked like any other.
func TestFetchFromAKnownAddress(t *testing.T) {
	img := pngBytes(t)
	c, _, path := newCache(t, "", func(w http.ResponseWriter, r *http.Request) { w.Write(img) })
	c.URL = func(context.Context, int) (string, error) {
		t.Error("Radarr was asked for an address already known")
		return "", errors.New("unused")
	}
	if err := c.FetchFrom(context.Background(), 603, "https://image.tmdb.org/t/p/original/known.png"); err != nil {
		t.Fatal(err)
	}
	if p, _ := path.Load().(string); p != "/t/p/w342/known.png" {
		t.Errorf("fetched %s", p)
	}
	if err := c.FetchFrom(context.Background(), 604, "https://example.com/t/p/original/x.png"); err == nil {
		t.Error("a known address on another host was fetched")
	}
}

func TestFetchWithoutALookup(t *testing.T) {
	c := New(t.TempDir(), func(context.Context, int) (string, error) { return "", errors.New("radarr is down") }, nil)
	if err := c.Fetch(context.Background(), 603); err == nil {
		t.Error("a failed lookup gave no error")
	}
	if err := c.Fetch(context.Background(), 0); err == nil {
		t.Error("TMDb id 0 was accepted")
	}
}

func TestSized(t *testing.T) {
	got, err := Sized("https://image.tmdb.org/t/p/original/f89U3ADr1oiB1s9GkdPOEpXUk5H.jpg")
	if err != nil || got != "https://image.tmdb.org/t/p/w342/f89U3ADr1oiB1s9GkdPOEpXUk5H.jpg" {
		t.Errorf("Sized = %q, %v", got, err)
	}
	if _, err := Sized("https://user@image.tmdb.org/t/p/original/a.jpg"); err == nil {
		t.Error("an address with a user was accepted")
	}
}
