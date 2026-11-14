package makemkv

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Runner starts makemkvcon and turns what it does into results and errors.
// One Runner can serve many drives; each call starts its own process.
type Runner struct {
	// Bin is the path to makemkvcon.
	Bin string
	// Watchdog holds the stall and hang thresholds for rips.
	Watchdog WatchdogConfig
	// KillAfter is how long a process gets to exit after SIGTERM before it is
	// killed outright.
	KillAfter time.Duration
	// InfoTimeout bounds a scan. Scans of fake-playlist discs can take
	// minutes, but never this long.
	InfoTimeout time.Duration

	// Env holds extra environment variables for makemkvcon, such as
	// FAKE_MAKEMKVCON when it is the fake (tests and development).
	Env []string

	// Seams for tests and for operating systems without /proc. They default
	// to the /proc implementations on Linux.
	readCounter func(pid int) (int64, error)
	debugLogs   func(pid int) []DebugLog
}

// NewRunner returns a Runner with Discern's defaults.
func NewRunner(bin string) *Runner {
	return &Runner{
		Bin:         bin,
		Watchdog:    DefaultWatchdogConfig,
		KillAfter:   10 * time.Second,
		InfoTimeout: 15 * time.Minute,
		readCounter: procReadCounter,
		debugLogs:   procDebugLogs,
	}
}

// ExitError reports a makemkvcon run that ended with a non-zero exit status.
// Messages are MakeMKV's own, the best clue to what went wrong.
type ExitError struct {
	Op       string // "info" or "mkv"
	Code     int
	Messages []Message
	Stderr   string
}

func (e *ExitError) Error() string {
	s := fmt.Sprintf("makemkvcon %s exited with status %d", e.Op, e.Code)
	if n := len(e.Messages); n > 0 {
		s += fmt.Sprintf(": MSG %d %q", e.Messages[n-1].Code, e.Messages[n-1].Text)
	}
	return s
}

// WatchdogError reports a rip the watchdog stopped. Evidence holds what was
// saved before the process was stopped.
type WatchdogError struct {
	Verdict  Verdict
	Evidence *Evidence
}

func (e *WatchdogError) Error() string {
	return fmt.Sprintf("rip %s: %s", e.Verdict, e.Evidence.Reason)
}

// Limits on what is kept from a process's output, so a misbehaving
// makemkvcon can't make Discern use unbounded memory.
const (
	maxLine        = 1 << 20  // longest robot line accepted
	keptOutputTail = 64 << 10 // stdout and stderr kept for error reports
)

// Info scans the disc in device (for example "/dev/sr0"). The output is
// parsed as it streams, so its size doesn't matter; only the last 64 KiB is
// kept, to report MakeMKV's messages if the scan fails.
func (r *Runner) Info(ctx context.Context, device string) (*Disc, error) {
	ctx, cancel := context.WithTimeout(ctx, r.InfoTimeout)
	defer cancel()
	cmd := r.command(ctx, "-r", "--cache=1", "--minlength=0", "info", "dev:"+device)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr := newCappedBuffer(keptOutputTail)
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting makemkvcon: %w", err)
	}

	tail := newCappedBuffer(keptOutputTail)
	disc, parseErr := ParseInfo(io.TeeReader(stdout, tail))
	if parseErr != nil {
		// Keep the pipe drained so the process can't block on a full pipe;
		// InfoTimeout still bounds how long that takes.
		io.Copy(tail, stdout)
	}
	waitErr := cmd.Wait()

	switch {
	case waitErr == nil && parseErr == nil:
		// MakeMKV can report "Operation successfully completed" and exit 0
		// after failing to read parts of the disc. Fail closed instead.
		if err := checkComplete(disc); err != nil {
			return nil, err
		}
		return disc, nil
	case ctx.Err() != nil:
		return nil, fmt.Errorf("makemkvcon info: %w", ctx.Err())
	case waitErr != nil:
		// A failed scan usually also leaves its output incomplete. The exit
		// status and MakeMKV's own messages are the real cause, so they win
		// over the parse error.
		return nil, exitError("info", waitErr, messagesIn([]byte(tail.String())), stderr.String())
	default:
		return nil, fmt.Errorf("makemkvcon info: %w", parseErr)
	}
}

// RipResult is a rip that makemkvcon finished with exit status 0. That is
// necessary but not sufficient: the verify step checks the files themselves.
type RipResult struct {
	OutputFiles []string
	Messages    []Message
}

