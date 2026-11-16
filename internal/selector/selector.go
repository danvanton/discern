// Package selector decides which of a disc's titles is the movie (DESIGN
// section 7, decisions D43-D49). It weighs the evidence, strongest first:
//
//  1. TheDiscDB knows the disc by its shape and names one main title.
//  2. The scan alone: after the decoy checks, only one title is
//     feature-length.
//
// Anything else is a hold: the selector still pre-ticks its best guess, but
// says why it wouldn't act alone. MakeMKV's own main-feature mark is
// reported and can raise a hold when it disagrees, but never decides on its
// own: TheDiscDB's records don't keep it, so the benchmark can't measure it.
//
// Decide is a pure function of its input, so the app and the benchmark
// (cmd/discern-bench) run exactly the same code.
package selector

import (
	"cmp"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/danvanton/discern/internal/discdb"
	"github.com/danvanton/discern/internal/format"
	"github.com/danvanton/discern/internal/jobs"
	"github.com/danvanton/discern/internal/makemkv"
)

// The decoy checks' thresholds (decision D45), measured on TheDiscDB's
// movie discs (docs/DESIGN.md, section 8).
const (
	// FeatureLength is the shortest title counted as a film.
	FeatureLength = 45 * time.Minute
	// Two titles are the same video through two playlists when their
	// lengths and sizes are this close.
	dupLength = 500 * time.Millisecond
	dupSize   = 0.001
	// A feature-length title carrying less data than this, or less than
	// lowRateShare of the disc's richest feature-length title, is an extra
	// or a loop of small clips. The leanest real main movie in TheDiscDB
	// is 3.6 Mbit/s; long extras run at 5-17 against the film's 20-100.
	minRate      = 3e6 // bits per second
	lowRateShare = 0.5
	// Titles within this share of each other's length, three or more, mean
	// a disc with decoy playlists.
	lookalikeShare = 0.02
	lookalikeCount = 3
	// A shorter title of richerLength or more carrying over richerShare of
	// the pick's bitrate may be the film itself, shorter than FeatureLength
	// (decision D52): a 25-minute TV special beside a 47-minute
	// documentary at two thirds of its bitrate.
	richerLength = 20 * time.Minute
	richerShare  = 1.25
	// At most this many titles are named in one reason, so a disc with 400
	// decoy playlists still gives a short page.
	maxNamed = 5
)

// Input is what the selector knows about a disc.
type Input struct {
	Disc *makemkv.Disc
	// Match is the disc recognised in TheDiscDB; nil if it isn't, or if
	// there is no index.
	Match *discdb.Match
	// Tied are the movies whose discs all fit the scan when Match is nil
	// because more than one movie fits (discdb.Index.Lookup fails closed).
	Tied []discdb.Movie
	// Indexed is true when TheDiscDB's index was searched, so a nil Match
	// means TheDiscDB doesn't know the disc.
	Indexed bool
	// Movie is the movie Identify found from the disc's name, nil if none.
	Movie *jobs.Movie
}

// Decide picks the titles to pre-tick, and says why, and what would stop it
// acting alone.
func Decide(in Input) jobs.Selection {
	x := &decision{in: in, byIndex: map[int]makemkv.Title{}}
	if in.Disc == nil || len(in.Disc.Titles) == 0 {
		x.hold("the scan found no titles")
		return x.done(jobs.Low)
	}
	for _, t := range in.Disc.Titles {
		x.byIndex[t.Index] = t
	}
	if in.Match != nil {
		if sel, ok := x.fromDiscDB(); ok {
			return sel
		}
	} else if len(in.Tied) > 0 {
		var named []string
		for _, mv := range in.Tied {
			named = append(named, jobs.Movie{Title: mv.Title, Year: mv.Year}.Name())
		}
		if len(named) > maxNamed {
			named = append(named[:maxNamed], fmt.Sprintf("%d more", len(in.Tied)-maxNamed))
		}
		x.hold("TheDiscDB has discs of %d movies that fit this one (%s), so it can't say which", len(in.Tied), strings.Join(named, ", "))
	} else if in.Indexed {
		x.reason("TheDiscDB doesn't know this disc")
	}
	return x.fromScan()
}

type decision struct {
	in       Input
	byIndex  map[int]makemkv.Title
	sel      jobs.Selection
	key      string       // the decisive reason, for the one-sentence summary
	excluded map[int]bool // titles TheDiscDB calls something other than the movie
}

func (x *decision) reason(f string, a ...any) {
	x.sel.Reasons = append(x.sel.Reasons, fmt.Sprintf(f, a...))
}
func (x *decision) hold(f string, a ...any) { x.sel.Holds = append(x.sel.Holds, fmt.Sprintf(f, a...)) }

