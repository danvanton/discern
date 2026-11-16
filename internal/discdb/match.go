package discdb

import (
	"math"
	"slices"
	"time"

	"github.com/danvanton/discern/internal/makemkv"
)

// Matching a scanned disc to TheDiscDB by shape (decision D39): its titles'
// playlist files, segment maps, lengths and sizes. These survive MakeMKV
// numbering titles differently; on the owner's own UHD discs, every title
// matched exactly (191 of 191, and 162 of 162).

// shape is what a title is looked up by: its playlist and segment map.
type shape struct{ file, segments string }

// ref is a title in the index: disc and title positions.
type ref struct{ disc, title int }

// Tolerances for a title to match: MakeMKV versions may round a length or
// count a size slightly differently, though the real discs matched exactly.
const (
	lengthSlack = 2     // seconds
	sizeSlack   = 0.005 // share of the size
)

// coverage is how much must line up both ways for a disc to match: nearly
// all of the scan's titles, and nearly all of the known disc's. A disc
// can't match on a few short, common clips that many discs share.
const coverage = 0.9

// longTitle is where the running-time test starts (decision D46): the
// matched titles must also cover nearly all the running time of the scan's
// titles this long or longer. Large 4K discs share hundreds of identical
// studio clips, so counting titles alone let a disc TheDiscDB doesn't know
// match another movie's disc (1917 matched Jaws) on clips alone, with the
// films themselves unmatched.
const longTitle = 20 * time.Minute

// prepare builds the lookup Match uses. A title without a playlist or a
// segment map has no shape to match by, so it isn't indexed: matching on
// length and size alone isn't matching by shape.
func (ix *Index) prepare() {
	ix.byShape = map[shape][]ref{}
	ix.shortest = make([]int, len(ix.Discs))
	ix.shaped = make([]int, len(ix.Discs))
	for di, d := range ix.Discs {
		ix.shortest[di] = -1
		for ti, t := range d.Titles {
			if !hasShape(t.File, t.Segments) {
				continue
			}
			k := shape{t.File, t.Segments}
			ix.byShape[k] = append(ix.byShape[k], ref{di, ti})
			ix.shaped[di]++
			if ix.shortest[di] < 0 || t.Seconds < ix.shortest[di] {
				ix.shortest[di] = t.Seconds
			}
		}
	}
}

func hasShape(file, segments string) bool { return file != "" && segments != "" }

// Match is a scanned disc recognised in TheDiscDB.
type Match struct {
	Disc *Disc
	// Titles maps the scan's title numbers to TheDiscDB's titles.
	Titles map[int]Title
}

// Mains returns the scan's title numbers that TheDiscDB calls the main
// movie, in order. There can be two, one per cut.
func (m *Match) Mains() []int {
	var out []int
	for idx, t := range m.Titles {
		if t.Type == MainMovie {
			out = append(out, idx)
		}
	}
	slices.Sort(out)
	return out
}

// Match finds the known disc the scan is, or nil. Discs for which skip
// returns true are left out: the benchmark hides a disc's own release to
// measure what happens with a disc TheDiscDB doesn't know.
//
// It fails closed: if discs of two different movies both fit, it matches
// neither, however much better one fits than the other.
func (ix *Index) Match(d *makemkv.Disc, skip func(*Disc) bool) *Match {
	m, _ := ix.Lookup(d, skip)
	return m
}

