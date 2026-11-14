package steps

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/danvanton/discern/internal/jobs"
	"github.com/danvanton/discern/internal/makemkv"
)

// The size a rip may have, as a share of the size MakeMKV gave the title in
// the scan. An MKV is a little smaller than the disc's transport stream (it
// drops the stream overhead), so a complete rip lands somewhat under 100%. A
// rip that stopped early lands far under: the Kill Bill stall was 640 MiB of
// 73 GB, under 1%. The bounds are deliberately wide until real rips have
// been measured; see the open questions in docs/DESIGN.md.
//
// When only chosen languages are kept, the file is smaller by the tracks left
// out, which the scan's size still counts: several lossless audio tracks can
// be half a Blu-ray title. The floor is then much lower, and the track check
// below is what catches a file without sound.
const (
	minSizeRatio        = 0.70
	minTrimmedSizeRatio = 0.30
	maxSizeRatio        = 1.10
)

// Verify checks the rip before anything is imported: one file per chosen
// title, each a plausible size for that title, and, where mkvmerge is
// available (Steps.Tracks), a Matroska file with video and with audio if the
// title has any. It reads only, so it is safe to run again.
func (s *Steps) Verify(ctx context.Context, j *jobs.Job, dir string) error {
	floor := minSizeRatio
	if s.Trimmed != nil {
		trimmed, err := s.Trimmed(ctx)
		if err != nil {
			return fmt.Errorf("reading the language settings: %w", err)
		}
		if trimmed {
			floor = minTrimmedSizeRatio
		}
	}
	return verifyDir(ctx, j, dir, floor, s.Tracks)
}

// VerifyDir is Verify with the default size floor and no track check, for
// tests.
func VerifyDir(j *jobs.Job, dir string) error {
	return verifyDir(context.Background(), j, dir, minSizeRatio, nil)
}

func verifyDir(ctx context.Context, j *jobs.Job, dir string, floor float64, tracks func(context.Context, string) (FileTracks, error)) error {
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
			if ratio < floor || ratio > maxSizeRatio {
				return fmt.Errorf("%s is %.0f%% of the size the scan gave title %d (%d of %d bytes); a complete rip is %.0f-%.0f%%",
					filepath.Base(path), ratio*100, idx, info.Size(), t.SizeBytes, floor*100, maxSizeRatio*100)
			}
		}
		if tracks == nil {
			continue
		}
		got, err := tracks(ctx, path)
		if err != nil {
			return fmt.Errorf("%s: %w", filepath.Base(path), err)
		}
		if got.Video == 0 {
			return fmt.Errorf("%s has no video track", filepath.Base(path))
		}
		if got.Audio == 0 && hasAudio(t) {
			return fmt.Errorf("%s has no audio track, though title %d has audio on the disc: check the languages in Settings",
				filepath.Base(path), idx)
		}
	}
	return nil
}

func hasAudio(t makemkv.Title) bool {
	for _, s := range t.Streams {
		if s.Type == "Audio" {
			return true
		}
	}
	return false
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
