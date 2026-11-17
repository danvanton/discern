package makemkv

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
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

	// OnLive, if set, is told how each rip is going after every watchdog
	// sample, with the drive it reads from. It is called on the rip's
	// goroutine, so it must return quickly.
	OnLive func(device string, l Live)

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
	Op       string // "info", "mkv" or "backup"
	Code     int
	Messages []Message // from stdout: MSG lines and plain text
	Stderr   []Message // the last lines of stderr, as plain text
}

// MakeMKVMessages are the run's messages, then its stderr lines, the best
// clue to what went wrong.
func (e *ExitError) MakeMKVMessages() []Message {
	return append(append([]Message(nil), e.Messages...), e.Stderr...)
}

// msgStarted is the line every makemkvcon run prints first: "MakeMKV v2.0.0
// linux(x64-release) started".
const msgStarted = 1005

// Error names the run's last message from stdout. Only when stdout has
// nothing past the start-up line does stderr's last line stand in. The text
// is shortened: it becomes the job's note.
func (e *ExitError) Error() string {
	s := fmt.Sprintf("makemkvcon %s exited with status %d", e.Op, e.Code)
	m, ok := Message{}, false
	if n := len(e.Messages); n > 0 {
		m, ok = e.Messages[n-1], true
	}
	if n := len(e.Stderr); n > 0 && (!ok || m.Code == msgStarted) {
		m, ok = e.Stderr[n-1], true
	}
	switch {
	case !ok:
	case m.Code == MsgPlainText:
		s += fmt.Sprintf(": %q", shorten(m.Text, 200))
	default:
		s += fmt.Sprintf(": MSG %d %q", m.Code, shorten(m.Text, 200))
	}
	return s
}

// WatchdogError reports a rip the watchdog stopped. Evidence holds what was
// saved before the process was stopped.
type WatchdogError struct {
	Verdict  Verdict
	Evidence *Evidence
	Messages []Message // MakeMKV's messages up to the verdict
}

func (e *WatchdogError) Error() string {
	return fmt.Sprintf("rip %s: %s", e.Verdict, e.Evidence.Reason)
}

// MakeMKVMessages are the rip's messages up to the verdict.
func (e *WatchdogError) MakeMKVMessages() []Message { return e.Messages }

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
		return nil, exitError("info", waitErr, messagesIn([]byte(tail.wholeLines())), stderr)
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
// like progress, and would be mixed up with this rip's files. Progress and
// the watchdog work as watched says.
func (r *Runner) Rip(ctx context.Context, device string, title int, outDir string, onProgress func(Progress)) (*RipResult, error) {
	msgs, err := r.watched(ctx, "mkv", device, outDir, onProgress, nil,
		"-r", "--progress=-same", "--minlength=0", "mkv", "dev:"+device, strconv.Itoa(title), outDir)
	if err != nil {
		return nil, err
	}
	files, err := filepath.Glob(filepath.Join(outDir, "*.mkv"))
	if err != nil {
		return nil, err
	}
	return &RipResult{OutputFiles: files, Messages: msgs}, nil
}

// Backup copies the disc in device, decrypted, into outDir (makemkvcon
// backup --decrypt), for the Backup + remux strategy (DESIGN section 9.3).
// outDir must be empty, as for Rip, and the watchdog watches it the same
// way: a backup that stops growing while the drive reads is stalled. It
// returns MakeMKV's messages.
//
// makemkvcon's backup takes only a "disc:N" source, not "dev:<device>", so
// disc is device's number from the scan's drive list (Disc.DiscNumber,
// decision D72). The numbers could have changed since the scan, so the run
// is checked against the drive list MakeMKV prints as it starts: if drive
// disc isn't device there, or isn't listed at all, the backup is stopped.
func (r *Runner) Backup(ctx context.Context, device string, disc int, outDir string, onProgress func(Progress)) ([]Message, error) {
	if disc < 0 {
		return nil, fmt.Errorf("drive number %d for %s is not valid", disc, device)
	}
	return r.watched(ctx, "backup", device, outDir, onProgress, &driveCheck{device: device, disc: disc},
		"-r", "--progress=-same", "backup", "--decrypt", "disc:"+strconv.Itoa(disc), outDir)
}

// outputCheck vets makemkvcon's output records as they arrive (watched's
// vet): check returning an error stops the run, and ended, called after a
// clean exit, can refuse a run that never showed what check needed.
type outputCheck interface {
	check(Record) error
	ended() error
}

