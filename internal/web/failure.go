package web

import (
	"archive/zip"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/danvanton/discern/internal/jobs"
)

// The failure page (decisions D35-D37): a failure in plain words, with what
// to do next, and the technical details folded underneath.

// plain is a failure explained: what happened, and what to do about it.
type plain struct {
	What string
	Todo string
}

// plainWords explain each failure kind the engine records (decision D36).
// Kinds not listed are a step's name, explained by plainSteps.
var plainWords = map[string]plain{
	jobs.KindDiscChanged: {"The disc in the drive isn't the one this job scanned.",
		"Put the right disc back and press Retry: Discern scans it again first."},
	jobs.KindScanIncomplete: {"MakeMKV couldn't read the whole disc.",
		"Clean the disc and press Retry. If it fails again the same way, the disc may be damaged."},
	jobs.KindMakeMKVExit: {"MakeMKV stopped with an error.",
		"Retry once; if it fails the same way, the disc or MakeMKV can't handle this title. Any messages MakeMKV printed are under Details."},
	jobs.KindRipStalled: {"The rip stalled: MakeMKV kept reading the disc but stopped writing.",
		"Some UHD discs do this in MakeMKV 2.0.0. Retry may stall again; the diagnostics under Details are what a MakeMKV bug report needs."},
	jobs.KindRipHung: {"The rip froze: MakeMKV stopped reading and writing.",
		"Check the drive is still connected and the disc spins, then press Retry."},
	jobs.KindVerifySize: {"The ripped file isn't the size the scan expected.",
		"The rip probably stopped early. Press Retry; if it fails again, the disc may be damaged."},
	jobs.KindVerifyNoVideo: {"The ripped file isn't a playable video.",
		"Press Retry to rip it again."},
	jobs.KindVerifyNoAudio: {"The ripped file has no sound.",
		"The languages kept may not be on this disc: check this disc's languages or the Settings page, then press Retry."},
	jobs.KindVerifyLossless: {"The rip kept a language's audio but lost its lossless track.",
		"The track selection dropped it, which is a Discern bug: please report it. Retry rips again the same way."},
}

// plainSteps explain a failure named only by its step.
var plainSteps = map[string]plain{
	"scan":          {"Reading the disc failed.", "Check the disc is in and the drive works, then press Retry."},
	"identify":      {"Working out the movie failed.", "Radarr may be unreachable: check it, then press Retry."},
	"select":        {"Choosing the titles failed.", "Press Retry."},
	"check disc":    {"Checking the disc before the rip failed.", "Make sure the drive is closed with the disc in, then press Retry."},
	"clear staging": {"The staging folder couldn't be emptied.", "Check the staging folder's permissions and free space, then press Retry."},
	"rip":           {"The rip failed.", "The details below say why. Press Retry to rip again."},
	"verify":        {"Checking the rip failed.", "The details below say why. Press Retry to check again."},
	"import":        {"Radarr couldn't import the rip.", "Fix what the details below say in Radarr, then press Retry: the rip is kept, and only the import runs again."},
}

// explain says a failed job's failure in plain words. A job that failed
// before failures were recorded gets its note's first line.
func explain(j *jobs.Job) plain {
	if j.Failure != nil {
		if p, ok := plainWords[j.Failure.Kind]; ok {
			return p
		}
		if p, ok := plainSteps[j.Failure.Kind]; ok {
			return p
		}
	}
	first, _, _ := strings.Cut(j.Note, "\n")
	return plain{What: first, Todo: "Press Retry, or Cancel the job."}
}

// diagnostics sends a failed rip's diagnostics bundle as one .zip (decision
// D37). It is behind the login like every page: MakeMKV's debug log names
// the drive and the disc.
func (s *server) diagnostics(w http.ResponseWriter, r *http.Request) {
	j, ok := s.loadJob(w, r)
	if !ok {
		return
	}
	if j.Failure == nil || j.Failure.Diagnostics == "" {
		http.NotFound(w, r)
		return
	}
	dir, ok := s.b.Diagnostics(j.Failure.Diagnostics)
	if !ok {
		http.NotFound(w, r)
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		s.fail(w, err)
		return
	}
	// A bundle with a large debug log can take longer than the server's
	// write timeout over a slow connection; this one response gets longer.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(10 * time.Minute))
	// The name passed the bundle-name check, so it is safe in the header.
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="discern-`+j.Failure.Diagnostics+`.zip"`)
	zw := zip.NewWriter(w)
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		if err := addFile(zw, filepath.Join(dir, e.Name()), e.Name()); err != nil {
			// The headers are sent; a broken zip is all that can be done.
			s.log.Warn("sending diagnostics", "job", j.ID, "err", err)
			return
		}
	}
	if err := zw.Close(); err != nil {
		s.log.Warn("sending diagnostics", "job", j.ID, "err", err)
	}
}

func addFile(zw *zip.Writer, path, name string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	dst, err := zw.Create(name)
	if err != nil {
		return err
	}
	_, err = io.Copy(dst, f)
	return err
}
