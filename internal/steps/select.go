package steps

import (
	"context"
	"fmt"

	"github.com/danvanton/discern/internal/jobs"
	"github.com/danvanton/discern/internal/makemkv"
	"github.com/danvanton/discern/internal/selector"
	"github.com/danvanton/discern/internal/settings"
)

// Select asks the selector (internal/selector) which titles are the movie,
// with TheDiscDB's index when there is one, then decides whether the job
// may go on without asking (auto).
func (s *Steps) Select(ctx context.Context, j *jobs.Job) (jobs.Selection, error) {
	in := selector.Input{Disc: j.Disc, Movie: j.Movie}
	if s.Index != nil {
		if ix := s.Index(); ix != nil {
			in.Indexed = true
			in.Match, in.Tied = ix.Lookup(j.Disc, nil)
		}
	}
	sel := selector.Decide(in)
	s.auto(ctx, in, &sel)
	return sel, nil
}

// auto applies the Settings page's auto-import mode (decisions D60-D63).
// Off, nothing changes. Otherwise a sure choice (selector.Unsure) for a
// movie Radarr doesn't have yet is marked Auto when the mode is on, or
// Trial when it is on trial; anything else gets the reason as Wait. Every
// doubt fails closed: settings that can't be read, or a Radarr that can't
// be asked, leave the job waiting for the user, never ripping.
func (s *Steps) auto(ctx context.Context, in selector.Input, sel *jobs.Selection) {
	if s.AutoImport == nil {
		return
	}
	mode, err := s.AutoImport(ctx)
	if err == nil {
		err = mode.Validate() // a mode from a newer Discern
	}
	if err != nil {
		sel.Wait = fmt.Sprintf("the auto-import setting can't be read (%v)", err)
		return
	}
	if mode != settings.AutoTrial && mode != settings.AutoOn {
		return
	}
	if why := selector.Unsure(in, *sel); why != "" {
		sel.Wait = why
		return
	}
	if why := s.inRadarr(ctx, sel.Movie); why != "" {
		sel.Wait = why
		return
	}
	if mode == settings.AutoTrial {
		sel.Trial = true
		return
	}
	sel.Auto = true
}

// inRadarr says why m can't be imported without asking because of
// Radarr's library, or "" when Radarr doesn't have it yet.
func (s *Steps) inRadarr(ctx context.Context, m *jobs.Movie) string {
	if s.HasMovie == nil {
		return "Radarr can't be asked whether it has this movie"
	}
	has, err := s.HasMovie(ctx, m.TMDbID)
	switch {
	case err != nil:
		return fmt.Sprintf("Radarr couldn't be asked whether it has %s (%v)", m.Name(), err)
	case has:
		return fmt.Sprintf("Radarr already has %s", m.Name())
	}
	return ""
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