// Rip saves one title of the disc in device as MKV into outDir. outDir must
// be empty (it is created if missing): the watchdog measures output as the
// size of everything in it, so leftovers from an earlier attempt would look
// like progress, and would be mixed up with this rip's files.
//
// onProgress, if not nil, is called from another goroutine with the latest
// progress. Progress lines are never queued behind a slow callback: values
// that arrive while it runs are replaced by newer ones, so a slow callback
// can't hold up makemkvcon. Rip returns only after the last callback has
// returned.
//
// The watchdog samples the process every Watchdog.Interval. On a stall or
// hang it saves the evidence, stops the process, and Rip returns a
// *WatchdogError.
func (r *Runner) Rip(ctx context.Context, device string, title int, outDir string, onProgress func(Progress)) (*RipResult, error) {
	if err := r.Watchdog.Validate(); err != nil {
		return nil, err
	}
	if err := ensureEmptyDir(outDir); err != nil {
		return nil, err
	}
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	cmd := r.command(runCtx, "-r", "--progress=-same", "--minlength=0",
		"mkv", "dev:"+device, strconv.Itoa(title), outDir)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr := newCappedBuffer(keptOutputTail)
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting makemkvcon: %w", err)
	}

	progress := newProgressForwarder(onProgress)
	defer progress.close() // waits for the last callback
	out := &robotLog{tail: newRing[string](200)}
	readDone := make(chan error, 1)
	go func() {
		// On unreadable output, stop the process: a rip whose output can't
		// be followed must not continue unwatched.
		readDone <- out.consume(stdout, progress.send, stop)
	}()

	samples := newRing[Sample](360) // the last hour at the default interval
	wd := NewWatchdog(r.Watchdog)
	ticker := time.NewTicker(r.Watchdog.Interval)
	defer ticker.Stop()
	var werr *WatchdogError
	tick := ticker.C

	// Sample the starting point right away, so the first interval counts.
	wd.Observe(r.sample(cmd.Process.Pid, outDir, samples))
	var readErr error
loop:
	for {
		select {
		case readErr = <-readDone:
			break loop
		case <-tick:
			s := r.sample(cmd.Process.Pid, outDir, samples)
			if v := wd.Observe(s); v != Healthy {
				// The evidence must be saved before the process stops: the
				// debug log only exists while makemkvcon holds it open.
				werr = &WatchdogError{Verdict: v, Evidence: r.evidence(v, cmd.Process.Pid, samples.items(), out, stderr.String())}
				tick = nil // no more sampling
				stop()     // SIGTERM now, SIGKILL after KillAfter
			}
		}
	}
	waitErr := cmd.Wait()

	switch {
	case werr != nil:
		return nil, werr
	case waitErr == nil && readErr == nil:
		// A clean exit counts, even if ctx was cancelled a moment later.
		files, err := filepath.Glob(filepath.Join(outDir, "*.mkv"))
		if err != nil {
			return nil, err
		}
		return &RipResult{OutputFiles: files, Messages: out.messages()}, nil
	case ctx.Err() != nil:
		return nil, fmt.Errorf("makemkvcon mkv: %w", ctx.Err())
	case readErr != nil:
		return nil, fmt.Errorf("reading makemkvcon output: %w", readErr)
	default:
		return nil, exitError("mkv", waitErr, out.messages(), stderr.String())
	}
}

// ensureEmptyDir creates dir if needed and fails if it already holds
// anything.
func ensureEmptyDir(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	if len(entries) > 0 {
		return fmt.Errorf("rip output folder %q is not empty (%d entries); a rip needs a folder of its own", dir, len(entries))
	}
	return nil
}

// command builds the makemkvcon command. Cancelling ctx sends SIGTERM, and
// Go's exec package kills the process if it hasn't exited KillAfter later.
func (r *Runner) command(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, r.Bin, args...)
	cmd.Cancel = func() error { return terminate(cmd.Process) }
	cmd.WaitDelay = r.KillAfter
	if len(r.Env) > 0 {
		cmd.Env = append(os.Environ(), r.Env...)
	}
	return cmd
}

func (r *Runner) sample(pid int, outDir string, keep *ring[Sample]) Sample {
	s := Sample{At: time.Now(), WrittenBytes: dirSize(outDir)}
	if r.readCounter != nil {
		if n, err := r.readCounter(pid); err == nil {
			s.ReadBytes, s.ReadKnown = n, true
		}
	}
	keep.add(s)
	return s
}

func (r *Runner) evidence(v Verdict, pid int, samples []Sample, out *robotLog, stderr string) *Evidence {
	e := &Evidence{
		Verdict:    v,
		At:         time.Now(),
		Samples:    samples,
		RobotTail:  out.tailLines(),
		StderrTail: stderr,
		Reason:     describe(v, samples),
	}
	if r.debugLogs != nil {
		e.DebugLogs = r.debugLogs(pid)
	}
	return e
}

