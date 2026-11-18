package steps

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/danvanton/discern/internal/jobs"
	"github.com/danvanton/discern/internal/lang"
	"github.com/danvanton/discern/internal/makemkv"
	"github.com/danvanton/discern/internal/makemkv/fake"
)

// The fake's playlist tracks: video 0; audio 1 (TrueHD eng), 2 (AC-3 eng),
// 3 (AC-3 fre); subtitles 4 (eng), 5 (fre).
var fakeTracks = []sourceTrack{
	{0, "video", "HEVC", "und"}, {1, "audio", "TrueHD", "eng"}, {2, "audio", "AC-3", "eng"},
	{3, "audio", "AC-3", "fre"}, {4, "subtitles", "PGS", "eng"}, {5, "subtitles", "PGS", "fre"},
}

// The language rule on mkvmerge's tracks (decision D70).
func TestChooseTracks(t *testing.T) {
	for _, c := range []struct {
		name        string
		choice      *lang.Choice
		audio, subs []int
	}{
		{"no choice keeps everything", nil, []int{1, 2, 3}, []int{4, 5}},
		{"English", &lang.Choice{Audio: []string{"eng"}, Subtitles: []string{"eng"}}, []int{1, 2}, []int{4}},
		{"French first, codes in either form", &lang.Choice{Audio: []string{"fra", "eng"}, Subtitles: []string{"fre"}}, []int{3, 1, 2}, []int{5}},
		{"no subtitles", &lang.Choice{Audio: []string{"eng"}}, []int{1, 2}, nil},
		{"no audio in the chosen languages keeps its own", &lang.Choice{Audio: []string{"jpn"}, Subtitles: []string{"eng"}}, []int{1, 2, 3}, []int{4}},
	} {
		audio, subs := chooseTracks(fakeTracks, c.choice)
		if !slices.Equal(audio, c.audio) || !slices.Equal(subs, c.subs) {
			t.Errorf("%s: audio %v, subtitles %v; want %v, %v", c.name, audio, subs, c.audio, c.subs)
		}
	}
	// A track with no language is kept with the chosen ones, after them.
	und := append(slices.Clone(fakeTracks), sourceTrack{6, "audio", "LPCM", "und"})
	if audio, _ := chooseTracks(und, &lang.Choice{Audio: []string{"fre"}}); !slices.Equal(audio, []int{3, 6}) {
		t.Errorf("no-language audio: %v; want [3 6]", audio)
	}
}

// fakeBackup lays out a backup as makemkvcon writes it: BDMV with a
// playlist and a stream file of size bytes.
func fakeBackup(t *testing.T, size int) string {
	t.Helper()
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "BDMV", "PLAYLIST"), 0o755)
	os.MkdirAll(filepath.Join(dir, "BDMV", "STREAM"), 0o755)
	os.WriteFile(filepath.Join(dir, "BDMV", "PLAYLIST", "00800.mpls"), []byte("MPLS0300"), 0o644)
	os.WriteFile(filepath.Join(dir, "BDMV", "STREAM", "00000.m2ts"), make([]byte, size), 0o644)
	return dir
}

// Remux builds each title's MKV with mkvmerge (the test binary as a fake),
// asking for the tracks the rule keeps, in order, and reports progress.
func TestRemux(t *testing.T) {
	exe, _ := os.Executable()
	log := filepath.Join(t.TempDir(), "args.log")
	t.Setenv(fake.MkvmergeEnvVar, log)
	s := &Steps{Mkvmerge: exe, Languages: func(context.Context, *jobs.Job) (*lang.Choice, error) {
		return &lang.Choice{Audio: []string{"eng"}, Subtitles: []string{"eng"}, Commentary: true}, nil
	}}
	backup := fakeBackup(t, 4096)
	out := t.TempDir()
	j := &jobs.Job{Titles: []int{0}, Disc: titles(makemkv.Title{Index: 0, SourceFile: "00800.mpls", SizeBytes: 4096, OutputFile: "Movie_t00.mkv"})}
	var last float64
	note, err := s.Remux(ctx, j, backup, out, func(p float64) { last = p })
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(out, "Movie_t00.mkv")); err != nil || info.Size() != 4096 {
		t.Fatalf("remuxed file: %v, %v", info, err)
	}
	if note != "" || last != 1 {
		t.Errorf("note %q, last progress %v", note, last)
	}
	// Video and the first audio track are default, the rest not, so no
	// subtitles turn on by themselves (decision D73).
	b, _ := os.ReadFile(log)
	want := "--audio-tracks 1,2 --subtitle-tracks 4" +
		" --default-track-flag 0:1 --default-track-flag 1:1 --default-track-flag 2:0 --default-track-flag 4:0" +
		" --track-order 0:0,0:1,0:2,0:4"
	if args := string(b); !strings.Contains(args, want) {
		t.Errorf("mkvmerge was asked for:\n%s", args)
	}

	// Forced-only can't be honoured: the note says so when the title has
	// subtitles in the chosen languages, and not when it has none.
	s.Languages = func(context.Context, *jobs.Job) (*lang.Choice, error) {
		return &lang.Choice{Audio: []string{"eng"}, Subtitles: []string{"eng"}, ForcedOnly: true, Commentary: true}, nil
	}
	if note, err := s.Remux(ctx, j, backup, t.TempDir(), nil); err != nil || note != "" {
		t.Errorf("forced only, no subtitles on the title: note %q, %v", note, err)
	}
	j.Disc.Titles[0].Streams = []makemkv.Stream{{Type: "Subtitles", Lang: "eng"}}
	if note, err := s.Remux(ctx, j, backup, t.TempDir(), nil); err != nil || !strings.Contains(note, "can't pick forced subtitles") {
		t.Errorf("forced only: note %q, %v", note, err)
	}
}

