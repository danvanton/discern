package drive

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// states plays drive states in order, repeating the last.
func states(seq ...State) (func(string) (State, error), *int) {
	n := 0
	return func(string) (State, error) {
		s := seq[min(n, len(seq)-1)]
		n++
		return s, nil
	}, &n
}

func TestWaitReadyWaitsForTheDiscToSpinUp(t *testing.T) {
	status, calls := states(NotReady, NotReady, DiscOK)
	if err := waitReady(context.Background(), status, "/dev/sr0", time.Second, time.Millisecond); err != nil || *calls != 3 {
		t.Fatalf("waitReady = %v after %d checks; want nil after 3", err, *calls)
	}
}

func TestWaitReadyFailsClosed(t *testing.T) {
	for name, tc := range map[string]struct {
		seq  []State
		want string
	}{
		"never ready": {[]State{NotReady}, "still not ready"},
		"tray open":   {[]State{TrayOpen}, "no readable disc (tray open)"},
		"no disc":     {[]State{NoDisc}, "no readable disc (no disc)"},
	} {
		status, _ := states(tc.seq...)
		err := waitReady(context.Background(), status, "/dev/sr0", 20*time.Millisecond, time.Millisecond)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v; want one containing %q", name, err, tc.want)
		}
	}
}

func TestWaitReadyPassesWhereStatusIsUnsupported(t *testing.T) {
	unsupported := func(string) (State, error) { return Unknown, ErrUnsupported }
	if err := waitReady(context.Background(), unsupported, "/dev/sr0", time.Second, time.Millisecond); err != nil {
		t.Fatalf("waitReady = %v; want nil, so development machines can still scan", err)
	}
}

func TestWaitReadyStopsOnShutdown(t *testing.T) {
	status, _ := states(NotReady)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitReady(ctx, status, "/dev/sr0", time.Hour, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v; want context.Canceled", err)
	}
}
