package drive

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// WaitReady waits until device reports a readable disc, checking every
// `every` for at most `timeout`. Scanning a drive that is still spinning up
// gives MakeMKV read errors and skipped titles (the first homelab run lost a
// scan that way), so scans wait for this first. Where drive status isn't
// available (not Linux), it returns at once.
func WaitReady(ctx context.Context, device string, timeout, every time.Duration) error {
	return waitReady(ctx, Status, device, timeout, every)
}

func waitReady(ctx context.Context, status func(string) (State, error), device string, timeout, every time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		s, err := status(device)
		switch {
		case errors.Is(err, ErrUnsupported):
			return nil
		case err != nil:
			return err
		case s == DiscOK:
			return nil
		case s != NotReady:
			return fmt.Errorf("%s has no readable disc (%s)", device, s)
		case !time.Now().Before(deadline):
			return fmt.Errorf("%s was still not ready after %s", device, timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(every):
		}
	}
}
