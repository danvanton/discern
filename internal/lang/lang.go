// Package lang knows the language codes on discs and in files.
//
// ISO 639-2 gives some languages two three-letter codes: a bibliographic one
// (/B: "fre", "ger", "dut") and a terminology one (/T: "fra", "deu", "nld").
// Both turn up for the same disc: MakeMKV's scan printed /T codes for a real
// UHD disc (deu, fra, nld), and the Matroska file it wrote carries /B codes
// (ger, fre, dut). Discern stores and compares the /B form, through Canonical.
package lang

import "strings"

// Language is a language Discern can name.
type Language struct {
	Code string // ISO 639-2/B
	Name string // in English, as Radarr names it too
}

// All are the languages the Settings page offers: English, then the
// languages found on discs, by name. Name knows more than these.
var All = []Language{
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

// bibliographic maps each ISO 639-2/T code that differs from its /B code.
// These twenty are all there are.
var bibliographic = map[string]string{
	"bod": "tib", "ces": "cze", "cym": "wel", "deu": "ger", "ell": "gre",
	"eus": "baq", "fas": "per", "fra": "fre", "hye": "arm", "isl": "ice",
	"kat": "geo", "mkd": "mac", "mri": "mao", "msa": "may", "mya": "bur",
	"nld": "dut", "ron": "rum", "slk": "slo", "sqi": "alb", "zho": "chi",
}

// withdrawn maps the codes ISO 639-2 withdrew in 2008 to the ones that
// replaced them. A disc pressed before then may still carry one.
var withdrawn = map[string]string{
	"scc": "srp", // Serbian
	"scr": "hrv", // Croatian
	"mol": "rum", // Moldavian, now counted as Romanian
}

// named are languages Discern doesn't offer but can still name when a file
// has them, so Radarr is told the whole list. The names are Radarr's own
// (its Language.cs, v6.4.4); nob, nno, yue and cmn are counted as the
// language Radarr files them under.
var named = map[string]string{
	"afr": "Afrikaans", "alb": "Albanian", "ben": "Bengali", "bos": "Bosnian",
	"geo": "Georgian", "kan": "Kannada", "mac": "Macedonian", "mal": "Malayalam",
	"mar": "Marathi", "mon": "Mongolian", "roh": "Romansh", "tgl": "Tagalog",
	"urd": "Urdu",
	"nob": "Norwegian", "nno": "Norwegian", "yue": "Chinese", "cmn": "Chinese",
}

// Canonical returns the /B form of a code, lowercased: "fra" and "FRE" both
// give "fre". A withdrawn code gives its replacement. A code that has only
// one form comes back as it is.
func Canonical(code string) string {
	code = strings.ToLower(strings.TrimSpace(code))
	if b, ok := bibliographic[code]; ok {
		return b
	}
	if c, ok := withdrawn[code]; ok {
		return c
	}
	return code
}

// None reports whether code says a track has no language: no code at all, or
// "und" (undetermined), which is how Matroska files and some discs say it.
// It expects a Canonical code.
func None(code string) bool {
	return code == "" || code == "und"
}

// Name returns the English name of a language Discern knows, or "".
func Name(code string) string {
	code = Canonical(code)
	for _, l := range All {
		if l.Code == code {
			return l.Name
		}
	}
	return named[code]
}

// Offered reports whether code is one the Settings page offers, in the /B
// form it stores.
func Offered(code string) bool {
	for _, l := range All {
		if l.Code == code {
			return true
		}
	}
	return false
}

// WellFormed reports whether code looks like an ISO 639-2 code: three
// lowercase letters. Codes come from discs, so they are checked before use.
func WellFormed(code string) bool {
	if len(code) != 3 {
		return false
	}
	for _, c := range code {
		if c < 'a' || c > 'z' {
			return false
		}
	}
	return true
}
