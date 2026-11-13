// Package fake is a stand-in for makemkvcon, for tests and development
// without a drive. It understands the two commands Discern uses, "info" and
// "mkv", and behaves as a scenario says: replay a recorded scan, write a rip
// at a given pace, stall the way MakeMKV 2.0.0 did on a Dolby Vision UHD disc,
// or freeze completely.
//
// The scenario is JSON in the FAKE_MAKEMKVCON environment variable, so the
// same fake serves Go tests (which re-run the test binary as the fake) and
// cmd/fake-makemkvcon (a real binary for manual runs).
package fake

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// EnvVar holds the scenario.
const EnvVar = "FAKE_MAKEMKVCON"

// Scenario describes how the fake behaves. Zero values are sensible defaults.
type Scenario struct {
	Info Info `json:"info"`
	Mkv  Mkv  `json:"mkv"`
}

// Info is the behaviour of "makemkvcon info".
type Info struct {
	File     string `json:"file"`      // robot output to print, as recorded
	ExitCode int    `json:"exit_code"` // exit status afterwards
}

// Stall modes for Mkv.StallMode.
const (
	StallReading = "reading" // keep reading, write nothing: the Kill Bill pattern
	StallFrozen  = "frozen"  // read nothing, write nothing
)

// Mkv is the behaviour of "makemkvcon mkv".
type Mkv struct {
	TitleName    string `json:"title_name"`     // output is "<name>_tNN.mkv"; default "Fake Title"
	TotalBytes   int64  `json:"total_bytes"`    // size of the finished rip
	ChunkBytes   int    `json:"chunk_bytes"`    // bytes per write; default 64 KiB
	ChunkDelayMs int    `json:"chunk_delay_ms"` // pause after each chunk
	StallAtBytes int64  `json:"stall_at_bytes"` // stop writing here; 0 = never stall
	StallMode    string `json:"stall_mode"`     // StallReading (default) or StallFrozen
	ExitCode     int    `json:"exit_code"`      // exit status after a finished rip

	// JunkLineBytes, if set, prints one line that long before the rip starts,
	// longer than any robot line Discern accepts.
	JunkLineBytes int `json:"junk_line_bytes"`

	// DebugLog, if set, is written to a temporary file that is then deleted
	// while still open, the way MakeMKV keeps its debug log. On Linux it can
	// only be read back through /proc/<pid>/fd.
	DebugLog string `json:"debug_log"`

	// ReadCounterFile, if set, gets the fake's running total of bytes read
	// after every read. Tests use it where /proc is not available.
	ReadCounterFile string `json:"read_counter_file"`
}

// keepOpen holds the deleted debug log's file. Without a live reference the
// garbage collector could close it, and the log would vanish.
var keepOpen *os.File

// Main runs the fake with makemkvcon's arguments (without the program name)
// and returns the exit status.
func Main(args []string, stdout, stderr io.Writer) int {
	var sc Scenario
	if raw := os.Getenv(EnvVar); raw != "" {
		if err := json.Unmarshal([]byte(raw), &sc); err != nil {
			fmt.Fprintf(stderr, "fake makemkvcon: bad %s: %v\n", EnvVar, err)
			return 2
		}
	}
	var pos []string // arguments that are not options
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			pos = append(pos, a)
		}
	}
	if len(pos) == 0 {
		fmt.Fprintln(stderr, "fake makemkvcon: no command")
		return 2
	}
	switch pos[0] {
	case "info":
		return info(sc.Info, stdout, stderr)
	case "mkv":
		if len(pos) != 4 {
			fmt.Fprintln(stderr, "fake makemkvcon: usage: mkv <source> <title> <dir>")
			return 2
		}
		title, err := strconv.Atoi(pos[2])
		if err != nil {
			fmt.Fprintf(stderr, "fake makemkvcon: bad title %q\n", pos[2])
			return 2
		}
		return mkv(sc.Mkv, title, pos[3], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "fake makemkvcon: unknown command %q\n", pos[0])
		return 2
	}
}

func info(in Info, stdout, stderr io.Writer) int {
	if in.File != "" {
		data, err := os.ReadFile(in.File)
		if err != nil {
			fmt.Fprintf(stderr, "fake makemkvcon: %v\n", err)
			return 2
		}
		stdout.Write(data)
	}
	return in.ExitCode
}

