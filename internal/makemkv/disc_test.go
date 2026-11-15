package makemkv

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func loadSample(t *testing.T) *Disc {
	t.Helper()
	f, err := os.Open("../../testdata/makemkv/info-sample-movie.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	d, err := ParseInfo(f)
	if err != nil {
		t.Fatalf("ParseInfo: %v", err)
	}
	return d
}

func TestParseInfoDisc(t *testing.T) {
	d := loadSample(t)
	if d.Name != "Sample Movie" || d.Volume != "SAMPLE_MOVIE" {
		t.Errorf("disc name/volume = %q/%q", d.Name, d.Volume)
	}
	if len(d.Titles) != 4 {
		t.Fatalf("got %d titles, want 4", len(d.Titles))
	}
	if len(d.Messages) != 5 {
		t.Errorf("got %d messages, want 5", len(d.Messages))
	}
	if last := d.Messages[len(d.Messages)-1]; last.Code != 5011 {
		t.Errorf("last message code = %d, want 5011", last.Code)
	}
}

func TestParseInfoMainTitle(t *testing.T) {
	m := loadSample(t).Titles[0]
	checks := []struct {
		name      string
		got, want any
	}{
		{"index", m.Index, 0},
		{"chapters", m.Chapters, 24},
		{"duration", m.Duration, time.Hour + 58*time.Minute + 12*time.Second},
		{"size", m.SizeBytes, int64(33721839616)},
		{"source file", m.SourceFile, "00800.mpls"},
		{"segment map", m.SegmentMap, "12"},
		{"output file", m.OutputFile, "Sample Movie_t00.mkv"},
		{"streams", len(m.Streams), 3},
		{"video", m.Streams[0].Type + " " + m.Streams[0].VideoSize, "Video 1920x1080"},
		{"audio", m.Streams[1].Codec + " " + m.Streams[1].Lang, "TrueHD eng"},
		{"subtitles", m.Streams[2].Type, "Subtitles"},
		{"raw attrs kept", m.Attrs[Attr(30)], "Sample Movie - 24 chapter(s) , 31.4 GB"},
		{"no MakeMKV main-feature mark", m.MakeMKVMainFeature, false},
	}
	for _, c := range checks {
		if !reflect.DeepEqual(c.got, c.want) {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
}

func TestParseInfoEscapedTitleName(t *testing.T) {
	if got := loadSample(t).Titles[3].Name; got != `Sample "Teaser", short` {
		t.Errorf("title 3 name = %q", got)
	}
}

func TestMainFeatureMark(t *testing.T) {
	in := strings.Join([]string{
		"TCOUNT:2",
		`TINFO:0,27,0,"Movie_t00.mkv"`,
		`TINFO:1,27,0,"Movie-SEG_MainFeature_t01.mkv"`,
	}, "\n")
	d, err := ParseInfo(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if d.Titles[0].MakeMKVMainFeature || !d.Titles[1].MakeMKVMainFeature {
		t.Errorf("main-feature marks = %v, %v; want false, true",
			d.Titles[0].MakeMKVMainFeature, d.Titles[1].MakeMKVMainFeature)
	}
}

func TestParseInfoRejectsBadLine(t *testing.T) {
	_, err := ParseInfo(strings.NewReader("TCOUNT:1\nTINFO:0,9,broken\n"))
	if err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Errorf("err = %v, want an error naming line 2", err)
	}
}

// Completeness checks: a scan cut short, or one whose values can't be read,
// must be refused rather than used with titles or values missing.
func TestParseInfoRefusesIncompleteOrUnreadableScans(t *testing.T) {
	cases := []struct {
		name, in, wantErr string
	}{
		{"no TCOUNT at all", `TINFO:0,9,0,"1:58:12"`, "no TCOUNT"},
		{"fewer titles than announced", "TCOUNT:2\n" + `TINFO:0,9,0,"1:58:12"`, "announced 2 titles but described 1"},
		{"streams for an undescribed title", "TCOUNT:1\n" + `TINFO:0,9,0,"1:58:12"` + "\n" + `SINFO:5,0,1,6201,"Video"`, "title 5"},
		{"unreadable duration", "TCOUNT:1\n" + `TINFO:0,9,0,"1:58:12.5"`, "not h:mm:ss"},
		{"unreadable size", "TCOUNT:1\n" + `TINFO:0,11,0,"31.4 GB"`, "not a size"},
		{"unreadable chapter count", "TCOUNT:1\n" + `TINFO:0,8,0,"many"`, "not a count"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ParseInfo(strings.NewReader(c.in))
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("err = %v, want one containing %q", err, c.wantErr)
			}
		})
	}
}

func TestParseInfoMissingValuesReadAsZero(t *testing.T) {
	d, err := ParseInfo(strings.NewReader("TCOUNT:1\n" + `TINFO:0,2,0,"No numbers"`))
	if err != nil {
		t.Fatal(err)
	}
	if ti := d.Titles[0]; ti.Duration != 0 || ti.SizeBytes != 0 || ti.Chapters != 0 {
		t.Errorf("missing values = %v, %d, %d; want all zero", ti.Duration, ti.SizeBytes, ti.Chapters)
	}
}

func TestParseInfoKeepsUnknownLines(t *testing.T) {
	in := "Your MakeMKV registration key has expired\nTCOUNT:0\nNEWTHING:1\n"
	d, err := ParseInfo(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Your MakeMKV registration key has expired", "NEWTHING:1"}
	if !reflect.DeepEqual(d.UnknownLines, want) {
		t.Errorf("UnknownLines = %q, want %q", d.UnknownLines, want)
	}
}

func TestSegments(t *testing.T) {
	cases := []struct {
		in   string
		want []int
		bad  bool
	}{
		{"", nil, false},
		{"12", []int{12}, false},
		{"9,8", []int{9, 8}, false},
		{"13-16", []int{13, 14, 15, 16}, false},
		{"1-3,7", []int{1, 2, 3, 7}, false},
		{"99999", []int{99999}, false},
		{"5-3", nil, true},           // runs backwards
		{"x", nil, true},             // not a number
		{"-1", nil, true},            // negative
		{"100000", nil, true},        // above the highest clip number
		{"0-2000000000", nil, true},  // would allocate gigabytes
		{"0-9999,0-9999", nil, true}, // 20,000 segments, over the limit
	}
	for _, c := range cases {
		got, err := Title{SegmentMap: c.in}.Segments()
		if (err != nil) != c.bad {
			t.Errorf("Segments(%q) error = %v, want error: %v", c.in, err, c.bad)
			continue
		}
		if !c.bad && !reflect.DeepEqual(got, c.want) {
			t.Errorf("Segments(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestParseDuration(t *testing.T) {
	good := map[string]time.Duration{
		"1:58:12":  time.Hour + 58*time.Minute + 12*time.Second,
		"0:00:16":  16 * time.Second,
		"12:00:00": 12 * time.Hour,
	}
	for in, want := range good {
		got, err := parseDuration(in)
		if err != nil || got != want {
			t.Errorf("parseDuration(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "12:00", "a:b:c", "1:58:12.5", "1:60:00", "-1:00:00"} {
		if got, err := parseDuration(in); err == nil {
			t.Errorf("parseDuration(%q) = %v, want an error", in, got)
		}
	}
}

// Every scan in testdata must parse: the demo and the tests rely on them.
func TestAllTestdataScansParse(t *testing.T) {
	files, _ := filepath.Glob("../../testdata/makemkv/info-*.txt")
	if len(files) == 0 {
		t.Fatal("no scans in testdata")
	}
	for _, f := range files {
		r, err := os.Open(f)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ParseInfo(r); err != nil {
			t.Errorf("%s: %v", filepath.Base(f), err)
		}
		r.Close()
	}
}

func TestResolution(t *testing.T) {
	for size, want := range map[string]int{
		"3840x2160": 2160, "1920x1080": 1080, "1920x800": 1080, // cropped 2.39:1
		"1440x1080": 1080, // anamorphic
		"1280x720":  720, "720x480": 480, "720x576": 480,
	} {
		title := Title{Streams: []Stream{{Type: "Audio"}, {Type: "Video", VideoSize: size}}}
		if got, ok := title.Resolution(); !ok || got != want {
			t.Errorf("Resolution(%s) = %d, %v; want %d", size, got, ok, want)
		}
	}
	for _, size := range []string{"", "1920", "axb", "0x1080", "-5x5"} {
		if _, ok := (Title{Streams: []Stream{{Type: "Video", VideoSize: size}}}).Resolution(); ok {
			t.Errorf("Resolution(%q) was readable", size)
		}
	}
}
