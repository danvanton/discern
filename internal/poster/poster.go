// Package poster fetches movie posters and keeps them on disk, so the web UI
// can show them without the browser ever contacting TMDb (decisions D23,
// D31).
//
// A poster is decoration. Nothing here can fail a job: a poster that can't be
// fetched is simply not shown.
package poster

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"time"
)

const (
	// host is the only place posters are fetched from: TMDb's image server,
	// which is where Radarr's lookup points.
	host = "image.tmdb.org"
	// width is the size asked for: 342 pixels wide is about 30 KB, sharp on a
	// phone, where the original can be several megabytes.
	width = "w342"
	// maxBytes caps a download. A w342 poster is far smaller.
	maxBytes = 2 << 20
	// retryAfter is how long a failed fetch waits before it is tried again:
	// long enough not to hammer Radarr or TMDb from every page refresh,
	// short enough that a brief outage doesn't cost the poster for good.
	retryAfter = time.Hour
)

// tmdbPath is a TMDb image address: /t/p/<size>/<file>.
var tmdbPath = regexp.MustCompile(`^/t/p/[a-z0-9]+/([A-Za-z0-9_-]+\.(?:jpg|jpeg|png))$`)

// types are the image formats kept, by the content type the bytes show.
var types = map[string]string{"image/jpeg": ".jpg", "image/png": ".png"}

// Cache keeps posters in Dir as <tmdb id>.jpg or .png.
type Cache struct {
	Dir string
	// URL returns a movie's poster address, from Radarr's lookup.
	URL func(ctx context.Context, tmdbID int) (string, error)
	// Client fetches the poster; a client with a timeout by default.
	Client *http.Client
	Log    *slog.Logger

	mu    sync.Mutex
	found map[int]file      // posters known to be on disk, so pages don't stat them
	tried map[int]time.Time // when a fetch last started
	now   func() time.Time  // time.Now; tests set it
}

type file struct{ path, contentType string }

// New returns a Cache that fetches with a 20-second timeout and follows
// redirects only within TMDb's image server.
func New(dir string, url func(ctx context.Context, tmdbID int) (string, error), log *slog.Logger) *Cache {
	return &Cache{
		Dir: dir,
		URL: url,
		Client: &http.Client{
			Timeout: 20 * time.Second,
			CheckRedirect: func(r *http.Request, via []*http.Request) error {
				if r.URL.Scheme != "https" || r.URL.Host != host || len(via) > 3 {
					return errors.New("poster redirected away from TMDb's image server")
				}
				return nil
			},
		},
		Log: log,
	}
}

// File returns the cached poster for tmdbID, with its content type. A poster
// found once is remembered: posters are only ever added.
func (c *Cache) File(tmdbID int) (path, contentType string, ok bool) {
	if tmdbID <= 0 {
		return "", "", false
	}
	c.mu.Lock()
	f, ok := c.found[tmdbID]
	c.mu.Unlock()
	if ok {
		return f.path, f.contentType, true
	}
	for ct, ext := range types {
		p := filepath.Join(c.Dir, strconv.Itoa(tmdbID)+ext)
		if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() {
			c.mu.Lock()
			if c.found == nil {
				c.found = map[int]file{}
			}
			c.found[tmdbID] = file{p, ct}
			c.mu.Unlock()
			return p, ct, true
		}
	}
	return "", "", false
}

// Due reports whether the poster for tmdbID is missing and may be fetched:
// never tried, or the last try is older than retryAfter. It only looks at
// memory, so a page can ask at every refresh.
func (c *Cache) Due(tmdbID int) bool {
	if tmdbID <= 0 {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.found[tmdbID]; ok {
		return false
	}
	at, tried := c.tried[tmdbID]
	return !tried || c.clock().Sub(at) >= retryAfter
}

func (c *Cache) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// Fetch gets the poster for tmdbID, unless it is already cached or was
// tried in the last retryAfter: a failure is not retried at every page view.
func (c *Cache) Fetch(ctx context.Context, tmdbID int) error {
	if tmdbID <= 0 {
		return fmt.Errorf("no poster for TMDb id %d", tmdbID)
	}
	if _, _, ok := c.File(tmdbID); ok {
		return nil
	}
	c.mu.Lock()
	if at, tried := c.tried[tmdbID]; tried && c.clock().Sub(at) < retryAfter {
		c.mu.Unlock()
		return nil
	}
	if c.tried == nil {
		c.tried = map[int]time.Time{}
	}
	c.tried[tmdbID] = c.clock()
	c.mu.Unlock()

	err := c.fetch(ctx, tmdbID)
	if err != nil && c.Log != nil {
		c.Log.Warn("no poster for this movie; trying again in an hour", "tmdb", tmdbID, "err", err)
	}
	return err
}

func (c *Cache) fetch(ctx context.Context, tmdbID int) error {
	raw, err := c.URL(ctx, tmdbID)
	if err != nil {
		return err
	}
	u, err := Sized(raw)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := c.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("TMDb's image server answered %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return err
	}
	if len(body) > maxBytes {
		return fmt.Errorf("the poster is over %d bytes", maxBytes)
	}
	// The bytes decide what it is, not the server's Content-Type.
	ext, ok := types[http.DetectContentType(body)]
	if !ok {
		return fmt.Errorf("the poster is not a JPEG or PNG image")
	}
	return save(c.Dir, strconv.Itoa(tmdbID)+ext, body)
}

// Sized checks that raw is a poster on TMDb's image server and returns its
// address at the width Discern asks for. Anything else is refused: the
// address comes from Radarr, and Discern fetches nothing else.
func Sized(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	m := tmdbPath.FindStringSubmatch(u.Path)
	if u.Scheme != "https" || u.Host != host || u.User != nil || m == nil {
		return "", fmt.Errorf("%q is not a poster on TMDb's image server", raw)
	}
	return "https://" + host + "/t/p/" + width + "/" + m[1], nil
}

// save writes the file whole or not at all: a temporary file renamed into
// place, so a page never shows half a poster.
func save(dir, name string, body []byte) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".poster-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // a no-op once renamed
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, name))
}
