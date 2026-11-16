package discdb

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// How often the index is refreshed, and how soon a failed refresh is tried
// again. TheDiscDB changes a few times a day at most.
const (
	refreshEvery = 24 * time.Hour
	retryAfter   = time.Hour
)

// Limits on what is downloaded: the index is a few MB, its checksum one line.
const (
	maxDownload = 64 << 20
	maxChecksum = 1 << 10
)

// fileName is the index's name in the catalog's folder.
const fileName = "index.json.gz"

// Catalog is the index Discern uses: the last good copy, kept on disk in
// Dir and refreshed from URL. A download is used only when its SHA-256
// matches the published one and its contents pass Read's checks; anything
// else leaves the last good index in place. Offline, it uses what it has.
type Catalog struct {
	URL    string // the index; URL + ".sha256" is its checksum
	Dir    string
	Client *http.Client
	Log    *slog.Logger

	mu  sync.RWMutex
	ix  *Index
	sum string // the SHA-256 of the index in use, hex

	lastErr string // the last refresh's error, so a repeat isn't logged as loudly
}

// NewCatalog returns a catalog that downloads with a time limit.
func NewCatalog(url, dir string, log *slog.Logger) *Catalog {
	return &Catalog{URL: url, Dir: dir, Client: &http.Client{Timeout: 5 * time.Minute}, Log: log}
}

// Index is the index in use, or nil before one was loaded.
func (c *Catalog) Index() *Index {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.ix
}

// LoadLocal reads the copy kept on disk. Having none yet is not an error.
func (c *Catalog) LoadLocal() error {
	b, err := os.ReadFile(filepath.Join(c.Dir, fileName))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	ix, err := Read(bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("the saved disc index: %w", err)
	}
	c.use(ix, sha(b))
	return nil
}

// Refresh downloads the published index if it differs from the one in use,
// checks it, saves it and starts using it. On any failure the index in use
// stays.
func (c *Catalog) Refresh(ctx context.Context) error {
	if c.URL == "" {
		return nil
	}
	sumLine, err := c.get(ctx, c.URL+".sha256", maxChecksum)
	if err != nil {
		return fmt.Errorf("the disc index's checksum: %w", err)
	}
	want, _, _ := strings.Cut(strings.TrimSpace(string(sumLine)), " ")
	if raw, err := hex.DecodeString(want); err != nil || len(raw) != sha256.Size {
		return fmt.Errorf("the disc index's checksum %q isn't a SHA-256", want)
	}
	want = strings.ToLower(want)
	c.mu.RLock()
	same := want == c.sum
	c.mu.RUnlock()
	if same {
		return nil // nothing new
	}
	b, err := c.get(ctx, c.URL, maxDownload)
	if err != nil {
		return fmt.Errorf("the disc index: %w", err)
	}
	if got := sha(b); got != want {
		return fmt.Errorf("the downloaded disc index's SHA-256 is %s, the published one %s", got, want)
	}
	ix, err := Read(bytes.NewReader(b))
	if err != nil {
		return err
	}
	// An index of older data than the one in use (a re-run on an old
	// commit, a mistake) would quietly lose the discs added since.
	if cur := c.Index(); cur != nil && ix.Built.Before(cur.Built) {
		return fmt.Errorf("the published disc index (TheDiscDB of %s) is older than the one in use (%s); keeping it",
			ix.Built.Format(time.RFC3339), cur.Built.Format(time.RFC3339))
	}
	if err := save(c.Dir, b); err != nil {
		return err
	}
	c.use(ix, want)
	if c.Log != nil {
		c.Log.Info("disc index updated", "discs", len(ix.Discs), "built", ix.Built, "thediscdb", ix.Source)
	}
	return nil
}

// Run refreshes the index now and then once a day, sooner after a failure,
// until ctx ends.
func (c *Catalog) Run(ctx context.Context) {
	for {
		wait := refreshEvery
		err := c.Refresh(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			wait = retryAfter
		}
		c.report(err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// report logs a refresh's failure: a new one as a warning, the same one
// again (an hour later, offline, or before the index was first published)
// only at debug level, so it doesn't bury the log. A success after failures
// says so once.
func (c *Catalog) report(err error) {
	if c.Log == nil {
		return
	}
	switch {
	case err == nil && c.lastErr != "":
		c.Log.Info("disc index refreshed again after a failure")
		c.lastErr = ""
	case err == nil:
	case err.Error() == c.lastErr:
		c.Log.Debug("disc index still not updated; using the last good one", "err", err)
	default:
		c.Log.Warn("disc index not updated; using the last good one, and trying again hourly", "err", err)
		c.lastErr = err.Error()
	}
}

func (c *Catalog) use(ix *Index, sum string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ix, c.sum = ix, sum
}

// get fetches url, refusing a body over limit bytes.
func (c *Catalog) get(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s answered %s", req.URL.Host, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("over %d bytes", limit)
	}
	return b, nil
}

// save writes the index whole or not at all.
func save(dir string, b []byte) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".index-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // a no-op once renamed
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, fileName))
}

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}