// robotLog consumes makemkvcon's stdout: it keeps the last lines for the
// evidence bundle, collects messages and forwards progress.
type robotLog struct {
	mu   sync.Mutex
	tail *ring[string]
	msgs []Message
}

// consume reads r to the end. If a line can't be read (longer than maxLine),
// it calls onBadOutput, then keeps draining r so the process never blocks on
// a full pipe, and returns the read error once r ends.
func (l *robotLog) consume(r io.Reader, onProgress func(Progress), onBadOutput func()) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), maxLine)
	for sc.Scan() {
		line := sc.Text()
		rec, err := ParseLine(line)
		l.mu.Lock()
		l.tail.add(line)
		if m, ok := rec.(Message); ok && err == nil {
			l.msgs = append(l.msgs, m)
		}
		l.mu.Unlock()
		if p, ok := rec.(Progress); ok && err == nil {
			onProgress(p)
		}
	}
	err := sc.Err()
	if err != nil {
		onBadOutput()
		io.Copy(io.Discard, r)
	}
	return err
}

func (l *robotLog) tailLines() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.tail.items()
}

func (l *robotLog) messages() []Message {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]Message(nil), l.msgs...)
}

// progressForwarder hands progress to a callback on its own goroutine, keeping
// only the latest value while the callback is busy. Sending never blocks.
type progressForwarder struct {
	ch   chan Progress
	done chan struct{}
}

func newProgressForwarder(fn func(Progress)) *progressForwarder {
	f := &progressForwarder{ch: make(chan Progress, 1), done: make(chan struct{})}
	go func() {
		defer close(f.done)
		for p := range f.ch {
			if fn != nil {
				fn(p)
			}
		}
	}()
	return f
}

// send is only ever called from the one stdout-reading goroutine, so the
// drain-then-send below can't race with another sender.
func (f *progressForwarder) send(p Progress) {
	select {
	case f.ch <- p:
	default:
		select { // full: replace the waiting value with the newer one
		case <-f.ch:
		default:
		}
		select {
		case f.ch <- p:
		default:
		}
	}
}

// close stops the forwarder after delivering what is waiting, and waits for
// the last callback to return.
func (f *progressForwarder) close() {
	close(f.ch)
	<-f.done
}

// messagesIn picks the MSG records out of raw output, for error reports on
// output that may not parse as a whole.
func messagesIn(out []byte) []Message {
	var msgs []Message
	for _, line := range strings.Split(string(out), "\n") {
		if rec, err := ParseLine(line); err == nil {
			if m, ok := rec.(Message); ok {
				msgs = append(msgs, m)
			}
		}
	}
	return msgs
}

func exitError(op string, err error, msgs []Message, stderr string) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return &ExitError{Op: op, Code: ee.ExitCode(), Messages: msgs, Stderr: stderr}
	}
	return fmt.Errorf("makemkvcon %s: %w", op, err)
}

// dirSize is the total size of the regular files directly in dir. A missing
// or unreadable dir counts as 0: no output yet.
//
// Each file is Stat-ed by path rather than read from the directory listing:
// on Windows the listing's size of a file that is still open for writing can
// be out of date, which would make a healthy rip look stalled.
func dirSize(dir string) int64 {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	var total int64
	for _, e := range entries {
		info, err := os.Stat(filepath.Join(dir, e.Name()))
		if err == nil && info.Mode().IsRegular() {
			total += info.Size()
		}
	}
	return total
}

// cappedBuffer keeps only the last max bytes written to it, so a chatty
// process can't make it grow without bound. It is safe to read while the
// process is still writing to it.
type cappedBuffer struct {
	mu  sync.Mutex
	b   bytes.Buffer
	max int
}

func newCappedBuffer(max int) *cappedBuffer { return &cappedBuffer{max: max} }

func (c *cappedBuffer) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.b.String()
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.b.Write(p)
	if over := c.b.Len() - c.max; over > 0 {
		c.b.Next(over)
	}
	return len(p), nil
}

// ring keeps the last n items added. It is not safe for concurrent use on
// its own; callers that share one lock around it.
type ring[T any] struct {
	buf  []T
	next int
	full bool
}

func newRing[T any](n int) *ring[T] { return &ring[T]{buf: make([]T, n)} }

func (r *ring[T]) add(v T) {
	r.buf[r.next] = v
	r.next = (r.next + 1) % len(r.buf)
	if r.next == 0 {
		r.full = true
	}
}

// items returns the kept items, oldest first.
func (r *ring[T]) items() []T {
	if !r.full {
		return append([]T(nil), r.buf[:r.next]...)
	}
	return append(append([]T(nil), r.buf[r.next:]...), r.buf[:r.next]...)
}
