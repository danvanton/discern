package steps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"time"
)

// FileTracks counts a Matroska file's tracks by type.
type FileTracks struct {
	Video, Audio, Subtitles int
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
			Type string `json:"type"`
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
		case "subtitles":
			t.Subtitles++
		}
	}
	return t, nil
}
