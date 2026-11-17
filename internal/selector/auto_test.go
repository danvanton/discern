package selector

import (
	"strings"
	"testing"

	"github.com/danvanton/discern/internal/jobs"
)

// The owner's Star Trek III UHD is sure: TheDiscDB knows it, and the
// disc's name found the same movie. Anything less is not (decision D60).
func TestUnsure(t *testing.T) {
	ix := sampleIndex(t)
	d := ownerScan(t, "scan-star-trek-iii-uhd.json")
	spock := &jobs.Movie{TMDbID: 157, Title: "Star Trek III: The Search for Spock", Year: 1984}
	in := Input{Disc: d, Match: ix.Match(d, nil), Indexed: true, Movie: spock}
	if why := Unsure(in, Decide(in)); why != "" {
		t.Errorf("the owner's disc is unsure: %s", why)
	}

	for name, c := range map[string]struct {
		in   Input
		want string
	}{
		"no movie from the name": {Input{Disc: d, Match: in.Match, Indexed: true}, "found no movie to check TheDiscDB's"},
		"another movie":          {Input{Disc: d, Match: in.Match, Indexed: true, Movie: &jobs.Movie{TMDbID: 1, Title: "Elsewhere"}}, "in doubt"},
		"not known":              {Input{Disc: d, Indexed: true, Movie: spock}, "TheDiscDB doesn't know this disc"},
		"no index":               {Input{Disc: d, Movie: spock}, "TheDiscDB's index isn't loaded yet"},
	} {
		if why := Unsure(c.in, Decide(c.in)); !strings.Contains(why, c.want) {
			t.Errorf("%s: %q, want %q", name, why, c.want)
		}
	}

	// A Medium choice from the scan alone is not sure.
	scanOnly := Input{Disc: disc(title(0, 110, 30), title(1, 5, 10)), Indexed: true, Movie: spock}
	if sel := Decide(scanOnly); sel.Confidence != jobs.Medium || !strings.Contains(Unsure(scanOnly, sel), "doesn't know this disc") {
		t.Errorf("a scan-only choice: %s, %q", sel.Confidence, Unsure(scanOnly, sel))
	}

	// An edition in the disc's name that TheDiscDB's title name doesn't carry.
	cut := *d
	cut.Name = "Star Trek III: The Search for Spock - Director's Cut"
	edition := Input{Disc: &cut, Match: in.Match, Indexed: true, Movie: spock}
	if why := Unsure(edition, Decide(edition)); !strings.Contains(why, "the disc says Director's Cut") {
		t.Errorf("an edition TheDiscDB doesn't name: %q", why)
	}
}

func TestNamesEdition(t *testing.T) {
	for _, c := range []struct {
		name, title, edition string
		want                 bool
	}{
		{"Star Trek II: The Wrath of Khan Director's Edition", "Star Trek II: The Wrath of Khan", "Director's Cut", true},
		{"Aliens – Special Edition", "Aliens", "Special Edition", true},
		{"Blade Runner: The Final Cut", "Blade Runner", "Final Cut", true},
		{"The Lord of the Rings EXTENDED", "The Lord of the Rings", "Extended Edition", true},
		{"Apocalypse Now", "Apocalypse Now", "Redux", false},
		{"", "Alien", "Director's Cut", false},
		{"Seven Samurai", "Seven Samurai", "Criterion Collection", false},
		// The edition word in the movie's own title doesn't count.
		{"Special Forces", "Special Forces", "Special Edition", false},
		{"Special Forces Special Edition", "Special Forces", "Special Edition", true},
	} {
		if got := namesEdition(c.name, c.title, c.edition); got != c.want {
			t.Errorf("namesEdition(%q, %q, %q) = %v", c.name, c.title, c.edition, got)
		}
	}
}
