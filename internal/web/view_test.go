package web

import (
	"testing"
	"time"

	"github.com/danvanton/discern/internal/jobs"
	"github.com/danvanton/discern/internal/makemkv"
)

// Time left is the time so far, scaled by the share still to do (D29). It
// is held back until a minute has passed and 2% is done.
func TestTimingFor(t *testing.T) {
	now := time.Date(2026, 11, 15, 15, 0, 0, 0, time.UTC)
	ripFrom := func(ago time.Duration) []jobs.Event {
		return []jobs.Event{
			{At: now.Add(-3 * time.Hour), To: jobs.Ripping}, // an earlier attempt: not this one
			{At: now.Add(-3 * time.Hour), From: jobs.Ripping, To: jobs.Failed},
			{At: now.Add(-ago), From: jobs.Failed, To: jobs.Ripping},
		}
	}
	for name, c := range map[string]struct {
		state    jobs.State
		progress float64
		events   []jobs.Event
		want     timing
	}{
		"halfway after 20 min": {jobs.Ripping, 0.5, ripFrom(20 * time.Minute),
			timing{Running: true, Elapsed: 20 * time.Minute, Left: 20 * time.Minute, LeftKnown: true}},
		"a quarter after 10 min": {jobs.Ripping, 0.25, ripFrom(10 * time.Minute),
			timing{Running: true, Elapsed: 10 * time.Minute, Left: 30 * time.Minute, LeftKnown: true}},
		"too early to say": {jobs.Ripping, 0.3, ripFrom(50 * time.Second),
			timing{Running: true, Elapsed: 50 * time.Second}},
		"too little done": {jobs.Ripping, 0.01, ripFrom(5 * time.Minute),
			timing{Running: true, Elapsed: 5 * time.Minute}},
		"finished": {jobs.Ripping, 1, ripFrom(50 * time.Minute),
			timing{Running: true, Elapsed: 50 * time.Minute}},
		"scanning: elapsed only": {jobs.Scanning, 0.5, []jobs.Event{{At: now.Add(-2 * time.Minute), To: jobs.Scanning}},
			timing{Running: true, Elapsed: 2 * time.Minute}},
		"waiting for the user: untimed": {jobs.NeedsYou, 0, []jobs.Event{{At: now.Add(-time.Hour), To: jobs.NeedsYou}},
			timing{}},
		"no history": {jobs.Ripping, 0.5, nil, timing{}},
		// A clock that went backwards gives no negative time.
		"start in the future": {jobs.Ripping, 0.5, ripFrom(-time.Minute), timing{Running: true}},
	} {
		got := timingFor(&jobs.Job{State: c.state, Progress: c.progress}, c.events, now, time.Time{})
		if got != c.want {
			t.Errorf("%s: %+v; want %+v", name, got, c.want)
		}
	}

	// The disc check took 5 minutes before makemkvcon started: the rip is
	// timed from makemkvcon's start, so at 2% after 1 min, 49 min are left,
	// not 4 h 54 min.
	j := &jobs.Job{State: jobs.Ripping, Progress: 0.02}
	got := timingFor(j, ripFrom(6*time.Minute), now, now.Add(-time.Minute))
	if got.Elapsed != time.Minute || got.Left != 49*time.Minute {
		t.Errorf("timed from makemkvcon's start: %+v; want 1 min in, 49 min left", got)
	}
	// A start from before this attempt (an old report) is ignored.
	if got := timingFor(j, ripFrom(6*time.Minute), now, now.Add(-time.Hour)); got.Elapsed != 6*time.Minute {
		t.Errorf("an older start was used: %+v", got)
	}
}

func TestLeftWording(t *testing.T) {
	left := funcs["left"].(func(time.Duration) string)
	if got := left(40 * time.Second); got != "under a minute" {
		t.Errorf("left(40s) = %q", got)
	}
	if got := left(25 * time.Minute); got != "about 25 min" {
		t.Errorf("left(25m) = %q", got)
	}
}

