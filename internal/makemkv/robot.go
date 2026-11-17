// Package makemkv runs makemkvcon and turns its robot-mode output into Go
// values. It is the only package in Discern that knows MakeMKV exists: the
// rest of the program sees Disc, Title and Stream, never raw output lines.
package makemkv

import (
	"fmt"
	"strconv"
	"strings"
)

// Robot mode (makemkvcon -r) prints one record per line:
//
//	PREFIX:field,field,field
//
// Numbers are bare. Strings are in double quotes, and a backslash escapes the
// next character. Each prefix has a fixed list of fields:
//
//	MSG:code,flags,count,"text","format","param",...   a message
//	DRV:index,visible,enabled,flags,"drive","disc","device"
//	TCOUNT:count                                       number of titles
//	CINFO:attr,code,"value"                            disc attribute
//	TINFO:title,attr,code,"value"                      title attribute
//	SINFO:title,stream,attr,code,"value"               stream attribute
//	PRGT:code,id,"name"                                name of the whole job
//	PRGC:code,id,"name"                                name of the current step
//	PRGV:current,total,max                             progress counters

// Record is one parsed line of robot output. The concrete types below each
// implement it; a type switch tells them apart.
type Record interface{ isRecord() }

// Message is a MSG line: something MakeMKV wants to tell the user. Code
// identifies the message (for example 5010 "Failed to open disc"), so errors
// are recognised by number, never by scraping the English text.
type Message struct {
	Code   int
	Flags  int
	Text   string   // the message as MakeMKV would print it
	Format string   // the same message with %1, %2... placeholders
	Params []string // the values that fill the placeholders
}

// Drive is a DRV line: one drive slot. Empty slots have an empty Device.
type Drive struct {
	Index    int
	Visible  int
	Enabled  int
	Flags    int
	Name     string // drive model, e.g. "BD-RE ASUS BW-16D1HT 3.10"
	DiscName string // label of the disc in it, if any
	Device   string // e.g. "/dev/sr0"
}

// TitleCount is a TCOUNT line.
type TitleCount struct{ Count int }

// DiscInfo is a CINFO line: one attribute of the whole disc.
type DiscInfo struct {
	Attr  Attr
	Code  int
	Value string
}

// TitleInfo is a TINFO line: one attribute of one title.
type TitleInfo struct {
	Title int
	Attr  Attr
	Code  int
	Value string
}

// StreamInfo is an SINFO line: one attribute of one stream of one title.
type StreamInfo struct {
	Title  int
	Stream int
	Attr   Attr
	Code   int
	Value  string
}

// ProgressName is a PRGT (whole job) or PRGC (current step) line.
type ProgressName struct {
	Current bool // true for PRGC, false for PRGT
	Code    int
	ID      int
	Name    string
}

// Progress is a PRGV line. Current and Total are both measured against Max:
// Current/Max is the current step's progress, Total/Max the whole job's.
type Progress struct {
	Current int
	Total   int
	Max     int
}

// Unknown is a line this parser does not know: an unknown prefix, or a plain
// text line with no prefix at all (Prefix is then empty). It is returned, not
// treated as an error, so a newer MakeMKV that adds a record type, or prints
// a human-readable notice such as an expired key, does not break Discern.
// Its fields are never parsed.
type Unknown struct {
	Prefix string
	Line   string
}

func (Message) isRecord()      {}
func (Drive) isRecord()        {}
func (TitleCount) isRecord()   {}
func (DiscInfo) isRecord()     {}
func (TitleInfo) isRecord()    {}
func (StreamInfo) isRecord()   {}
func (ProgressName) isRecord() {}
func (Progress) isRecord()     {}
func (Unknown) isRecord()      {}

// knownPrefixes are the record types build() understands. Anything else is
// returned as Unknown without looking at its fields.
var knownPrefixes = map[string]bool{
	"MSG": true, "DRV": true, "TCOUNT": true, "CINFO": true, "TINFO": true,
	"SINFO": true, "PRGT": true, "PRGC": true, "PRGV": true,
}

