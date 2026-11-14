// Package settings holds what the web UI's Settings page changes (decision
// D21), as opposed to the container's environment variables, which configure
// what Discern needs before it starts.
package settings

import (
	"errors"
	"fmt"
)

// Settings is every setting on the page.
type Settings struct {
	// Eject opens the tray when a job is done (decision D25).
	Eject bool
	// Languages chooses the tracks MakeMKV keeps. Nil means MakeMKV's own
	// default selection, as before any choice was saved (decision D24).
	Languages *Languages
}

// Languages is the user's track choice, turned into MakeMKV's selection rule
// before each rip (makemkv.SelectionRule).
type Languages struct {
	Audio      []string `json:"audio"`       // language codes, most preferred first
	Subtitles  []string `json:"subtitles"`   // language codes; none keeps no subtitles
	ForcedOnly bool     `json:"forced_only"` // keep only forced subtitles of those languages
	Commentary bool     `json:"commentary"`  // keep commentary and other special tracks
}

// Defaults are the settings before anything is saved.
func Defaults() Settings { return Settings{Eject: true} }

// Choice is a language offered on the page.
type Choice struct {
	Code string // ISO 639-2/B, as MakeMKV prints it: "fre", not "fra"
	Name string
}

// Choices are the languages the page offers: English, then the languages
// found on discs, by name. MakeMKV matches ISO 639-2/B and /T codes alike
// ("fre" and "fra"); Discern stores the /B code it shows.
var Choices = []Choice{
	{"eng", "English"},
	{"ara", "Arabic"}, {"bul", "Bulgarian"}, {"cat", "Catalan"}, {"chi", "Chinese"},
	{"hrv", "Croatian"}, {"cze", "Czech"}, {"dan", "Danish"}, {"dut", "Dutch"},
	{"est", "Estonian"}, {"fin", "Finnish"}, {"fre", "French"}, {"ger", "German"},
	{"gre", "Greek"}, {"heb", "Hebrew"}, {"hin", "Hindi"}, {"hun", "Hungarian"},
	{"ice", "Icelandic"}, {"ind", "Indonesian"}, {"ita", "Italian"}, {"jpn", "Japanese"},
	{"kor", "Korean"}, {"lav", "Latvian"}, {"lit", "Lithuanian"}, {"may", "Malay"},
	{"nor", "Norwegian"}, {"per", "Persian"}, {"pol", "Polish"}, {"por", "Portuguese"},
	{"rum", "Romanian"}, {"rus", "Russian"}, {"srp", "Serbian"}, {"slo", "Slovak"},
	{"slv", "Slovenian"}, {"spa", "Spanish"}, {"swe", "Swedish"}, {"tam", "Tamil"},
	{"tel", "Telugu"}, {"tha", "Thai"}, {"tur", "Turkish"}, {"ukr", "Ukrainian"},
	{"vie", "Vietnamese"},
}

var known = func() map[string]bool {
	m := map[string]bool{}
	for _, c := range Choices {
		m[c.Code] = true
	}
	return m
}()

// Validate checks the choice before it is saved: known codes only, each once,
// and at least one audio language, since a rule that keeps no audio would
// make silent rips.
func (l Languages) Validate() error {
	if len(l.Audio) == 0 {
		return errors.New("choose at least one audio language")
	}
	for _, list := range [][]string{l.Audio, l.Subtitles} {
		seen := map[string]bool{}
		for _, c := range list {
			if !known[c] {
				return fmt.Errorf("%q is not a language Discern offers", c)
			}
			if seen[c] {
				return fmt.Errorf("%q is chosen twice", c)
			}
			seen[c] = true
		}
	}
	return nil
}
