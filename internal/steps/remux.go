package steps

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/danvanton/discern/internal/format"
	"github.com/danvanton/discern/internal/jobs"
	"github.com/danvanton/discern/internal/lang"
	"github.com/danvanton/discern/internal/makemkv"
)

// The Backup + remux strategy (DESIGN section 9.3, decisions D11, D64-D70):
// makemkvcon copies the disc, then mkvmerge builds each chosen title's MKV
// from its playlist, skipping MakeMKV's muxer, which is what stalls.

// Backup copies the disc in j's drive into dir with makemkvcon backup.
func (s *Steps) Backup(ctx context.Context, j *jobs.Job, dir string, progress func(float64)) error {
	onProgress := func(p makemkv.Progress) {
		if progress != nil && p.Max > 0 {
			progress(float64(p.Total) / float64(p.Max))
		}
	}
	if _, err := s.runner.Backup(ctx, j.Drive, dir, onProgress); err != nil {
		return s.saveDiagnostics(j, err)
	}
	return nil
}

// Fallback checks that j can take the Backup + remux route: mkvmerge is
// installed, every chosen title comes from a Blu-ray playlist or clip (a
// DVD can't be remuxed: decision D64), and staging has the room (room).
func (s *Steps) Fallback(_ context.Context, j *jobs.Job, dir string) error {
	if s.Mkvmerge == "" {
		return errors.New("mkvmerge isn't installed, so a backup can't be remuxed")
	}
	if j.Disc == nil {
		return errors.New("no scan to back up from")
	}
	for _, idx := range j.Titles {
		if t, ok := title(j.Disc, idx); !ok || !t.BluRaySource() {
			return fmt.Errorf("title %d isn't from a Blu-ray playlist: only Blu-ray and UHD discs can be backed up and remuxed", idx)
		}
	}
	return s.room(j, dir)
}

// room checks that the staging filesystem holding dir has the space a
// backup needs (decision D68): the largest the disc could be, since backup
// copies all of it, plus the chosen titles' MKVs and a margin. It returns
// an error wrapping jobs.ErrNoRoom that says how much is missing.
func (s *Steps) room(j *jobs.Job, dir string) error {
	need := backupNeeds(j)
	read := s.FreeSpace
	if read == nil {
		read = freeSpace
	}
	free, err := read(existing(dir))
	if err != nil {
		return fmt.Errorf("reading staging's free space: %w", err)
	}
	if free < need {
		return fmt.Errorf("%w: it needs about %s free in staging, and %s is free",
			jobs.ErrNoRoom, format.Bytes(need), format.Bytes(free))
	}
	return nil
}

// The space a backup is allowed for (decision D68).
const (
	uhdBound      = 100_000_000_000 // a BD-100, the largest UHD disc
	blurayBound   = 50_000_000_000  // a BD-50
	backupMargin  = 5_000_000_000
	uhdVideoWidth = 3840
)

// backupNeeds is the free space j's backup + remux needs.
func backupNeeds(j *jobs.Job) int64 {
	need := int64(blurayBound + backupMargin)
	if j.Disc == nil {
		return need
	}
	for _, t := range j.Disc.Titles {
		for _, st := range t.Streams {
			if w, _, ok := strings.Cut(st.VideoSize, "x"); ok && w == strconv.Itoa(uhdVideoWidth) {
				need = uhdBound + backupMargin
			}
		}
		if slices.Contains(j.Titles, t.Index) {
			need += t.SizeBytes
		}
	}
	return need
}

// existing returns dir, or its nearest parent that exists: a job's staging
// folder may not be there yet.
func existing(dir string) string {
	for {
		if _, err := os.Stat(dir); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return dir
		}
		dir = parent
	}
}

// Remux builds the MKV of each of j's titles in dir from the backup in
// backupDir, with mkvmerge, keeping the tracks the language rule keeps
// (decision D70). It returns a note for the job when the rule couldn't be
// followed exactly (commentary, forced subtitles), or "".
func (s *Steps) Remux(ctx context.Context, j *jobs.Job, backupDir, dir string, progress func(float64)) (string, error) {
	if s.Mkvmerge == "" {
		return "", errors.New("mkvmerge isn't installed, so the backup can't be remuxed")
	}
	if j.Disc == nil {
		return "", errors.New("no scan to remux from")
	}
	root, err := bdmvRoot(backupDir)
	if err != nil {
		return "", err
	}
	var choice *lang.Choice
	if s.Languages != nil {
		if choice, err = s.Languages(ctx, j); err != nil {
			return "", err
		}
	}
	sizes := titleSizes(j)
	var total, done int64
	for _, t := range j.Titles {
		total += sizes[t]
	}
	for _, idx := range j.Titles {
		t, ok := title(j.Disc, idx)
		if !ok {
			return "", fmt.Errorf("title %d isn't on the scanned disc", idx)
		}
		src, err := sourcePath(root, t)
		if err != nil {
			return "", fmt.Errorf("title %d: %w", idx, err)
		}
		tracks, err := s.identify(ctx, src)
		if err != nil {
			return "", fmt.Errorf("title %d: %w", idx, err)
		}
		audio, subs := chooseTracks(tracks, choice)
		out := filepath.Join(dir, outputName(t))
		size := sizes[idx]
		onProgress := func(p float64) {
			if progress != nil && total > 0 {
				progress((float64(done) + p*float64(size)) / float64(total))
			}
		}
		if err := s.mkvmerge(ctx, src, out, tracks, audio, subs, onProgress); err != nil {
			return "", fmt.Errorf("title %d: %w", idx, err)
		}
		done += size
	}
	return remuxNote(j, choice), nil
}