func TestStepsFor(t *testing.T) {
	now := time.Date(2026, 11, 15, 15, 0, 0, 0, time.UTC)
	ev := []jobs.Event{
		{At: now.Add(-60 * time.Minute), To: jobs.Detected},
		{At: now.Add(-60 * time.Minute), From: jobs.Detected, To: jobs.Scanning},
		{At: now.Add(-57 * time.Minute), From: jobs.Scanning, To: jobs.Identifying},
		{At: now.Add(-57 * time.Minute), From: jobs.Identifying, To: jobs.Selecting},
		{At: now.Add(-57 * time.Minute), From: jobs.Selecting, To: jobs.NeedsYou},
		{At: now.Add(-50 * time.Minute), From: jobs.NeedsYou, To: jobs.Ripping},
		{At: now.Add(-2 * time.Minute), From: jobs.Ripping, To: jobs.Verifying},
	}
	summary := func(steps []step) string {
		s := ""
		for _, st := range steps {
			s += st.State + ":" + st.Detail + " "
		}
		return s
	}
	for name, c := range map[string]struct {
		job  jobs.Job
		want string
	}{
		"verifying":                {jobs.Job{State: jobs.Verifying}, "done:3 min done:48 min now: next: "},
		"failed in verify":         {jobs.Job{State: jobs.Failed, FailedIn: jobs.Verifying}, "done:3 min done:48 min failed: next: "},
		"failed, no failure event": {jobs.Job{State: jobs.Failed, FailedIn: jobs.Detected}, "failed: next: next: next: "},
		"done":                     {jobs.Job{State: jobs.Done}, "done:3 min done:48 min done: done: "},
		"scanning":                 {jobs.Job{State: jobs.Scanning}, "now: next: next: next: "},
	} {
		if got := summary(stepsFor(&c.job, ev, now)); got != c.want {
			t.Errorf("%s: %s; want %s", name, got, c.want)
		}
	}
	if s := stepsFor(&jobs.Job{State: jobs.Cancelled}, ev, now); s != nil {
		t.Errorf("a cancelled job shows steps: %v", s)
	}
	// A swapped disc fails the check at the start of the rip; Retry rescans
	// (FailedIn is Detected), but the failed step is the rip.
	swapped := append(ev[:6:6], jobs.Event{At: now.Add(-49 * time.Minute), From: jobs.Ripping, To: jobs.Failed})
	got := summary(stepsFor(&jobs.Job{State: jobs.Failed, FailedIn: jobs.Detected}, swapped, now))
	if want := "done:3 min failed: next: next: "; got != want {
		t.Errorf("disc swapped: %s; want %s", got, want)
	}
}

func TestCardLine(t *testing.T) {
	for want, c := range map[string]card{
		"1999 · 4K": {Job: &jobs.Job{Movie: &jobs.Movie{Year: 1999}}, Quality: "4K"},
		"4K":        {Job: &jobs.Job{Movie: &jobs.Movie{}}, Quality: "4K"}, // no year: no stray dot
		"1999":      {Job: &jobs.Job{Movie: &jobs.Movie{Year: 1999}}},
		"":          {Job: &jobs.Job{}},
	} {
		if got := c.Line(); got != want {
			t.Errorf("Line() = %q; want %q", got, want)
		}
	}
}

func TestQuality(t *testing.T) {
	video := func(size string) *jobs.Job {
		return &jobs.Job{Titles: []int{0}, Disc: &makemkv.Disc{Titles: []makemkv.Title{
			{Index: 0, Streams: []makemkv.Stream{{Type: "Video", VideoSize: size}}}}}}
	}
	for size, want := range map[string]string{"3840x2160": "4K", "1920x1080": "1080p", "1280x720": "720p", "720x480": "DVD", "": ""} {
		if got := quality(video(size)); got != want {
			t.Errorf("quality(%s) = %q; want %q", size, got, want)
		}
	}
	if got := quality(&jobs.Job{}); got != "" {
		t.Errorf("no scan: %q", got)
	}
}
