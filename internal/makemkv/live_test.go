package makemkv

import (
	"context"
	"errors"
	"math"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/danvanton/discern/internal/makemkv/fake"
)

func TestLiveFrom(t *testing.T) {
	t0 := time.Date(2026, 11, 15, 14, 0, 0, 0, time.UTC)
	at := func(sec int, read, written int64) Sample {
		return Sample{At: t0.Add(time.Duration(sec) * time.Second), ReadBytes: read, ReadKnown: true, WrittenBytes: written}
	}
	// A rip at 18 MB/s read and 16 MB/s written for the last minute, after a
	// slow start that must not count.
	samples := []Sample{at(0, 0, 0), at(30, 30e6, 0)}
	for s := 40; s <= 100; s += 10 {
		samples = append(samples, at(s, 30e6+int64(s-40)*18e6, int64(s-40)*16e6))
	}
	l, ok := liveFrom(samples, time.Minute, Healthy)
	if !ok || !l.ReadKnown || math.Abs(l.ReadRate-18e6) > 1 || math.Abs(l.WriteRate-16e6) > 1 || l.Verdict != Healthy {
		t.Errorf("liveFrom = %+v, %v; want 18 MB/s read, 16 MB/s written, healthy", l, ok)
	}

	if _, ok := liveFrom(samples[:1], time.Minute, Healthy); ok {
		t.Error("one sample gave speeds")
	}
	// Without a read counter (not Linux), only the write speed is known.
	noRead := []Sample{{At: t0, WrittenBytes: 0}, {At: t0.Add(10 * time.Second), WrittenBytes: 100e6}}
	if l, ok := liveFrom(noRead, time.Minute, Stalled); !ok || l.ReadKnown || l.ReadRate != 0 || l.WriteRate != 10e6 || l.Verdict != Stalled {
		t.Errorf("without a read counter: %+v, %v", l, ok)
	}
	// The first sample had no read count yet: the read speed starts later.
	late := []Sample{{At: t0}, at(10, 0, 50e6), at(20, 100e6, 100e6)}
	if l, ok := liveFrom(late, time.Minute, Healthy); !ok || !l.ReadKnown || l.ReadRate != 10e6 || l.WriteRate != 5e6 {
		t.Errorf("read count missing at first: %+v, %v; want 10 MB/s read from the second sample, 5 MB/s written", l, ok)
	}
	// Output that shrank (a file replaced) is no progress, not a negative speed.
	shrank := []Sample{{At: t0, WrittenBytes: 100}, {At: t0.Add(time.Second), WrittenBytes: 50}}
	if l, _ := liveFrom(shrank, time.Minute, Healthy); l.WriteRate != 0 {
		t.Errorf("shrinking output: write rate %v; want 0", l.WriteRate)
	}
}

// A rip tells OnLive how it is going, with its drive, and a stall's last
// report carries the verdict.
func TestRipReportsLive(t *testing.T) {
	counter := filepath.Join(t.TempDir(), "read-counter")
	r := testRunner(t, fake.Scenario{Mkv: fake.Mkv{
		TotalBytes: 100 * mib, StallAtBytes: 1 * mib, ChunkDelayMs: 2,
		StallMode: fake.StallReading, ReadCounterFile: counter,
	}})
	r.readCounter = counterFileReader(counter)
	var (
		mu     sync.Mutex
		got    []Live
		device string
	)
	r.OnLive = func(d string, l Live) {
		mu.Lock()
		defer mu.Unlock()
		device = d
		got = append(got, l)
	}
	_, err := r.Rip(context.Background(), "/dev/sr1", 1, t.TempDir(), nil)
	var we *WatchdogError
	if !errors.As(err, &we) {
		t.Fatalf("err = %v; want the stall", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) == 0 || device != "/dev/sr1" {
		t.Fatalf("OnLive was called %d times, for %q", len(got), device)
	}
	if last := got[len(got)-1]; last.Verdict != Stalled {
		t.Errorf("last report = %+v; want stalled", last)
	}
	// Read speeds were reported. (Not necessarily in every report: on
	// Windows the fake's counter file can't always be read while it is
	// being replaced, and that sample has no read count.)
	read := false
	for _, l := range got {
		read = read || (l.ReadKnown && l.ReadRate > 0)
	}
	if !read {
		t.Error("no report had a read speed")
	}
	// Every report gives the same start: when makemkvcon started.
	if got[0].Started.IsZero() || !got[len(got)-1].Started.Equal(got[0].Started) || time.Since(got[0].Started) > time.Minute {
		t.Errorf("start times %v … %v; want one recent start", got[0].Started, got[len(got)-1].Started)
	}
	wrote := false
	for _, l := range got {
		wrote = wrote || l.WriteRate > 0
	}
	if !wrote {
		t.Error("no report showed the output growing before the stall")
	}
}
