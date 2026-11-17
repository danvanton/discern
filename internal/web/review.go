package web

import (
	"cmp"
	"context"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/danvanton/discern/internal/jobs"
	"github.com/danvanton/discern/internal/lang"
	"github.com/danvanton/discern/internal/makemkv"
	"github.com/danvanton/discern/internal/settings"
)

// The review page, where the user approves a disc (decisions D32-D34): the
// suggested title first, the others grouped by length, the movie found by
// name if it is wrong, and this disc's languages.

// review is what the review page shows beyond the job itself.
type review struct {
	Groups titleGroups
	Langs  langChoice
	Search *search   // results of a movie search, if one was made
	Pick   *pickView // a movie chosen on this page, not yet saved
	Movie  int       // the TMDb id the approve form sends

	// Decision is the selector's; nil for a job selected before it was
	// saved.
	Decision *decisionView
	// DiscDBSays is the movie TheDiscDB names for the disc when it isn't
	// the job's movie (decision D47), offered as one tap.
	DiscDBSays *jobs.Movie
	// DiscDBAgrees is true when TheDiscDB names the job's movie.
	DiscDBAgrees bool
}

// titleRow is one title in the review page's list.
type titleRow struct {
	makemkv.Title
	Chosen bool
	Label  *jobs.TitleLabel // what TheDiscDB says it is, if it knows the disc
}

// decisionView is the selector's decision as the page shows it
// (decision D43): how sure it is, what would stop it acting alone, and
// what the evidence was.
type decisionView struct {
	Badge   string // e.g. "Check"
	Class   string // the badge's colour
	Summary string // the decisive reason, when nothing holds it
	Holds   []string
	Reasons []string
	// Auto is what auto-import made of the disc, when it is on or on
	// trial (decisions D60-D62).
	Auto string
}

func viewDecision(sel *jobs.Selection) *decisionView {
	if sel == nil {
		return nil
	}
	v := &decisionView{Holds: sel.Holds, Reasons: sel.Reasons}
	switch sel.Confidence {
	case jobs.High:
		v.Badge, v.Class = "Known disc", "ok"
	case jobs.Medium:
		v.Badge, v.Class = "Likely", "busy"
	default:
		v.Badge, v.Class = "Check", "ask"
	}
	if len(sel.Holds) == 0 {
		v.Summary = sel.Reason
	}
	switch {
	case sel.Trial:
		v.Auto = "Trial: Discern would have ripped the ticked title on its own."
	case sel.Wait != "":
		v.Auto = "Not ripped on its own: " + sel.Wait + "."
	}
	return v
}

// titleGroups is the disc's titles grouped against the chosen ones (D32).
type titleGroups struct {
	Main   []titleRow // the chosen titles: the suggestion, until the user changes it
	Cuts   []titleRow // at least cutShare of the main length: maybe another cut
	Extras []titleRow // from clipLength up to that
	Clips  []titleRow // shorter than clipLength
}

const (
	cutShare   = 0.75
	clipLength = 2 * time.Minute
)

