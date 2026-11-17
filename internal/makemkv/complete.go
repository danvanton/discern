package makemkv

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// MakeMKV message codes that mean a scan didn't read the whole disc. Seen on
// a real UHD disc scanned while the drive was still spinning up: MakeMKV
// logged 1,204 read errors, skipped 35 titles, then reported success.
const (
	msgReadError        = 2003 // "Error '…' occurred while reading '…' at offset '…'"
	msgTitleSkipped     = 3016 // "Title #… was skipped"
	msgTitleUndecodable = 5043 // "Failed to decode audio/video data for title #… , title skipped."
	msgOpenFailed       = 5010 // "Failed to open disc"
)

// MsgPlainText is the code of a Message made from a plain text line, which
// has no code of its own (MakeMKV's codes start at 1000). MakeMKV prints
// some errors that way even in robot mode, such as `Backup source must start
// with "disc:"`.
const MsgPlainText = 0

// Problem reports whether m is one of the MakeMKV errors Discern knows, or a
// plain text line, for the failure page to mark. MakeMKV's flags field is 0
// on every message recorded so far, so it can't tell errors apart; the codes
// can.
func (m Message) Problem() bool {
	switch m.Code {
	case MsgPlainText, msgReadError, msgTitleSkipped, msgTitleUndecodable, msgOpenFailed:
		return true
	}
	return false
}

// IncompleteScanError is a scan that makemkvcon finished with exit status 0,
// but without reading the whole disc. Its titles can't be trusted: some are
// missing, and the rest may be numbered differently from a complete scan.
//
// The usual cause is a drive that isn't ready yet, so a later scan may
// succeed; a damaged disc fails every time.
type IncompleteScanError struct {
	ReadErrors int
	Skipped    int
	First      string    // the first error or skip message, as MakeMKV printed it
	Messages   []Message // all the scan's messages
}

func (e *IncompleteScanError) Error() string {
	return fmt.Sprintf("MakeMKV couldn't read the whole disc: %s, %s (first: %s)",
		plural(e.ReadErrors, "read error", "read errors"), plural(e.Skipped, "title skipped", "titles skipped"), e.First)
}

// checkComplete returns an IncompleteScanError if the scan skipped titles, or
// read errors say the drive wasn't ready. Other read errors alone don't count:
// copy-protected DVDs (ARccOS, RipGuard) carry deliberately unreadable
// sectors that MakeMKV reads around on every scan, and a title it couldn't
// read is reported as skipped anyway.
func checkComplete(d *Disc) error {
	e := &IncompleteScanError{}
	notReady := false
	for _, m := range d.Messages {
		switch m.Code {
		case msgReadError:
			e.ReadErrors++
			notReady = notReady || strings.Contains(m.Text, "NOT READY")
		case msgTitleSkipped, msgTitleUndecodable:
			e.Skipped++
		default:
			continue
		}
		if e.First == "" {
			e.First = shorten(m.Text, 200)
		}
	}
	if e.Skipped == 0 && !notReady {
		return nil
	}
	e.Messages = d.Messages
	return e
}

// plural formats a count with its noun: "1 read error", "3 read errors".
func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// shorten cuts s to at most n runes. Messages come from the disc, which is
// untrusted, so they are bounded before they reach the job's note.
func shorten(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}

// MakeMKVMessages are the scan's messages, for the failure page.
func (e *IncompleteScanError) MakeMKVMessages() []Message { return e.Messages }
