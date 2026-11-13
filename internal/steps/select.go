package steps

import (
	"fmt"

	"github.com/danvanton/discern/internal/format"
	"github.com/danvanton/discern/internal/jobs"
	"github.com/danvanton/discern/internal/makemkv"
)

// Suggest picks the title most likely to be the movie, for the user to
// confirm. It is v0.1's selector: simple on purpose, and never automatic
// (Auto is always false). The real selector, with TheDiscDB and decoy
// checks, comes in v0.2 and is measured by the benchmark (docs/DESIGN.md,
// sections 7 and 8).
//
// The order of preference: the one title MakeMKV marked as the main feature,
// then the longest title (the larger one if two are equally long).
func Suggest(d *makemkv.Disc) jobs.Selection {
	if d == nil || len(d.Titles) == 0 {
		return jobs.Selection{Reason: "the scan found no titles"}
	}
	var marked []makemkv.Title
	for _, t := range d.Titles {
		if t.MakeMKVMainFeature {
			marked = append(marked, t)
		}
	}
	if len(marked) == 1 {
		return jobs.Selection{
			Titles: []int{marked[0].Index},
			Reason: fmt.Sprintf("MakeMKV marked title %d (%s) as the main feature", marked[0].Index, format.Clock(marked[0].Duration)),
		}
	}
	best := d.Titles[0]
	for _, t := range d.Titles[1:] {
		if t.Duration > best.Duration || (t.Duration == best.Duration && t.SizeBytes > best.SizeBytes) {
			best = t
		}
	}
	return jobs.Selection{
		Titles: []int{best.Index},
		Reason: fmt.Sprintf("title %d is the longest (%s)", best.Index, format.Clock(best.Duration)),
	}
}

// SameDisc reports whether two scans are of the same disc: the same label
// and the same titles, by playlist, length and size. Either scan missing
// counts as different, so a job without a scan never rips.
func SameDisc(a, b *makemkv.Disc) bool {
	if a == nil || b == nil || a.Volume != b.Volume || len(a.Titles) != len(b.Titles) {
		return false
	}
	for i := range a.Titles {
		x, y := a.Titles[i], b.Titles[i]
		if x.Index != y.Index || x.SourceFile != y.SourceFile || x.Duration != y.Duration || x.SizeBytes != y.SizeBytes {
			return false
		}
	}
	return true
}