// groupTitles sorts the disc's titles into the review page's groups, each
// longest first. The chosen titles come first, with the cuts the user must
// choose between (decision D49), unticked. The main length is the longest
// of those, or the longest title's if there are none.
func groupTitles(j *jobs.Job) titleGroups {
	var g titleGroups
	if j.Disc == nil {
		return g
	}
	var choices []int
	var labels map[int]jobs.TitleLabel
	if j.Selection != nil {
		choices, labels = j.Selection.Choices, j.Selection.Labels
	}
	isMain := func(idx int) bool { return slices.Contains(j.Titles, idx) || slices.Contains(choices, idx) }
	var main time.Duration
	for _, t := range j.Disc.Titles {
		if isMain(t.Index) {
			main = max(main, t.Duration)
		}
	}
	if main == 0 {
		for _, t := range j.Disc.Titles {
			main = max(main, t.Duration)
		}
	}
	for _, t := range j.Disc.Titles {
		row := titleRow{Title: t, Chosen: slices.Contains(j.Titles, t.Index)}
		if l, ok := labels[t.Index]; ok {
			row.Label = &l
		}
		switch {
		case isMain(t.Index):
			g.Main = append(g.Main, row)
		case float64(t.Duration) >= cutShare*float64(main):
			g.Cuts = append(g.Cuts, row)
		case t.Duration >= clipLength:
			g.Extras = append(g.Extras, row)
		default:
			g.Clips = append(g.Clips, row)
		}
	}
	for _, rows := range [][]titleRow{g.Main, g.Cuts, g.Extras, g.Clips} {
		slices.SortStableFunc(rows, func(a, b titleRow) int { return cmp.Compare(b.Duration, a.Duration) })
	}
	return g
}

// langChoice is the review page's language panel (D34): the Settings page's
// choice, summed up, and the disc's own languages to pick from.
type langChoice struct {
	Default    *settings.Languages // the Settings page's; nil is MakeMKV's default
	Unreadable bool                // the Settings page's choice can't be read
	On         bool                // the disc has (or the form sent) its own choice
	Audio      []langBox
	Subtitles  []langBox
	ForcedOnly bool
	Commentary bool
}

type langBox struct {
	lang.Language
	On bool // ticked to start with
}

// langChoiceFor offers the disc's languages that Discern can name and
// store: every title's, since the choice applies to whichever titles are
// ticked, with the chosen titles' languages first. They start ticked as the
// job's own choice has them, if it has one (a Retry, or a refused approval
// shown again), or else as the Settings page has them: audio in its order
// first, and with MakeMKV's default on, all the disc's audio.
func langChoiceFor(j *jobs.Job, def *settings.Languages) langChoice {
	c := langChoice{Default: def, On: j.Languages != nil}
	want := def
	if c.On {
		want = j.Languages
	}
	var audio, subs []string
	if j.Disc != nil {
		titles := slices.Clone(j.Disc.Titles)
		slices.SortStableFunc(titles, func(a, b makemkv.Title) int {
			return cmp.Compare(notChosen(j, a), notChosen(j, b))
		})
		for _, t := range titles {
			for _, s := range t.Streams {
				code := lang.Canonical(s.Lang)
				if !lang.Offered(code) {
					continue
				}
				switch s.Type {
				case "Audio":
					audio = appendNew(audio, code)
				case "Subtitles":
					subs = appendNew(subs, code)
				}
			}
		}
	}
	var wantAudio, wantSubs []string
	if want != nil {
		wantAudio, wantSubs = want.Audio, want.Subtitles
		c.ForcedOnly, c.Commentary = want.ForcedOnly, want.Commentary
	}
	slices.SortStableFunc(audio, func(a, b string) int { return cmp.Compare(rank(wantAudio, a), rank(wantAudio, b)) })
	for _, code := range audio {
		c.Audio = append(c.Audio, langBox{lang.Language{Code: code, Name: lang.Name(code)}, want == nil || slices.Contains(wantAudio, code)})
	}
	for _, code := range subs {
		c.Subtitles = append(c.Subtitles, langBox{lang.Language{Code: code, Name: lang.Name(code)}, slices.Contains(wantSubs, code)})
	}
	return c
}

// notChosen sorts the chosen titles before the others: 0 for chosen, 1 not.
func notChosen(j *jobs.Job, t makemkv.Title) int {
	if slices.Contains(j.Titles, t.Index) {
		return 0
	}
	return 1
}

