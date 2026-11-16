package steps

import (
	"context"

	"github.com/danvanton/discern/internal/jobs"
	"github.com/danvanton/discern/internal/makemkv"
	"github.com/danvanton/discern/internal/selector"
)

// Select asks the selector (internal/selector) which titles are the movie,
// with TheDiscDB's index when there is one. It only suggests: the user
// approves every disc until auto-import (decision D40).
func (s *Steps) Select(_ context.Context, j *jobs.Job) (jobs.Selection, error) {
	in := selector.Input{Disc: j.Disc, Movie: j.Movie}
	if s.Index != nil {
		if ix := s.Index(); ix != nil {
			in.Indexed = true
			in.Match, in.Tied = ix.Lookup(j.Disc, nil)
		}
	}
	return selector.Decide(in), nil
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
