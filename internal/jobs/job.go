package jobs

import (
	"fmt"
	"time"

	"github.com/danvanton/discern/internal/lang"
	"github.com/danvanton/discern/internal/makemkv"
)

// Job is one disc's trip through Discern.
type Job struct {
	ID    int64
	Drive string // device path, e.g. "/dev/sr0"
	State State

	Disc   *makemkv.Disc // what the scan found; nil before Scanning finishes
	Movie  *Movie        // which movie it is; nil if unknown
	Titles []int         // the titles chosen to rip
	// Selection is what the selector suggested, with its reasons; nil
	// before Select, and for jobs selected before it was saved.
	Selection *Selection
	// Languages is this disc's own track choice, made on the review page
	// (decision D34); nil means the Settings page's choice applies.
	Languages *lang.Choice

	// Why the job is waiting in NeedsYou, or why it failed, in one sentence
	// for the UI.
	Note string
	// Failure says why a failed job failed, for the failure page; nil
	// otherwise.
	Failure *Failure
	// FailedIn is where Retry resumes: usually the state the job was in when
	// it failed, but Detected when its disc was swapped (a fresh scan is
	// needed).
	FailedIn State
	// Progress of the current rip, 0 to 1. Read from the store; only
	// Store.SetProgress writes it.
	Progress float64

	CreatedAt time.Time
	UpdatedAt time.Time
}

// Movie is the identified movie, in the terms Radarr and TMDb use.
type Movie struct {
	TMDbID int    `json:"tmdb_id"`
	Title  string `json:"title"`
	Year   int    `json:"year"`
}

// Name is the movie's title with its year, "Alien (1979)", or the title
// alone when the year isn't known.
func (m Movie) Name() string {
	if m.Year > 0 {
		return fmt.Sprintf("%s (%d)", m.Title, m.Year)
	}
	return m.Title
}

// Event is one transition in a job's history, as saved in the database.
type Event struct {
	JobID int64
	At    time.Time
	From  State
	To    State
	Note  string
}

// Selection is what the Select step decided: the selector's decision
// (internal/selector, decisions D43-D49). It is saved with the job, so the
// review page shows what made the choice even after TheDiscDB's index has
// changed.
type Selection struct {
	// Titles are the titles to pre-tick; none when the user must choose.
	Titles []int `json:"titles"`
	// Choices are titles the user must choose between, none of them
	// pre-ticked: the cuts TheDiscDB names when it lists two main titles
	// (decision D41, D49).
	Choices    []int      `json:"choices,omitempty"`
	Confidence Confidence `json:"confidence"`
	// Reason sums the decision up in one sentence: the first hold, or else
	// the first reason. It becomes the job's note.
	Reason string `json:"reason"`
	// Reasons say what the evidence was, for the review page.
	Reasons []string `json:"reasons,omitempty"`
	// Holds say why the selector wouldn't act alone; empty if it would.
	Holds []string `json:"holds,omitempty"`
	// Labels are TheDiscDB's names for the disc's titles, by title number.
	Labels map[int]TitleLabel `json:"labels,omitempty"`
	// Movie is the movie TheDiscDB names for the disc, when it knows the
	// disc. The engine uses it when Identify found none (decision D47).
	Movie *Movie `json:"movie,omitempty"`
	// Auto is true when the selector is confident enough to rip without
	// asking. It is always false until auto-import (decision D40).
	Auto bool `json:"auto,omitempty"`
}

// Confidence is how strong the selector's evidence is (decision D44).
type Confidence string

const (
	// High: TheDiscDB knows the disc and names one main title, and nothing
	// contradicts it.
	High Confidence = "high"
	// Medium: the scan alone points to one title (the only feature-length
	// one after the decoy checks), and nothing contradicts it.
	Medium Confidence = "medium"
	// Low: there are holds. The pre-ticked titles, if any, are a guess.
	Low Confidence = "low"
)

// TitleLabel is what TheDiscDB says a title is.
type TitleLabel struct {
	Type string `json:"type,omitempty"` // "MainMovie", "Extra", "Trailer"...
	Name string `json:"name,omitempty"` // e.g. "Alien Director's Cut"
}
