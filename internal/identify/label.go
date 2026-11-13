// Package identify turns what a disc says about itself (its volume label and
// name) into something a movie search can use.
package identify

import (
	"regexp"
	"strconv"
	"strings"
)

// Label is a cleaned-up disc label.
type Label struct {
	Term    string // search term, e.g. "Solo A Star Wars Story"; "" if the label says nothing about the movie
	Year    int    // a year in the label, e.g. 2007 from "Spider-Man 3 (2007)"; 0 if none
	Edition string // a Plex edition name, e.g. "Ultimate Cut"; "" if none
}

// editions maps edition words to the edition name used in the library
// ({edition-…}). Specific phrases come first, so "Extended Cut" isn't read as
// just "Extended". The list comes from the pipeline this project grew out of,
// where each one was seen on a real disc.
//
// An edition is only recognised at the end of a label, after the title:
// "Watchmen- Ultimate Cut", "BLADE_RUNNER_FINAL_CUT". Edition words elsewhere
// are part of the title: "UNCUT_GEMS", "THE_FINAL_CUT".
var editions = []struct {
	re   *regexp.Regexp
	name string
}{
	{trailingRe(`ultimate cut`), "Ultimate Cut"},
	{trailingRe(`directors? cut|director's cut`), "Director's Cut"},
	{trailingRe(`extended cut`), "Extended Cut"},
	{trailingRe(`extended edition`), "Extended Edition"},
	{trailingRe(`theatrical cut`), "Theatrical Cut"},
	{trailingRe(`final cut`), "Final Cut"},
	{trailingRe(`special edition`), "Special Edition"},
	{trailingRe(`collectors? edition|collector's edition`), "Collector's Edition"},
	{trailingRe(`anniversary edition`), "Anniversary Edition"},
	{trailingRe(`criterion collection|criterion`), "Criterion Collection"},
	{trailingRe(`unrated`), "Unrated"},
	{trailingRe(`uncut`), "Uncut"},
	{trailingRe(`remastered`), "Remastered"},
	{trailingRe(`redux`), "Redux"},
	{trailingRe(`imax`), "IMAX"},
}

// noise is what labels add about the disc itself, not the movie: format,
// disc number, picture format.
var noise = wordsRe(`uhd|4k|ultra hd|blu ?ray|bluray|bd|bd25|bd50|dvd|disc ?\d+|disk ?\d+|d\d|cd\d|` +
	`widescreen|ws|fullscreen|fs|hdr|dolby vision|dv`)

// filler words are what generic labels such as DVD_VIDEO, BD_ROM, VIDEO_TS
// or LOGICAL_VOLUME_ID are left with once the noise is gone. A label made only
// of them says nothing about the movie, and searching for it would match
// some random film.
var filler = map[string]bool{
	"video": true, "rom": true, "ts": true, "logical": true, "volume": true, "id": true,
	"disc": true, "disk": true, "movie": true, "feature": true, "main": true, "title": true,
	"untitled": true, "no": true, "name": true, "label": true, "cdrom": true, "bdmv": true,
}

// articles don't count as a title on their own: "THE_FINAL_CUT" is a film
// called The Final Cut, not "The" in its Final Cut.
var articles = map[string]bool{"the": true, "a": true, "an": true}

var (
	// Only a year in brackets is a release year: "Spider-Man 3 (2007)". A
	// bare number is part of the title: "BLADE_RUNNER_2049", "1917".
	yearRe = regexp.MustCompile(`[\(\[](19\d\d|20\d\d)[\)\]]`)
	sepRe  = regexp.MustCompile(`\s*[-–:]\s+|\s+[-–:]\s*|_+|\.`) // "Watchmen- Ultimate Cut", "SOLO_A_STAR"
)

// wordsRe matches alternatives as whole words, ignoring case, with spaces in
// them matching any run of spaces.
func wordsRe(alts string) *regexp.Regexp {
	return regexp.MustCompile(`(?i)\b(?:` + strings.ReplaceAll(alts, " ", `\s+`) + `)\b`)
}

// trailingRe matches alternatives as the last words of a string.
func trailingRe(alts string) *regexp.Regexp {
	return regexp.MustCompile(`(?i)(?:^|\s)(?:` + strings.ReplaceAll(alts, " ", `\s+`) + `)$`)
}

// Parse cleans a disc label (the volume name or MakeMKV's disc name).
func Parse(label string) Label {
	var l Label
	s := sepRe.ReplaceAllString(label, " ")
	if m := yearRe.FindStringSubmatch(s); m != nil {
		l.Year, _ = strconv.Atoi(m[1])
		s = yearRe.ReplaceAllString(s, " ")
	}
	s = strings.Join(strings.Fields(noise.ReplaceAllString(s, " ")), " ")

	for _, e := range editions {
		loc := e.re.FindStringIndex(s)
		if loc == nil || !hasTitleWord(s[:loc[0]]) {
			continue
		}
		l.Edition = e.name
		s = strings.TrimSpace(s[:loc[0]])
		break
	}
	words := strings.Fields(s)

	if allFiller(words) {
		words = nil
	}
	// An all-capitals label (THE_MATRIX) gets title case for the search.
	if allCaps(words) {
		for i, w := range words {
			words[i] = strings.ToUpper(w[:1]) + strings.ToLower(w[1:])
		}
	}
	// Labels sometimes put the article last: "MATRIX_THE".
	if n := len(words); n > 1 && strings.EqualFold(words[n-1], "the") {
		words = append([]string{words[n-1]}, words[:n-1]...)
	}
	l.Term = strings.Join(words, " ")
	if l.Term == "" {
		l.Edition = ""
	}
	return l
}

// hasTitleWord reports whether s has a word that can be a title: anything
// but an article.
func hasTitleWord(s string) bool {
	for _, w := range strings.Fields(s) {
		if !articles[strings.ToLower(w)] {
			return true
		}
	}
	return false
}

func allFiller(words []string) bool {
	for _, w := range words {
		if !filler[strings.ToLower(w)] {
			return false
		}
	}
	return true
}

func allCaps(words []string) bool {
	letters := false
	for _, w := range words {
		for _, r := range w {
			if r >= 'a' && r <= 'z' {
				return false
			}
			if r >= 'A' && r <= 'Z' {
				letters = true
			}
		}
	}
	return letters
}
