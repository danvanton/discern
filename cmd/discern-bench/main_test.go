package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danvanton/discern/internal/discdb"
)

// The samples in testdata/discdb: Alien (two cuts), Despicable Me (fake
// playlists), Kill Bill, Star Trek II and III.
func sample(t *testing.T) *discdb.Index {
	t.Helper()
	ix, _, err := discdb.Build(filepath.Join("..", "..", "testdata", "discdb"), "test", time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	return ix
}

func TestBenchScoresTheSamples(t *testing.T) {
	r := Bench(sample(t))
	// Known: every one-main disc is found, and right.
	if k := r.Known.OneMain; k.Wrong != 0 || k.Right == 0 || k.Right+k.Abstained != r.Longest.Right+r.Longest.Wrong {
		t.Errorf("known run %+v, against %d discs", k, r.Longest.Right+r.Longest.Wrong)
	}
	// Unseen: each sample's release is hidden, so the scan decides, and the
	// fake-playlist disc is held, never wrong.
	if u := r.Unseen.OneMain; u.Wrong != 0 || u.Abstained == 0 {
		t.Errorf("unseen run %+v", u)
	}
	// Alien's two cuts: asked when TheDiscDB knows the disc.
	if r.Known.TwoMain.Held != 1 || r.Known.TwoMain.Wrong != 0 {
		t.Errorf("two cuts %+v", r.Known.TwoMain)
	}
	// Despicable Me is the sample on which the longest title is wrong.
	if r.Longest.Wrong == 0 {
		t.Errorf("baseline %+v: the fake-playlist disc should beat it", r.Longest)
	}
	var out bytes.Buffer
	r.Print(&out)
	if !strings.Contains(out.String(), "| Unseen by TheDiscDB (the gate) |") {
		t.Errorf("table:\n%s", out.String())
	}
}

// Leave-one-out hides the disc's own release, and the same physical disc
// filed under another movie (a double feature).
func TestSameDisc(t *testing.T) {
	d := &discdb.Disc{Movie: discdb.Movie{TMDbID: 1}, Release: "r", ContentHash: "AB"}
	for _, c := range []struct {
		k    discdb.Disc
		want bool
	}{
		{discdb.Disc{Movie: discdb.Movie{TMDbID: 1}, Release: "r"}, true},                     // its release
		{discdb.Disc{Movie: discdb.Movie{TMDbID: 2}, Release: "r"}, false},                    // another movie's
		{discdb.Disc{Movie: discdb.Movie{TMDbID: 2}, Release: "x", ContentHash: "AB"}, true},  // the same disc elsewhere
		{discdb.Disc{Movie: discdb.Movie{TMDbID: 1}, Release: "y", ContentHash: "CD"}, false}, // another release
	} {
		if got := sameDisc(d, &c.k); got != c.want {
			t.Errorf("sameDisc(%+v) = %v", c.k, got)
		}
	}
	// No hash: only the release.
	if sameDisc(&discdb.Disc{Release: "r"}, &discdb.Disc{Release: "x"}) {
		t.Error("two discs without a hash counted as one")
	}
}

func TestGate(t *testing.T) {
	built := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)
	res := func(right, wrong int) Result {
		return Result{Index: "abc", Built: built, Unseen: Run{OneMain: Score{Right: right, Abstained: 10, Wrong: wrong}}}
	}
	if err := Gate(res(1000, 2), res(1000, 2)); err != nil {
		t.Errorf("the same scores failed: %v", err)
	}
	if err := Gate(res(1000, 3), res(1001, 2)); err != nil {
		t.Errorf("fewer wrong failed: %v", err)
	}
	if err := Gate(res(1000, 2), res(1000, 3)); err == nil || !strings.Contains(err.Error(), "went up from 2 to 3") {
		t.Errorf("more wrong: %v", err)
	}
	if err := Gate(res(100, 1), res(100, 1)); err == nil || !strings.Contains(err.Error(), "over the target") {
		t.Errorf("1%% wrong: %v", err)
	}
	if Target(res(100, 1)) == nil || Target(res(1000, 2)) != nil {
		t.Error("Target: 1% wrong passed, or 0.2% failed")
	}
	other := res(1000, 2)
	other.Index = "def"
	if err := Gate(other, res(1000, 2)); err == nil {
		t.Error("scores from two indexes were compared")
	}
}

// run writes the JSON that -compare reads back.
func TestRunWritesAndCompares(t *testing.T) {
	dir := t.TempDir()
	ixPath := filepath.Join(dir, "ix.json.gz")
	f, err := os.Create(ixPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := sample(t).Write(f); err != nil {
		t.Fatal(err)
	}
	f.Close()
	scores := filepath.Join(dir, "scores.json")
	var out bytes.Buffer
	if err := run(ixPath, scores, "", false, true, &out); err != nil {
		t.Fatal(err)
	}
	if err := run(ixPath, "", scores, true, false, &out); err != nil {
		t.Errorf("compared with itself: %v", err)
	}
	if err := run("", "", "", false, false, &out); err == nil {
		t.Error("ran without an index")
	}
}

// A disc whose answer looks wrong (decision D53) isn't scored, but listed.
func TestADoubtfulAnswerIsNotScored(t *testing.T) {
	ix := sample(t)
	ix.Discs = append(ix.Discs, discdb.Disc{Movie: discdb.Movie{TMDbID: 724495, Title: "The Woman King", Year: 2022}, Release: "2022-4k",
		Titles: []discdb.Title{
			{Index: 0, File: "00001.mpls", Segments: "1", Seconds: 8092, Size: 72e9},
			{Index: 2, File: "00725.m2ts", Segments: "725", Seconds: 588, Size: 1.3e9, Type: discdb.MainMovie},
		}})
	before := Bench(sample(t))
	r := Bench(ix)
	if len(r.Doubtful) != 1 || !strings.Contains(r.Doubtful[0], "The Woman King") || r.Unseen.OneMain != before.Unseen.OneMain {
		t.Errorf("doubtful %q, unseen %+v (was %+v)", r.Doubtful, r.Unseen.OneMain, before.Unseen.OneMain)
	}
}