// remuxNote says when the remux kept more than the language rule asks for
// (decision D70), only where it could matter: forced-only subtitles when
// the chosen titles have subtitles in the chosen languages, and dropped
// commentary when a kept language has more than one audio track (one may
// be commentary). MakeMKV's scan doesn't mark either for Discern.
func remuxNote(j *jobs.Job, c *lang.Choice) string {
	if c == nil || j.Disc == nil {
		return ""
	}
	subs, audio := map[string]int{}, map[string]int{}
	for _, idx := range j.Titles {
		t, ok := title(j.Disc, idx)
		if !ok {
			continue
		}
		for _, st := range t.Streams {
			code := lang.Canonical(st.Lang)
			switch st.Type {
			case "Subtitles":
				subs[code]++
			case "Audio":
				audio[code]++
			}
		}
	}
	forced, commentary := false, false
	for _, l := range canonical(c.Subtitles) {
		forced = forced || (c.ForcedOnly && subs[l] > 0)
	}
	for _, l := range canonical(c.Audio) {
		commentary = commentary || (!c.Commentary && audio[l] > 1)
	}
	switch {
	case forced && commentary:
		return "made by backup + remux, which can't pick forced subtitles or tell commentary apart: whole subtitle tracks and every audio track in your languages were kept"
	case forced:
		return "made by backup + remux, which can't pick forced subtitles: whole subtitle tracks in your languages were kept"
	case commentary:
		return "made by backup + remux, which can't tell commentary apart: every audio track in your languages was kept"
	}
	return ""
}

// bdmvRoot finds the disc folder in a backup: the folder holding BDMV,
// which MakeMKV writes at the top or one level down. Anything else is an
// error: the remux never guesses at what the backup holds.
func bdmvRoot(backupDir string) (string, error) {
	if isDir(filepath.Join(backupDir, "BDMV", "PLAYLIST")) {
		return backupDir, nil
	}
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if e.IsDir() && isDir(filepath.Join(backupDir, e.Name(), "BDMV", "PLAYLIST")) {
			return filepath.Join(backupDir, e.Name()), nil
		}
	}
	return "", fmt.Errorf("the backup in %s has no BDMV folder: only Blu-ray and UHD discs can be remuxed", backupDir)
}

