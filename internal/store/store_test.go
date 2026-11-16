package store

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/danvanton/discern/internal/jobs"
	"github.com/danvanton/discern/internal/makemkv"
	"github.com/danvanton/discern/internal/settings"
)

func openTest(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "discern.db")
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

var ctx = context.Background()

func TestMigrationsAreAppliedOnceAndSurviveReopening(t *testing.T) {
	s, path := openTest(t)
	j, err := s.CreateJob(ctx, "/dev/sr0")
	if err != nil {
		t.Fatal(err)
	}
	s.Close()

	s2, err := Open(ctx, path) // runs migrate again: must be a no-op
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if _, err := s2.Job(ctx, j.ID); err != nil {
		t.Errorf("job lost after reopening: %v", err)
	}
	var version int
	s2.db.QueryRow("PRAGMA user_version").Scan(&version)
	files, _ := fs.Glob(migrations, "migrations/*.sql")
	if version != len(files) {
		t.Errorf("user_version = %d, want %d (one per migration file)", version, len(files))
	}
}

func TestRefusesANewerSchema(t *testing.T) {
	s, path := openTest(t)
	s.db.Exec("PRAGMA user_version = 99")
	s.Close()
	if _, err := Open(ctx, path); err == nil {
		t.Fatal("opened a database with a newer schema, want an error")
	}
}

