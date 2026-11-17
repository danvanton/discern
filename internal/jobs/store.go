package jobs

import (
	"context"
	"errors"
)

// Store is what the engine needs from persistence. The interface lives here,
// with the code that uses it, and internal/store implements it with SQLite.
// The engine never sees SQL.
type Store interface {
	// CreateJob starts a job in Detected for a drive. It returns
	// ErrDriveBusy if the drive already has an active job.
	CreateJob(ctx context.Context, drive string) (*Job, error)
	// Job returns one job, or ErrNotFound.
	Job(ctx context.Context, id int64) (*Job, error)
	// ActiveJobs returns the jobs in an Active state, oldest first.
	ActiveJobs(ctx context.Context) ([]*Job, error)
	// Move saves j with its fields as they are now and its state changed to
	// to, and records the transition in the job's history, all in one
	// transaction. It only succeeds if the job is still in j.State in the
	// database (compare-and-swap); otherwise it returns ErrConflict. On
	// success it updates j.State and j.UpdatedAt.
	//
	// Move saves the fields the engine owns (scan, movie, titles, note,
	// failed-in). Progress is not one of them: it is only ever written by
	// SetProgress, so a Move made from an older copy of the job can't undo it.
	Move(ctx context.Context, j *Job, to State, note string) error
	// SetProgress records rip progress without adding to the history. It
	// returns ErrNotFound for an unknown job.
	SetProgress(ctx context.Context, id int64, progress float64) error
	// Events returns a job's history, oldest first.
	Events(ctx context.Context, id int64) ([]Event, error)
	// RecordAttempt adds a rip attempt to the strategy history.
	RecordAttempt(ctx context.Context, a Attempt) error
	// LastResult returns the result of the latest attempt with this
	// strategy on this disc and MakeMKV version, or "" if there was none.
	LastResult(ctx context.Context, disc, makemkv string, s Strategy) (string, error)
}

var (
	ErrNotFound  = errors.New("job not found")
	ErrConflict  = errors.New("job changed since it was read")
	ErrDriveBusy = errors.New("the drive already has an active job")
	ErrRunning   = errors.New("job is already running")
	// ErrNoRoom means staging hasn't the space for a backup (decision
	// D68): the job waits for the user rather than failing.
	ErrNoRoom = errors.New("not enough free space for backup + remux")
	// ErrNoMovie means a job was approved before its movie is known. The
	// import needs one; whoever shows the error says how to choose it.
	ErrNoMovie = errors.New("the job has no movie yet: the import needs to know which one it is")
	// ErrNoTitles means a job was approved with no title ticked.
	ErrNoTitles = errors.New("choose at least one title")
	// ErrDiscChanged is returned by Steps.CheckDisc when the drive holds a
	// different disc from the one the job scanned.
	ErrDiscChanged = errors.New("the disc in the drive is not the one this job scanned")
)
