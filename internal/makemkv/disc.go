package makemkv

import (
	"bufio"
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Disc is what a scan (makemkvcon info) found on one disc.
type Disc struct {
	Name     string // disc title as MakeMKV reports it, e.g. "Sample Movie"
	Volume   string // volume label, e.g. "SAMPLE_MOVIE"
	Titles   []Title
	Messages []Message
	Attrs    map[Attr]string // every CINFO attribute, by number

	// UnknownLines holds lines the parser did not recognise: new record types
	// and plain-text notices (such as an expired-key warning). They are kept
	// for diagnostics and shown to the user; they never fail the scan.
	UnknownLines []string
}

// Title is one title on the disc. On a Blu-ray a title is a playlist.
type Title struct {
	Index      int // MakeMKV's title number, as used in "mkv disc:0 <index>"
	Name       string
	Chapters   int
	Duration   time.Duration
	SizeBytes  int64
	SourceFile string // e.g. "00800.mpls"
	SegmentMap string // e.g. "3,13,15,1"
	OutputFile string // e.g. "Sample Movie_t00.mkv"

	// MakeMKVMainFeature is true when MakeMKV itself marked this title as the
	// main feature. On discs with fake playlists, MakeMKV can run the disc's
	// menu program to find the real one, and then puts "SEG_MainFeature" or
	// "FPL_MainFeature" into the title's names. It is strong evidence, not
	// proof (see docs/DESIGN.md, section 7).
	MakeMKVMainFeature bool

	Streams []Stream
	Attrs   map[Attr]string // every TINFO attribute, by number
}

// Stream is one video, audio or subtitle track of a title.
type Stream struct {
	Index     int
	Type      string // "Video", "Audio" or "Subtitles"
	Codec     string // short codec name, e.g. "HEVC", "TrueHD", "PGS"
	Lang      string // e.g. "eng"
	VideoSize string // e.g. "3840x2160", video only
	Attrs     map[Attr]string
}

// Limits on segment maps. The values come from the disc, so they are
// untrusted input: without limits, a map such as "0-2000000000" would make
// Segments allocate gigabytes. Blu-ray clip files are numbered with five
// digits (00000.m2ts to 99999.m2ts), and a playlist holds at most 999 play
// items, so these limits are far above anything a real disc uses.
const (
	maxClipNumber = 99999
	maxSegments   = 10000
)

// Segments expands the title's segment map into clip numbers. MakeMKV writes
// runs of clips as ranges, so "1-3,7" becomes [1 2 3 7].
func (t Title) Segments() ([]int, error) {
	if t.SegmentMap == "" {
		return nil, nil
	}
	bad := func(why string) error { return fmt.Errorf("segment map %q: %s", t.SegmentMap, why) }
	clip := func(s string) (int, error) {
		n, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil || n < 0 || n > maxClipNumber {
			return 0, bad(fmt.Sprintf("%q is not a clip number (0-%d)", s, maxClipNumber))
		}
		return n, nil
	}
	var out []int
	for _, part := range strings.Split(t.SegmentMap, ",") {
		lo, hi, isRange := strings.Cut(part, "-")
		a, err := clip(lo)
		if err != nil {
			return nil, err
		}
		b := a
		if isRange {
			if b, err = clip(hi); err != nil {
				return nil, err
			}
			if b < a {
				return nil, bad(fmt.Sprintf("range %q runs backwards", part))
			}
		}
		if len(out)+(b-a+1) > maxSegments {
			return nil, bad(fmt.Sprintf("more than %d segments", maxSegments))
		}
		for n := a; n <= b; n++ {
			out = append(out, n)
		}
	}
	return out, nil
}

// ParseInfo reads the robot output of "makemkvcon -r info" and builds a Disc.
// It fails closed: a malformed record, a value it cannot read, or a scan with
// fewer titles than MakeMKV announced is an error, because a scan Discern
// cannot read completely must not be used to decide what to rip.
func ParseInfo(r io.Reader) (*Disc, error) {
	b := newBuilder()
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024) // some MSG lines are long
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := sc.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		rec, err := ParseLine(line)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNo, err)
		}
		b.add(rec)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("reading robot output: %w", err)
	}
	return b.disc()
}

// builder collects records into a Disc. Titles and streams arrive as many
// separate lines (one per attribute), so they are gathered in maps first and
// turned into structs at the end.
type builder struct {
	d         Disc
	titles    map[int]map[Attr]string
	streams   map[int]map[int]map[Attr]string // title -> stream -> attrs
	declared  int                             // title count from TCOUNT
	sawTCount bool
}

func newBuilder() *builder {
	return &builder{
		d:       Disc{Attrs: map[Attr]string{}},
		titles:  map[int]map[Attr]string{},
		streams: map[int]map[int]map[Attr]string{},
	}
}

func (b *builder) add(rec Record) {
	switch r := rec.(type) {
	case Message:
		b.d.Messages = append(b.d.Messages, r)
	case TitleCount:
		b.declared, b.sawTCount = r.Count, true
	case DiscInfo:
		b.d.Attrs[r.Attr] = r.Value
	case TitleInfo:
		if b.titles[r.Title] == nil {
			b.titles[r.Title] = map[Attr]string{}
		}
		b.titles[r.Title][r.Attr] = r.Value
	case StreamInfo:
		if b.streams[r.Title] == nil {
			b.streams[r.Title] = map[int]map[Attr]string{}
		}
		if b.streams[r.Title][r.Stream] == nil {
			b.streams[r.Title][r.Stream] = map[Attr]string{}
		}
		b.streams[r.Title][r.Stream][r.Attr] = r.Value
	case Unknown:
		b.d.UnknownLines = append(b.d.UnknownLines, r.Line)
	}
	// DRV and progress lines carry nothing for the Disc.
}