// decisive is a reason that is also the decision's summary.
func (x *decision) decisive(f string, a ...any) {
	x.reason(f, a...)
	x.key = x.sel.Reasons[len(x.sel.Reasons)-1]
}

// done fixes the confidence: any hold makes it Low.
func (x *decision) done(c jobs.Confidence) jobs.Selection {
	if len(x.sel.Holds) > 0 {
		c = jobs.Low
	}
	x.sel.Confidence = c
	switch {
	case len(x.sel.Holds) > 0:
		x.sel.Reason = x.sel.Holds[0]
	default:
		x.sel.Reason = x.key
	}
	if x.sel.Titles == nil {
		x.sel.Titles = []int{}
	}
	return x.sel
}

// fromDiscDB decides from a TheDiscDB match. It returns false when the
// match names no main title (decision D48), or when its record looks wrong
// (decision D53), and the scan has to decide.
func (x *decision) fromDiscDB() (jobs.Selection, bool) {
	m := x.in.Match
	mv := m.Disc.Movie
	x.sel.Movie = &jobs.Movie{TMDbID: mv.TMDbID, Title: mv.Title, Year: mv.Year}
	for idx, t := range m.Titles {
		if t.Type != "" || t.Name != "" {
			if x.sel.Labels == nil {
				x.sel.Labels = map[int]jobs.TitleLabel{}
			}
			x.sel.Labels[idx] = jobs.TitleLabel{Type: t.Type, Name: t.Name}
		}
	}
	x.reason("TheDiscDB knows this disc: %s, %s (%d of its titles line up)", x.sel.Movie.Name(), release(m.Disc), len(m.Titles))
	if id := x.in.Movie; id != nil && id.TMDbID != mv.TMDbID {
		x.hold("TheDiscDB says this disc is %s, but its name found %s", x.sel.Movie.Name(), id.Name())
	}
	mains := m.Mains()
	if len(mains) == 1 {
		if long, ok := x.doubtful(mains[0]); ok {
			// The record's classes can't be trusted either: the scan decides.
			x.hold("TheDiscDB's record of this disc looks wrong: it names %s as the movie, but leaves %s unclassified",
				x.name(mains[0]), x.name(long))
			return jobs.Selection{}, false
		}
	}
	switch len(mains) {
	case 0:
		x.reason("TheDiscDB doesn't say which title is the movie")
		x.excluded = map[int]bool{}
		for idx, t := range m.Titles {
			if t.Type != "" {
				x.excluded[idx] = true
			}
		}
		return jobs.Selection{}, false
	case 1:
	default:
		x.sel.Choices = mains
		var named []string
		for _, idx := range mains {
			named = append(named, x.name(idx))
		}
		x.hold("TheDiscDB lists %d main titles: %s. Choose the one to keep", len(mains), strings.Join(named, " and "))
		return x.done(jobs.Low), true
	}
	main := x.byIndex[mains[0]]
	x.sel.Titles = []int{main.Index}
	x.decisive("TheDiscDB names %s as the movie", x.name(main.Index))
	// A feature-length title TheDiscDB doesn't list could be another cut.
	// A long one stops the match itself (decision D46); this catches a
	// shorter one beside much more running time that does line up.
	var unknown []int
	for _, t := range x.in.Disc.Titles {
		if _, ok := m.Titles[t.Index]; !ok && t.Duration >= FeatureLength && !SameVideo(t, main) {
			unknown = append(unknown, t.Index)
		}
	}
	if len(unknown) > 0 {
		x.hold("%s %s feature-length but not in TheDiscDB's record of this disc", x.names(unknown), plural(len(unknown), "is", "are"))
	}
	x.checkMark(main)
	return x.done(jobs.High), true
}

// doubtful reports whether TheDiscDB's one main title, main (a scan title
// number), looks mislabelled (DoubtfulMain), and returns the scan's number
// of the title that makes it so.
func (x *decision) doubtful(main int) (int, bool) {
	m := x.in.Match
	var record []discdb.Title
	scanIndex := map[int]int{} // the record's title number -> the scan's
	for idx, t := range m.Titles {
		record = append(record, t)
		scanIndex[t.Index] = idx
	}
	long, ok := DoubtfulMain(m.Titles[main], record)
	return scanIndex[long.Index], ok
}

