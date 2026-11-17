package selector

import (
	"fmt"
	"strings"

	"github.com/danvanton/discern/internal/identify"
	"github.com/danvanton/discern/internal/jobs"
)

// Unsure says why sel, decided from in, may not be ripped without asking
// (decision D60), or "" when it may. Auto-import acts only when two
// independent sources agree:
//
//   - TheDiscDB knows the disc and names one main title, with no holds
//     (High);
//   - the movie found from the disc's own name (in.Movie, from Identify)
//     is the one TheDiscDB names;
//   - an edition the disc's name or label gives ("Director's Cut") is in
//     TheDiscDB's name for that title.
//
// Whether Radarr already has the movie (decision D61) is the caller's to
// check: it needs Radarr. Like Decide, Unsure is a pure function.
func Unsure(in Input, sel jobs.Selection) string {
	switch {
	case !in.Indexed:
		return "TheDiscDB's index isn't loaded yet, and auto-import acts only on discs it knows"
	case len(sel.Holds) > 0:
		// The movie found from the disc's name disagreeing with TheDiscDB's
		// is one of these holds (decision D47).
		return "something is in doubt"
	case sel.Confidence != jobs.High:
		return "TheDiscDB doesn't know this disc, and auto-import acts only on discs it knows"
	case len(sel.Titles) != 1 || sel.Movie == nil:
		return "TheDiscDB doesn't name one title as the movie"
	case in.Movie == nil:
		return fmt.Sprintf("the disc's name found no movie to check TheDiscDB's (%s) against", sel.Movie.Name())
	}
	if in.Disc != nil {
		name := sel.Labels[sel.Titles[0]].Name
		for _, label := range []string{in.Disc.Name, in.Disc.Volume} {
			if e := identify.Parse(label).Edition; e != "" && !namesEdition(name, sel.Movie.Title, e) {
				return fmt.Sprintf("the disc says %s, and TheDiscDB's name for the title (%q) doesn't", e, name)
			}
		}
	}
	return ""
}

// namesEdition reports whether TheDiscDB's title name carries the
// edition's distinctive word: "director" for "Director's Cut", "extended"
// for "Extended Edition". The movie's own title is taken out of the name
// first, so "Special Forces" doesn't name a Special Edition. Case and
// apostrophes don't matter.
func namesEdition(name, title, edition string) bool {
	word, _, _ := strings.Cut(edition, " ")
	rest := simple(name)
	if t := simple(title); t != "" {
		rest = strings.ReplaceAll(rest, t, "")
	}
	return strings.Contains(rest, strings.TrimSuffix(simple(word), "s"))
}

// simple lowercases s and drops apostrophes: "Director's" becomes
// "directors".
func simple(s string) string {
	return strings.NewReplacer("'", "", "’", "").Replace(strings.ToLower(s))
}
