package fake

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// MkvmergeEnvVar turns a test binary (or cmd/fake-mkvmerge) into a fake
// mkvmerge for the Backup + remux strategy. Its value is "on", or a file
// that each run's arguments are appended to, one line per run, so a test
// can check which tracks were asked for.
const MkvmergeEnvVar = "FAKE_MKVMERGE"

// The tracks the fake finds in a playlist or clip: Blu-ray-like, with the
// IDs mkvmerge would give them.
var sourceTracks = []map[string]any{
	{"id": 0, "type": "video", "codec": "HEVC/H.265/MPEG-H", "properties": map[string]any{"language": "und"}},
	{"id": 1, "type": "audio", "codec": "TrueHD Atmos", "properties": map[string]any{"language": "eng"}},
	{"id": 2, "type": "audio", "codec": "AC-3", "properties": map[string]any{"language": "eng"}},
	{"id": 3, "type": "audio", "codec": "AC-3", "properties": map[string]any{"language": "fre"}},
	{"id": 4, "type": "subtitles", "codec": "HDMV PGS", "properties": map[string]any{"language": "eng"}},
	{"id": 5, "type": "subtitles", "codec": "HDMV PGS", "properties": map[string]any{"language": "fre"}},
}

// The tracks the fake reports for a finished MKV.
var fileTracks = []map[string]any{
	{"id": 0, "type": "video", "codec": "HEVC/H.265/MPEG-H", "properties": map[string]any{"language": "und"}},
	{"id": 1, "type": "audio", "codec": "TrueHD Atmos", "properties": map[string]any{"language": "eng"}},
	{"id": 2, "type": "subtitles", "codec": "HDMV PGS", "properties": map[string]any{"language": "eng"}},
}

// writeOptions are the options Discern passes when it writes a file, each
// with the form of its value (nil: no value), as the image's mkvmerge v92
// lists them (--default-track-flag <TID[:bool]>, checked in its binary on
// 2026-11-17). Anything else is refused, so a misspelled option or a wrong
// value fails a test instead of the first real remux.
var writeOptions = map[string]*regexp.Regexp{
	"--gui-mode":           nil,
	"--no-audio":           nil,
	"--no-subtitles":       nil,
	"-o":                   regexp.MustCompile(`.`),
	"--audio-tracks":       regexp.MustCompile(`^[0-9]+(,[0-9]+)*$`),
	"--subtitle-tracks":    regexp.MustCompile(`^[0-9]+(,[0-9]+)*$`),
	"--default-track-flag": regexp.MustCompile(`^[0-9]+:[01]$`),
	"--track-order":        regexp.MustCompile(`^0:[0-9]+(,0:[0-9]+)*$`),
}

// Mkvmerge is the fake mkvmerge. It understands the two uses Discern has:
// "-J <file>" identifies a playlist, clip or MKV; "--gui-mode -o <out> ...
// <source>" writes out as large as the backup's stream file, with GUI-mode
// progress lines.
func Mkvmerge(args []string, stdout, stderr io.Writer) int {
	if log := os.Getenv(MkvmergeEnvVar); log != "" && log != "on" {
		if f, err := os.OpenFile(log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
			fmt.Fprintln(f, strings.Join(args, " "))
			f.Close()
		}
	}
	if len(args) == 2 && args[0] == "-J" {
		tracks := sourceTracks
		if strings.HasSuffix(strings.ToLower(args[1]), ".mkv") {
			tracks = fileTracks
		}
		if _, err := os.Stat(args[1]); err != nil {
			fmt.Fprintf(stderr, "fake mkvmerge: %v\n", err)
			return 2
		}
		json.NewEncoder(stdout).Encode(map[string]any{
			"container": map[string]any{"recognized": true, "supported": true},
			"tracks":    tracks,
		})
		return 0
	}
	var out string
	for i := 0; i < len(args)-1; i++ { // the last argument is the source
		value, known := writeOptions[args[i]]
		if !known {
			fmt.Fprintf(stderr, "fake mkvmerge: Error: unknown option or argument %q\n", args[i])
			return 2
		}
		if value == nil {
			continue
		}
		if i+1 >= len(args)-1 || !value.MatchString(args[i+1]) {
			fmt.Fprintf(stderr, "fake mkvmerge: Error: %s needs a value like %s\n", args[i], value)
			return 2
		}
		if args[i] == "-o" {
			out = args[i+1]
		}
		i++
	}
	if out == "" || len(args) == 0 {
		fmt.Fprintln(stderr, "fake mkvmerge: usage: -J <file> | --gui-mode -o <out> ... <source>")
		return 2
	}
	src := args[len(args)-1]
	stream := filepath.Join(filepath.Dir(filepath.Dir(src)), "STREAM", "00000.m2ts")
	in, err := os.Open(stream)
	if err != nil {
		fmt.Fprintf(stderr, "fake mkvmerge: %v\n", err)
		return 2
	}
	defer in.Close()
	f, err := os.Create(out)
	if err != nil {
		fmt.Fprintf(stderr, "fake mkvmerge: %v\n", err)
		return 2
	}
	defer f.Close()
	for _, pct := range []int{0, 50} {
		fmt.Fprintf(stdout, "#GUI#progress %d%%\n", pct)
	}
	if _, err := io.Copy(f, in); err != nil {
		fmt.Fprintf(stderr, "fake mkvmerge: %v\n", err)
		return 2
	}
	fmt.Fprintln(stdout, "#GUI#progress 100%")
	fmt.Fprintln(stdout, "Multiplexing took 1 second.")
	return 0
}
