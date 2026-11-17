// Package jobs moves each disc through Discern's steps: scan, identify,
// select, rip, verify, import (docs/DESIGN.md, section 6).
//
// A job's state is saved after every step, so the database, not memory, is
// the source of truth. Every step is safe to run again, so recovering from a
// crash is simply "run the step the saved state names". That is the whole
// recovery design.
package jobs

import "fmt"

// State is where a job is in its journey. The values are stored in the
// database, so they must never be renamed.
type State string

const (
	Detected    State = "detected"    // a disc went in; nothing done yet
	Scanning    State = "scanning"    // makemkvcon info is reading the disc
	Identifying State = "identifying" // working out which movie this is
	Selecting   State = "selecting"   // choosing the titles to rip
	NeedsYou    State = "needs_you"   // waiting for the user to choose or approve
	Ripping     State = "ripping"     // makemkvcon mkv, under the watchdog
	Stalled     State = "stalled"     // the watchdog stopped a stalled rip; the fallback comes next (D64)
	BackingUp   State = "backing_up"  // makemkvcon backup copies the disc (D11)
	Remuxing    State = "remuxing"    // mkvmerge builds the MKVs from the backup
	Verifying   State = "verifying"   // checking the files against the scan
	Importing   State = "importing"   // handing the files to Radarr or the library
	Done        State = "done"
	Failed      State = "failed"
	Cancelled   State = "cancelled"
)

// transitions lists, for each state, the states a job may move to next. It is
// data, not code, so the whole state machine can be read (and drawn) from
// this one table, and extended by adding entries.
var transitions = map[State][]State{
	Detected:    {Scanning, Failed, Cancelled},
	Scanning:    {Identifying, Failed, Cancelled},
	Identifying: {Selecting, Failed, Cancelled},
	Selecting:   {Ripping, NeedsYou, Failed, Cancelled},
	NeedsYou:    {Ripping, Cancelled},
	// BackingUp: the job takes the Backup + remux route, chosen by the user
	// or by the strategy history (decisions D66, D69).
	Ripping: {Verifying, Stalled, BackingUp, Failed, Cancelled},
	Stalled: {BackingUp, Failed, Cancelled},
	// NeedsYou: staging hasn't the space for a backup (decision D68).
	BackingUp: {Remuxing, NeedsYou, Failed, Cancelled},
	Remuxing:  {Verifying, Failed, Cancelled},
	Verifying: {Importing, Failed, Cancelled},
	// No Cancelled: an import that has started must finish or fail, so
	// Radarr and Discern never disagree about where the file is.
	Importing: {Done, Failed},
	// Retry resumes at the step that failed, or rescans from the start.
	Failed:    {Detected, Ripping, BackingUp, Remuxing, Verifying, Importing, Cancelled},
	Done:      nil,
	Cancelled: nil,
}

// AllStates returns every state, in the order a job meets them.
func AllStates() []State {
	return []State{Detected, Scanning, Identifying, Selecting, NeedsYou, Ripping,
		Stalled, BackingUp, Remuxing, Verifying, Importing, Done, Failed, Cancelled}
}

// CanMove reports whether a job may go from one state to another.
func CanMove(from, to State) bool {
	for _, s := range transitions[from] {
		if s == to {
			return true
		}
	}
	return false
}

// Terminal states end a job: nothing more happens to it.
func (s State) Terminal() bool { return s == Done || s == Cancelled }

// Active states hold the drive: no other job may start on it. Failed is not
// active (the drive is free for the next disc) until the job is retried.
func (s State) Active() bool { return !s.Terminal() && s != Failed }

// Valid reports whether s is one of the states above, for values read back
// from the database.
func (s State) Valid() bool {
	_, ok := transitions[s]
	return ok
}

// TransitionError reports a move the table above does not allow.
type TransitionError struct{ From, To State }

func (e *TransitionError) Error() string {
	return fmt.Sprintf("a job can't go from %s to %s", e.From, e.To)
}