// DoubtfulMain reports whether a record's main title looks mislabelled
// (decision D53): it is under FeatureLength, while a title the record
// leaves unclassified is feature-length and more than twice as long. It
// returns that title, the longest if there are several. TheDiscDB's 4K
// record of The Woman King names a 9:48 extra as the movie and leaves the
// 2:14:52 film unclassified; it is the only record in the catalogue this
// flags. A short film beside a long extra the record calls an extra (a TV
// special's disc) is not flagged.
func DoubtfulMain(main discdb.Title, record []discdb.Title) (discdb.Title, bool) {
	feature := int(FeatureLength / time.Second)
	if main.Seconds >= feature {
		return discdb.Title{}, false
	}
	var long discdb.Title
	found := false
	for _, t := range record {
		if t.Type != "" || t.Seconds < feature || t.Seconds <= 2*main.Seconds {
			continue
		}
		if !found || t.Seconds > long.Seconds || t.Seconds == long.Seconds && t.Index < long.Index {
			long, found = t, true
		}
	}
	return long, found
}

// fromScan decides from the scan alone: the only feature-length title left
// after the decoy checks, or a hold.
func (x *decision) fromScan() jobs.Selection {
	feats := x.features()
	switch len(feats) {
	case 0:
		// The longest title TheDiscDB doesn't rule out, if there is one.
		candidates := slices.DeleteFunc(slices.Clone(x.in.Disc.Titles), func(t makemkv.Title) bool { return x.excluded[t.Index] })
		if len(candidates) == 0 {
			candidates = x.in.Disc.Titles
		}
		best := longest(candidates)
		x.sel.Titles = []int{best.Index}
		if best.Duration >= FeatureLength {
			x.hold("no feature-length title passed the checks: %s is only the longest", x.name(best.Index))
		} else {
			x.hold("no title is %d minutes or more: %s is only the longest", int(FeatureLength.Minutes()), x.name(best.Index))
		}
		return x.done(jobs.Low)
	case 1:
		t := feats[0]
		x.sel.Titles = []int{t.Index}
		x.decisive("%s is the only feature-length title", x.name(t.Index))
		var richer []int
		for _, o := range x.in.Disc.Titles {
			if o.Duration >= richerLength && o.Duration < t.Duration && !x.excluded[o.Index] &&
				!SameVideo(o, t) && rate(o) > richerShare*rate(t) {
				richer = append(richer, o.Index)
			}
		}
		if len(richer) > 0 {
			x.hold("%s %s shorter than %s but %s over %g times its data per second: the film may be under %d minutes",
				x.names(richer), plural(len(richer), "is", "are"), x.name(t.Index), plural(len(richer), "carries", "carry"), richerShare, int(FeatureLength.Minutes()))
		}
		x.checkMark(t)
		return x.done(jobs.Medium)
	}
	guess := longest(feats)
	var marked []makemkv.Title
	for _, t := range feats {
		if t.MakeMKVMainFeature {
			marked = append(marked, t)
		}
	}
	if len(marked) == 1 {
		guess = marked[0]
	}
	x.sel.Titles = []int{guess.Index}
	look := 0
	for _, t := range feats {
		if math.Abs(float64(t.Duration-guess.Duration)) <= lookalikeShare*float64(guess.Duration) {
			look++
		}
	}
	idx := indexes(feats)
	if look >= lookalikeCount {
		x.hold("%d titles are within %d%% of each other's length: a disc with decoy playlists", look, int(lookalikeShare*100))
	} else {
		x.hold("%s are %s feature-length: perhaps another cut, or a decoy", x.names(idx), plural(len(idx)-1, "both", "all"))
	}
	if len(marked) == 1 {
		x.reason("pre-ticked %s: MakeMKV marked it as the main feature", x.name(guess.Index))
	} else {
		x.reason("pre-ticked %s: the longest", x.name(guess.Index))
	}
	return x.done(jobs.Low)
}