// driveCheck is Backup's outputCheck: once makemkvcon's drive list (a block
// of DRV lines) has been printed, drive disc must be in it with device.
type driveCheck struct {
	device  string
	disc    int
	listing bool // a DRV line has been seen
	listed  bool // drive disc was in the list, with device
}

func (c *driveCheck) check(rec Record) error {
	if d, ok := rec.(Drive); ok {
		c.listing = true
		if d.Index == c.disc {
			if d.Device != c.device {
				return fmt.Errorf("MakeMKV's drive %d is %q now, not %s: the drive numbers changed since the scan; take the disc out and put it back in to scan it again", c.disc, d.Device, c.device)
			}
			c.listed = true
		}
		return nil
	}
	if c.listing && !c.listed {
		return fmt.Errorf("MakeMKV's drive list has no drive %d (%s): take the disc out and put it back in to scan it again", c.disc, c.device)
	}
	return nil
}

func (c *driveCheck) ended() error {
	if !c.listed {
		return fmt.Errorf("makemkvcon backup never listed drive %d (%s), so it can't be told which drive it read", c.disc, c.device)
	}
	return nil
}

// RefusedError reports a run its output check refused: stopped while
// running, or, with Finished, refused after it exited cleanly. Messages are
// MakeMKV's up to then.
type RefusedError struct {
	Op       string
	Err      error
	Finished bool
	Messages []Message
}

func (e *RefusedError) Error() string { return fmt.Sprintf("makemkvcon %s stopped: %v", e.Op, e.Err) }
func (e *RefusedError) Unwrap() error { return e.Err }

// MakeMKVMessages are the run's messages up to the refusal.
func (e *RefusedError) MakeMKVMessages() []Message { return e.Messages }

// watched runs makemkvcon with args, writing into outDir, under the
// watchdog, and returns MakeMKV's messages once it exits cleanly. op names
// the command in errors.
//
// onProgress, if not nil, is called from another goroutine with the latest
// progress. Progress lines are never queued behind a slow callback: values
// that arrive while it runs are replaced by newer ones, so a slow callback
// can't hold up makemkvcon. watched returns only after the last callback
// has returned.
//
// The watchdog samples the process every Watchdog.Interval. On a stall or
// hang it saves the evidence, stops the process, and watched returns a
// *WatchdogError.
//
// vet, if not nil, checks the output records as they arrive; a record it
// refuses stops the process, and watched returns a *RefusedError.
func (r *Runner) watched(ctx context.Context, op, device, outDir string, onProgress func(Progress), vet outputCheck, args ...string) ([]Message, error) {
	if err := r.Watchdog.Validate(); err != nil {
		return nil, err
	}
	if err := ensureEmptyDir(outDir); err != nil {
		return nil, err
	}
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	cmd := r.command(runCtx, args...)
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
	out := &robotLog{tail: newRing[string](200), vet: vet}
	readDone := make(chan error, 1)
	go func() {
		// On unreadable output, stop the process: a rip whose output can't
		// be followed must not continue unwatched.
		readDone <- out.consume(stdout, progress.send, stop)
	}()

	started := time.Now()
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
			v := wd.Observe(s)
			if r.OnLive != nil {
				if l, ok := liveFrom(samples.items(), liveWindow, v); ok {
					l.Started = started
					r.OnLive(device, l)
				}
			}
			if v != Healthy {
				// The evidence must be saved before the process stops: the
				// debug log only exists while makemkvcon holds it open.
				werr = &WatchdogError{Verdict: v, Evidence: r.evidence(v, cmd.Process.Pid, samples.items(), out, stderr.String()),
					Messages: out.messages()}
				tick = nil // no more sampling
				stop()     // SIGTERM now, SIGKILL after KillAfter
			}
		}
	}
	waitErr := cmd.Wait()

	switch {
	case werr != nil:
		return nil, werr
	case out.refusal() != nil:
		return nil, &RefusedError{Op: op, Err: out.refusal(), Messages: out.messages()}
	case waitErr == nil && readErr == nil:
		// A clean exit counts, even if ctx was cancelled a moment later.
		if vet != nil {
			if err := vet.ended(); err != nil {
				return nil, &RefusedError{Op: op, Err: err, Finished: true, Messages: out.messages()}
			}
		}
		return out.messages(), nil
	case ctx.Err() != nil:
		return nil, fmt.Errorf("makemkvcon %s: %w", op, ctx.Err())
	case readErr != nil:
		return nil, fmt.Errorf("reading makemkvcon output: %w", readErr)
	default:
		return nil, exitError(op, waitErr, out.messages(), stderr)
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

	vet     outputCheck // if set, vets each record
	refused error       // the first error vet returned
}

