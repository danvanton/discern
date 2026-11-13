// Package drive notices discs going into optical drives, and ejects them.
//
// It asks the kernel directly (an ioctl on /dev/srN) instead of asking
// MakeMKV: listing drives through makemkvcon makes it query every drive,
// which disturbs a rip in progress (docs/DESIGN.md, section 5).
package drive

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

// State is what a drive reports about its tray.
type State int

const (
	Unknown  State = iota
	NoDisc         // tray closed and empty
	TrayOpen       // tray open
	NotReady       // a disc is spinning up
	DiscOK         // a disc is in and readable
)

func (s State) String() string {
	return [...]string{"unknown", "no disc", "tray open", "not ready", "disc ready"}[s]
}

// ErrUnsupported means drives can't be watched or ejected on this operating
// system (anything but Linux). Discs are then started from the web UI's Scan
// button.
var ErrUnsupported = errors.New("optical drive control needs Linux")

// Tracker counts tray changes per drive. Something that scanned a disc can
// note the count, and later know the disc can't have been swapped if the
// count hasn't moved. It is safe for concurrent use.
type Tracker struct {
	mu    sync.Mutex
	gen   map[string]uint64
	known map[string]bool
}

func NewTracker() *Tracker {
	return &Tracker{gen: map[string]uint64{}, known: map[string]bool{}}
}

// Generation returns how many changes drive has seen, and whether the count
// can be trusted: false until the drive has been read successfully, and on
// systems where drives can't be watched.
func (t *Tracker) Generation(drive string) (uint64, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.gen[drive], t.known[drive]
}

func (t *Tracker) observe(drive string, changed bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if changed {
		t.gen[drive]++
	}
	t.known[drive] = true
}

// Watcher polls drives and calls OnDisc when a disc becomes ready in one.
type Watcher struct {
	Drives []string
	Every  time.Duration
	// Status reads a drive's state; Status() in this package by default.
	Status func(device string) (State, error)
	// OnDisc is called, on the watcher's goroutine, when a drive goes from
	// any other state to DiscOK.
	OnDisc func(ctx context.Context, device string)
	// Tracker, if set, is told about every change of every drive.
	Tracker *Tracker
	Log     *slog.Logger
}

// Run polls until ctx ends.
//
// Only a change to DiscOK starts anything. A disc already in the tray when
// Discern starts does not: it may be one that was finished before the
// restart, and ripping it again would be wrong. The user starts such a disc
// with the Scan button.
func (w *Watcher) Run(ctx context.Context) {
	status := w.Status
	if status == nil {
		status = Status
	}
	last := map[string]State{}
	lastErr := map[string]string{}
	first := true
	t := time.NewTicker(w.Every)
	defer t.Stop()
	for {
		for _, d := range w.Drives {
			s, err := status(d)
			if errors.Is(err, ErrUnsupported) {
				w.Log.Warn("drive detection is not available on this system; start discs with the Scan button", "drive", d)
				return
			}
			if err != nil {
				// Log a failing drive once per new error, not every tick.
				if lastErr[d] != err.Error() {
					w.Log.Warn("reading drive status failed", "drive", d, "err", err)
					lastErr[d] = err.Error()
				}
				last[d] = Unknown
				// While a drive can't be read, a swap could go unseen.
				if w.Tracker != nil {
					w.Tracker.observe(d, true)
				}
				continue
			}
			delete(lastErr, d)
			if w.Tracker != nil {
				w.Tracker.observe(d, !first && s != last[d])
			}
			if s == DiscOK && last[d] != DiscOK && !first {
				w.Log.Info("disc inserted", "drive", d)
				w.OnDisc(ctx, d)
			}
			last[d] = s
		}
		first = false
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
