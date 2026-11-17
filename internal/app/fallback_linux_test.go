package app

import (
	"context"
	"testing"
	"time"

	"github.com/danvanton/discern/internal/jobs"
	"github.com/danvanton/discern/internal/makemkv"
)

// A real stall on Linux, where the watchdog reads the process's counters
// in /proc: the fake rip keeps reading and stops writing, the watchdog calls
// it stalled, and the job finishes through backup + remux on its own
// (decision D64). Elsewhere a stall can't be told from a hang.
func TestAStalledRipFinishesThroughTheFallback(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t, 0, "")
	fallbackScenario(t, w)
	// Wide margins for a slow CI runner: output stops within milliseconds
	// of the start, reading goes on, and a hang (nothing read) needs ten
	// times the stall's wait.
	w.runner.Watchdog = makemkv.WatchdogConfig{Interval: 100 * time.Millisecond, StartupGrace: 2 * time.Second,
		StallAfter: 2 * time.Second, HangAfter: 20 * time.Second}
	j, _ := w.app.DiscInserted(ctx, "/dev/sr0")
	waiting := w.waitFor(t, j.ID, jobs.NeedsYou)
	if err := w.app.Approve(ctx, j.ID, waiting.Titles, nil, nil, jobs.Direct); err != nil {
		t.Fatal(err)
	}
	done := w.waitFor(t, j.ID, jobs.Done)
	if done.Strategy != jobs.Backup {
		t.Errorf("strategy %q; want backup", done.Strategy)
	}
	events, _ := w.store.Events(ctx, j.ID)
	stalled := false
	for _, e := range events {
		stalled = stalled || e.To == jobs.Stalled
	}
	if !stalled {
		t.Error("the job never went through Stalled")
	}
}