func TestCreateAndReadJob(t *testing.T) {
	s, _ := openTest(t)
	j, err := s.CreateJob(ctx, "/dev/sr0")
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Job(ctx, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != jobs.Detected || got.Drive != "/dev/sr0" || len(got.Titles) != 0 || got.Disc != nil {
		t.Errorf("got %+v", got)
	}
	if _, err := s.Job(ctx, 999); !errors.Is(err, jobs.ErrNotFound) {
		t.Errorf("missing job: err = %v, want ErrNotFound", err)
	}
}

func TestMoveSavesFieldsAndHistory(t *testing.T) {
	s, _ := openTest(t)
	j, _ := s.CreateJob(ctx, "/dev/sr0")
	j.Disc = &makemkv.Disc{Name: "Sample", Volume: "SAMPLE", Titles: []makemkv.Title{{Index: 0, Duration: 2 * time.Hour}}}
	j.Movie = &jobs.Movie{TMDbID: 603, Title: "The Matrix", Year: 1999}
	j.Titles = []int{0}
	if err := s.Move(ctx, j, jobs.Scanning, "starting"); err != nil {
		t.Fatal(err)
	}
	if j.State != jobs.Scanning {
		t.Errorf("Move didn't update j.State: %s", j.State)
	}

	got, _ := s.Job(ctx, j.ID)
	if got.Disc == nil || got.Disc.Titles[0].Duration != 2*time.Hour || got.Disc.Volume != "SAMPLE" {
		t.Errorf("disc didn't round-trip: %+v", got.Disc)
	}
	if !reflect.DeepEqual(got.Movie, j.Movie) || !reflect.DeepEqual(got.Titles, []int{0}) {
		t.Errorf("movie/titles = %+v / %v", got.Movie, got.Titles)
	}

	events, err := s.Events(ctx, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].To != jobs.Detected || events[1].From != jobs.Detected ||
		events[1].To != jobs.Scanning || events[1].Note != "starting" {
		t.Errorf("events = %+v", events)
	}
}

// A disc's own languages (decision D34) are saved with its job, and nil
// (the Settings page's choice) reads back as nil.
func TestMoveSavesTheDiscsLanguages(t *testing.T) {
	s, _ := openTest(t)
	j, _ := s.CreateJob(ctx, "/dev/sr0")
	if got, _ := s.Job(ctx, j.ID); got.Languages != nil {
		t.Errorf("a new job has languages %+v", got.Languages)
	}
	j.Languages = &settings.Languages{Audio: []string{"eng", "jpn"}, Subtitles: []string{"eng"}, ForcedOnly: true}
	if err := s.Move(ctx, j, jobs.Scanning, ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Job(ctx, j.ID); !reflect.DeepEqual(got.Languages, j.Languages) {
		t.Errorf("languages = %+v; want %+v", got.Languages, j.Languages)
	}
	j.Languages = nil
	if err := s.Move(ctx, j, jobs.Identifying, ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Job(ctx, j.ID); got.Languages != nil {
		t.Errorf("cleared languages read back %+v", got.Languages)
	}
}

// A failed job's description (decisions D35-D37) is saved with it.
func TestMoveSavesTheFailure(t *testing.T) {
	s, _ := openTest(t)
	j, _ := s.CreateJob(ctx, "/dev/sr0")
	j.Failure = &jobs.Failure{Kind: jobs.KindRipStalled, Diagnostics: "job-1-20261116-010203",
		Messages: []makemkv.Message{{Code: 2003, Text: "Error 'Scsi error - NOT READY' occurred"}}}
	if err := s.Move(ctx, j, jobs.Failed, "rip failed"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Job(ctx, j.ID); !reflect.DeepEqual(got.Failure, j.Failure) {
		t.Errorf("failure = %+v; want %+v", got.Failure, j.Failure)
	}
}

// The selector's decision (decision D43) is saved with the job, labels and
// all.
func TestMoveSavesTheSelection(t *testing.T) {
	s, _ := openTest(t)
	j, _ := s.CreateJob(ctx, "/dev/sr0")
	if got, _ := s.Job(ctx, j.ID); got.Selection != nil {
		t.Errorf("a new job has a selection: %+v", got.Selection)
	}
	j.Selection = &jobs.Selection{Titles: []int{}, Choices: []int{3, 7}, Confidence: jobs.Low,
		Reason: "two cuts", Reasons: []string{"TheDiscDB knows this disc"}, Holds: []string{"two cuts"},
		Labels: map[int]jobs.TitleLabel{3: {Type: "MainMovie", Name: "Theatrical"}, 7: {Type: "MainMovie", Name: "Director's Cut"}},
		Movie:  &jobs.Movie{TMDbID: 348, Title: "Alien", Year: 1979}}
	if err := s.Move(ctx, j, jobs.Scanning, ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Job(ctx, j.ID); !reflect.DeepEqual(got.Selection, j.Selection) {
		t.Errorf("selection = %+v; want %+v", got.Selection, j.Selection)
	}
}

// Move is a compare-and-swap: a stale copy of the job can't overwrite a newer
// state.
func TestMoveRefusesAStaleJob(t *testing.T) {
	s, _ := openTest(t)
	j, _ := s.CreateJob(ctx, "/dev/sr0")
	stale := *j
	if err := s.Move(ctx, j, jobs.Scanning, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.Move(ctx, &stale, jobs.Cancelled, ""); !errors.Is(err, jobs.ErrConflict) {
		t.Fatalf("stale move: err = %v, want ErrConflict", err)
	}
	if got, _ := s.Job(ctx, j.ID); got.State != jobs.Scanning {
		t.Errorf("state = %s after a refused move", got.State)
	}
	missing := jobs.Job{ID: 999, State: jobs.Detected}
	if err := s.Move(ctx, &missing, jobs.Scanning, ""); !errors.Is(err, jobs.ErrNotFound) {
		t.Errorf("missing job: err = %v, want ErrNotFound", err)
	}
}

func TestOneActiveJobPerDrive(t *testing.T) {
	s, _ := openTest(t)
	first, _ := s.CreateJob(ctx, "/dev/sr0")
	if _, err := s.CreateJob(ctx, "/dev/sr0"); !errors.Is(err, jobs.ErrDriveBusy) {
		t.Fatalf("second job on a busy drive: err = %v, want ErrDriveBusy", err)
	}
	if _, err := s.CreateJob(ctx, "/dev/sr1"); err != nil {
		t.Errorf("another drive: %v", err)
	}

	// Failing frees the drive; retrying the failed job then conflicts with
	// the new one.
	s.Move(ctx, first, jobs.Failed, "")
	second, err := s.CreateJob(ctx, "/dev/sr0")
	if err != nil {
		t.Fatalf("drive should be free after a failure: %v", err)
	}
	if err := s.Move(ctx, first, jobs.Ripping, "retry"); !errors.Is(err, jobs.ErrDriveBusy) {
		t.Errorf("retry on a busy drive: err = %v, want ErrDriveBusy", err)
	}
	_ = second
}

// The database's idea of "active" (the partial unique index) must match the
// Go code's State.Active, or the two would disagree about which drives are
// free.
func TestIndexMatchesActiveStates(t *testing.T) {
	for _, st := range jobs.AllStates() {
		s, _ := openTest(t)
		j, _ := s.CreateJob(ctx, "/dev/sr0")
		// Put the job straight into st, bypassing the transition table.
		if _, err := s.db.Exec(`UPDATE jobs SET state = ? WHERE id = ?`, st, j.ID); err != nil {
			t.Fatal(err)
		}
		_, err := s.CreateJob(ctx, "/dev/sr0")
		blocked := errors.Is(err, jobs.ErrDriveBusy)
		if blocked != st.Active() {
			t.Errorf("state %s: drive blocked = %v, but Active() = %v", st, blocked, st.Active())
		}
		active, _ := s.ActiveJobs(ctx)
		listed := len(active) > 0 && active[0].ID == j.ID
		if listed != st.Active() {
			t.Errorf("state %s: listed by ActiveJobs = %v, but Active() = %v", st, listed, st.Active())
		}
	}
}

func TestSetProgress(t *testing.T) {
	s, _ := openTest(t)
	j, _ := s.CreateJob(ctx, "/dev/sr0")
	if err := s.SetProgress(ctx, j.ID, 0.42); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Job(ctx, j.ID)
	if got.Progress != 0.42 {
		t.Errorf("progress = %v", got.Progress)
	}
	if events, _ := s.Events(ctx, j.ID); len(events) != 1 {
		t.Errorf("progress added %d history entries, want none", len(events)-1)
	}
}

func TestUnknownStateInTheDatabaseIsAnError(t *testing.T) {
	s, _ := openTest(t)
	j, _ := s.CreateJob(ctx, "/dev/sr0")
	s.db.Exec(`UPDATE jobs SET state = 'exploded' WHERE id = ?`, j.ID)
	if _, err := s.Job(ctx, j.ID); err == nil {
		t.Error("read a job with an unknown state, want an error")
	}
}

func TestOpenRejectsPathsTheDriverWouldMisread(t *testing.T) {
	for _, name := range []string{"what?.db", "a#b.db"} {
		if s, err := Open(ctx, filepath.Join(t.TempDir(), name)); err == nil {
			s.Close()
			t.Errorf("opened %q, want an error", name)
		}
	}
}

func TestSetProgressOnAnUnknownJob(t *testing.T) {
	s, _ := openTest(t)
	if err := s.SetProgress(ctx, 999, 0.5); !errors.Is(err, jobs.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// A new job and the same job read back must look the same.
func TestCreatedJobMatchesItsReadBack(t *testing.T) {
	s, _ := openTest(t)
	j, _ := s.CreateJob(ctx, "/dev/sr0")
	got, _ := s.Job(ctx, j.ID)
	j.CreatedAt, j.UpdatedAt = got.CreatedAt, got.UpdatedAt // stored at millisecond precision
	if !reflect.DeepEqual(j, got) {
		t.Errorf("created %+v\nread    %+v", j, got)
	}
}

func TestMoveLeavesProgressAlone(t *testing.T) {
	s, _ := openTest(t)
	j, _ := s.CreateJob(ctx, "/dev/sr0")
	s.SetProgress(ctx, j.ID, 0.6)
	j.Progress = 0 // an older copy
	s.Move(ctx, j, jobs.Scanning, "")
	if got, _ := s.Job(ctx, j.ID); got.Progress != 0.6 {
		t.Errorf("progress = %v after Move, want 0.6", got.Progress)
	}
}

// Many goroutines using the store at once must all finish: the single
// connection is shared, never held across another query.
func TestConcurrentUseFinishes(t *testing.T) {
	s, _ := openTest(t)
	done := make(chan error)
	for i := range 20 {
		go func() {
			drive := fmt.Sprintf("/dev/sr%d", i)
			j, err := s.CreateJob(ctx, drive)
			if err == nil {
				err = s.Move(ctx, j, jobs.Scanning, "")
			}
			if err == nil {
				err = s.SetProgress(ctx, j.ID, 0.5)
			}
			if err == nil {
				_, err = s.ActiveJobs(ctx)
			}
			if err == nil {
				_, err = s.Events(ctx, j.ID)
			}
			done <- err
		}()
	}
	timeout := time.After(20 * time.Second)
	for range 20 {
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-timeout:
			t.Fatal("store calls did not finish: deadlock?")
		}
	}
}

func TestRecentJobs(t *testing.T) {
	s, _ := openTest(t)
	var ids []int64
	for i, st := range []jobs.State{jobs.Done, jobs.Failed, jobs.Scanning, jobs.Cancelled} {
		j, err := s.CreateJob(ctx, fmt.Sprintf("/dev/sr%d", i))
		if err != nil {
			t.Fatal(err)
		}
		s.now = func() time.Time { return time.UnixMilli(int64(1000 * (i + 1))) }
		if err := s.Move(ctx, j, st, ""); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, j.ID)
	}
	recent, err := s.RecentJobs(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	// Newest first, active (scanning) jobs left out, at most 2.
	if len(recent) != 2 || recent[0].ID != ids[3] || recent[1].ID != ids[1] {
		t.Errorf("recent = %v, %v", recent[0].ID, recent[1].ID)
	}
	if err := s.Ping(ctx); err != nil {
		t.Error(err)
	}
}