// ParseLine parses one line of robot output. A known prefix with malformed
// fields is an error. An unknown prefix, or a line with no prefix, is
// returned as Unknown.
func ParseLine(line string) (Record, error) {
	line = strings.TrimRight(line, "\r\n")
	prefix, rest, ok := strings.Cut(line, ":")
	if !ok || !knownPrefixes[prefix] {
		// Plain text can hold a colon too (`Backup source must start with
		// "disc:"`), so only capital letters before it make a record prefix.
		if !ok || !isPrefix(prefix) {
			prefix = ""
		}
		return Unknown{Prefix: prefix, Line: line}, nil
	}
	f, err := splitFields(rest)
	if err != nil {
		return nil, fmt.Errorf("%s line %q: %w", prefix, line, err)
	}
	rec, err := build(prefix, line, f)
	if err != nil {
		return nil, fmt.Errorf("%s line %q: %w", prefix, line, err)
	}
	return rec, nil
}

// isPrefix reports whether s looks like a robot record type: capital letters
// only, as MSG, DRV and TINFO are.
func isPrefix(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return true
}

func build(prefix, line string, f fields) (Record, error) {
	switch prefix {
	case "MSG":
		if err := f.atLeast(5); err != nil {
			return nil, err
		}
		m := Message{Code: f.int(0), Flags: f.int(1), Text: f.str(3), Format: f.str(4)}
		count := f.int(2)
		for i := 5; i < len(f.v); i++ {
			m.Params = append(m.Params, f.str(i))
		}
		if f.err == nil && count != len(m.Params) {
			return nil, fmt.Errorf("declares %d params, has %d", count, len(m.Params))
		}
		return m, f.err
	case "DRV":
		if err := f.exactly(7); err != nil {
			return nil, err
		}
		return Drive{f.int(0), f.int(1), f.int(2), f.int(3), f.str(4), f.str(5), f.str(6)}, f.err
	case "TCOUNT":
		if err := f.exactly(1); err != nil {
			return nil, err
		}
		return TitleCount{f.int(0)}, f.err
	case "CINFO":
		if err := f.exactly(3); err != nil {
			return nil, err
		}
		return DiscInfo{Attr(f.int(0)), f.int(1), f.str(2)}, f.err
	case "TINFO":
		if err := f.exactly(4); err != nil {
			return nil, err
		}
		return TitleInfo{f.int(0), Attr(f.int(1)), f.int(2), f.str(3)}, f.err
	case "SINFO":
		if err := f.exactly(5); err != nil {
			return nil, err
		}
		return StreamInfo{f.int(0), f.int(1), Attr(f.int(2)), f.int(3), f.str(4)}, f.err
	case "PRGT", "PRGC":
		if err := f.exactly(3); err != nil {
			return nil, err
		}
		return ProgressName{prefix == "PRGC", f.int(0), f.int(1), f.str(2)}, f.err
	case "PRGV":
		if err := f.exactly(3); err != nil {
			return nil, err
		}
		return Progress{f.int(0), f.int(1), f.int(2)}, f.err
	default:
		// Unreachable: ParseLine only calls build for known prefixes.
		return Unknown{Prefix: prefix, Line: line}, nil
	}
}

// fields holds the comma-separated values of one line. Its helpers record the
// first conversion error in err instead of returning it, which keeps build()
// short: check f.err once at the end.
type fields struct {
	v   []string
	err error
}

func (f *fields) exactly(n int) error {
	if len(f.v) != n {
		return fmt.Errorf("want %d fields, got %d", n, len(f.v))
	}
	return nil
}

func (f *fields) atLeast(n int) error {
	if len(f.v) < n {
		return fmt.Errorf("want at least %d fields, got %d", n, len(f.v))
	}
	return nil
}

func (f *fields) int(i int) int {
	n, err := strconv.Atoi(f.v[i])
	if err != nil && f.err == nil {
		f.err = fmt.Errorf("field %d: %q is not a number", i, f.v[i])
	}
	return n
}

func (f *fields) str(i int) string { return f.v[i] }

// splitFields splits on commas outside double quotes and removes the quotes.
// Inside quotes a backslash makes the next character literal, so a value may
// contain commas, quotes and backslashes.
func splitFields(s string) (fields, error) {
	var out []string
	var cur strings.Builder
	inQuotes, escaped := false, false
	for _, r := range s {
		switch {
		case escaped:
			cur.WriteRune(r)
			escaped = false
		case inQuotes && r == '\\':
			escaped = true
		case r == '"':
			inQuotes = !inQuotes
		case r == ',' && !inQuotes:
			out = append(out, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	if inQuotes {
		return fields{}, fmt.Errorf("unterminated quoted string")
	}
	out = append(out, cur.String())
	return fields{v: out}, nil
}
