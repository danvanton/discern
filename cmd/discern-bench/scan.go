package main

import (
	"github.com/danvanton/discern/internal/discdb"
	"github.com/danvanton/discern/internal/jobs"
	"github.com/danvanton/discern/internal/makemkv"
	"github.com/danvanton/discern/internal/selector"
)

type selectorResult = jobs.Selection

// mainTitles are the disc's titles TheDiscDB calls the main movie.
func mainTitles(d *discdb.Disc) []discdb.Title {
	var out []discdb.Title
	for _, t := range d.Titles {
		if t.Type == discdb.MainMovie {
			out = append(out, t)
		}
	}
	return out
}

// sameDisc reports whether k must be hidden while d is scored: the same
// release of the same movie, or the same physical disc filed elsewhere (the
// same ContentHash, as a double feature is under each of its movies).
func sameDisc(d, k *discdb.Disc) bool {
	if k.Movie.TMDbID == d.Movie.TMDbID && k.Release == d.Release {
		return true
	}
	return d.ContentHash != "" && k.ContentHash == d.ContentHash
}

// sameVideo reports whether the scan's title pick is the main movie, or
// the same video through another playlist.
func sameVideo(scan *makemkv.Disc, pick int, main discdb.Title) bool {
	if pick == main.Index {
		return true
	}
	var p, m *makemkv.Title
	for i := range scan.Titles {
		switch scan.Titles[i].Index {
		case pick:
			p = &scan.Titles[i]
		case main.Index:
			m = &scan.Titles[i]
		}
	}
	return p != nil && m != nil && selector.SameVideo(*p, *m)
}

// actsAlone reports whether the selector would act without the user: any
// confidence but Low, with a title to rip.
func actsAlone(s jobs.Selection) bool {
	return s.Confidence != jobs.Low && len(s.Titles) > 0
}