// Summary says in one line what the Settings page keeps.
func (c langChoice) Summary() string {
	if c.Unreadable {
		return "The Settings page's languages can't be read: open Settings to fix them before ripping"
	}
	l := c.Default
	if l == nil {
		return "All tracks: MakeMKV's default selection"
	}
	s := "Audio " + names(l.Audio) + " · "
	if len(l.Subtitles) == 0 {
		s += "no subtitles"
	} else {
		s += "Subtitles " + names(l.Subtitles)
		if l.ForcedOnly {
			s += ", forced only"
		}
	}
	if l.Commentary {
		s += " · commentary kept"
	}
	return s
}

func names(codes []string) string {
	var out []string
	for _, c := range codes {
		if n := lang.Name(c); n != "" {
			out = append(out, n)
		} else {
			out = append(out, c)
		}
	}
	return strings.Join(out, ", ")
}

// rank is code's place in want, with codes not in it after all those that are.
func rank(want []string, code string) int {
	if i := slices.Index(want, code); i >= 0 {
		return i
	}
	return len(want)
}

func appendNew(list []string, s string) []string {
	if slices.Contains(list, s) {
		return list
	}
	return append(list, s)
}

// search is a movie search on the review page (D33).
type search struct {
	Query   string
	Results []pickView
	Err     string
}

// pickView is a movie the user can choose, with whether its poster is cached.
type pickView struct {
	jobs.Movie
	Poster bool
}

// discLanguages reads the review form's language panel. It returns nil
// unless "Choose languages for this disc" is ticked: the Settings page's
// choice then applies. Codes are taken as the page sent them; the app
// checks them before the rip.
func discLanguages(form url.Values) *lang.Choice {
	if form.Get("disc_langs") != "on" {
		return nil
	}
	return &lang.Choice{
		Audio:      form["disc_audio"],
		Subtitles:  form["disc_subtitles"],
		ForcedOnly: form.Get("disc_forced") == "on",
		Commentary: form.Get("disc_commentary") == "on",
	}
}

// postedTitles reads the titles ticked in the review form, skipping any
// that isn't a number (the approve handler refuses those).
func postedTitles(form url.Values) []int {
	var out []int
	for _, v := range form["title"] {
		if n, err := strconv.Atoi(v); err == nil {
			out = append(out, n)
		}
	}
	return out
}

// reviewFor builds the review page's data. pick is a TMDb id chosen on the
// page (from a search result or typed), 0 if none; q is a search, "" if
// none. A failed lookup is reported on the page, never as a page error.
func (s *server) reviewFor(ctx context.Context, j *jobs.Job, pick int, q string) (review, string) {
	st, err := s.b.Settings(ctx)
	rv := review{Groups: groupTitles(j), Decision: viewDecision(j.Selection), Langs: langChoiceFor(j, st.Languages)}
	rv.Langs.Unreadable = err != nil
	if j.Selection != nil && j.Selection.Movie != nil {
		if j.Movie != nil && j.Movie.TMDbID == j.Selection.Movie.TMDbID {
			rv.DiscDBAgrees = true
		} else {
			rv.DiscDBSays = j.Selection.Movie
		}
	}
	if j.Movie != nil {
		rv.Movie = j.Movie.TMDbID
	}
	var problem string
	if pick > 0 && (j.Movie == nil || pick != j.Movie.TMDbID) {
		m, err := s.b.LookupMovie(ctx, pick)
		if err != nil {
			problem = "TMDb " + strconv.Itoa(pick) + " couldn't be looked up: " + err.Error()
		} else {
			_, _, has := s.b.Poster(m.TMDbID)
			rv.Pick, rv.Movie = &pickView{Movie: *m, Poster: has}, m.TMDbID
		}
	}
	if q = strings.TrimSpace(q); q != "" {
		rv.Search = &search{Query: q}
		found, err := s.b.SearchMovies(ctx, q)
		if err != nil {
			rv.Search.Err = err.Error()
		}
		for _, m := range found {
			_, _, has := s.b.Poster(m.TMDbID)
			rv.Search.Results = append(rv.Search.Results, pickView{Movie: m, Poster: has})
		}
	}
	return rv, problem
}
