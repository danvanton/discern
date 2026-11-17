package web

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/danvanton/discern/internal/jobs"
	"github.com/danvanton/discern/internal/lang"
	"github.com/danvanton/discern/internal/makemkv"
	"github.com/danvanton/discern/internal/settings"
)

func indexes(rows []titleRow) []int {
	var out []int
	for _, r := range rows {
		out = append(out, r.Index)
	}
	return out
}

// The disc's titles are grouped against the chosen one (decision D32), each
// group longest first.
func TestGroupTitles(t *testing.T) {
	min := func(m int) time.Duration { return time.Duration(m) * time.Minute }
	disc := &makemkv.Disc{Titles: []makemkv.Title{
		{Index: 0, Duration: min(116)},                     // the Director's Edition, chosen
		{Index: 1, Duration: min(113)},                     // the theatrical cut: another cut
		{Index: 2, Duration: min(87)},                      // exactly 75%: still a cut
		{Index: 3, Duration: min(20)},                      // an extra
		{Index: 4, Duration: 2 * time.Minute},              // 2 minutes: an extra
		{Index: 5, Duration: time.Minute + 59*time.Second}, // a clip
		{Index: 6, Duration: 30 * time.Second},             // a clip
		{Index: 7, Duration: min(86)},                      // just under 75%: an extra
	}}
	g := groupTitles(&jobs.Job{Disc: disc, Titles: []int{0}})
	for name, c := range map[string]struct{ got, want []int }{
		"main":   {indexes(g.Main), []int{0}},
		"cuts":   {indexes(g.Cuts), []int{1, 2}},
		"extras": {indexes(g.Extras), []int{7, 3, 4}},
		"clips":  {indexes(g.Clips), []int{5, 6}},
	} {
		if !slices.Equal(c.got, c.want) {
			t.Errorf("%s = %v; want %v", name, c.got, c.want)
		}
	}
	if !g.Main[0].Chosen || g.Cuts[0].Chosen {
		t.Error("only the chosen title is ticked")
	}

	// Nothing chosen: measured against the longest title, which then counts
	// as a possible cut like any other.
	g = groupTitles(&jobs.Job{Disc: disc})
	if len(g.Main) != 0 || !slices.Equal(indexes(g.Cuts), []int{0, 1, 2}) {
		t.Errorf("nothing chosen: main %v, cuts %v", indexes(g.Main), indexes(g.Cuts))
	}
	if g := groupTitles(&jobs.Job{}); g.Main != nil || g.Clips != nil {
		t.Error("no scan, yet groups")
	}
}

// The language panel offers every title's languages that Discern can store,
// in either code form, the chosen titles' first, ticked as Settings have
// them (D34): the choice applies to whichever titles are ticked.
func TestLangChoiceFor(t *testing.T) {
	disc := &makemkv.Disc{Titles: []makemkv.Title{
		// Listed first, but not chosen: its languages come after the chosen title's.
		{Index: 1, Streams: []makemkv.Stream{{Type: "Audio", Lang: "jpn"}}},
		{Index: 0, Streams: []makemkv.Stream{
			{Type: "Audio", Lang: "eng"}, {Type: "Audio", Lang: "deu"}, {Type: "Audio", Lang: "fra"},
			{Type: "Audio", Lang: "ger"}, {Type: "Audio", Lang: "und"}, {Type: "Audio", Lang: "qaa"},
			{Type: "Subtitles", Lang: "eng"}, {Type: "Subtitles", Lang: "nld"},
		}},
	}}
	j := &jobs.Job{Disc: disc, Titles: []int{0}}
	def := &settings.Languages{Audio: []string{"fre", "eng"}, Subtitles: []string{"dut"}, ForcedOnly: true}
	c := langChoiceFor(j, def)
	got := func(boxes []langBox) (codes []string, on []bool) {
		for _, b := range boxes {
			codes, on = append(codes, b.Code), append(on, b.On)
		}
		return
	}
	audio, audioOn := got(c.Audio)
	subs, subsOn := got(c.Subtitles)
	if want := []string{"fre", "eng", "ger", "jpn"}; !slices.Equal(audio, want) || !slices.Equal(audioOn, []bool{true, true, false, false}) {
		t.Errorf("audio = %v ticked %v; want %v, Settings' order first, German and Japanese unticked", audio, audioOn, want)
	}
	if c.On {
		t.Error("the switch is on for a disc without its own choice")
	}
	if !slices.Equal(subs, []string{"eng", "dut"}) || !slices.Equal(subsOn, []bool{false, true}) || !c.ForcedOnly {
		t.Errorf("subtitles = %v ticked %v, forced %v", subs, subsOn, c.ForcedOnly)
	}
	if c.Audio[0].Name != "French" {
		t.Errorf("names: %+v", c.Audio[0])
	}
	if s := c.Summary(); s != "Audio French, English · Subtitles Dutch, forced only" {
		t.Errorf("Summary = %q", s)
	}

	// MakeMKV's default on the Settings page: all the disc's audio ticked.
	c = langChoiceFor(j, nil)
	if _, on := got(c.Audio); !slices.Equal(on, []bool{true, true, true, true}) || c.Summary() != "All tracks: MakeMKV's default selection" {
		t.Errorf("default: ticked %v, summary %q", on, c.Summary())
	}

	// The disc's own choice, saved before a Retry or sent with a refused
	// approval, comes back with the switch on and its own ticks.
	j.Languages = &lang.Choice{Audio: []string{"jpn"}, Commentary: true}
	c = langChoiceFor(j, def)
	audio, audioOn = got(c.Audio)
	if !c.On || !slices.Equal(audio, []string{"jpn", "eng", "ger", "fre"}) || !slices.Equal(audioOn, []bool{true, false, false, false}) ||
		c.ForcedOnly || !c.Commentary {
		t.Errorf("own choice: on %v, audio %v ticked %v, forced %v, commentary %v", c.On, audio, audioOn, c.ForcedOnly, c.Commentary)
	}
	if s := (langChoice{Unreadable: true}).Summary(); !strings.Contains(s, "can't be read") {
		t.Errorf("unreadable Settings: %q", s)
	}
	if s := (langChoice{Default: &settings.Languages{Audio: []string{"eng"}, Commentary: true}}).Summary(); s != "Audio English · no subtitles · commentary kept" {
		t.Errorf("Summary = %q", s)
	}
}

// The review page says what auto-import made of the disc: a trial's
// "would have", or why it didn't act (decisions D60-D62).
func TestViewDecisionSaysWhatAutoImportDid(t *testing.T) {
	if v := viewDecision(&jobs.Selection{Confidence: jobs.High}); v.Auto != "" {
		t.Errorf("auto-import off: %q", v.Auto)
	}
	if v := viewDecision(&jobs.Selection{Confidence: jobs.High, Trial: true}); !strings.HasPrefix(v.Auto, "Trial: Discern would have ripped") {
		t.Errorf("trial: %q", v.Auto)
	}
	v := viewDecision(&jobs.Selection{Confidence: jobs.High, Wait: "Radarr already has Alien (1979)"})
	if v.Auto != "Not ripped on its own: Radarr already has Alien (1979)." {
		t.Errorf("wait: %q", v.Auto)
	}
}
