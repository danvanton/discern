package steps

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/NotNad/trayarr/internal/jobs"
	"github.com/NotNad/trayarr/internal/makemkv"
)

// The size a rip may have, as a share of the size MakeMKV gave the title in
// the scan. An MKV is a little smaller than the disc's transport stream (it
// drops the stream overhead), so a complete rip lands somewhat under 100%. A
// rip that stopped early lands far under: the Kill Bill stall was 640 MiB of
// 73 GB, under 1%. The bounds are deliberately wide until real rips have
// been measured; see the open questions in docs/DESIGN.md.
const (
	minSizeRatio = 0.70
	maxSizeRatio = 1.10
)

// Verify checks the rip before anything is imported: one file per chosen
// title, each a plausible size for that title. It reads only, so it is safe
// to run again.
func (s *Steps) Verify(_ context.Context, j *jobs.Job, dir string) error {
	return VerifyDir(j, dir)
}

// VerifyDir is Verify without the receiver, for tests.
func VerifyDir(j *jobs.Job, dir string) error {
	if j.Disc == nil {
		return fmt.Errorf("no scan to verify against")
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.mkv"))
	if err != nil {
		return err
	}
	if len(files) != len(j.Titles) {
		return fmt.Errorf("found %d files for %d chosen titles", len(files), len(j.Titles))
	}
	for _, idx := range j.Titles {
		t, ok := title(j.Disc, idx)
		if !ok {
			return fmt.Errorf("title %d is not in the scan", idx)
		}
		path, err := fileFor(files, t)
		if err != nil {
			return err
		}
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		if info.Size() == 0 {
			return fmt.Errorf("%s is empty", filepath.Base(path))
		}
		if t.SizeBytes > 0 {
			ratio := float64(info.Size()) / float64(t.SizeBytes)
			if ratio < minSizeRatio || ratio > maxSizeRatio {
				return fmt.Errorf("%s is %.0f%% of the size the scan gave title %d (%d of %d bytes); a complete rip is %.0f-%.0f%%",
					filepath.Base(path), ratio*100, idx, info.Size(), t.SizeBytes, minSizeRatio*100, maxSizeRatio*100)
			}
		}
	}
	return nil
}

func title(d *makemkv.Disc, idx int) (makemkv.Title, bool) {
	for _, t := range d.Titles {
		if t.Index == idx {
			return t, true
		}
	}
	return makemkv.Title{}, false
}

// fileFor finds the rip of title t: the file name MakeMKV announced in the
// scan, or else the "_tNN.mkv" ending MakeMKV gives every title.
func fileFor(files []string, t makemkv.Title) (string, error) {
	suffix := fmt.Sprintf("_t%02d.mkv", t.Index)
	for _, f := range files {
		base := filepath.Base(f)
		if (t.OutputFile != "" && base == t.OutputFile) || strings.HasSuffix(base, suffix) {
			return f, nil
		}
	}
	return "", fmt.Errorf("no file for title %d (expected %q)", t.Index, t.OutputFile)
}
