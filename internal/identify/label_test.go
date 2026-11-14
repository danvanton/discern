package identify

import (
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	cases := []struct {
		in   string
		want Label
	}{
		// Labels and disc names seen on real discs (see docs/DESIGN.md).
		{"SOLO_A_STAR_WARS_STORY", Label{Term: "Solo A Star Wars Story"}},
		{"Solo- A Star Wars Story", Label{Term: "Solo A Star Wars Story"}},
		{"THE_WHOLE_BLOODY_AFFAIR_UHD_D1", Label{Term: "The Whole Bloody Affair"}},
		{"Watchmen- Ultimate Cut", Label{Term: "Watchmen", Edition: "Ultimate Cut"}},
		{"Watchmen- Ultimate Cut (2009)", Label{Term: "Watchmen", Year: 2009, Edition: "Ultimate Cut"}},
		{"BLADE_RUNNER_FINAL_CUT", Label{Term: "Blade Runner", Edition: "Final Cut"}},
		{"Spider-Man 3 (2007)", Label{Term: "Spider-Man 3", Year: 2007}},
		{"Mad Max- Fury Road", Label{Term: "Mad Max Fury Road"}},
		{"MATRIX_THE", Label{Term: "The Matrix"}},
		{"Wicked (Part 1)", Label{Term: "Wicked (Part 1)"}},
		{"Kill Bill- The Whole Bloody Affair Disc 1", Label{Term: "Kill Bill The Whole Bloody Affair"}},
		{"ALIENS_SPECIAL_EDITION_BD", Label{Term: "Aliens", Edition: "Special Edition"}},
		{"Apocalypse Now Redux", Label{Term: "Apocalypse Now", Edition: "Redux"}},
		{"The Shining", Label{Term: "The Shining"}},
		{"1917", Label{Term: "1917"}},                                 // a bare number is the title
		{"BLADE_RUNNER_2049", Label{Term: "Blade Runner 2049"}},       // not a year
		{"2001_A_SPACE_ODYSSEY", Label{Term: "2001 A Space Odyssey"}}, // not a year either

		// Edition words that are part of the title stay in it.
		{"UNCUT_GEMS", Label{Term: "Uncut Gems"}},
		{"THE_FINAL_CUT", Label{Term: "The Final Cut"}},
		{"REDUX", Label{Term: "Redux"}},
		{"Uncut Gems (2019)", Label{Term: "Uncut Gems", Year: 2019}},

		// Generic labels say nothing about the movie.
		{"DVD_VIDEO", Label{}},
		{"BD_ROM", Label{}},
		{"VIDEO_TS", Label{}},
		{"LOGICAL_VOLUME_ID", Label{}},
		{"BLURAY", Label{}},
		{"DISC_1", Label{}},
		{"", Label{}},
	}
	for _, c := range cases {
		if got := Parse(c.in); got != c.want {
			t.Errorf("Parse(%q)\n got  %+v\n want %+v", c.in, got, c.want)
		}
	}
}

func TestParseSaysNothingForBonusNamesAndOverlongLabels(t *testing.T) {
	for _, s := range []string{"Special Features", "BONUS_DISC", "Extras", strings.Repeat("Word ", 30)} {
		if l := Parse(s); l.Term != "" {
			t.Errorf("Parse(%.30q) = %q; want no search term", s, l.Term)
		}
	}
	if l := Parse("Special Correspondents"); l.Term == "" {
		t.Error("a title with one bonus-like word lost its term")
	}
}

func TestTitleFits(t *testing.T) {
	for _, c := range []struct {
		term, title string
		want        bool
	}{
		{"Star Trek II The Wrath of Khan", "Star Trek II: The Wrath of Khan", true},
		{"Star Trek I The Motion Picture", "Star Trek: The Motion Picture", true},
		{"The Whole Bloody Affair", "Kill Bill: The Whole Bloody Affair", true},
		{"Tarantino Collection", "Parasite", false},
		{"The Matrix", "The Movie", false},  // only common words in common
		{"Star Trek II", "Rocky II", false}, // "ii" is too short to count
	} {
		if got := TitleFits(c.term, c.title); got != c.want {
			t.Errorf("TitleFits(%q, %q) = %v; want %v", c.term, c.title, got, c.want)
		}
	}
}
