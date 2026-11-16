package jobs

import (
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
	// Languages is this disc's own track choice, made on the review page
	// (decision D34); nil means the Settings page's choice applies.
	Languages *lang.Choice

	// Why the job is waiting in NeedsYou, or why it failed, in one sentence
	// for the UI.
	Note string
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

// Event is one transition in a job's history, as saved in the database.
type Event struct {
	JobID int64
	At    time.Time
	From  State
	To    State
	Note  string
}

// Selection is what the Select step decided.
type Selection struct {
	Titles []int
	// Auto is true when the selector is confident enough to rip without
	// asking. In v0.1 it is always false: the user approves every disc.
	Auto bool
	// Reason explains the choice, or why it needs the user.
	Reason string
}
