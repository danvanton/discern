package discdb

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// published serves an index and its checksum the way the release does,
// counting the index downloads.
type published struct {
	index []byte
	sum   string // the checksum line's hash; the index's own if empty
	gets  atomic.Int32
}

func (p *published) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/discdb-index.json.gz":
		p.gets.Add(1)
		w.Write(p.index)
	case "/discdb-index.json.gz.sha256":
		sum := p.sum
		if sum == "" {
			sum = sha(p.index)
		}
		fmt.Fprintf(w, "%s  discdb-index.json.gz\n", strings.ToUpper(sum))
	default:
		http.NotFound(w, r)
	}
}

func indexBytes(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := build(t).Write(&b); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func catalogFor(t *testing.T, p *published) (*Catalog, string) {
	t.Helper()
	srv := httptest.NewServer(p)
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	return NewCatalog(srv.URL+"/discdb-index.json.gz", dir, nil), dir
}

// A published index is downloaded, checked, saved and used; an unchanged
// one isn't downloaded again; and the saved copy is used after a restart.
func TestCatalogRefresh(t *testing.T) {
	p := &published{index: indexBytes(t)}
	c, dir := catalogFor(t, p)
	if c.Index() != nil {
		t.Fatal("an index before any was loaded")
	}
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ix := c.Index(); ix == nil || len(ix.Discs) != 9 {
		t.Fatalf("index in use: %+v", ix)
	}
	if err := c.Refresh(context.Background()); err != nil || p.gets.Load() != 1 {
		t.Errorf("an unchanged index was downloaded again (%d downloads, %v)", p.gets.Load(), err)
	}
	// After a restart, offline: the saved copy is used.
	again := NewCatalog("", dir, nil)
	if err := again.LoadLocal(); err != nil || again.Index() == nil || len(again.Index().Discs) != 9 {
		t.Errorf("the saved index wasn't loaded: %v", err)
	}
	if err := NewCatalog("", t.TempDir(), nil).LoadLocal(); err != nil {
		t.Errorf("no saved index yet should not be an error: %v", err)
	}
}

// A download that doesn't match its checksum, doesn't parse, or isn't
// there leaves the last good index in use and on disk.
func TestCatalogKeepsTheLastGoodIndex(t *testing.T) {
	good := indexBytes(t)
	p := &published{index: good}
	c, dir := catalogFor(t, p)
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	inUse := c.Index()

	for name, change := range map[string]func(){
		// A new checksum is published, but the index served doesn't match it
		// (the window while the workflow replaces the two files).
		"checksum mismatch": func() { p.index, p.sum = append(bytes.Clone(good), 0), sha([]byte("the next index")) },
		"not an index":      func() { p.index, p.sum = []byte("not gzip"), "" },
		"bad checksum line": func() { p.index, p.sum = good, "zz" },
	} {
		change()
		if err := c.Refresh(context.Background()); err == nil {
			t.Errorf("%s: no error", name)
		}
		if c.Index() != inUse {
			t.Errorf("%s: the index in use changed", name)
		}
		saved, _ := os.ReadFile(filepath.Join(dir, fileName))
		if !bytes.Equal(saved, good) {
			t.Errorf("%s: the saved index changed", name)
		}
	}

	// A checksum line that isn't a SHA-256 is refused before the index is
	// downloaded.
	p.index, p.sum = good, "zz"
	before := p.gets.Load()
	if err := c.Refresh(context.Background()); err == nil || !strings.Contains(err.Error(), "isn't a SHA-256") || p.gets.Load() != before {
		t.Errorf("a malformed checksum line: %v, %d downloads", err, p.gets.Load()-before)
	}

	// Nothing published at all.
	c.URL = c.URL + "-missing"
	if err := c.Refresh(context.Background()); err == nil || c.Index() != inUse {
		t.Errorf("a missing index: %v", err)
	}
	// No address: refreshing is off.
	c.URL = ""
	if err := c.Refresh(context.Background()); err != nil {
		t.Errorf("with no address: %v", err)
	}
}

// A corrupt saved copy is reported, not used.
func TestCatalogRefusesACorruptSavedIndex(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, fileName), []byte("garbage"), 0o644)
	c := NewCatalog("", dir, nil)
	if err := c.LoadLocal(); err == nil || c.Index() != nil {
		t.Errorf("a corrupt saved index was loaded (%v)", err)
	}
}

// An index of older data than the one in use is refused: a re-run on an
// old commit mustn't lose the discs added since.
func TestCatalogRefusesAnOlderIndex(t *testing.T) {
	ix := build(t)
	var newer, older bytes.Buffer
	ix.Write(&newer)
	ix.Built = ix.Built.Add(-30 * 24 * time.Hour)
	ix.Write(&older)
	p := &published{index: newer.Bytes()}
	c, _ := catalogFor(t, p)
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	inUse := c.Index()
	p.index = older.Bytes()
	if err := c.Refresh(context.Background()); err == nil || !strings.Contains(err.Error(), "older than the one in use") || c.Index() != inUse {
		t.Errorf("an older index: %v", err)
	}
}

// A checksum line of the right length that isn't hexadecimal is refused
// before the index is downloaded.
func TestCatalogRefusesANonHexChecksum(t *testing.T) {
	p := &published{index: indexBytes(t), sum: strings.Repeat("g", 64)}
	c, _ := catalogFor(t, p)
	if err := c.Refresh(context.Background()); err == nil || !strings.Contains(err.Error(), "isn't a SHA-256") || p.gets.Load() != 0 {
		t.Errorf("a non-hex checksum: %v, %d downloads", err, p.gets.Load())
	}
}

// A refresh failure is a warning the first time, debug when it repeats, and
// a recovery says so once.
func TestCatalogReportsRepeatsQuietly(t *testing.T) {
	var logs bytes.Buffer
	c := NewCatalog("", "", slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	offline := errors.New("dial tcp: no route to host")
	c.report(offline)
	c.report(offline)
	c.report(offline)
	c.report(errors.New("checksum mismatch"))
	c.report(nil)
	c.report(nil)
	out := logs.String()
	if n := strings.Count(out, "level=WARN"); n != 2 {
		t.Errorf("%d warnings; want 2 (the first failure, then a different one):\n%s", n, out)
	}
	if n := strings.Count(out, "level=DEBUG"); n != 2 {
		t.Errorf("%d debug lines; want 2 (the repeats)", n)
	}
	if n := strings.Count(out, "refreshed again after a failure"); n != 1 {
		t.Errorf("the recovery was said %d times; want once", n)
	}
}
