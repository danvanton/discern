package selector

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/danvanton/discern/internal/discdb"
	"github.com/danvanton/discern/internal/jobs"
	"github.com/danvanton/discern/internal/makemkv"
)

// title makes a scan title: its length in minutes and its bitrate in Mbit/s
// (which sets its size).
func title(idx int, minutes float64, mbps float64) makemkv.Title {
	d := time.Duration(minutes * float64(time.Minute))
	return makemkv.Title{Index: idx, SourceFile: "x.mpls", SegmentMap: "1", Duration: d,
		SizeBytes: int64(mbps * 1e6 / 8 * d.Seconds())}
}

func disc(ts ...makemkv.Title) *makemkv.Disc { return &makemkv.Disc{Titles: ts} }

func decide(d *makemkv.Disc) jobs.Selection { return Decide(Input{Disc: d, Indexed: true}) }

func wantPick(t *testing.T, s jobs.Selection, c jobs.Confidence, titles ...int) {
	t.Helper()
	if s.Confidence != c || !slices.Equal(s.Titles, titles) {
		t.Errorf("got %s %v; want %s %v\nreasons %q\nholds %q", s.Confidence, s.Titles, c, titles, s.Reasons, s.Holds)
	}
	if (c == jobs.Low) != (len(s.Holds) > 0) {
		t.Errorf("confidence %s with holds %q: Low must mean held", s.Confidence, s.Holds)
	}
	if s.Reason == "" {
		t.Error("no one-sentence reason")
	}
}

func hasText(list []string, sub string) bool {
	return slices.ContainsFunc(list, func(s string) bool { return strings.Contains(s, sub) })
}

func TestNoTitlesIsAHold(t *testing.T) {
	for _, d := range []*makemkv.Disc{nil, disc()} {
		s := decide(d)
		if s.Confidence != jobs.Low || len(s.Titles) != 0 || s.Reason != "the scan found no titles" {
			t.Errorf("%+v", s)
		}
	}
}

func TestTheOnlyFeatureLengthTitle(t *testing.T) {
	s := decide(disc(title(0, 1, 30), title(1, 118, 30), title(2, 25, 20), title(3, 44, 25)))
	wantPick(t, s, jobs.Medium, 1)
	if !hasText(s.Reasons, "only feature-length") || !hasText(s.Reasons, "TheDiscDB doesn't know") {
		t.Errorf("reasons %q", s.Reasons)
	}
}

// The same video through two playlists is one film, not two.
func TestACopyIsNotASecondFilm(t *testing.T) {
	a, b := title(3, 118, 30), title(7, 118, 30)
	b.SizeBytes += b.SizeBytes / 2000 // 0.05%: within the slack
	wantPick(t, decide(disc(a, b)), jobs.Medium, 3)

	b.SizeBytes += b.SizeBytes / 200             // now 0.55%: another video
	wantPick(t, decide(disc(a, b)), jobs.Low, 7) // the larger, as a guess
}

// A making-of longer than the film, at a fraction of its bitrate, is no
// rival (Event Horizon, Hellboy and others in TheDiscDB).
func TestALeanLongTitleIsAnExtra(t *testing.T) {
	s := decide(disc(title(0, 96, 45), title(1, 103, 7.2)))
	wantPick(t, s, jobs.Medium, 0)
	if !hasText(s.Reasons, "title 1 (1:43:00) carries under half the 45.0 Mbit/s of the disc's richest") {
		t.Errorf("reasons %q", s.Reasons)
	}
	// Under 3 Mbit/s is never the film, even alone.
	s = decide(disc(title(0, 100, 2.5), title(1, 30, 20)))
	wantPick(t, s, jobs.Low, 0)
	if !hasText(s.Holds, "no feature-length title passed the checks") || !hasText(s.Reasons, "title 0 (1:40:00) carries under 3 Mbit/s") {
		t.Errorf("holds %q, reasons %q", s.Holds, s.Reasons)
	}
}

func TestTwoFeaturesHold(t *testing.T) {
	s := decide(disc(title(0, 113, 30), title(1, 116, 30)))
	wantPick(t, s, jobs.Low, 1) // the longest, as a guess
	if !hasText(s.Holds, "perhaps another cut") {
		t.Errorf("holds %q", s.Holds)
	}
	// MakeMKV's mark picks the guess, but doesn't lift the hold.
	a, b := title(0, 113, 30), title(1, 116, 30)
	a.MakeMKVMainFeature = true
	s = decide(disc(a, b))
	wantPick(t, s, jobs.Low, 0)
	if !hasText(s.Reasons, "MakeMKV marked it") {
		t.Errorf("reasons %q", s.Reasons)
	}
}

