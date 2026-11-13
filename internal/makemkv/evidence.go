package makemkv

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/danvanton/discern/internal/format"
)

// Evidence is what the watchdog saves about a rip before stopping it: enough
// for a bug report on MakeMKV's forum, and for Discern's own tests (a
// recorded stall becomes a test case).
type Evidence struct {
	Verdict    Verdict
	At         time.Time
	Reason     string   // one sentence, for the UI and the log
	Samples    []Sample // the counter history, oldest first
	RobotTail  []string // the last lines of robot output
	StderrTail string
	DebugLogs  []DebugLog // MakeMKV's own debug log, if it could be found
}

// DebugLog is a file makemkvcon held open but had already deleted, copied
// from /proc/<pid>/fd before the process stopped.
type DebugLog struct {
	FD        int
	Target    string // what the link pointed to, e.g. "/tmp/xyz (deleted)"
	Data      []byte
	Truncated bool // only the first part was kept (see the caps below)
}

// Caps on debug-log capture. MakeMKV's own log was about 2 MB after half an
// hour of a stalled rip; the caps leave room for that and bound the rest.
const (
	maxDebugLog      = 16 << 20 // per file
	maxDebugLogTotal = 32 << 20 // all files together
	maxDebugLogs     = 4        // number of files
)

// isDebugLogCandidate says whether an open-but-deleted file, by its
// /proc/<pid>/fd link target, may be MakeMKV's debug log: a deleted regular
// file on a real filesystem. Anonymous memory files (memfd, used by the Java
// runtime among others) and devices are skipped.
func isDebugLogCandidate(target string) bool {
	path, deleted := strings.CutSuffix(target, " (deleted)")
	return deleted && strings.HasPrefix(path, "/") &&
		!strings.HasPrefix(path, "/memfd:") && !strings.HasPrefix(path, "/dev/")
}

// debugLogAllowance is how many bytes of the next debug log may be kept,
// given how many logs and bytes are already kept. Zero means stop.
func debugLogAllowance(kept int, keptBytes int64) int64 {
	if kept >= maxDebugLogs {
		return 0
	}
	return min(int64(maxDebugLog), maxDebugLogTotal-keptBytes)
}

// WriteBundle writes the evidence into dir as plain files: summary.txt,
// samples.csv, robot-output.txt, stderr.txt and one debug-fd<N>.log per
// debug log. dir is created if needed.
func (e *Evidence) WriteBundle(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	var sum strings.Builder
	fmt.Fprintf(&sum, "verdict: %s\nat: %s\nreason: %s\n", e.Verdict, e.At.Format(time.RFC3339), e.Reason)
	for _, d := range e.DebugLogs {
		fmt.Fprintf(&sum, "debug log: fd %d, %s, %d bytes", d.FD, d.Target, len(d.Data))
		if d.Truncated {
			sum.WriteString(" (truncated)")
		}
		sum.WriteString("\n")
	}
	var csv strings.Builder
	csv.WriteString("time,read_bytes,read_known,written_bytes\n")
	for _, s := range e.Samples {
		fmt.Fprintf(&csv, "%s,%d,%t,%d\n", s.At.Format(time.RFC3339Nano), s.ReadBytes, s.ReadKnown, s.WrittenBytes)
	}
	files := map[string][]byte{
		"summary.txt":      []byte(sum.String()),
		"samples.csv":      []byte(csv.String()),
		"robot-output.txt": []byte(strings.Join(e.RobotTail, "\n") + "\n"),
		"stderr.txt":       []byte(e.StderrTail),
	}
	for _, d := range e.DebugLogs {
		files[fmt.Sprintf("debug-fd%d.log", d.FD)] = d.Data
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// describe turns the verdict and the samples into one sentence, e.g.
// "output stood still at 640.0 MiB for 2m0s while 2.0 GiB more was read".
func describe(v Verdict, samples []Sample) string {
	if len(samples) == 0 {
		return v.String()
	}
	last := samples[len(samples)-1]
	// Find when output last grew.
	grewAt, grewRead := samples[0].At, samples[0].ReadBytes
	for i := 1; i < len(samples); i++ {
		if samples[i].WrittenBytes > samples[i-1].WrittenBytes {
			grewAt, grewRead = samples[i].At, samples[i].ReadBytes
		}
	}
	still := last.At.Sub(grewAt).Round(time.Second)
	switch v {
	case Stalled:
		return fmt.Sprintf("output stood still at %s for %s while %s more was read",
			format.Bytes(last.WrittenBytes), still, format.Bytes(last.ReadBytes-grewRead))
	case Hung:
		return fmt.Sprintf("nothing was read or written for %s; output at %s", still, format.Bytes(last.WrittenBytes))
	}
	return v.String()
}
