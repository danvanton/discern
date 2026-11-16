package steps

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"time"

	"github.com/danvanton/discern/internal/jobs"
	"github.com/danvanton/discern/internal/makemkv"
)

// BundleName is the form of a diagnostics folder's name: the job and when.
// The web UI checks a name against it before reading the folder.
var BundleName = regexp.MustCompile(`^job-[0-9]+-[0-9]{8}-[0-9]{6}$`)

// saveDiagnostics writes a stalled or hung rip's evidence (MakeMKV's debug
// log, its output and the counter samples) into the Diagnostics folder, as
// section 9.2 of the design says, and returns err carrying the folder's
// name for the failure page. Any other error comes back as it is. Failing to
// save is reported in the returned error, but never hides the rip's own.
func (s *Steps) saveDiagnostics(j *jobs.Job, err error) error {
	var we *makemkv.WatchdogError
	if s.Diagnostics == "" || !errors.As(err, &we) || we.Evidence == nil {
		return err
	}
	we.Evidence.Redact(s.Secrets...)
	name := fmt.Sprintf("job-%d-%s", j.ID, time.Now().UTC().Format("20060102-150405"))
	if werr := we.Evidence.WriteBundle(filepath.Join(s.Diagnostics, name)); werr != nil {
		return fmt.Errorf("%w (and its diagnostics couldn't be saved: %v)", err, werr)
	}
	return &diagnosedError{err: err, bundle: name}
}

// diagnosedError is a rip error whose evidence was saved.
type diagnosedError struct {
	err    error
	bundle string
}

func (e *diagnosedError) Error() string             { return e.err.Error() }
func (e *diagnosedError) Unwrap() error             { return e.err }
func (e *diagnosedError) DiagnosticsBundle() string { return e.bundle }