func TestLookalikesAreDecoys(t *testing.T) {
	var ts []makemkv.Title
	for i := 0; i < 300; i++ {
		ts = append(ts, title(i, 95+float64(i%10)/10, 34+float64(i)/100))
	}
	s := decide(disc(ts...))
	if s.Confidence != jobs.Low || !hasText(s.Holds, "decoy playlists") {
		t.Errorf("got %s, holds %q", s.Confidence, s.Holds)
	}
	for _, r := range append(s.Reasons, s.Holds...) {
		if len(r) > 400 {
			t.Errorf("a reason of %d characters: %.80s...", len(r), r)
		}
	}
}

func TestNoFeatureLengthTitle(t *testing.T) {
	s := decide(disc(title(0, 25, 30), title(1, 12, 30)))
	wantPick(t, s, jobs.Low, 0)
	if !hasText(s.Holds, "no title is 45 minutes") {
		t.Errorf("holds %q", s.Holds)
	}
}

func TestMakeMKVsMarkElsewhereIsAHold(t *testing.T) {
	a, b := title(0, 118, 30), title(1, 30, 30)
	b.MakeMKVMainFeature = true
	s := decide(disc(a, b))
	wantPick(t, s, jobs.Low, 0)
	if !hasText(s.Holds, "MakeMKV marked title 1") {
		t.Errorf("holds %q", s.Holds)
	}
	a.MakeMKVMainFeature, b.MakeMKVMainFeature = true, false
	wantPick(t, decide(disc(a, b)), jobs.Medium, 0)
}

// The samples in testdata/discdb, and the owner's own scans.

func sampleIndex(t *testing.T) *discdb.Index {
	t.Helper()
	ix, _, err := discdb.Build(filepath.Join("..", "..", "testdata", "discdb"), "test", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return ix
}

func ownerScan(t *testing.T, name string) *makemkv.Disc {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "makemkv", name))
	if err != nil {
		t.Fatal(err)
	}
	var d makemkv.Disc
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatal(err)
	}
	return &d
}

func find(t *testing.T, ix *discdb.Index, tmdb int, format string) *discdb.Disc {
	t.Helper()
	for i, d := range ix.Discs {
		if d.Movie.TMDbID == tmdb && (format == "" || d.Format == format) && len(d.Titles) > 20 {
			return &ix.Discs[i]
		}
	}
	t.Fatalf("no sample disc for TMDb %d", tmdb)
	return nil
}

func TestTheOwnersDiscsAreHigh(t *testing.T) {
	ix := sampleIndex(t)
	for _, c := range []struct {
		scan       string
		tmdb, main int
	}{{"scan-star-trek-ii-uhd.json", 154, 14}, {"scan-star-trek-iii-uhd.json", 157, 29}} {
		d := ownerScan(t, c.scan)
		s := Decide(Input{Disc: d, Match: ix.Match(d, nil), Indexed: true, Movie: &jobs.Movie{TMDbID: c.tmdb, Title: "Star Trek"}})
		wantPick(t, s, jobs.High, c.main)
		if s.Movie == nil || s.Movie.TMDbID != c.tmdb {
			t.Errorf("%s: TheDiscDB's movie %+v", c.scan, s.Movie)
		}
		if s.Labels[c.main].Type != discdb.MainMovie {
			t.Errorf("%s: label %+v", c.scan, s.Labels[c.main])
		}
		// The same disc found as another movie by its name is a hold.
		s = Decide(Input{Disc: d, Match: ix.Match(d, nil), Indexed: true, Movie: &jobs.Movie{TMDbID: 1, Title: "Elsewhere", Year: 2001}})
		wantPick(t, s, jobs.Low, c.main)
		if !hasText(s.Holds, "but its name found Elsewhere (2001)") {
			t.Errorf("holds %q", s.Holds)
		}
	}
}

// A feature-length title TheDiscDB's record doesn't list could be another
// cut: hold. (A long one stops the match itself, decision D46, so this is
// for a short one beside much more running time that does line up.)
func TestAnUnlistedFeatureIsAHold(t *testing.T) {
	known := discdb.Disc{Movie: discdb.Movie{TMDbID: 7, Title: "Box Set"}, Release: "r"}
	for i := 0; i < 10; i++ {
		tt := discdb.Title{Index: i, File: fmt.Sprintf("%05d.mpls", i), Segments: fmt.Sprint(i), Seconds: 6000 + i, Size: 30e9}
		if i == 4 {
			tt.Type = discdb.MainMovie
		} else {
			tt.Type = "Extra"
		}
		known.Titles = append(known.Titles, tt)
	}
	ix := indexOf(t, known)
	d := known.Scan()
	d.Titles = append(d.Titles, makemkv.Title{Index: 99, SourceFile: "00099.mpls", SegmentMap: "99",
		Duration: 46 * time.Minute, SizeBytes: 10e9})
	m := ix.Match(d, nil)
	if m == nil {
		t.Fatal("no match")
	}
	s := Decide(Input{Disc: d, Match: m, Indexed: true})
	wantPick(t, s, jobs.Low, 4)
	if !hasText(s.Holds, "title 99 (0:46:00) is feature-length but not in TheDiscDB's record") {
		t.Errorf("holds %q", s.Holds)
	}
}

