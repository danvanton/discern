package makemkv

import (
	"fmt"
	"time"
)

// The watchdog decides, from two counters sampled over time, whether a rip is
// healthy, stalled or hung (docs/DESIGN.md, section 9.2). It does not need
// MakeMKV's cooperation: it only watches what the operating system already
// knows about any process, the bytes it reads and the bytes it writes.
//
// This file is pure logic: no clocks, files or processes. The runner feeds it
// samples; tests feed it made-up ones.

// Verdict is the watchdog's judgement after a sample.
type Verdict int

const (
	Healthy Verdict = iota
	// Stalled: data keeps coming in, nothing comes out. The pattern seen with
	// MakeMKV 2.0.0 on a Dolby Vision profile 7 UHD disc: reading at 17 MB/s,
	// output frozen at 640 MiB.
	Stalled
	// Hung: neither counter moves at all.
	Hung
)

func (v Verdict) String() string {
	switch v {
	case Healthy:
		return "healthy"
	case Stalled:
		return "stalled"
	case Hung:
		return "hung"
	}
	return fmt.Sprintf("Verdict(%d)", int(v))
}

// WatchdogConfig holds the thresholds. They are settings, because drives and
// discs differ; the defaults are loose enough for a slow drive.
type WatchdogConfig struct {
	// Interval is how often the runner takes a sample.
	Interval time.Duration
	// StartupGrace is how long reading without writing is normal before the
	// first byte of output. MakeMKV reads the disc's structure and keys for a
	// while before it writes anything.
	StartupGrace time.Duration
	// StallAfter is how long output may stand still while reading continues,
	// once output has started.
	StallAfter time.Duration
	// HangAfter is how long both counters may stand still.
	HangAfter time.Duration
}

// Validate rejects settings the watchdog can't work with: every threshold
// must be positive, a sample must come more often than a stall is declared,
// and a hang must take longer than a stall.
func (c WatchdogConfig) Validate() error {
	switch {
	case c.Interval <= 0 || c.StartupGrace <= 0 || c.StallAfter <= 0 || c.HangAfter <= 0:
		return fmt.Errorf("watchdog settings must all be positive: %+v", c)
	case c.Interval >= c.StallAfter:
		return fmt.Errorf("watchdog interval (%s) must be shorter than the stall time (%s)", c.Interval, c.StallAfter)
	case c.StallAfter >= c.HangAfter:
		return fmt.Errorf("watchdog stall time (%s) must be shorter than the hang time (%s)", c.StallAfter, c.HangAfter)
	}
	return nil
}

// DefaultWatchdogConfig is what Discern uses unless the user changes it.
var DefaultWatchdogConfig = WatchdogConfig{
	Interval:     10 * time.Second,
	StartupGrace: 5 * time.Minute,
	StallAfter:   2 * time.Minute,
	HangAfter:    10 * time.Minute,
}

// Sample is one reading of the two counters.
type Sample struct {
	At           time.Time
	ReadBytes    int64 // bytes the process has read so far
	ReadKnown    bool  // false where the OS doesn't expose read counts (not Linux)
	WrittenBytes int64 // size of the output so far
}

// Watchdog keeps the state between samples. The zero value is not usable;
// create one with NewWatchdog.
type Watchdog struct {
	cfg           WatchdogConfig
	started       bool
	everWritten   bool
	readKnown     bool // read counts have been available at least once
	lastRead      int64
	lastWritten   int64
	lastReadAt    time.Time
	lastWrittenAt time.Time
}

func NewWatchdog(cfg WatchdogConfig) *Watchdog { return &Watchdog{cfg: cfg} }

// Observe takes the next sample and returns the verdict. Samples must arrive
// in time order. The first sample only sets the starting point.
//
// A sample without read counts, after earlier ones had them, is treated as
// "no news about reading" rather than "reading unknown": one failed read of
// /proc must not turn a stall into a hang.
func (w *Watchdog) Observe(s Sample) Verdict {
	if !w.started {
		w.started = true
		w.lastRead, w.lastWritten = s.ReadBytes, s.WrittenBytes
		w.lastReadAt, w.lastWrittenAt = s.At, s.At
		w.everWritten = s.WrittenBytes > 0
		w.readKnown = s.ReadKnown
		return Healthy
	}
	if s.WrittenBytes > w.lastWritten {
		w.lastWritten, w.lastWrittenAt, w.everWritten = s.WrittenBytes, s.At, true
	}
	if s.ReadKnown {
		if !w.readKnown {
			// First sample with read counts: a starting point, not progress.
			w.readKnown, w.lastRead, w.lastReadAt = true, s.ReadBytes, s.At
		} else if s.ReadBytes > w.lastRead {
			w.lastRead, w.lastReadAt = s.ReadBytes, s.At
		}
	}

	sinceWrite := s.At.Sub(w.lastWrittenAt)
	sinceRead := s.At.Sub(w.lastReadAt)

	// Before any output, a long read is normal (start-up); after it, the
	// tolerance is shorter.
	stallLimit := w.cfg.StallAfter
	if !w.everWritten {
		stallLimit = w.cfg.StartupGrace
	}
	if w.readKnown && sinceWrite >= stallLimit && sinceRead < stallLimit {
		return Stalled
	}

	// Without read counts, output alone decides; with them, the process is
	// only quiet if both have stood still.
	quiet := sinceWrite
	if w.readKnown && sinceRead < quiet {
		quiet = sinceRead
	}
	if quiet >= w.cfg.HangAfter {
		return Hung
	}
	return Healthy
}
