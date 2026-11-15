package web

import (
	"context"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/danvanton/discern/internal/drive"
	"github.com/danvanton/discern/internal/format"
	"github.com/danvanton/discern/internal/jobs"
	"github.com/danvanton/discern/internal/makemkv"
)

// What the redesigned pages show about a job (decision D22): it is named by
// its movie, not a number, with its poster, how long the current step has
// run, and, while ripping, the time left and the speeds.

// card is one job as the pages show it.
type card struct {
	Job     *jobs.Job
	Poster  bool   // a poster is cached for its movie
	Quality string // "4K", "1080p", "720p", "DVD", or "" before a title is chosen
	Timing  timing
	Live    *makemkv.Live // nil unless ripping and the watchdog reported lately
}

// Line is the card's second line: the year and the quality, whichever are
// known, "1999 · 4K".
func (c card) Line() string {
	var parts []string
	if c.Job.Movie != nil && c.Job.Movie.Year > 0 {
		parts = append(parts, strconv.Itoa(c.Job.Movie.Year))
	}
	if c.Quality != "" {
		parts = append(parts, c.Quality)
	}
	return strings.Join(parts, " · ")
}

// timing is how long the current step has run and, for a rip, how long it
// has left (decision D29).
type timing struct {
	Running   bool // the step's start is known
	Elapsed   time.Duration
	Left      time.Duration
	LeftKnown bool // false: "estimating"
}

// A rip's time left is shown once it has run this long and got this far:
// before that, MakeMKV is still reading the disc's keys and the estimate
// would swing wildly.
const (
	estimateAfter = time.Minute
	estimateFrom  = 0.02
)

// timingFor works out the current step's timing from the job's history.
// Only steps that take a while are timed. A rip is timed from ripStart, when
// makemkvcon started, if the watchdog has reported it: the disc check before
// it can take minutes, which would inflate the estimate for the whole rip.
func timingFor(j *jobs.Job, events []jobs.Event, now, ripStart time.Time) timing {
	switch j.State {
	case jobs.Scanning, jobs.Ripping, jobs.Verifying, jobs.Importing:
	default:
		return timing{}
	}
	start, ok := lastEntry(events, j.State)
	if !ok {
		return timing{}
	}
	if j.State == jobs.Ripping && ripStart.After(start) {
		start = ripStart
	}
	t := timing{Running: true, Elapsed: max(now.Sub(start), 0)}
	p := j.Progress
	if j.State == jobs.Ripping && t.Elapsed >= estimateAfter && p >= estimateFrom && p < 1 {
		// What took Elapsed did share p, so share 1-p takes Elapsed*(1-p)/p.
		t.Left = time.Duration(float64(t.Elapsed) * (1 - p) / p)
		t.LeftKnown = true
	}
	return t
}

// lastEntry returns when the job last entered state s.
func lastEntry(events []jobs.Event, s jobs.State) (time.Time, bool) {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].To == s {
			return events[i].At, true
		}
	}
	return time.Time{}, false
}

// quality names the chosen title's picture as people say it.
func quality(j *jobs.Job) string {
	if j.Disc == nil || len(j.Titles) == 0 {
		return ""
	}
	for _, t := range j.Disc.Titles {
		if t.Index != j.Titles[0] {
			continue
		}
		res, ok := t.Resolution()
		if !ok {
			return ""
		}
		return map[int]string{2160: "4K", 1080: "1080p", 720: "720p", 480: "DVD"}[res]
	}
	return ""
}

// step is one line of the ripping page's progress list.
type step struct {
	Name   string
	State  string // "done", "now", "next" or "failed": also its CSS class
	Detail string
}

// The steps a job goes through, as the ripping page lists them, with the
// states that belong to each and the one whose length is the step's: the
// scan, not the wait for the user's approval.
var stepList = []struct {
	name   string
	states []jobs.State
	timed  jobs.State
}{
	{"Read the disc", []jobs.State{jobs.Detected, jobs.Scanning, jobs.Identifying, jobs.Selecting, jobs.NeedsYou}, jobs.Scanning},
	{"Rip", []jobs.State{jobs.Ripping}, jobs.Ripping},
	{"Check the rip", []jobs.State{jobs.Verifying}, jobs.Verifying},
	{"Import to Radarr", []jobs.State{jobs.Importing}, jobs.Importing},
}

// stepsFor lists the steps, marking the current one, or the one that
// failed. A cancelled job shows no steps.
func stepsFor(j *jobs.Job, events []jobs.Event, now time.Time) []step {
	if j.State == jobs.Cancelled {
		return nil
	}
	at := j.State
	if j.State == jobs.Failed {
		// Where it failed is the state it left for Failed. FailedIn is where
		// Retry resumes, which differs for a swapped disc: the check at the
		// start of the rip failed, but Retry rescans.
		at = j.FailedIn
		for i := len(events) - 1; i >= 0; i-- {
			if events[i].To == jobs.Failed {
				at = events[i].From
				break
			}
		}
	}
	current := len(stepList) // Done: every step is done
	for i, s := range stepList {
		for _, st := range s.states {
			if st == at {
				current = i
			}
		}
	}
	var out []step
	for i, s := range stepList {
		st := step{Name: s.name, State: "next"}
		switch {
		case i < current:
			st.State = "done"
			if d, ok := took(events, s.timed); ok {
				st.Detail = format.Minutes(d)
			}
		case i == current && j.State == jobs.Failed:
			st.State = "failed"
		case i == current:
			st.State = "now"
		}
		out = append(out, st)
	}
	return out
}

// took is how long the job last spent in state s: from entering it to
// leaving it.
func took(events []jobs.Event, s jobs.State) (time.Duration, bool) {
	start, ok := lastEntry(events, s)
	if !ok {
		return 0, false
	}
	for _, e := range events {
		if e.From == s && !e.At.Before(start) {
			return e.At.Sub(start), true
		}
	}
	return 0, false
}

// driveCard is one drive on the dashboard: its job if it has one, or else
// what its tray is doing.
type driveCard struct {
	Path    string
	Name    string // "sr0"
	Tray    drive.State
	Pending bool // a new disc waits for the current job to end
	Job     *card
}

// cardFor builds a job's card. Events are read only for jobs whose step is
// timed.
func (s *server) cardFor(ctx context.Context, j *jobs.Job, events []jobs.Event, now time.Time) (card, error) {
	c := card{Job: j, Quality: quality(j)}
	if j.Movie != nil {
		_, _, c.Poster = s.b.Poster(j.Movie.TMDbID)
	}
	if events == nil && j.State.Active() {
		var err error
		if events, err = s.b.Events(ctx, j.ID); err != nil {
			return card{}, err
		}
	}
	var ripStart time.Time
	if j.State == jobs.Ripping {
		if l, ok := s.b.Live(j.Drive); ok {
			c.Live = &l
			ripStart = l.Started
		}
	}
	c.Timing = timingFor(j, events, now, ripStart)
	return c, nil
}

func driveName(p string) string { return path.Base(p) }