// consume reads r to the end. If a line can't be read (longer than maxLine),
// or vet refuses a record, it calls stop, then keeps draining r so the
// process never blocks on a full pipe. It returns the read error once r
// ends.
func (l *robotLog) consume(r io.Reader, onProgress func(Progress), stop func()) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), maxLine)
	for sc.Scan() {
		line := sc.Text()
		rec, err := ParseLine(line)
		l.mu.Lock()
		l.tail.add(line)
		if m, ok := asMessage(rec, err); ok {
			l.msgs = append(l.msgs, m)
		}
		vet := l.vet != nil && l.refused == nil && err == nil
		l.mu.Unlock()
		if vet {
			if cerr := l.vet.check(rec); cerr != nil {
				l.mu.Lock()
				l.refused = cerr
				l.mu.Unlock()
				stop()
			}
		}
		if p, ok := rec.(Progress); ok && err == nil {
			onProgress(p)
		}
	}
	err := sc.Err()
	if err != nil {
		stop()
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

func (l *robotLog) refusal() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.refused
}

// asMessage is the message a parsed line carries: a MSG line, or a plain
// text line, which MakeMKV prints outside its robot records for some errors
// ("Backup source must start with "disc:"") and is kept as a message with
// code MsgPlainText, so a failure shows it.
func asMessage(rec Record, err error) (Message, bool) {
	if err != nil {
		return Message{}, false
	}
	switch r := rec.(type) {
	case Message:
		return r, true
	case Unknown:
		if r.Prefix == "" && strings.TrimSpace(r.Line) != "" {
			return Message{Code: MsgPlainText, Text: strings.TrimSpace(r.Line)}, true
		}
	}
	return Message{}, false
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

// messagesIn picks the messages (asMessage) out of raw output, for error
// reports on output that may not parse as a whole.
func messagesIn(out []byte) []Message {
	var msgs []Message
	for _, line := range strings.Split(string(out), "\n") {
		if m, ok := asMessage(ParseLine(line)); ok {
			msgs = append(msgs, m)
		}
	}
	return msgs
}

// keptStderrLines is how many of a failed run's last stderr lines join its
// messages.
const keptStderrLines = 10

// exitError reports a run that exited with an error. The last lines
// makemkvcon wrote to stderr are kept as plain-text messages, so the failure
// page shows them too.
func exitError(op string, err error, msgs []Message, stderr *cappedBuffer) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		var lines []Message
		for _, l := range strings.Split(stderr.wholeLines(), "\n") {
			if l = strings.TrimSpace(l); l != "" {
				lines = append(lines, Message{Code: MsgPlainText, Text: l})
			}
		}
		return &ExitError{Op: op, Code: ee.ExitCode(), Messages: msgs, Stderr: lines[max(0, len(lines)-keptStderrLines):]}
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
	// Every file below dir counts: a rip writes one file at the top, but a
	// backup writes the disc's folders (BDMV/STREAM/...).
	var total int64
	filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // a file that went away between listing and reading
		}
		// os.Stat, not d.Info: on Windows the listing's size stays stale
		// while makemkvcon still has the file open.
		if d.Type().IsRegular() {
			if info, err := os.Stat(path); err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total
}

// cappedBuffer keeps only the last max bytes written to it, so a chatty
// process can't make it grow without bound. It is safe to read while the
// process is still writing to it.
type cappedBuffer struct {
	mu  sync.Mutex
	b   bytes.Buffer
	max int
	cut bool // bytes were dropped from the front
}

func newCappedBuffer(max int) *cappedBuffer { return &cappedBuffer{max: max} }

func (c *cappedBuffer) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.b.String()
}

// wholeLines is what String returns, less the first line when the front
// was dropped: that line is a fragment, which must not pass for a message.
func (c *cappedBuffer) wholeLines() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.b.String()
	if c.cut {
		_, s, _ = strings.Cut(s, "\n")
	}
	return s
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.b.Write(p)
	if over := c.b.Len() - c.max; over > 0 {
		c.b.Next(over)
		c.cut = true
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
