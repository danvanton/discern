package app

import (
	"context"
	"fmt"
	"slices"

	"github.com/danvanton/discern/internal/jobs"
	"github.com/danvanton/discern/internal/makemkv"
	"github.com/danvanton/discern/internal/makemkv/install"
)

// selectionSetting is MakeMKV's setting for its track selection rule.
const selectionSetting = "app_DefaultSelectionString"

// ApplyLanguages puts the saved language choice into MakeMKV's settings
// before a rip (decisions D20, D24). It is Steps.BeforeRip.
//
// With no choice saved it removes Discern's rule, so MakeMKV's own default
// applies. With one, a chosen title that has no audio in those languages (a
// foreign film) keeps its own audio as well, for this rip only: its languages
// are added after the user's. The per-disc choice on the review page will
// replace that.
//
// The settings file is shared by every makemkvcon, which is fine with one
// drive; several drives ripping at once (v0.4) will need one per drive.
func (a *App) ApplyLanguages(ctx context.Context, j *jobs.Job) error {
	if a.MakeMKVSettings == "" {
		return nil // development, with the fake makemkvcon
	}
	st, err := a.store.Settings(ctx)
	if err != nil {
		return fmt.Errorf("reading the settings: %w", err)
	}
	rule := ""
	if l := st.Languages; l != nil {
		audio := l.Audio
		if own := ownAudio(j, audio); len(own) > 0 {
			a.log.Info("a title has no audio in the chosen languages; keeping its own for this rip",
				"job", j.ID, "chosen", audio, "kept", own)
			audio = append(slices.Clone(audio), own...)
		}
		if rule, err = makemkv.SelectionRule(audio, l.Subtitles, l.ForcedOnly, l.Commentary); err != nil {
			return err
		}
	}
	if _, err := install.WriteSetting(a.MakeMKVSettings, selectionSetting, rule); err != nil {
		return fmt.Errorf("writing MakeMKV's track selection: %w", err)
	}
	return nil
}

// LanguagesChosen reports whether rips keep only chosen languages, which
// makes the files smaller than the scan said. It is Steps.Trimmed.
func (a *App) LanguagesChosen(ctx context.Context) (bool, error) {
	if a.MakeMKVSettings == "" {
		return false, nil
	}
	st, err := a.store.Settings(ctx)
	return st.Languages != nil, err
}

// ownAudio returns the audio languages of the job's chosen titles that have
// audio, but none in the chosen languages and none without a language. The
// codes come from the disc; SelectionRule checks them before they are used.
func ownAudio(j *jobs.Job, chosen []string) []string {
	if j.Disc == nil {
		return nil
	}
	var own []string
	for _, t := range j.Disc.Titles {
		if !slices.Contains(j.Titles, t.Index) {
			continue
		}
		kept := false
		var langs []string
		for _, s := range t.Streams {
			if s.Type != "Audio" {
				continue
			}
			if s.Lang == "" || slices.Contains(chosen, s.Lang) {
				kept = true
			}
			langs = append(langs, s.Lang)
		}
		if kept {
			continue
		}
		for _, l := range langs {
			if !slices.Contains(own, l) {
				own = append(own, l)
			}
		}
	}
	return own
}