// The commentary note only where a kept language has more than one audio
// track: one of them may be commentary.
func TestRemuxNote(t *testing.T) {
	c := &lang.Choice{Audio: []string{"eng"}, Subtitles: []string{"eng"}}
	one := &jobs.Job{Titles: []int{0}, Disc: titles(makemkv.Title{Index: 0, Streams: []makemkv.Stream{{Type: "Audio", Lang: "eng"}, {Type: "Audio", Lang: "fra"}}})}
	if note := remuxNote(one, c); note != "" {
		t.Errorf("one English track: %q", note)
	}
	two := &jobs.Job{Titles: []int{0}, Disc: titles(makemkv.Title{Index: 0, Streams: []makemkv.Stream{{Type: "Audio", Lang: "eng"}, {Type: "Audio", Lang: "eng"}}})}
	if note := remuxNote(two, c); !strings.Contains(note, "can't tell commentary apart") {
		t.Errorf("two English tracks: %q", note)
	}
	if note := remuxNote(two, &lang.Choice{Audio: []string{"eng"}, Commentary: true}); note != "" {
		t.Errorf("commentary kept anyway: %q", note)
	}

	// No subtitle turns on by itself (decision D73): said whenever the remux
	// keeps one, with or without a language rule; not when it keeps none.
	subbed := &jobs.Job{Titles: []int{0}, Disc: titles(makemkv.Title{Index: 0, Streams: []makemkv.Stream{{Type: "Audio", Lang: "eng"}, {Type: "Subtitles", Lang: "eng"}}})}
	for _, c := range []*lang.Choice{c, nil} {
		if note := remuxNote(subbed, c); !strings.Contains(note, "no subtitles turn on by themselves") {
			t.Errorf("subtitles kept (choice %v): %q", c, note)
		}
	}
	if note := remuxNote(subbed, &lang.Choice{Audio: []string{"eng"}}); note != "" {
		t.Errorf("no subtitles kept: %q", note)
	}
}

// Only the first video track is default, even when mkvmerge lists more.
func TestRemuxMarksOneVideoDefault(t *testing.T) {
	exe, _ := os.Executable()
	log := filepath.Join(t.TempDir(), "args.log")
	t.Setenv(fake.MkvmergeEnvVar, log)
	s := &Steps{Mkvmerge: exe}
	src := filepath.Join(fakeBackup(t, 10), "BDMV", "PLAYLIST", "00800.mpls")
	tracks := []sourceTrack{{ID: 0, Type: "video"}, {ID: 1, Type: "video"}, {ID: 2, Type: "audio"}}
	if err := s.mkvmerge(ctx, src, filepath.Join(t.TempDir(), "out.mkv"), tracks, []int{2}, nil, nil); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(log)
	if want := "--default-track-flag 0:1 --default-track-flag 1:0 --default-track-flag 2:1 --track-order 0:0,0:1,0:2"; !strings.Contains(string(b), want) {
		t.Errorf("mkvmerge was asked for:\n%s", b)
	}
}

// The fake mkvmerge refuses an option or a value the real one wouldn't
// take, so the remux's arguments are checked by every test that runs it.
func TestFakeMkvmergeRefusesUnknownOptions(t *testing.T) {
	src := filepath.Join(fakeBackup(t, 10), "BDMV", "PLAYLIST", "00800.mpls")
	out := filepath.Join(t.TempDir(), "out.mkv")
	for _, bad := range [][]string{
		{"--gui-mode", "-o", out, "--default-track", "0:1", src},
		{"--gui-mode", "-o", out, "--default-track-flag", "0:yes", src},
	} {
		if code := fake.Mkvmerge(bad, io.Discard, io.Discard); code != 2 {
			t.Errorf("%q: exit %d, want 2", bad, code)
		}
	}
	if code := fake.Mkvmerge([]string{"--gui-mode", "-o", out, "--default-track-flag", "0:1", src}, io.Discard, io.Discard); code != 0 {
		t.Errorf("a good command: exit %d", code)
	}
}