func indexOf(t *testing.T, discs ...discdb.Disc) *discdb.Index {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ix.json.gz")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := (&discdb.Index{Version: discdb.Version, Discs: discs}).Write(f); err != nil {
		t.Fatal(err)
	}
	f.Close()
	ix, err := discdb.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return ix
}

// Alien's disc lists both cuts as MainMovie: ask, with both named (D41).
func TestTwoCutsAsk(t *testing.T) {
	ix := sampleIndex(t)
	alien := find(t, ix, 348, "")
	d := alien.Scan()
	s := Decide(Input{Disc: d, Match: ix.Match(d, nil), Indexed: true})
	if s.Confidence != jobs.Low || len(s.Titles) != 0 || len(s.Choices) != 2 {
		t.Fatalf("got %s, titles %v, choices %v", s.Confidence, s.Titles, s.Choices)
	}
	if !hasText(s.Holds, "Theatrical") || !hasText(s.Holds, "Director's Cut") {
		t.Errorf("holds %q don't name the cuts", s.Holds)
	}
}

// Despicable Me's fake playlists: TheDiscDB knows the real one; unknown,
// the disc is held.
func TestFakePlaylists(t *testing.T) {
	ix := sampleIndex(t)
	dm := find(t, ix, 20352, "")
	d := dm.Scan()
	var main int
	for _, t := range dm.Titles {
		if t.Type == discdb.MainMovie {
			main = t.Index
		}
	}
	wantPick(t, Decide(Input{Disc: d, Match: ix.Match(d, nil), Indexed: true}), jobs.High, main)
	s := Decide(Input{Disc: d, Indexed: true})
	if s.Confidence != jobs.Low {
		t.Errorf("unknown fake-playlist disc: %s %v, reasons %q", s.Confidence, s.Titles, s.Reasons)
	}
}

// A match that names no main title still names the movie and rules out the
// titles it classifies (D48); the scan decides, at most Medium.
func TestAMatchWithoutAMain(t *testing.T) {
	ix := sampleIndex(t)
	d := ownerScan(t, "scan-star-trek-iii-uhd.json")
	m := ix.Match(d, nil)
	main := m.Mains()[0]
	m.Titles[main] = discdb.Title{} // no class
	s := Decide(Input{Disc: d, Match: m, Indexed: true})
	if s.Confidence == jobs.High || s.Movie == nil || s.Movie.TMDbID != 157 {
		t.Errorf("got %s, movie %+v", s.Confidence, s.Movie)
	}
	if !hasText(s.Reasons, "doesn't say which title is the movie") {
		t.Errorf("reasons %q", s.Reasons)
	}
}

// A shorter title with much more data per second may be the film itself,
// under 45 minutes (decision D52): Frosty the Snowman's disc, a 25:44
// special beside a 47-minute documentary at two thirds of its bitrate.
func TestARicherShorterTitleMayBeTheFilm(t *testing.T) {
	s := decide(disc(title(3, 47, 26.7), title(4, 25.7, 39.9)))
	wantPick(t, s, jobs.Low, 3)
	if !hasText(s.Holds, "title 4 (0:25:42) is shorter than title 3 (0:47:00) but carries over 1.25 times its data per second") {
		t.Errorf("holds %q", s.Holds)
	}
	// Under 1.25 times, or under 20 minutes, is no rival.
	wantPick(t, decide(disc(title(0, 118, 30), title(1, 30, 36))), jobs.Medium, 0)
	wantPick(t, decide(disc(title(0, 118, 30), title(1, 19, 60))), jobs.Medium, 0)
}