// Lookup is Match that also says why there is no match: when discs of two
// or more movies fit, it returns those movies, ordered by TMDb id (a double
// feature filed under each of its films, say).
func (ix *Index) Lookup(d *makemkv.Disc, skip func(*Disc) bool) (*Match, []Movie) {
	if ix == nil || d == nil || len(d.Titles) == 0 {
		return nil, nil
	}
	// For each candidate disc, which scan titles line up with which of its
	// titles. A scan title counts once per disc.
	found := map[int]map[int]int{} // disc → scan title index → title position
	// Nothing is indexed under an empty shape, so a scan title without one
	// finds nothing.
	for _, st := range d.Titles {
		for _, r := range ix.byShape[shape{st.SourceFile, st.SegmentMap}] {
			kd := &ix.Discs[r.disc]
			if skip != nil && skip(kd) {
				continue
			}
			if !sameLength(st, kd.Titles[r.title]) {
				continue
			}
			if found[r.disc] == nil {
				found[r.disc] = map[int]int{}
			}
			if _, ok := found[r.disc][st.Index]; !ok {
				found[r.disc][st.Index] = r.title
			}
		}
	}
	best, bestCount, classified := -1, 0, 0
	fits := map[int]Movie{} // the movies whose discs fit, by TMDb id
	for di, titles := range found {
		if !ix.covers(di, d, titles) {
			continue
		}
		kd := &ix.Discs[di]
		fits[kd.Movie.TMDbID] = kd.Movie
		if best >= 0 && ix.Discs[best].Movie.TMDbID != kd.Movie.TMDbID {
			continue // two movies fit: trust neither (below)
		}
		n, c := len(titles), classifiedCount(kd)
		// The same movie in another release: keep the better fit, then the
		// one that says more about its titles.
		if n > bestCount || n == bestCount && c > classified {
			best, bestCount, classified = di, n, c
		}
	}
	if len(fits) > 1 {
		tied := make([]Movie, 0, len(fits))
		for _, mv := range fits {
			tied = append(tied, mv)
		}
		slices.SortFunc(tied, func(a, b Movie) int { return a.TMDbID - b.TMDbID })
		return nil, tied
	}
	if best < 0 {
		return nil, nil
	}
	m := &Match{Disc: &ix.Discs[best], Titles: map[int]Title{}}
	for idx, pos := range found[best] {
		m.Titles[idx] = ix.Discs[best].Titles[pos]
	}
	return m, nil
}

// Scan is the scan MakeMKV would give of this disc, from its record. The
// records keep no chapters and no MakeMKV main-feature mark.
func (d *Disc) Scan() *makemkv.Disc {
	out := &makemkv.Disc{Name: d.Name}
	for _, t := range d.Titles {
		out.Titles = append(out.Titles, makemkv.Title{
			Index: t.Index, SourceFile: t.File, SegmentMap: t.Segments,
			Duration: time.Duration(t.Seconds) * time.Second, SizeBytes: t.Size,
		})
	}
	return out
}

// covers reports whether the matched titles cover nearly all of both
// discs. On the scan's side, only titles the known disc could have listed
// count: a record made with MakeMKV's default minimum length (2 minutes)
// has none of the short clips Discern's scan (no minimum) lists. On the
// known disc's side, its own titles found count, each once, however many
// scan titles share one. The scan's long titles must also be covered by
// running time (longTitle).
func (ix *Index) covers(di int, d *makemkv.Disc, titles map[int]int) bool {
	eligible := 0
	var long, longFound time.Duration
	for _, st := range d.Titles {
		secs := int(st.Duration.Round(time.Second) / time.Second)
		if !hasShape(st.SourceFile, st.SegmentMap) || secs < ix.shortest[di]-lengthSlack {
			continue
		}
		eligible++
		if st.Duration >= longTitle {
			long += st.Duration
			if _, ok := titles[st.Index]; ok {
				longFound += st.Duration
			}
		}
	}
	distinct := map[int]bool{}
	for _, pos := range titles {
		distinct[pos] = true
	}
	return float64(len(titles)) >= coverage*float64(eligible) &&
		float64(len(distinct)) >= coverage*float64(ix.shaped[di]) &&
		float64(longFound) >= coverage*float64(long)
}

func sameLength(st makemkv.Title, t Title) bool {
	secs := int(st.Duration.Round(time.Second) / time.Second)
	if abs(secs-t.Seconds) > lengthSlack {
		return false
	}
	return math.Abs(float64(st.SizeBytes-t.Size)) <= sizeSlack*float64(max(st.SizeBytes, t.Size))
}

func classifiedCount(d *Disc) int {
	n := 0
	for _, t := range d.Titles {
		if t.Type != "" {
			n++
		}
	}
	return n
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