func mkv(m Mkv, title int, dir string, stdout, stderr io.Writer) int {
	fail := func(err error) int {
		fmt.Fprintf(stderr, "fake makemkvcon: %v\n", err)
		return 2
	}
	if m.TitleName == "" {
		m.TitleName = "Fake Title"
	}
	if m.ChunkBytes <= 0 {
		m.ChunkBytes = 64 << 10
	}
	if m.DebugLog != "" {
		if err := hideDebugLog(m.DebugLog); err != nil {
			return fail(err)
		}
	}
	src, err := newReader(m.ReadCounterFile)
	if err != nil {
		return fail(err)
	}
	out, err := os.Create(filepath.Join(dir, fmt.Sprintf("%s_t%02d.mkv", m.TitleName, title)))
	if err != nil {
		return fail(err)
	}
	defer out.Close()

	fmt.Fprintf(stdout, "PRGT:5018,0,\"Saving to MKV file\"\n")
	fmt.Fprintf(stdout, "PRGC:5018,0,\"Saving to MKV file\"\n")
	if m.JunkLineBytes > 0 {
		fmt.Fprintln(stdout, strings.Repeat("x", m.JunkLineBytes))
	}
	chunk := make([]byte, m.ChunkBytes)
	delay := time.Duration(m.ChunkDelayMs) * time.Millisecond
	var written int64
	for written < m.TotalBytes {
		if m.StallAtBytes > 0 && written >= m.StallAtBytes {
			stall(m.StallMode, src, delay)
		}
		if err := src.read(); err != nil {
			return fail(err)
		}
		n := int64(len(chunk))
		if left := m.TotalBytes - written; left < n {
			n = left
		}
		if _, err := out.Write(chunk[:n]); err != nil {
			return fail(err)
		}
		written += n
		// PRGV counts against a max of 65536, like makemkvcon.
		fmt.Fprintf(stdout, "PRGV:%d,%d,65536\n", written*65536/m.TotalBytes, written*65536/m.TotalBytes)
		time.Sleep(delay)
	}
	if m.ExitCode == 0 {
		fmt.Fprintf(stdout, "MSG:5036,0,1,\"Copy complete. 1 titles saved.\",\"Copy complete. %%1 titles saved.\",\"1\"\n")
	}
	return m.ExitCode
}

// stall never returns: the process waits to be stopped, as MakeMKV did.
func stall(mode string, src *reader, delay time.Duration) {
	if delay <= 0 {
		delay = 5 * time.Millisecond
	}
	for {
		if mode != StallFrozen {
			src.read() // keep the read counter rising, write nothing
		}
		time.Sleep(delay)
	}
}

// hideDebugLog writes the log to a temporary file and deletes it while
// keeping it open. Windows can't delete an open file, so there the file
// just stays.
func hideDebugLog(text string) error {
	f, err := os.CreateTemp("", "fake-makemkv-debug-*.log")
	if err != nil {
		return err
	}
	if _, err := f.WriteString(text); err != nil {
		return err
	}
	keepOpen = f
	if runtime.GOOS != "windows" {
		return os.Remove(f.Name())
	}
	return nil
}

// reader stands in for the disc: it reads the fake's own executable over and
// over, so the operating system's read counter rises the way it does for a
// real rip.
type reader struct {
	f           *os.File
	buf         []byte
	total       int64
	counterFile string
}

func newReader(counterFile string) (*reader, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	f, err := os.Open(exe)
	if err != nil {
		return nil, err
	}
	return &reader{f: f, buf: make([]byte, 64<<10), counterFile: counterFile}, nil
}

func (r *reader) read() error {
	n, err := r.f.Read(r.buf)
	if errors.Is(err, io.EOF) {
		_, err = r.f.Seek(0, io.SeekStart)
	}
	if err != nil {
		return err
	}
	r.total += int64(n)
	if r.counterFile != "" {
		// Write to a temporary name and rename, so a reader never sees a
		// half-written number. Best effort: on Windows the rename fails while
		// the test happens to be reading the file, and the next read updates
		// it anyway. A missed update only delays the count by one chunk.
		tmp := r.counterFile + ".tmp"
		if os.WriteFile(tmp, []byte(strconv.FormatInt(r.total, 10)), 0o644) == nil {
			_ = os.Rename(tmp, r.counterFile)
		}
	}
	return nil
}
