package lang

import (
	"errors"
	"fmt"
)

// Choice is a choice of tracks by language, turned into MakeMKV's selection
// rule before a rip (makemkv.SelectionRule). The Settings page saves one for
// every disc (settings.Languages); the review page can save one for a single
// disc (decision D34). It lives here, with the codes it holds, so the job
// engine can carry one without depending on the Settings page's package.
type Choice struct {
	Audio      []string `json:"audio"`       // language codes, most preferred first
	Subtitles  []string `json:"subtitles"`   // language codes; none keeps no subtitles
	ForcedOnly bool     `json:"forced_only"` // keep only forced subtitles of those languages
	Commentary bool     `json:"commentary"`  // keep commentary and other special tracks
}

// Validate checks the choice before it is saved: offered codes only, each
// once, and at least one audio language, since a rule that keeps no audio
// would make silent rips.
func (c Choice) Validate() error {
	if len(c.Audio) == 0 {
		return errors.New("choose at least one audio language")
	}
	for _, list := range [][]string{c.Audio, c.Subtitles} {
		seen := map[string]bool{}
		for _, code := range list {
			if !Offered(code) {
				return fmt.Errorf("%q is not a language Discern offers", code)
			}
			if seen[code] {
				return fmt.Errorf("%q is chosen twice", code)
			}
			seen[code] = true
		}
	}
	return nil
}
