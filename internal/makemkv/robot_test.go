package makemkv

import (
	"reflect"
	"testing"
)

// Table-driven test: each case is one input line and the record it must
// produce. Adding a case is adding one entry to the table.
func TestParseLine(t *testing.T) {
	cases := []struct {
		name string
		line string
		want Record
	}{
		{"title count", "TCOUNT:4", TitleCount{4}},
		{"disc info", `CINFO:32,0,"SAMPLE_MOVIE"`, DiscInfo{AttrVolumeName, 0, "SAMPLE_MOVIE"}},
		{"title info", `TINFO:0,9,0,"1:58:12"`, TitleInfo{0, AttrDuration, 0, "1:58:12"}},
		{"stream info", `SINFO:0,1,3,0,"eng"`, StreamInfo{0, 1, AttrLangCode, 0, "eng"}},
		{"progress", "PRGV:120,450,65536", Progress{120, 450, 65536}},
		{"progress name, current step", `PRGC:5018,0,"Scanning CD-ROM devices"`, ProgressName{true, 5018, 0, "Scanning CD-ROM devices"}},
		{"progress name, whole job", `PRGT:5018,0,"Opening DVD disc"`, ProgressName{false, 5018, 0, "Opening DVD disc"}},
		{
			"drive",
			`DRV:0,2,999,12,"BD-RE ASUS BW-16D1HT 3.10","SAMPLE_MOVIE","/dev/sr0"`,
			Drive{0, 2, 999, 12, "BD-RE ASUS BW-16D1HT 3.10", "SAMPLE_MOVIE", "/dev/sr0"},
		},
		{"empty drive slot", `DRV:1,256,999,0,"","",""`, Drive{1, 256, 999, 0, "", "", ""}},
		{
			"message with params",
			`MSG:3307,0,2,"File 00012.m2ts was added as title #0","File %1 was added as title #%2","00012.m2ts","0"`,
			Message{3307, 0, "File 00012.m2ts was added as title #0", "File %1 was added as title #%2", []string{"00012.m2ts", "0"}},
		},
		{"message without params", `MSG:5011,0,0,"Done","Done"`, Message{5011, 0, "Done", "Done", nil}},
		{
			"colons inside a message",
			`MSG:3001,0,1,"Saving to: /out/a.mkv","Saving to: %1","/out/a.mkv"`,
			Message{3001, 0, "Saving to: /out/a.mkv", "Saving to: %1", []string{"/out/a.mkv"}},
		},
		{"comma inside quotes", `TINFO:3,2,0,"Short, sweet"`, TitleInfo{3, AttrName, 0, "Short, sweet"}},
		{"escaped quote", `TINFO:3,2,0,"The \"Teaser\""`, TitleInfo{3, AttrName, 0, `The "Teaser"`}},
		{"escaped backslash", `TINFO:3,2,0,"a\\b"`, TitleInfo{3, AttrName, 0, `a\b`}},
		{"windows line ending", "TCOUNT:4\r", TitleCount{4}},
		{"unknown prefix is kept, not an error", "NEWTHING:1,2", Unknown{"NEWTHING", "NEWTHING:1,2"}},
		{
			"unknown prefix is never parsed, even with a stray quote",
			`NEWTHING:1,"a stray " quote`,
			Unknown{"NEWTHING", `NEWTHING:1,"a stray " quote`},
		},
		{
			"plain text without a prefix is kept, not an error",
			"Your MakeMKV registration key has expired",
			Unknown{"", "Your MakeMKV registration key has expired"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseLine(tc.line)
			if err != nil {
				t.Fatalf("ParseLine(%q) error: %v", tc.line, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ParseLine(%q)\n got  %#v\n want %#v", tc.line, got, tc.want)
			}
		})
	}
}

// Malformed known records must be errors, so a scan Discern can't fully read
// is never used to decide what to rip.
func TestParseLineErrors(t *testing.T) {
	bad := []string{
		"TCOUNT:four",                     // not a number
		"TCOUNT:1,2",                      // too many fields
		`TINFO:0,9,"1:58:12"`,             // too few fields
		`TINFO:0,2,0,"unclosed`,           // quote never closed
		`DRV:0,2,999,12,"a","b"`,          // DRV needs 7 fields
		`MSG:5011,0,0,"only one"`,         // MSG needs at least 5
		`SINFO:x,0,1,6201,"Video"`,        // title is not a number
		`MSG:3025,0,3,"text","fmt","one"`, // declares 3 params, has 1
		`MSG:5011,0,0,"text","fmt","extra"`,
		`MSG:5011,0,x,"text","fmt"`, // param count is not a number
	}
	for _, line := range bad {
		if rec, err := ParseLine(line); err == nil {
			t.Errorf("ParseLine(%q) = %#v, want an error", line, rec)
		}
	}
}
