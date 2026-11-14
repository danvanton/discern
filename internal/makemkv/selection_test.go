package makemkv

import (
	"strings"
	"testing"
)

func TestSelectionRule(t *testing.T) {
	got, err := SelectionRule([]string{"eng", "fre"}, []string{"eng"}, true, false)
	if err != nil {
		t.Fatal(err)
	}
	want := "-sel:all,+sel:video,+sel:(audio&(eng|fre|nolang|single)),+sel:(subtitle&(eng)&forced)," +
		"-sel:(havemulti|havecore),-sel:mvcvideo,-sel:special,=100:all,-2:eng,-1:fre"
	if got != want {
		t.Errorf("rule =\n  %s\nwant\n  %s", got, want)
	}

	// No subtitles chosen, commentary kept.
	got, _ = SelectionRule([]string{"eng"}, nil, false, true)
	if strings.Contains(got, "subtitle") || strings.Contains(got, "special") {
		t.Errorf("rule = %s; want no subtitle selection and commentary kept", got)
	}
}

func TestSelectionRuleRejectsBadCodes(t *testing.T) {
	for _, audio := range [][]string{nil, {"en"}, {"ENG"}, {"eng)|all"}, {"e,g"}} {
		if _, err := SelectionRule(audio, nil, false, false); err == nil {
			t.Errorf("SelectionRule(%q) was accepted", audio)
		}
	}
	if _, err := SelectionRule([]string{"eng"}, []string{"x\"y"}, false, false); err == nil {
		t.Error("a subtitle code with a quote was accepted")
	}
}
