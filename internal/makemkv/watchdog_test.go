package makemkv

import (
	"testing"
	"time"
)

const mib = 1 << 20

// step describes the counters at one moment, as seconds since the start.
type step struct {
	sec     int
	read    int64
	written int64
	want    Verdict
}

// run feeds the steps to a fresh watchdog and checks each verdict.
func run(t *testing.T, cfg WatchdogConfig, readKnown bool, steps []step) {
	t.Helper()
	w := NewWatchdog(cfg)
	t0 := time.Date(2026, 11, 12, 20, 28, 0, 0, time.UTC)
	for _, s := range steps {
		got := w.Observe(Sample{
			At:           t0.Add(time.Duration(s.sec) * time.Second),
			ReadBytes:    s.read,
			ReadKnown:    readKnown,
			WrittenBytes: s.written,
		})
		if got != s.want {
			t.Fatalf("at %ds (read %d, written %d): verdict %v, want %v", s.sec, s.read, s.written, got, s.want)
		}
	}
}

func TestWatchdogHealthyRip(t *testing.T) {
	run(t, DefaultWatchdogConfig, true, []step{
		{0, 0, 0, Healthy},
		{10, 170 * mib, 160 * mib, Healthy},
		{20, 340 * mib, 330 * mib, Healthy},
		{30, 510 * mib, 500 * mib, Healthy},
	})
}

// The Kill Bill pattern: output stops at exactly 640 MiB, the drive keeps
// reading at about 17 MB/s. Stalled once output has stood still for the full
// StallAfter (120 s), not before.
func TestWatchdogKillBillStall(t *testing.T) {
	const frozen = 671088640 // 640 MiB, where both Kill Bill rips stopped
	steps := []step{{0, 0, 0, Healthy}, {60, 1000 * mib, frozen, Healthy}}
	for sec := 70; sec < 180; sec += 10 {
		steps = append(steps, step{sec, int64(sec) * 17 * mib, frozen, Healthy})
	}
	steps = append(steps, step{180, 180 * 17 * mib, frozen, Stalled})
	run(t, DefaultWatchdogConfig, true, steps)
}

// MakeMKV reads for a while before writing anything. That is normal until
// StartupGrace (5 min); after it, the same pattern is a stall.
func TestWatchdogStartupGrace(t *testing.T) {
	run(t, DefaultWatchdogConfig, true, []step{
		{0, 0, 0, Healthy},
		{120, 200 * mib, 0, Healthy}, // past StallAfter, but nothing written yet
		{290, 500 * mib, 0, Healthy},
		{300, 520 * mib, 0, Stalled},
	})
}

func TestWatchdogHung(t *testing.T) {
	run(t, DefaultWatchdogConfig, true, []step{
		{0, 0, 0, Healthy},
		{30, 300 * mib, 280 * mib, Healthy},
		{300, 300 * mib, 280 * mib, Healthy},
		{629, 300 * mib, 280 * mib, Healthy},
		{630, 300 * mib, 280 * mib, Hung}, // 600 s with nothing moving
	})
}

// Output that starts growing again clears the stall: a slow patch on a
// scratched disc is not a stall.
func TestWatchdogRecovers(t *testing.T) {
	run(t, DefaultWatchdogConfig, true, []step{
		{0, 0, 0, Healthy},
		{10, 100 * mib, 90 * mib, Healthy},
		{100, 900 * mib, 90 * mib, Healthy}, // 90 s without output
		{110, 1000 * mib, 95 * mib, Healthy},
		{220, 2000 * mib, 95 * mib, Healthy}, // 110 s since the last growth
	})
}

// Without read counts (not Linux), a stall can't be told apart, so only the
// hang rule applies, on output alone.
func TestWatchdogWithoutReadCounts(t *testing.T) {
	run(t, DefaultWatchdogConfig, false, []step{
		{0, 0, 0, Healthy},
		{30, 0, 100 * mib, Healthy},
		{200, 0, 100 * mib, Healthy}, // would be Stalled with read counts
		{629, 0, 100 * mib, Healthy},
		{630, 0, 100 * mib, Hung},
	})
}

// Output that shrinks (a partial file deleted) must not count as progress.
func TestWatchdogShrinkingOutputIsNotProgress(t *testing.T) {
	run(t, DefaultWatchdogConfig, true, []step{
		{0, 0, 0, Healthy},
		{10, 100 * mib, 100 * mib, Healthy},
		{20, 200 * mib, 50 * mib, Healthy},
		{130, 1300 * mib, 60 * mib, Stalled}, // still below 100 MiB: no growth for 120 s
	})
}

func TestVerdictString(t *testing.T) {
	for v, want := range map[Verdict]string{Healthy: "healthy", Stalled: "stalled", Hung: "hung", 9: "Verdict(9)"} {
		if got := v.String(); got != want {
			t.Errorf("%d.String() = %q, want %q", int(v), got, want)
		}
	}
}

// One sample without read counts in the middle of a stall must not turn it
// into a hang: the job would fail instead of taking the fallback route.
func TestWatchdogMissingReadSampleKeepsStall(t *testing.T) {
	w := NewWatchdog(DefaultWatchdogConfig)
	t0 := time.Now()
	at := func(sec int) time.Time { return t0.Add(time.Duration(sec) * time.Second) }
	w.Observe(Sample{At: at(0), ReadKnown: true})
	w.Observe(Sample{At: at(10), ReadKnown: true, ReadBytes: 100 * mib, WrittenBytes: 90 * mib})
	for sec := 20; sec < 600; sec += 10 {
		w.Observe(Sample{At: at(sec), ReadKnown: true, ReadBytes: int64(sec) * 17 * mib, WrittenBytes: 90 * mib})
	}
	// 600 s after output last grew: one failed /proc read.
	if got := w.Observe(Sample{At: at(610), ReadKnown: false, WrittenBytes: 90 * mib}); got != Stalled {
		t.Errorf("verdict with a missing read sample = %v, want stalled", got)
	}
}

func TestWatchdogConfigValidate(t *testing.T) {
	if err := DefaultWatchdogConfig.Validate(); err != nil {
		t.Errorf("default config: %v", err)
	}
	bad := map[string]WatchdogConfig{
		"zero value":           {},
		"zero interval":        {0, time.Minute, time.Minute, 2 * time.Minute},
		"negative stall":       {time.Second, time.Minute, -time.Minute, 2 * time.Minute},
		"interval not < stall": {time.Minute, time.Minute, time.Minute, 2 * time.Minute},
		"stall not < hang":     {time.Second, time.Minute, 2 * time.Minute, 2 * time.Minute},
		"zero start-up grace":  {time.Second, 0, time.Minute, 2 * time.Minute},
	}
	for name, c := range bad {
		if err := c.Validate(); err == nil {
			t.Errorf("%s: Validate() = nil, want an error", name)
		}
	}
}