func isDir(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

// sourcePath is where a title's playlist or clip is in the backup. The name
// comes from the disc, so it is checked before it becomes part of a path.
func sourcePath(root string, t makemkv.Title) (string, error) {
	if !t.BluRaySource() {
		return "", fmt.Errorf("its source %q isn't a Blu-ray playlist or clip", t.SourceFile)
	}
	file := strings.ToLower(t.SourceFile)
	sub := "PLAYLIST"
	if strings.HasSuffix(file, ".m2ts") {
		sub = "STREAM"
	}
	p := filepath.Join(root, "BDMV", sub, file)
	if _, err := os.Stat(p); err != nil {
		return "", fmt.Errorf("the backup has no %s: %w", file, err)
	}
	return p, nil
}

// outputName is the remuxed file's name: the one MakeMKV would have given
// it, or "title_tNN.mkv". It comes from the disc, so only a plain file name
// ending in .mkv is used.
func outputName(t makemkv.Title) string {
	name := t.OutputFile
	if name == "" || name != filepath.Base(name) || strings.ContainsAny(name, `/\`) ||
		!strings.HasSuffix(strings.ToLower(name), ".mkv") || len(name) > 200 {
		return fmt.Sprintf("title_t%02d.mkv", t.Index)
	}
	return name
}

// sourceTrack is one track of a playlist or clip, as mkvmerge identifies it.
type sourceTrack struct {
	ID    int
	Type  string // "video", "audio" or "subtitles"
	Codec string
	Lang  string // lang.Canonical
}

// identify reads a playlist's or clip's tracks with "mkvmerge -J".
func (s *Steps) identify(ctx context.Context, src string) ([]sourceTrack, error) {
	out, runErr := exec.CommandContext(ctx, s.Mkvmerge, "-J", src).Output()
	var id struct {
		Container struct {
			Recognized bool `json:"recognized"`
		} `json:"container"`
		Tracks []struct {
			ID         int    `json:"id"`
			Type       string `json:"type"`
			Codec      string `json:"codec"`
			Properties struct {
				Language string `json:"language"`
			} `json:"properties"`
		} `json:"tracks"`
	}
	if err := json.Unmarshal(out, &id); err != nil || !id.Container.Recognized {
		if runErr != nil {
			return nil, fmt.Errorf("mkvmerge couldn't read %s: %w", filepath.Base(src), runErr)
		}
		return nil, fmt.Errorf("mkvmerge doesn't recognise %s", filepath.Base(src))
	}
	var tracks []sourceTrack
	for _, t := range id.Tracks {
		tracks = append(tracks, sourceTrack{ID: t.ID, Type: t.Type, Codec: t.Codec, Lang: lang.Canonical(t.Properties.Language)})
	}
	if !slices.ContainsFunc(tracks, func(t sourceTrack) bool { return t.Type == "video" }) {
		return nil, fmt.Errorf("mkvmerge finds no video in %s", filepath.Base(src))
	}
	return tracks, nil
}

// chooseTracks applies the language rule to mkvmerge's tracks (decision
// D70) and returns the audio and subtitle track ids to keep, the chosen
// languages first in the order they were chosen. With no choice (nil),
// every track is kept. Audio and subtitles are kept in the chosen languages
// and when they have no language; a title with no audio in the chosen
// languages keeps all its audio, as a Direct rip keeps a foreign film's.
func chooseTracks(tracks []sourceTrack, c *lang.Choice) (audio, subs []int) {
	keep := func(kind string, langs []string) []int {
		var ids []int
		for _, t := range tracks {
			if t.Type == kind && (c == nil || lang.None(t.Lang) || slices.Contains(langs, t.Lang)) {
				ids = append(ids, t.ID)
			}
		}
		if c != nil {
			rank := func(id int) int {
				for _, t := range tracks {
					if t.ID == id {
						if i := slices.Index(langs, t.Lang); i >= 0 {
							return i
						}
					}
				}
				return len(langs) // no language: after the chosen ones
			}
			slices.SortStableFunc(ids, func(a, b int) int { return cmp.Compare(rank(a), rank(b)) })
		}
		return ids
	}
	var langsA, langsS []string
	if c != nil {
		langsA, langsS = canonical(c.Audio), canonical(c.Subtitles)
	}
	audio, subs = keep("audio", langsA), keep("subtitles", langsS)
	if len(audio) == 0 {
		for _, t := range tracks {
			if t.Type == "audio" {
				audio = append(audio, t.ID)
			}
		}
	}
	return audio, subs
}

func canonical(codes []string) []string {
	out := make([]string, 0, len(codes))
	for _, c := range codes {
		out = append(out, lang.Canonical(c))
	}
	return out
}

// mkvmerge writes out from src with the chosen audio and subtitle tracks,
// in the chosen order (video first), reporting progress from mkvmerge's
// GUI mode. Exit status 1 is a warning: the file is written.
func (s *Steps) mkvmerge(ctx context.Context, src, out string, tracks []sourceTrack, audio, subs []int, progress func(float64)) error {
	args := []string{"--gui-mode", "-o", out}
	if len(audio) > 0 {
		args = append(args, "--audio-tracks", ids(audio))
	} else {
		args = append(args, "--no-audio")
	}
	if len(subs) > 0 {
		args = append(args, "--subtitle-tracks", ids(subs))
	} else {
		args = append(args, "--no-subtitles")
	}
	var order []string
	for _, t := range tracks {
		if t.Type == "video" {
			order = append(order, "0:"+strconv.Itoa(t.ID))
		}
	}
	for _, id := range slices.Concat(audio, subs) {
		order = append(order, "0:"+strconv.Itoa(id))
	}
	args = append(args, "--track-order", strings.Join(order, ","), src)

	cmd := exec.CommandContext(ctx, s.Mkvmerge, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting mkvmerge: %w", err)
	}
	var last []string // the last lines, for the error
	sc := bufio.NewScanner(stdout)
	for sc.Scan() {
		line := sc.Text()
		if pct, ok := strings.CutPrefix(line, "#GUI#progress "); ok {
			if n, err := strconv.Atoi(strings.TrimSuffix(pct, "%")); err == nil && progress != nil {
				progress(float64(min(max(n, 0), 100)) / 100)
			}
			continue
		}
		last = append(last, line)
		if len(last) > 5 {
			last = last[1:]
		}
	}
	// A line too long for the scanner ends the loop early: read the rest,
	// or mkvmerge blocks on a full pipe and Wait never returns.
	io.Copy(io.Discard, stdout)
	err = cmd.Wait()
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		err = nil // warnings only
	}
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("mkvmerge: %w", ctx.Err())
		}
		return fmt.Errorf("mkvmerge failed (%v): %s", err, strings.Join(last, " / "))
	}
	return nil
}

func ids(list []int) string {
	s := make([]string, len(list))
	for i, id := range list {
		s[i] = strconv.Itoa(id)
	}
	return strings.Join(s, ",")
}
