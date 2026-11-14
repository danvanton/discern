package settings

import (
	"strings"
	"testing"
)

func TestLanguagesValidate(t *testing.T) {
	if err := (Languages{Audio: []string{"eng", "fre"}, Subtitles: []string{"eng"}}).Validate(); err != nil {
		t.Fatalf("a valid choice: %v", err)
	}
	for want, l := range map[string]Languages{
		"at least one audio": {Subtitles: []string{"eng"}},
		"not a language":     {Audio: []string{"klingon"}},
		"chosen twice":       {Audio: []string{"eng", "eng"}},
	} {
		if err := l.Validate(); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%+v: err = %v; want one containing %q", l, err, want)
		}
	}
}
