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
	// Strategy is how the titles get from the disc to MKV (section 9.3):
	// Unset until someone chooses, when the strategy history decides;
	// Direct or Backup once the user chose one, or Backup once a rip
	// stalled or the history says Direct stalled on this disc before.
	Strategy Strategy

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
	// Auto is true when auto-import is on and the choice is sure enough to
	// rip without asking (decisions D60, D61).
	Auto bool `json:"auto,omitempty"`
	// Trial is true when auto-import is on trial and would have ripped this
	// disc without asking (decision D62).
	Trial bool `json:"trial,omitempty"`
	// Agreed records, for a trial, whether the user approved what Discern
	// would have done: the same titles and the same movie. Nil until the
	// user approves.
	Agreed *bool `json:"agreed,omitempty"`
	// Wait says why auto-import, on or on trial, didn't act on this disc.
	Wait string `json:"wait,omitempty"`
}

// Strategy is a rip strategy (DESIGN section 9.3, decision D11).
type Strategy string

const (
	// Unset: nobody chose; the strategy history decides (decision D66),
	// and Direct is tried unless it stalled on this disc before. The zero
	// value: a plain approval, and auto-import.
	Unset Strategy = ""
	// Direct: makemkvcon mkv straight to MKV, chosen: the history doesn't
	// override it.
	Direct Strategy = "direct"
	// Backup: makemkvcon backup copies the disc, then mkvmerge builds the
	// MKVs from the chosen titles' playlists.
	Backup Strategy = "backup"
)

// Attempt is one rip attempt in the strategy history (decisions D66, D71):
// how it went for this disc with this MakeMKV version.
type Attempt struct {
	Disc     string // makemkv.Disc.Fingerprint
	MakeMKV  string // MakeMKV's version; "" when unknown (a development makemkvcon)
	Strategy Strategy
	Result   string // AttemptOK, AttemptStalled or AttemptFailed
	JobID    int64
}

// Attempt results.
const (
	AttemptOK      = "ok"
	AttemptStalled = "stalled"
	AttemptFailed  = "failed"
)

// TrialTally is how auto-import's trial has gone (decision D62): of the
// discs it would have ripped on its own, how many the user approved the
// same way, and which jobs they approved otherwise.
type TrialTally struct {
	Agreed   int
	Differed []int64 // newest first
}

// Total is the number of trial discs the user has answered.
func (t TrialTally) Total() int { return t.Agreed + len(t.Differed) }

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
