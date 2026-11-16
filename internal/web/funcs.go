package web

import (
	"fmt"
	"html/template"
	"slices"
	"strings"
	"time"

	"github.com/danvanton/discern/internal/drive"
	"github.com/danvanton/discern/internal/format"
	"github.com/danvanton/discern/internal/jobs"
	"github.com/danvanton/discern/internal/lang"
	"github.com/danvanton/discern/internal/makemkv"
)

// funcs are the helpers templates can call. Each one formats; none decides.
var funcs = template.FuncMap{
	"clock": format.Clock,
	"has":   slices.Contains[[]string, string],
	"bytes": format.Bytes,
	"pct":   func(p float64) int { return int(p*100 + 0.5) },
	"ago":   ago,
	"when":  func(t time.Time) string { return t.Local().Format("2006-01-02 15:04:05") },
	"label": stateLabel,
	"kind":  stateKind,
	"chosen": func(j *jobs.Job, idx int) bool {
		return slices.Contains(j.Titles, idx)
	},
	"video": func(t makemkv.Title) string {
		for _, s := range t.Streams {
			if s.Type == "Video" {
				return strings.TrimSpace(s.Codec + " " + s.VideoSize)
			}
		}
		return ""
	},
	"audio": func(t makemkv.Title) string {
		// By name, each language once: a scan says "deu" or "fra" where the
		// Settings page says German or French. A code Discern can't name is
		// shown as it is.
		var langs []string
		for _, s := range t.Streams {
			code := lang.Canonical(s.Lang)
			if s.Type != "Audio" || lang.None(code) {
				continue
			}
			name := lang.Name(code)
			if name == "" {
				name = code
			}
			if !slices.Contains(langs, name) {
				langs = append(langs, name)
			}
		}
		return strings.Join(langs, ", ")
	},
	"discName": func(j *jobs.Job) string {
		if j.Disc == nil {
			return "scanning…"
		}
		if j.Disc.Name != "" {
			return j.Disc.Name
		}
		return j.Disc.Volume
	},
	"movieName": func(m *jobs.Movie) string {
		if m == nil {
			return "unknown"
		}
		if m.Year > 0 {
			return fmt.Sprintf("%s (%d)", m.Title, m.Year)
		}
		return m.Title
	},
	"waiting": func(s jobs.State) bool { return s == jobs.NeedsYou },
	"failed":  func(s jobs.State) bool { return s == jobs.Failed },
	"cancellable": func(s jobs.State) bool {
		return jobs.CanMove(s, jobs.Cancelled) && s != jobs.Failed
	},
	"ripping": func(s jobs.State) bool { return s == jobs.Ripping },
	"live":    func(s jobs.State) bool { return s.Active() && s != jobs.NeedsYou },
	"name":    jobName,
	"doing":   doing,
	"tray":    trayLabel,
	"minutes": format.Minutes,
	// left words an estimate: "about 25 min", or "under a minute" (never
	// "about under a minute").
	"left": func(d time.Duration) string {
		if d < time.Minute {
			return format.Minutes(d)
		}
		return "about " + format.Minutes(d)
	},
	"rate": format.Rate,
	// res names a title's picture: "4K", "1080p", "720p" or "DVD".
	"res": func(t makemkv.Title) string {
		r, ok := t.Resolution()
		if !ok {
			return ""
		}
		return resolutionNames[r]
	},
	// firstLine is a note's first line: enough on the dashboard to tell a
	// swapped disc from a stall. The job page shows the whole note.
	"firstLine": func(s string) string {
		first, _, _ := strings.Cut(s, "\n")
		return first
	},
	"initial": func(j *jobs.Job) string {
		for _, r := range jobName(j) {
			return strings.ToUpper(string(r))
		}
		return "?"
	},
}

// jobName is how a job is called on every page (decision D22: no job
// numbers): its movie, or the disc's name until the movie is known.
func jobName(j *jobs.Job) string {
	if j.Movie != nil && j.Movie.Title != "" {
		return j.Movie.Title
	}
	if j.Disc != nil {
		if j.Disc.Name != "" {
			return j.Disc.Name
		}
		if j.Disc.Volume != "" {
			return j.Disc.Volume
		}
	}
	return "New disc"
}

// doing says in words what a job is doing, or how it ended.
func doing(j *jobs.Job) string {
	switch j.State {
	case jobs.Detected, jobs.Scanning:
		return "Reading the disc"
	case jobs.Identifying, jobs.Selecting:
		return "Working out the movie"
	case jobs.NeedsYou:
		return "Needs you: check and approve"
	case jobs.Ripping:
		return fmt.Sprintf("Ripping %d%%", int(j.Progress*100+0.5))
	case jobs.Verifying:
		return "Checking the rip"
	case jobs.Importing:
		return "Importing to Radarr"
	case jobs.Done:
		return "In Radarr"
	case jobs.Failed:
		return "Failed"
	case jobs.Cancelled:
		return "Cancelled"
	}
	return stateLabel(j.State)
}

// trayLabel says what an idle drive's tray is doing.
func trayLabel(s drive.State) string {
	switch s {
	case drive.NoDisc:
		return "Empty"
	case drive.TrayOpen:
		return "Tray open"
	case drive.NotReady:
		return "Disc loading"
	case drive.DiscOK:
		return "Disc in, not started"
	}
	return "Waiting for a disc"
}

func stateLabel(s jobs.State) string {
	switch s {
	case jobs.NeedsYou:
		return "Needs you"
	case jobs.Detected:
		return "Disc found"
	}
	return strings.ToUpper(string(s)[:1]) + string(s)[1:]
}

// stateKind is the CSS class that colours a state.
func stateKind(s jobs.State) string {
	switch s {
	case jobs.Done:
		return "ok"
	case jobs.Failed:
		return "bad"
	case jobs.NeedsYou:
		return "ask"
	case jobs.Cancelled:
		return "off"
	}
	return "busy"
}

func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d h ago", int(d.Hours()))
	}
	return t.Local().Format("2 Jan 2006")
}
