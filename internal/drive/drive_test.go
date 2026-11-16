package drive

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// script plays back states for one drive, one per poll, then repeats the
// last.
type script struct {
	mu     sync.Mutex
	states []State
	errs   []error
	i      int
}

func (s *script) status(string) (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := min(s.i, len(s.states)-1)
	s.i++
	var err error
	if i < len(s.errs) {
		err = s.errs[i]
	}
	return s.states[i], err
}

func (s *script) done() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.i > len(s.states)+2
}

func watch(t *testing.T, sc *script) []string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	var started []string
	w := &Watcher{
		Drives: []string{"/dev/sr0"},
		Every:  time.Millisecond,
		Status: sc.status,
		OnDisc: func(_ context.Context, d string) {
			mu.Lock()
			started = append(started, d)
			mu.Unlock()
		},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	go w.Run(ctx)
	deadline := time.Now().Add(5 * time.Second)
	for !sc.done() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	mu.Lock()
	defer mu.Unlock()
	return started
}

func TestADiscGoingInStartsAJob(t *testing.T) {
	got := watch(t, &script{states: []State{NoDisc, TrayOpen, NotReady, DiscOK, DiscOK, DiscOK}})
	if len(got) != 1 {
		t.Errorf("OnDisc ran %d times, want once", len(got))
	}
}

// A disc already in the tray at start-up may be one finished before a
// restart: it must not be ripped again by itself.
func TestADiscPresentAtStartupIsLeftAlone(t *testing.T) {
	if got := watch(t, &script{states: []State{DiscOK, DiscOK, DiscOK}}); len(got) != 0 {
		t.Errorf("OnDisc ran %d times for a disc that was already there", len(got))
	}
}

func TestSwappingDiscsStartsOneJobEach(t *testing.T) {
	got := watch(t, &script{states: []State{NoDisc, DiscOK, DiscOK, TrayOpen, NoDisc, DiscOK, DiscOK}})
	if len(got) != 2 {
		t.Errorf("OnDisc ran %d times for two discs", len(got))
	}
}

func TestADriveThatErrorsAndRecoversCounts(t *testing.T) {
	boom := errors.New("drive gone")
	got := watch(t, &script{
		states: []State{NoDisc, Unknown, DiscOK},
		errs:   []error{nil, boom, nil},
	})
	if len(got) != 1 {
		t.Errorf("OnDisc ran %d times, want once after the drive came back", len(got))
	}
}

func TestStateNames(t *testing.T) {
	if DiscOK.String() != "disc ready" || TrayOpen.String() != "tray open" {
		t.Error("state names changed")
	}
}

func TestTrackerCountsChanges(t *testing.T) {
	tr := NewTracker()
	if _, ok := tr.Generation("/dev/sr0"); ok {
		t.Error("a drive never read can't be known")
	}
	sc := &script{states: []State{DiscOK, DiscOK, TrayOpen, NoDisc, DiscOK, DiscOK}}
	ctx, cancel := context.WithCancel(context.Background())
	w := &Watcher{Drives: []string{"/dev/sr0"}, Every: time.Millisecond, Status: sc.status,
		OnDisc: func(context.Context, string) {}, Tracker: tr, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	go w.Run(ctx)
	for !sc.done() {
		time.Sleep(time.Millisecond)
	}
	cancel()
	if gen, ok := tr.Generation("/dev/sr0"); !ok || gen != 3 {
		t.Errorf("generation = %d, %v; want 3 changes (open, empty, ready), known", gen, ok)
	}
}

// The tracker keeps the last state read, for the dashboard: Unknown until
// the first read and while the drive can't be read.
func TestTrackerKeepsTheLastState(t *testing.T) {
	tr := NewTracker()
	if s := tr.State("/dev/sr0"); s != Unknown {
		t.Errorf("before any read: %v", s)
	}
	tr.observe("/dev/sr0", TrayOpen, false)
	if s := tr.State("/dev/sr0"); s != TrayOpen {
		t.Errorf("after a read: %v; want tray open", s)
	}
	tr.observe("/dev/sr0", Unknown, true) // the drive couldn't be read
	if s := tr.State("/dev/sr0"); s != Unknown {
		t.Errorf("after a failed read: %v; want unknown", s)
	}
}

// A disc leaving the drive (the tray opens, or it reads as closed and
// empty) is reported once per removal, and also at the first read, so a
// disc taken out while Discern wasn't running is noticed (decision D58).
func TestARemovedDiscIsReported(t *testing.T) {
	for _, c := range []struct {
		name   string
		states []State
		want   int
	}{
		{"open, then shut empty, counts once", []State{DiscOK, TrayOpen, TrayOpen, NoDisc, NoDisc}, 1},
		{"two removals", []State{DiscOK, TrayOpen, DiscOK, DiscOK, NoDisc}, 2},
		{"open at start", []State{TrayOpen, TrayOpen, DiscOK}, 1},
		{"empty at start", []State{NoDisc, NoDisc}, 1},
		{"a disc throughout", []State{DiscOK, NotReady, DiscOK}, 0},
	} {
		sc := &script{states: c.states}
		ctx, cancel := context.WithCancel(context.Background())
		var mu sync.Mutex
		removed := 0
		w := &Watcher{Drives: []string{"/dev/sr0"}, Every: time.Millisecond, Status: sc.status,
			OnDisc:    func(context.Context, string) {},
			OnRemoved: func(context.Context, string) { mu.Lock(); removed++; mu.Unlock() },
			Log:       slog.New(slog.NewTextHandler(io.Discard, nil))}
		go w.Run(ctx)
		deadline := time.Now().Add(5 * time.Second)
		for !sc.done() && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		cancel()
		mu.Lock()
		if removed != c.want {
			t.Errorf("%s: OnRemoved ran %d times, want %d", c.name, removed, c.want)
		}
		mu.Unlock()
	}
}
