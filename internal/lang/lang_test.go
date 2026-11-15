package lang

import "testing"

func TestCanonical(t *testing.T) {
	for in, want := range map[string]string{
		"fra": "fre", "fre": "fre", "deu": "ger", "nld": "dut", "zho": "chi", // both forms, from a real scan and file
		"eng": "eng", "jpn": "jpn", // one form only
		"FRA": "fre", " deu ": "ger", "": "",
		"scc": "srp", "scr": "hrv", "mol": "rum", // withdrawn in 2008
	} {
		if got := Canonical(in); got != want {
			t.Errorf("Canonical(%q) = %q; want %q", in, got, want)
		}
	}
}

func TestEveryOfferedCodeIsCanonical(t *testing.T) {
	seen := map[string]bool{}
	for _, l := range All {
		if Canonical(l.Code) != l.Code || len(l.Code) != 3 || l.Name == "" || seen[l.Code] {
			t.Errorf("%+v: codes must be unique three-letter /B codes with a name", l)
		}
		seen[l.Code] = true
	}
	if Name("deu") != "German" || Name("ger") != "German" || Name("xxx") != "" {
		t.Errorf("Name: deu=%q ger=%q xxx=%q", Name("deu"), Name("ger"), Name("xxx"))
	}
}

func TestNameKnowsMoreThanThePageOffers(t *testing.T) {
	for code, want := range map[string]string{"ben": "Bengali", "nob": "Norwegian", "mkd": "Macedonian", "yue": "Chinese"} {
		if got := Name(code); got != want || Offered(code) {
			t.Errorf("Name(%q) = %q, offered %v; want %q and not offered", code, got, Offered(code), want)
		}
	}
	if !Offered("fre") || Offered("fra") {
		t.Error("Offered takes the stored /B form only: fre yes, fra no")
	}
}

func TestNone(t *testing.T) {
	for code, want := range map[string]bool{"": true, "und": true, "eng": false, "qaa": false} {
		if None(code) != want {
			t.Errorf("None(%q) = %v; want %v", code, !want, want)
		}
	}
	if Name("scc") != "Serbian" {
		t.Errorf("Name(scc) = %q; want Serbian", Name("scc"))
	}
}

func TestWellFormed(t *testing.T) {
	for code, want := range map[string]bool{"eng": true, "qaa": true, "en": false, "engl": false, "ENG": false, "e,g": false, "": false} {
		if WellFormed(code) != want {
			t.Errorf("WellFormed(%q) = %v; want %v", code, !want, want)
		}
	}
}