// Every doubt is an error: no mkvmerge, no BDMV (a DVD), a source name that
// isn't a Blu-ray file, a playlist missing from the backup.
func TestRemuxFailsClosed(t *testing.T) {
	exe, _ := os.Executable()
	t.Setenv(fake.MkvmergeEnvVar, "on")
	j := func(source string) *jobs.Job {
		return &jobs.Job{Titles: []int{0}, Disc: titles(makemkv.Title{Index: 0, SourceFile: source, SizeBytes: 10})}
	}
	backup := fakeBackup(t, 10)
	for name, c := range map[string]struct {
		s      *Steps
		job    *jobs.Job
		backup string
		want   string
	}{
		"no mkvmerge":      {&Steps{}, j("00800.mpls"), backup, "mkvmerge isn't installed"},
		"no BDMV":          {&Steps{Mkvmerge: exe}, j("00800.mpls"), t.TempDir(), "no BDMV folder"},
		"a DVD's source":   {&Steps{Mkvmerge: exe}, j("VTS_01_1.VOB"), backup, "isn't a Blu-ray playlist or clip"},
		"a path traversal": {&Steps{Mkvmerge: exe}, j("../../etc.mpls"), backup, "isn't a Blu-ray playlist or clip"},
		"missing playlist": {&Steps{Mkvmerge: exe}, j("00801.mpls"), backup, "the backup has no 00801.mpls"},
	} {
		if _, err := c.s.Remux(ctx, c.job, c.backup, t.TempDir(), nil); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v; want %q", name, err, c.want)
		}
	}
}

// The space a backup is allowed (decision D68): the whole disc, 100 GB for
// a UHD and 50 GB otherwise, plus the chosen titles and a margin.
func TestBackupNeeds(t *testing.T) {
	uhd := &jobs.Job{Titles: []int{1}, Disc: titles(
		makemkv.Title{Index: 0, SizeBytes: 1e9, Streams: []makemkv.Stream{{Type: "Video", VideoSize: "1920x1080"}}},
		makemkv.Title{Index: 1, SizeBytes: 60e9, Streams: []makemkv.Stream{{Type: "Video", VideoSize: "3840x2160"}}})}
	if got := backupNeeds(uhd); got != 100e9+5e9+60e9 {
		t.Errorf("UHD: %d", got)
	}
	bd := &jobs.Job{Titles: []int{0}, Disc: titles(makemkv.Title{Index: 0, SizeBytes: 30e9, Streams: []makemkv.Stream{{Type: "Video", VideoSize: "1920x1080"}}})}
	if got := backupNeeds(bd); got != 50e9+5e9+30e9 {
		t.Errorf("Blu-ray: %d", got)
	}
}

// Fallback needs mkvmerge and a Blu-ray, and says how much room is missing
// when staging can't hold the backup.
func TestFallback(t *testing.T) {
	exe, _ := os.Executable()
	s := &Steps{Mkvmerge: exe}
	if err := (&Steps{}).Fallback(ctx, &jobs.Job{}, t.TempDir()); err == nil || !strings.Contains(err.Error(), "mkvmerge isn't installed") {
		t.Errorf("no mkvmerge: %v", err)
	}
	dvd := &jobs.Job{Titles: []int{0}, Disc: titles(makemkv.Title{Index: 0, SourceFile: "VTS_01_1.VOB", SizeBytes: 1})}
	if err := s.Fallback(ctx, dvd, t.TempDir()); err == nil || !strings.Contains(err.Error(), "only Blu-ray and UHD") {
		t.Errorf("a DVD: %v", err)
	}
	small := &jobs.Job{Titles: []int{0}, Disc: titles(makemkv.Title{Index: 0, SourceFile: "00800.mpls", SizeBytes: 1})}
	huge := &jobs.Job{Titles: []int{0}, Disc: titles(makemkv.Title{Index: 0, SourceFile: "00800.MPLS", SizeBytes: 1 << 60})}
	dir := filepath.Join(t.TempDir(), "42") // not created yet
	if _, err := freeSpace(existing(dir)); err != nil {
		t.Fatalf("reading the free space: %v", err)
	}
	s.FreeSpace = func(string) (int64, error) { return 60e9, nil } // a Blu-ray: 55 GB + the title
	if err := s.Fallback(ctx, small, dir); err != nil {
		t.Errorf("a small disc: %v", err)
	}
	s.FreeSpace = nil // the system's
	if err := s.Fallback(ctx, huge, dir); !errors.Is(err, jobs.ErrNoRoom) || !strings.Contains(err.Error(), "free in staging") {
		t.Errorf("a huge disc: %v", err)
	}
}