// features returns the feature-length titles that pass the decoy checks,
// each video once, and records what was dropped.
func (x *decision) features() []makemkv.Title {
	var feats []makemkv.Title
	for _, t := range x.in.Disc.Titles {
		if t.Duration >= FeatureLength && !x.excluded[t.Index] {
			feats = append(feats, t)
		}
	}
	if x.excluded != nil {
		var dropped []int
		for idx := range x.excluded {
			if t := x.byIndex[idx]; t.Duration >= FeatureLength {
				dropped = append(dropped, idx)
			}
		}
		slices.Sort(dropped)
		if len(dropped) > 0 {
			x.reason("TheDiscDB calls %s something other than the movie", x.names(dropped))
		}
	}

	// Duplicates: keep the one MakeMKV marked, else the first.
	slices.SortStableFunc(feats, func(a, b makemkv.Title) int {
		return cmp.Compare(boolInt(b.MakeMKVMainFeature), boolInt(a.MakeMKVMainFeature))
	})
	var kept []makemkv.Title
	copies := 0
	for _, t := range feats {
		if slices.ContainsFunc(kept, func(k makemkv.Title) bool { return SameVideo(k, t) }) {
			copies++
			continue
		}
		kept = append(kept, t)
	}
	if copies > 0 {
		x.reason("%d feature-length %s the same video as another", copies, plural(copies, "title is", "titles are"))
	}

	// There is no play-all check (a title that plays others end to end):
	// on 60 of TheDiscDB's movie discs the film itself is such a title (an
	// intro, or a studio logo per language, then the film), and dropping
	// play-alls took wrong picks from 2 to 62 (decision D45).

	// Too little data.
	top := 0.0
	for _, t := range kept {
		top = max(top, rate(t))
	}
	var floor, share []int // under minRate; under lowRateShare of the top
	kept = slices.DeleteFunc(kept, func(t makemkv.Title) bool {
		switch r := rate(t); {
		case r < minRate:
			floor = append(floor, t.Index)
		case r < lowRateShare*top:
			share = append(share, t.Index)
		default:
			return false
		}
		return true
	})
	if len(floor) > 0 {
		slices.Sort(floor)
		x.reason("%s %s under %.0f Mbit/s, too little data for a film: an extra or a loop",
			x.names(floor), plural(len(floor), "carries", "carry"), minRate/1e6)
	}
	if len(share) > 0 {
		slices.Sort(share)
		x.reason("%s %s under half the %.1f Mbit/s of the disc's richest feature-length title: an extra or a loop",
			x.names(share), plural(len(share), "carries", "carry"), top/1e6)
	}
	slices.SortFunc(kept, func(a, b makemkv.Title) int { return cmp.Compare(a.Index, b.Index) })
	return kept
}

// checkMark compares MakeMKV's main-feature marks with the pick. A mark on
// any other video is a hold, whether or not the pick is marked too.
func (x *decision) checkMark(pick makemkv.Title) {
	var other []int
	pickMarked := false
	for _, t := range x.in.Disc.Titles {
		switch {
		case !t.MakeMKVMainFeature:
		case t.Index == pick.Index || SameVideo(t, pick):
			pickMarked = true
		default:
			other = append(other, t.Index)
		}
	}
	switch {
	case len(other) > 0 && pickMarked:
		x.hold("MakeMKV marked %s as the main feature as well as this one", x.names(other))
	case len(other) > 0:
		x.hold("MakeMKV marked %s as the main feature instead", x.names(other))
	case pickMarked:
		x.reason("MakeMKV marked it as the main feature too")
	}
}

// name is "title 14 (1:56:31)", with TheDiscDB's name when it has one.
func (x *decision) name(idx int) string {
	s := fmt.Sprintf("title %d (%s)", idx, format.Clock(x.byIndex[idx].Duration))
	if l, ok := x.sel.Labels[idx]; ok && l.Name != "" {
		s += fmt.Sprintf(", “%s”", l.Name)
	}
	return s
}

// names lists a few titles, and how many more.
func (x *decision) names(idx []int) string {
	var parts []string
	for i, n := range idx {
		if i == maxNamed {
			parts = append(parts, fmt.Sprintf("%d more", len(idx)-maxNamed))
			break
		}
		parts = append(parts, x.name(n))
	}
	switch len(parts) {
	case 1:
		return parts[0]
	default:
		return strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
	}
}

// SameVideo reports whether two titles are the same video: the same length
// and size, within the slack.
func SameVideo(a, b makemkv.Title) bool {
	if (a.Duration - b.Duration).Abs() > dupLength {
		return false
	}
	return math.Abs(float64(a.SizeBytes-b.SizeBytes)) <= dupSize*float64(max(a.SizeBytes, b.SizeBytes))
}

// rate is a title's bits per second.
func rate(t makemkv.Title) float64 {
	if t.Duration <= 0 {
		return 0
	}
	return float64(t.SizeBytes) * 8 / t.Duration.Seconds()
}

// longest is the longest title, then the one with the most chapters, then
// the largest, then the lowest number.
func longest(ts []makemkv.Title) makemkv.Title {
	return slices.MaxFunc(ts, func(a, b makemkv.Title) int {
		return cmp.Or(cmp.Compare(a.Duration, b.Duration), cmp.Compare(a.Chapters, b.Chapters),
			cmp.Compare(a.SizeBytes, b.SizeBytes), cmp.Compare(b.Index, a.Index))
	})
}

func indexes(ts []makemkv.Title) []int {
	out := make([]int, len(ts))
	for i, t := range ts {
		out[i] = t.Index
	}
	return out
}

// release names the known disc for the page: TheDiscDB's release folder and
// its name for the disc.
func release(d *discdb.Disc) string {
	s := "release " + d.Release
	if d.Name != "" {
		s += fmt.Sprintf(", disc “%s”", d.Name)
	}
	return s
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// plural is one when n is 1, else many: "is" or "are".
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
