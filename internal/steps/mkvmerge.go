package steps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/danvanton/discern/internal/lang"
)

// FileTracks is what a Matroska file holds: its tracks by type, the
// languages of its audio tracks, and the languages that have a lossless
// audio track. Languages are listed each once, in file order, as
// lang.Canonical codes; audio without a language ("und") isn't listed.
type FileTracks struct {
	Video, Audio, Subtitles int
	AudioLanguages          []string
	LosslessLanguages       []string
}

// clone returns a copy that shares no slice with t, so one caller can't
// change what another is given.
func (t FileTracks) clone() FileTracks {
	t.AudioLanguages = slices.Clone(t.AudioLanguages)
	t.LosslessLanguages = slices.Clone(t.LosslessLanguages)
	return t
}

// lossless reports whether an audio codec name, as MakeMKV's scan or
// mkvmerge gives it, is a lossless one: Dolby TrueHD (with or without
// Atmos), DTS-HD Master Audio (DTS:X too), PCM or FLAC. DTS-HD High
// Resolution and every Dolby Digital form are lossy.
func lossless(codec string) bool {
	c := strings.ToLower(codec)
	for _, name := range []string{"truehd", "master audio", "dts-hd ma", "pcm", "flac", "mlp"} {
		if strings.Contains(c, name) {
			return true
		}
	}
	return false
}

// Mkvmerge returns a track reader for Steps.Tracks that runs "mkvmerge -J"
// (MKVToolNix's identification, as JSON). It reads the file's headers only,
// so it takes a moment even for a 60 GB rip.
func Mkvmerge(bin string) func(ctx context.Context, file string) (FileTracks, error) {
	return func(ctx context.Context, file string) (FileTracks, error) {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		out, runErr := exec.CommandContext(ctx, bin, "-J", file).Output()
		tracks, err := parseIdentification(out)
		if err != nil && runErr != nil {
			// mkvmerge exits 1 for a mere warning and still identifies the
			// file, so its exit status matters only when the output doesn't.
			return FileTracks{}, fmt.Errorf("mkvmerge couldn't read it: %w", runErr)
		}
		return tracks, err
	}
}

// parseIdentification reads mkvmerge's JSON identification. A file mkvmerge
// doesn't recognise as a container it supports is an error.
func parseIdentification(out []byte) (FileTracks, error) {
	var id struct {
		Container struct {
			Recognized bool `json:"recognized"`
			Supported  bool `json:"supported"`
		} `json:"container"`
		Tracks []struct {
			Type       string `json:"type"`
			Codec      string `json:"codec"`
			Properties struct {
				Language string `json:"language"`
			} `json:"properties"`
		} `json:"tracks"`
	}
	if err := json.Unmarshal(out, &id); err != nil {
		return FileTracks{}, fmt.Errorf("mkvmerge's answer isn't readable: %w", err)
	}
	if !id.Container.Recognized || !id.Container.Supported {
		return FileTracks{}, errors.New("mkvmerge doesn't recognise it as a video file")
	}
	var t FileTracks
	for _, tr := range id.Tracks {
		switch tr.Type {
		case "video":
			t.Video++
		case "audio":
			t.Audio++
			code := lang.Canonical(tr.Properties.Language)
			if lang.None(code) {
				continue
			}
			if !slices.Contains(t.AudioLanguages, code) {
				t.AudioLanguages = append(t.AudioLanguages, code)
			}
			if lossless(tr.Codec) && !slices.Contains(t.LosslessLanguages, code) {
				t.LosslessLanguages = append(t.LosslessLanguages, code)
			}
		case "subtitles":
			t.Subtitles++
		}
	}
	return t, nil
}

// Remembered wraps a track reader so that reading the same, unchanged file
// again doesn't start mkvmerge twice: Verify reads a rip, and Import reads it
// again a moment later for its languages. It remembers one file, known by its
// path, size and modification time. Errors are not remembered.
//
// The lock guards only the remembered answer, never a read: mkvmerge can take
// a while on a slow disk, and another drive's job mustn't wait for it, or be
// unable to cancel while it waits. Each caller gets its own copy.
func Remembered(read func(ctx context.Context, file string) (FileTracks, error)) func(ctx context.Context, file string) (FileTracks, error) {
	var (
		mu     sync.Mutex
		key    string
		tracks FileTracks
	)
	return func(ctx context.Context, file string) (FileTracks, error) {
		info, err := os.Stat(file)
		if err != nil {
			return FileTracks{}, err
		}
		k := fmt.Sprintf("%s|%d|%d", file, info.Size(), info.ModTime().UnixNano())
		mu.Lock()
		hit, t := k == key, tracks.clone()
		mu.Unlock()
		if hit {
			return t, nil
		}
		t, err = read(ctx, file)
		if err != nil {
			return FileTracks{}, err
		}
		mu.Lock()
		key, tracks = k, t.clone()
		mu.Unlock()
		return t, nil
	}
}
