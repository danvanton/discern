package discdb

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/danvanton/discern/internal/makemkv"
)

// testdata/discdb is a small copy of TheDiscDB's repository (see its README).
var sampleRoot = filepath.Join("..", "..", "testdata", "discdb")

func build(t *testing.T) *Index {
	t.Helper()
	ix, rep, err := Build(sampleRoot, "d27d3c9", time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Movies != 5 || rep.Discs != 9 || len(rep.Skipped) != 0 {
		t.Fatalf("built %d movies, %d discs, skipped %v; want 5, 9, none", rep.Movies, rep.Discs, rep.Skipped)
	}
	return ix
}

// scan reads one of the owner's real scans, saved from the homelab.
func scan(t *testing.T, name string) *makemkv.Disc {
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

func TestBuildKeepsWhatTheSelectorNeeds(t *testing.T) {
	ix := build(t)
	var alien *Disc
	for i := range ix.Discs {
		if ix.Discs[i].Movie.TMDbID == 348 {
			alien = &ix.Discs[i]
		}
	}
	if alien == nil {
		t.Fatal("Alien (TMDb 348) isn't in the index")
	}
	if alien.Movie.Title != "Alien" || alien.Movie.Year != 1979 || alien.Movie.IMDbID != "tt0078748" ||
		alien.Release != "2013-blu-ray" || alien.ContentHash != "394AD63181B2C0A626214D317EA984F8" {
		t.Errorf("Alien's disc: %+v", alien)
	}
	// Two cuts, both MainMovie, named.
	var mains []string
	for _, tt := range alien.Titles {
		if tt.Type == MainMovie {
			mains = append(mains, tt.File+" "+tt.Name)
		}
	}
	if !slices.Equal(mains, []string{"00800.mpls Alien 1979 Theatrical Release", "00801.mpls Alien Director's Cut"}) {
		t.Errorf("Alien's main movies: %q", mains)
	}
	for _, tt := range alien.Titles {
		if tt.File == "00801.mpls" && (tt.Seconds != 1*3600+55*60+49 || tt.Size != 32131178496) {
			t.Errorf("Director's Cut: %d s, %d bytes", tt.Seconds, tt.Size)
		}
	}
}

func TestWriteAndReadBack(t *testing.T) {
	ix := build(t)
	var buf bytes.Buffer
	if err := ix.Write(&buf); err != nil {
		t.Fatal(err)
	}
	got, err := Read(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Discs) != 9 || got.Source != "d27d3c9" || !got.Built.Equal(ix.Built) {
		t.Errorf("read back %d discs, source %q, built %v", len(got.Discs), got.Source, got.Built)
	}
	if got.Match(scan(t, "scan-star-trek-ii-uhd.json"), nil) == nil {
		t.Error("a read-back index doesn't match: its lookup wasn't prepared")
	}
}

// A downloaded index is checked before use; a doubtful one is refused.
func TestReadRefusesABadIndex(t *testing.T) {
	gz := func(v any) *bytes.Buffer {
		var b bytes.Buffer
		zw := gzip.NewWriter(&b)
		json.NewEncoder(zw).Encode(v)
		zw.Close()
		return &b
	}
	good := Disc{Movie: Movie{TMDbID: 1, Title: "x"}, Titles: []Title{{File: "00800.mpls", Seconds: 10}}}
	for name, c := range map[string]struct {
		in   *bytes.Buffer
		want string
	}{
		"not gzip":      {bytes.NewBufferString(`{"version":1}`), "isn't gzip"},
		"not JSON":      {gz("nonsense"), "doesn't parse"},
		"newer format":  {gz(Index{Version: 2, Discs: []Disc{good}}), "format version 2"},
		"empty":         {gz(Index{Version: 1}), "no discs"},
		"no TMDb id":    {gz(Index{Version: 1, Discs: []Disc{{Titles: good.Titles}}}), "no TMDb id"},
		"no titles":     {gz(Index{Version: 1, Discs: []Disc{{Movie: good.Movie}}}), "0 titles"},
		"long name":     {gz(Index{Version: 1, Discs: []Disc{{Movie: Movie{TMDbID: 1, Title: strings.Repeat("x", 301)}, Titles: good.Titles}}}), "over 300"},
		"negative size": {gz(Index{Version: 1, Discs: []Disc{{Movie: good.Movie, Titles: []Title{{Size: -1}}}}}), "out of bounds"},
	} {
		if _, err := Read(c.in); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v; want %q", name, err, c.want)
		}
	}
}

// The owner's own discs, scanned on the homelab, match TheDiscDB title for
// title, and TheDiscDB's main movie is the title the owner ripped.
func TestTheOwnersDiscsMatch(t *testing.T) {
	ix := build(t)
	for name, want := range map[string]struct {
		tmdb   int
		format string
		mains  []int
	}{
		"scan-star-trek-ii-uhd.json":  {154, "UHD", []int{14}}, // the Director's Edition, job 5
		"scan-star-trek-iii-uhd.json": {157, "UHD", []int{29}}, // job 7
	} {
		d := scan(t, name)
		m := ix.Match(d, nil)
		if m == nil {
			t.Fatalf("%s: no match", name)
		}
		if m.Disc.Movie.TMDbID != want.tmdb || m.Disc.Format != want.format || len(m.Titles) != len(d.Titles) {
			t.Errorf("%s: matched TMDb %d %s with %d of %d titles", name, m.Disc.Movie.TMDbID, m.Disc.Format, len(m.Titles), len(d.Titles))
		}
		if !slices.Equal(m.Mains(), want.mains) {
			t.Errorf("%s: main movie %v; want %v", name, m.Mains(), want.mains)
		}
		// With its release hidden, as the benchmark does, nothing else fits.
		if m := ix.Match(d, func(k *Disc) bool { return k.Release == "2021-original-4-movie-collection-4k" }); m != nil {
			t.Errorf("%s: matched %s with its release hidden", name, m.Disc.Movie.Title)
		}
	}
}

// A disc matches only when nearly all titles line up both ways, and lengths
// and sizes agree.
func TestMatchNeedsTheWholeShape(t *testing.T) {
	ix := build(t)
	whole := scan(t, "scan-star-trek-iii-uhd.json")

	// A few of its titles alone are not the disc.
	part := *whole
	part.Titles = whole.Titles[:10]
	if m := ix.Match(&part, nil); m != nil {
		t.Errorf("10 of %d titles matched the whole disc", len(whole.Titles))
	}

	// A different length: that title doesn't line up.
	moved := *whole
	moved.Titles = slices.Clone(whole.Titles)
	for i := range moved.Titles {
		if moved.Titles[i].Index == 29 {
			moved.Titles[i].Duration += 3 * time.Second
		}
	}
	m := ix.Match(&moved, nil)
	if m == nil {
		t.Fatal("one title off shouldn't lose the match")
	}
	if _, ok := m.Titles[29]; ok || len(m.Mains()) != 0 {
		t.Error("a title 3 s off was still taken for TheDiscDB's main movie")
	}
	// Within the slack: 2 s and 0.4% still line up.
	near := *whole
	near.Titles = slices.Clone(whole.Titles)
	for i := range near.Titles {
		if near.Titles[i].Index == 29 {
			near.Titles[i].Duration += 2 * time.Second
			near.Titles[i].SizeBytes += near.Titles[i].SizeBytes / 250
		}
	}
	if m := ix.Match(&near, nil); m == nil || !slices.Equal(m.Mains(), []int{29}) {
		t.Error("a title within the slack didn't line up")
	}
}

// Two different movies that fit equally well: no match, never a guess.
func TestMatchFailsClosedOnATie(t *testing.T) {
	ix := build(t)
	var st3 Disc
	for _, d := range ix.Discs {
		if d.Movie.TMDbID == 157 && d.Format == "UHD" {
			st3 = d
		}
	}
	impostor := st3
	impostor.Movie = Movie{TMDbID: 999999, Title: "Impostor"}
	impostor.Release = "elsewhere"
	ix.Discs = append(ix.Discs, impostor)
	ix.prepare()
	if m := ix.Match(scan(t, "scan-star-trek-iii-uhd.json"), nil); m != nil {
		t.Errorf("matched %s though another movie's disc fits as well", m.Disc.Movie.Title)
	}
	// The same disc in another release of the same movie is no tie.
	ix.Discs[len(ix.Discs)-1].Movie = st3.Movie
	ix.prepare()
	if m := ix.Match(scan(t, "scan-star-trek-iii-uhd.json"), nil); m == nil || m.Disc.Movie.TMDbID != 157 {
		t.Error("the same disc in two releases of one movie should still match")
	}
}

func TestSeconds(t *testing.T) {
	for s, want := range map[string]int{"0:00:16": 16, "1:56:31": 6991, "10:00:00": 36000} {
		if got, err := seconds(s); err != nil || got != want {
			t.Errorf("seconds(%q) = %d, %v", s, got, err)
		}
	}
	for _, bad := range []string{"", "1:56", "1:x:00", "-1:00:00"} {
		if _, err := seconds(bad); err == nil {
			t.Errorf("seconds(%q) was accepted", bad)
		}
	}
}

// A record that doesn't parse is skipped and reported; the rest is kept.
func TestBuildSkipsABadRecord(t *testing.T) {
	root := t.TempDir()
	copyTree(t, sampleRoot, root)
	bad := filepath.Join(root, "data", "movie", "Alien (1979)", "2013-blu-ray", "disc01.json")
	os.WriteFile(bad, []byte("{not json"), 0o644)
	os.MkdirAll(filepath.Join(root, "data", "movie", "No Id (2000)", "r"), 0o755)
	os.WriteFile(filepath.Join(root, "data", "movie", "No Id (2000)", "metadata.json"), []byte(`{"Title":"No Id","ExternalIds":{"Tmdb":""}}`), 0o644)
	_, rep, err := Build(root, "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Discs != 8 || rep.Movies != 4 || len(rep.Skipped) != 2 {
		t.Errorf("built %d discs of %d movies, skipped %q", rep.Discs, rep.Movies, rep.Skipped)
	}
	empty := t.TempDir()
	os.MkdirAll(filepath.Join(empty, "data", "movie"), 0o755)
	if _, _, err := Build(empty, "", time.Now()); err == nil {
		t.Error("an index with no discs was built")
	}
}

func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// small builds an index from discs given as titles, prepared for matching.
func small(discs ...Disc) *Index {
	ix := &Index{Version: Version, Discs: discs}
	ix.prepare()
	return ix
}

func title(idx int, file, segs string, secs int, size int64) Title {
	return Title{Index: idx, File: file, Segments: segs, Seconds: secs, Size: size}
}

func scanOf(titles ...Title) *makemkv.Disc {
	d := &makemkv.Disc{}
	for _, t := range titles {
		d.Titles = append(d.Titles, makemkv.Title{Index: t.Index, SourceFile: t.File, SegmentMap: t.Segments,
			Duration: time.Duration(t.Seconds) * time.Second, SizeBytes: t.Size})
	}
	return d
}

// Any second movie that fits is a tie, even one title behind.
func TestMatchFailsClosedOnANearTie(t *testing.T) {
	ix := build(t)
	var st3 Disc
	for _, d := range ix.Discs {
		if d.Movie.TMDbID == 157 && d.Format == "UHD" {
			st3 = d
		}
	}
	impostor := st3
	impostor.Movie, impostor.Release = Movie{TMDbID: 999999, Title: "Impostor"}, "elsewhere"
	impostor.Titles = slices.Clone(st3.Titles[1:]) // one title fewer: still over 90%
	ix.Discs = append(ix.Discs, impostor)
	ix.prepare()
	if m := ix.Match(scan(t, "scan-star-trek-iii-uhd.json"), nil); m != nil {
		t.Errorf("matched %s though another movie's disc fits nearly as well", m.Disc.Movie.Title)
	}
}

// A record made with MakeMKV's default 2-minute minimum still matches a scan
// that lists every short clip too.
func TestMatchIgnoresClipsTheRecordCouldNotList(t *testing.T) {
	ix := build(t)
	for i := range ix.Discs {
		var long []Title
		for _, tt := range ix.Discs[i].Titles {
			if tt.Seconds >= 120 {
				long = append(long, tt)
			}
		}
		ix.Discs[i].Titles = long
	}
	ix.prepare()
	m := ix.Match(scan(t, "scan-star-trek-iii-uhd.json"), nil)
	if m == nil || !slices.Equal(m.Mains(), []int{29}) {
		t.Fatal("a record without the short clips didn't match the full scan")
	}
}

// The known disc's side counts its own titles, each once.
func TestMatchCountsEachKnownTitleOnce(t *testing.T) {
	var known []Title
	for i := 0; i < 20; i++ {
		known = append(known, title(i, fmt.Sprintf("%05d.mpls", i), fmt.Sprint(i), 600+i, int64(1e9)+int64(i)))
	}
	ix := small(Disc{Movie: Movie{TMDbID: 1, Title: "A"}, Titles: known})
	// 20 scan titles, but 18 of them are the same playlist as known title 0.
	var titles []Title
	for i := 0; i < 18; i++ {
		tt := known[0]
		tt.Index = i
		titles = append(titles, tt)
	}
	titles = append(titles, known[1], known[2])
	titles[18].Index, titles[19].Index = 18, 19
	if m := ix.Match(scanOf(titles...), nil); m != nil {
		t.Errorf("matched with only 3 of the known disc's 20 titles found")
	}
	if m := ix.Match(scanOf(known...), nil); m == nil {
		t.Error("the whole disc didn't match")
	}
}

// Titles without a playlist or segment map have no shape: length and size
// alone never make a match.
func TestMatchNeedsAShape(t *testing.T) {
	var shapeless []Title
	for i := 0; i < 5; i++ {
		shapeless = append(shapeless, title(i, "", "", 1800+i, int64(4e9)+int64(i)))
	}
	ix := small(Disc{Movie: Movie{TMDbID: 2, Title: "B"}, Titles: shapeless})
	if m := ix.Match(scanOf(shapeless...), nil); m != nil {
		t.Error("titles without a shape matched by length and size alone")
	}
	// A disc with both: the shapeless titles count on neither side, so its
	// ten shaped titles still match.
	var mixed []Title
	for i := 0; i < 10; i++ {
		mixed = append(mixed, title(i, fmt.Sprintf("%05d.mpls", i), fmt.Sprint(i), 600+i, int64(2e9)+int64(i)))
	}
	for i, tt := range shapeless {
		tt.Index = 10 + i
		mixed = append(mixed, tt)
	}
	ix = small(Disc{Movie: Movie{TMDbID: 3, Title: "C"}, Titles: mixed})
	if m := ix.Match(scanOf(mixed...), nil); m == nil || len(m.Titles) != 10 {
		t.Errorf("a disc with shapeless titles too didn't match on its shaped ones: %+v", m)
	}
}