func (b *builder) disc() (*Disc, error) {
	// Completeness checks first: a scan cut short (makemkvcon killed or
	// crashed) must not look like a disc with fewer titles.
	if !b.sawTCount {
		return nil, fmt.Errorf("scan has no TCOUNT line: output incomplete")
	}
	if len(b.titles) != b.declared {
		return nil, fmt.Errorf("scan announced %d titles but described %d: output incomplete", b.declared, len(b.titles))
	}
	for idx := range b.streams {
		if b.titles[idx] == nil {
			return nil, fmt.Errorf("streams listed for title %d, which has no title information", idx)
		}
	}

	d := b.d
	d.Name = d.Attrs[AttrName]
	d.Volume = d.Attrs[AttrVolumeName]
	for _, idx := range slices.Sorted(maps.Keys(b.titles)) {
		t, err := b.title(idx)
		if err != nil {
			return nil, err
		}
		d.Titles = append(d.Titles, t)
	}
	return &d, nil
}

func (b *builder) title(idx int) (Title, error) {
	a := b.titles[idx]
	t := Title{
		Index:      idx,
		Name:       a[AttrName],
		SourceFile: a[AttrSourceFile],
		SegmentMap: a[AttrSegmentsMap],
		OutputFile: a[AttrOutputFile],
		Attrs:      a,
	}
	// A missing attribute reads as zero ("unknown"). A present attribute
	// that does not parse is an error: it means MakeMKV's format changed, and
	// guessing would silently weaken every decision built on it.
	var err error
	if t.Chapters, err = optionalInt(a, AttrChapterCount); err != nil {
		return Title{}, fmt.Errorf("title %d: %w", idx, err)
	}
	if t.SizeBytes, err = optionalInt64(a, AttrDiskSizeBytes); err != nil {
		return Title{}, fmt.Errorf("title %d: %w", idx, err)
	}
	if s, ok := a[AttrDuration]; ok {
		if t.Duration, err = parseDuration(s); err != nil {
			return Title{}, fmt.Errorf("title %d: %w", idx, err)
		}
	}
	for _, s := range []string{t.Name, t.OutputFile, a[AttrComment]} {
		if strings.Contains(s, "SEG_MainFeature") || strings.Contains(s, "FPL_MainFeature") {
			t.MakeMKVMainFeature = true
		}
	}
	for _, sidx := range slices.Sorted(maps.Keys(b.streams[idx])) {
		sa := b.streams[idx][sidx]
		t.Streams = append(t.Streams, Stream{
			Index:     sidx,
			Type:      sa[AttrType],
			Codec:     sa[AttrCodecShort],
			Lang:      sa[AttrLangCode],
			VideoSize: sa[AttrVideoSize],
			Attrs:     sa,
		})
	}
	return t, nil
}

// parseDuration reads MakeMKV's "h:mm:ss" lengths.
func parseDuration(s string) (time.Duration, error) {
	parts := strings.Split(s, ":")
	if len(parts) != 3 {
		return 0, fmt.Errorf("duration %q is not h:mm:ss", s)
	}
	var total time.Duration
	for i, unit := range []time.Duration{time.Hour, time.Minute, time.Second} {
		n, err := strconv.Atoi(parts[i])
		if err != nil || n < 0 || (i > 0 && n > 59) {
			return 0, fmt.Errorf("duration %q is not h:mm:ss", s)
		}
		total += time.Duration(n) * unit
	}
	return total, nil
}

func optionalInt(a map[Attr]string, attr Attr) (int, error) {
	s, ok := a[attr]
	if !ok {
		return 0, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("attribute %d: %q is not a count", attr, s)
	}
	return n, nil
}

func optionalInt64(a map[Attr]string, attr Attr) (int64, error) {
	s, ok := a[attr]
	if !ok {
		return 0, nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("attribute %d: %q is not a size in bytes", attr, s)
	}
	return n, nil
}

// Resolution is the title's picture as a line count: 2160, 1080, 720, or 480
// for standard definition. It takes the higher of what the width and the
// height say: width catches films cropped to 2.39:1 and stored as 1920x800;
// height catches anamorphic 1440x1080 Blu-rays. Both are 1080. It reports
// false when the title has no readable video size.
func (t Title) Resolution() (int, bool) {
	for _, s := range t.Streams {
		if s.Type != "Video" || s.VideoSize == "" {
			continue
		}
		ws, hs, ok := strings.Cut(s.VideoSize, "x")
		w, errW := strconv.Atoi(ws)
		h, errH := strconv.Atoi(hs)
		if !ok || errW != nil || errH != nil || w <= 0 || h <= 0 {
			return 0, false
		}
		return max(lines(w, 3800, 1900, 1270), lines(h, 2000, 1000, 700)), true
	}
	return 0, false
}

// lines places a dimension against the 2160, 1080 and 720 thresholds.
func lines(v, uhd, fhd, hd int) int {
	switch {
	case v >= uhd:
		return 2160
	case v >= fhd:
		return 1080
	case v >= hd:
		return 720
	}
	return 480
}
