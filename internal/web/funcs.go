package web

import (
	"fmt"
	"html/template"
	"slices"
	"strings"
	"time"

	"github.com/NotNad/discern/internal/format"
	"github.com/NotNad/discern/internal/jobs"
	"github.com/NotNad/discern/internal/makemkv"
)

// funcs are the helpers templates can call. Each one formats; none decides.
var funcs = template.FuncMap{
	"clock": format.Clock,
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
		var langs []string
		for _, s := range t.Streams {
			if s.Type == "Audio" && s.Lang != "" && !slices.Contains(langs, s.Lang) {
				langs = append(langs, s.Lang)
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
