package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/danvanton/discern/internal/jobs"
	"github.com/danvanton/discern/internal/makemkv"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// The store implements the engine's interface; this line makes the compiler
// check that.
var _ jobs.Store = (*Store)(nil)

const jobColumns = `id, drive, state, disc, movie, titles, note, failed_in, progress, created_at, updated_at`

func (s *Store) CreateJob(ctx context.Context, drive string) (*jobs.Job, error) {
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() // no-op after Commit
	res, err := tx.ExecContext(ctx,
		`INSERT INTO jobs (drive, state, created_at, updated_at) VALUES (?, ?, ?, ?)`,
		drive, jobs.Detected, ms(now), ms(now))
	if isUniqueViolation(err) {
		return nil, fmt.Errorf("%w: %s", jobs.ErrDriveBusy, drive)
	}
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	if err := addEvent(ctx, tx, id, now, "", jobs.Detected, ""); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &jobs.Job{ID: id, Drive: drive, State: jobs.Detected, Titles: []int{}, CreatedAt: now, UpdatedAt: now}, nil
}

func (s *Store) Job(ctx context.Context, id int64) (*jobs.Job, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM jobs WHERE id = ?`, id)
	j, err := scanJob(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: %d", jobs.ErrNotFound, id)
	}
	return j, err
}

// inactiveStates are the states jobs.State.Active rejects. ActiveJobs builds
// its query from this list, so Go is the one source for it; the partial index
// in migrations/001_jobs.sql has to spell it out, and a test checks the two
// agree.
var inactiveStates = func() []any {
	var out []any
	for _, st := range jobs.AllStates() {
		if !st.Active() {
			out = append(out, st)
		}
	}
	return out
}()

// ActiveJobs reads every matching row before returning, so no query is left
// open on the store's single connection (see Open).
func (s *Store) ActiveJobs(ctx context.Context) ([]*jobs.Job, error) {
	marks := strings.TrimSuffix(strings.Repeat("?, ", len(inactiveStates)), ", ")
	rows, err := s.db.QueryContext(ctx, `SELECT `+jobColumns+` FROM jobs
		WHERE state NOT IN (`+marks+`) ORDER BY id`, inactiveStates...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*jobs.Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// RecentJobs returns up to limit jobs that are no longer active (done,
// failed or cancelled), most recently changed first, for the history list.
func (s *Store) RecentJobs(ctx context.Context, limit int) ([]*jobs.Job, error) {
	marks := strings.TrimSuffix(strings.Repeat("?, ", len(inactiveStates)), ", ")
	args := append(append([]any{}, inactiveStates...), limit)
	rows, err := s.db.QueryContext(ctx, `SELECT `+jobColumns+` FROM jobs
		WHERE state IN (`+marks+`) ORDER BY updated_at DESC, id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*jobs.Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// Ping checks the database answers, for the health check.
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

func (s *Store) Move(ctx context.Context, j *jobs.Job, to jobs.State, note string) error {
	disc, err := jsonOrNull(j.Disc)
	if err != nil {
		return err
	}
	movie, err := jsonOrNull(j.Movie)
	if err != nil {
		return err
	}
	titles, err := json.Marshal(nonNil(j.Titles))
	if err != nil {
		return err
	}
	now := s.now()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// "AND state = ?" is the compare-and-swap: the update only happens if
	// nobody moved the job since j was read.
	// Progress is deliberately not written here: only SetProgress writes it,
	// so a Move from an older copy of the job can't undo a newer value.
	res, err := tx.ExecContext(ctx, `UPDATE jobs
		SET state = ?, disc = ?, movie = ?, titles = ?, note = ?, failed_in = ?,
		    updated_at = ?
		WHERE id = ? AND state = ?`,
		to, disc, movie, string(titles), j.Note, j.FailedIn, ms(now),
		j.ID, j.State)
	if isUniqueViolation(err) {
		return fmt.Errorf("%w: %s", jobs.ErrDriveBusy, j.Drive)
	}
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM jobs WHERE id = ?)`, j.ID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("%w: %d", jobs.ErrNotFound, j.ID)
		}
		return fmt.Errorf("%w: job %d is no longer %s", jobs.ErrConflict, j.ID, j.State)
	}
	if err := addEvent(ctx, tx, j.ID, now, j.State, to, note); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	j.State, j.UpdatedAt = to, now
	return nil
}

func (s *Store) SetProgress(ctx context.Context, id int64, progress float64) error {
	res, err := s.db.ExecContext(ctx, `UPDATE jobs SET progress = ?, updated_at = ? WHERE id = ?`,
		progress, ms(s.now()), id)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return fmt.Errorf("%w: %d", jobs.ErrNotFound, id)
	}
	return nil
}

func (s *Store) Events(ctx context.Context, id int64) ([]jobs.Event, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT job_id, at, from_state, to_state, note FROM job_events WHERE job_id = ? ORDER BY id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []jobs.Event
	for rows.Next() {
		var e jobs.Event
		var at int64
		if err := rows.Scan(&e.JobID, &at, &e.From, &e.To, &e.Note); err != nil {
			return nil, err
		}
		e.At = fromMs(at)
		out = append(out, e)
	}
	return out, rows.Err()
}

func addEvent(ctx context.Context, tx *sql.Tx, id int64, at time.Time, from, to jobs.State, note string) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO job_events (job_id, at, from_state, to_state, note) VALUES (?, ?, ?, ?, ?)`,
		id, ms(at), from, to, note)
	return err
}

// scanJob reads one row in jobColumns order. *sql.Row and *sql.Rows both
// have this Scan method.
func scanJob(row interface{ Scan(...any) error }) (*jobs.Job, error) {
	var j jobs.Job
	var disc, movie sql.NullString
	var titles string
	var created, updated int64
	if err := row.Scan(&j.ID, &j.Drive, &j.State, &disc, &movie, &titles,
		&j.Note, &j.FailedIn, &j.Progress, &created, &updated); err != nil {
		return nil, err
	}
	if !j.State.Valid() {
		return nil, fmt.Errorf("job %d has unknown state %q", j.ID, j.State)
	}
	if disc.Valid {
		j.Disc = new(makemkv.Disc)
		if err := json.Unmarshal([]byte(disc.String), j.Disc); err != nil {
			return nil, fmt.Errorf("job %d disc: %w", j.ID, err)
		}
	}
	if movie.Valid {
		j.Movie = new(jobs.Movie)
		if err := json.Unmarshal([]byte(movie.String), j.Movie); err != nil {
			return nil, fmt.Errorf("job %d movie: %w", j.ID, err)
		}
	}
	if err := json.Unmarshal([]byte(titles), &j.Titles); err != nil {
		return nil, fmt.Errorf("job %d titles: %w", j.ID, err)
	}
	j.CreatedAt, j.UpdatedAt = fromMs(created), fromMs(updated)
	return &j, nil
}

// jsonOrNull stores a nil pointer as SQL NULL and anything else as JSON.
func jsonOrNull[T any](v *T) (sql.NullString, error) {
	if v == nil {
		return sql.NullString{}, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return sql.NullString{}, err
	}
	return sql.NullString{String: string(b), Valid: true}, nil
}

func nonNil(t []int) []int {
	if t == nil {
		return []int{}
	}
	return t
}

func isUniqueViolation(err error) bool {
	var se *sqlite.Error
	return errors.As(err, &se) && se.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE
}

// Times are stored as Unix milliseconds in UTC: sortable, compact, and free
// of time-zone text.
func ms(t time.Time) int64     { return t.UnixMilli() }
func fromMs(v int64) time.Time { return time.UnixMilli(v).UTC() }
