package makemkv

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var langCode = regexp.MustCompile(`^[a-z]{3}$`)

// SelectionRule builds MakeMKV's track selection rule (the
// app_DefaultSelectionString setting) for a language choice. The grammar is
// MakeMKV's: comma-separated "action:condition" tokens, applied left to right.
//
// Until the user chooses languages, Discern writes no rule and MakeMKV 2.0.0's
// own applies (decision D24). That one is built into makemkvcon, where its
// text was read:
//
//	-sel:all,+sel:(favlang|nolang),-sel:mvcvideo,=100:all,-10:favlang
//
// It is not the rule in MakeMKV's default.mmcp.xml, which adds
// "-sel:(havemulti|havecore)": MakeMKV ignores a profile's selection. Discern
// v0.1.3 copied that clause, and a real rip lost its lossless TrueHD 7.1
// track, keeping only the lossy copy (decision D28).
//
// Discern's rule differs from the built-in one in these ways:
//
//   - Video is always kept; 3D (mvcvideo) is dropped, as MakeMKV's default does.
//   - Audio in the chosen languages is kept, and so is audio with no language
//     or the only audio track, so a disc without the chosen languages still
//     has sound.
//   - Subtitles in the chosen languages are kept, only forced ones if
//     forcedOnly. No subtitle languages keeps no subtitles.
//   - No track is dropped for being a duplicate of another, as in MakeMKV's
//     default: a lossless track and its lossy copy are both kept, and so is a
//     stereo track beside a surround one.
//   - Commentary and other special tracks are dropped unless commentary, in
//     every language. MakeMKV's default keeps them.
//   - Tracks are ordered by the order of audio: MakeMKV's default lowers the
//     weight of favourite languages to put them first, so earlier languages
//     get lower weights. (Inferred from the default rule; check on a rip.)
//
// Codes must be three lowercase letters: they go into MakeMKV's settings.
func SelectionRule(audio, subtitles []string, forcedOnly, commentary bool) (string, error) {
	if len(audio) == 0 {
		return "", fmt.Errorf("no audio language chosen")
	}
	for _, c := range append(append([]string{}, audio...), subtitles...) {
		if !langCode.MatchString(c) {
			return "", fmt.Errorf("%q is not a three-letter language code", c)
		}
	}
	rule := []string{
		"-sel:all",
		"+sel:video",
		"+sel:(audio&(" + strings.Join(audio, "|") + "|nolang|single))",
	}
	if len(subtitles) > 0 {
		cond := "subtitle&(" + strings.Join(subtitles, "|") + ")"
		if forcedOnly {
			cond += "&forced"
		}
		rule = append(rule, "+sel:("+cond+")")
	}
	rule = append(rule, "-sel:mvcvideo")
	if !commentary {
		rule = append(rule, "-sel:special")
	}
	rule = append(rule, "=100:all")
	// One step per rank, so the weights stay well above zero however many
	// languages are chosen.
	for i, c := range audio {
		rule = append(rule, "-"+strconv.Itoa(len(audio)-i)+":"+c)
	}
	return strings.Join(rule, ","), nil
}
