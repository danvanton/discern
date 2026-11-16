package app

import (
	"context"
	"fmt"
	"slices"

	"github.com/danvanton/discern/internal/jobs"
	"github.com/danvanton/discern/internal/lang"
	"github.com/danvanton/discern/internal/makemkv"
	"github.com/danvanton/discern/internal/makemkv/install"
	"github.com/danvanton/discern/internal/settings"
)

// selectionSetting is MakeMKV's setting for its track selection rule.
const selectionSetting = "app_DefaultSelectionString"

// ApplyLanguages puts the language choice into MakeMKV's settings before a
// rip (decisions D20, D24, D34). It is Steps.BeforeRip.
//
// The choice is the disc's own, made on the review page, or else the
// Settings page's. With neither it removes Discern's rule, so MakeMKV's own
// default applies. With the Settings page's, a chosen title that has no
// audio in those languages (a foreign film) keeps its own audio as well, for
// this rip only: its languages are added after the user's. A disc's own
// choice is used exactly: the user chose from that disc's languages.
//
// The settings file is shared by every makemkvcon, which is fine with one
// drive; several drives ripping at once (v0.4) will need one per drive.
func (a *App) ApplyLanguages(ctx context.Context, j *jobs.Job) error {
	if a.MakeMKVSettings == "" {
		return nil // development, with the fake makemkvcon
	}
	l, perDisc, err := a.languagesFor(ctx, j)
	if err != nil {
		return err
	}
	rule := ""
	if l != nil {
		audio := l.Audio
		if !perDisc {
			if own := ownAudio(j, audio); len(own) > 0 {
				a.log.Info("a title has no audio in the chosen languages; keeping its own for this rip",
					"job", j.ID, "chosen", audio, "kept", own)
				audio = append(slices.Clone(audio), own...)
			}
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

// languagesFor returns the track choice for j's rip: the disc's own
// (perDisc), or the Settings page's. Nil means MakeMKV's default.
func (a *App) languagesFor(ctx context.Context, j *jobs.Job) (l *settings.Languages, perDisc bool, err error) {
	if j.Languages != nil {
		return j.Languages, true, nil
	}
	st, err := a.store.Settings(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("reading the settings: %w", err)
	}
	return st.Languages, false, nil
}

// LanguagesChosen reports whether j's rip keeps only chosen languages, which
// makes the file smaller than the scan said. It is Steps.Trimmed.
func (a *App) LanguagesChosen(ctx context.Context, j *jobs.Job) (bool, error) {
	if a.MakeMKVSettings == "" {
		return false, nil
	}
	l, _, err := a.languagesFor(ctx, j)
	return l != nil, err
}

// ownAudio returns the audio languages of the job's chosen titles that have
// audio, but none in the chosen languages and none without a language ("und"
// counts as none: the rule's "nolang" keeps those tracks, and "und" is not a
// language to put into the rule). Codes are compared in their canonical form:
// a scan says "fra" where the Settings page says "fre". They come from the
// disc, so a code that isn't three letters is left out rather than passed on:
// one odd code must not fail the rule for the whole rip.
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
			code := lang.Canonical(s.Lang)
			if lang.None(code) || slices.Contains(chosen, code) {
				kept = true
			}
			if lang.WellFormed(code) && !lang.None(code) {
				langs = append(langs, code)
			}
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