// The Woman King's 4K record names a 9:48 extra as the movie and leaves
// the film unclassified (decision D53): hold, and let the scan decide.
func TestADoubtfulRecordIsHeld(t *testing.T) {
	known := discdb.Disc{Movie: discdb.Movie{TMDbID: 724495, Title: "The Woman King", Year: 2022}, Release: "2022-4k",
		Titles: []discdb.Title{
			{Index: 0, File: "00001.mpls", Segments: "1", Seconds: 8092, Size: 72e9},
			{Index: 1, File: "00722.m2ts", Segments: "722", Seconds: 700, Size: 1.6e9, Type: "Extra", Name: "Woman / Warrior"},
			{Index: 2, File: "00725.m2ts", Segments: "725", Seconds: 588, Size: 1.3e9, Type: discdb.MainMovie, Name: "On Set"},
		}}
	ix := indexOf(t, known)
	d := known.Scan()
	s := Decide(Input{Disc: d, Match: ix.Match(d, nil), Indexed: true})
	wantPick(t, s, jobs.Low, 0)
	if !hasText(s.Holds, "looks wrong: it names title 2 (0:09:48), “On Set” as the movie, but leaves title 0 (2:14:52) unclassified") {
		t.Errorf("holds %q", s.Holds)
	}
	if s.Movie == nil || s.Movie.TMDbID != 724495 {
		t.Errorf("TheDiscDB's movie %+v", s.Movie)
	}
}

func TestDoubtfulMain(t *testing.T) {
	main := discdb.Title{Index: 4, Seconds: 1544, Type: discdb.MainMovie}
	for _, c := range []struct {
		name   string
		main   discdb.Title
		record []discdb.Title
		want   int // -1: not doubtful
	}{
		{"a film of feature length", discdb.Title{Seconds: 6000}, []discdb.Title{{Index: 1, Seconds: 9000}}, -1},
		{"a long title called an extra (Frosty)", main, []discdb.Title{{Index: 3, Seconds: 2820, Type: "Extra"}}, -1},
		{"unclassified, but under 45 minutes", main, []discdb.Title{{Index: 3, Seconds: 2600}}, -1},
		{"unclassified, not twice as long", discdb.Title{Seconds: 2600}, []discdb.Title{{Index: 3, Seconds: 5000}}, -1},
		{"unclassified and long: the longest", main, []discdb.Title{{Index: 3, Seconds: 2800}, {Index: 7, Seconds: 8092}, {Index: 9, Seconds: 8092}}, 7},
	} {
		long, ok := DoubtfulMain(c.main, c.record)
		if ok != (c.want >= 0) || ok && long.Index != c.want {
			t.Errorf("%s: %+v, %v; want %d", c.name, long, ok, c.want)
		}
	}
}

// Two MakeMKV marks on different videos are a hold even when one is the
// pick: MakeMKV itself wasn't sure.
func TestTwoMakeMKVMarksHold(t *testing.T) {
	a, b := title(0, 118, 30), title(5, 30, 30)
	a.MakeMKVMainFeature, b.MakeMKVMainFeature = true, true
	for _, order := range [][]makemkv.Title{{a, b}, {b, a}} {
		s := decide(disc(order...))
		wantPick(t, s, jobs.Low, 0)
		if !hasText(s.Holds, "MakeMKV marked title 5 (0:30:00) as the main feature as well as this one") {
			t.Errorf("holds %q", s.Holds)
		}
	}
}

// Discs of two movies fitting is said, and holds (Lookup's tie).
func TestATieInTheDiscDBHolds(t *testing.T) {
	s := Decide(Input{Disc: disc(title(0, 118, 30)), Indexed: true,
		Tied: []discdb.Movie{{TMDbID: 1, Title: "Face/Off", Year: 1997}, {TMDbID: 2, Title: "Snake Eyes", Year: 1998}}})
	wantPick(t, s, jobs.Low, 0)
	if !hasText(s.Holds, "TheDiscDB has discs of 2 movies that fit this one (Face/Off (1997), Snake Eyes (1998))") ||
		hasText(s.Reasons, "doesn't know this disc") {
		t.Errorf("holds %q, reasons %q", s.Holds, s.Reasons)
	}
}

// With no feature-length title, the fallback skips titles TheDiscDB calls
// something other than the movie.
func TestTheFallbackSkipsWhatTheDiscDBRulesOut(t *testing.T) {
	known := discdb.Disc{Movie: discdb.Movie{TMDbID: 9, Title: "Short"}, Release: "r", Titles: []discdb.Title{
		{Index: 0, File: "00000.mpls", Segments: "0", Seconds: 2400, Size: 9e9, Type: "Extra"},
		{Index: 1, File: "00001.mpls", Segments: "1", Seconds: 1800, Size: 7e9},
	}}
	ix := indexOf(t, known)
	d := known.Scan()
	s := Decide(Input{Disc: d, Match: ix.Match(d, nil), Indexed: true})
	wantPick(t, s, jobs.Low, 1)
}
