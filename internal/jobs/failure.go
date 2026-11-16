package jobs

import (
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/danvanton/discern/internal/makemkv"
)

// Failure is why a job failed, for the failure page (decisions D35-D37):
// what kind of failure it was, MakeMKV's last messages, and the diagnostics
// bundle saved for it. It is saved with the failure. Retry and Cancel clear
// it, except the bundle's name (keepBundle), so a stall's evidence can still
// be downloaded from the job's page.
type Failure struct {
	Kind        string            `json:"kind"`
	Messages    []makemkv.Message `json:"messages,omitempty"`
	Diagnostics string            `json:"diagnostics,omitempty"` // the bundle's folder name
}

// Failure kinds the web UI explains in plain words. describe recognises
// the makemkv errors itself, and other errors say their kind through a
// FailKind method; any other error is named by its step ("scan", "check
// disc", "rip", "verify", "import"...).
const (
	KindDiscChanged    = "disc-changed"    // ErrDiscChanged
	KindScanIncomplete = "scan-incomplete" // makemkv.IncompleteScanError
	KindMakeMKVExit    = "makemkv-exit"    // makemkv.ExitError
	KindRipStalled     = "rip-stalled"     // makemkv.WatchdogError, stalled
	KindRipHung        = "rip-hung"        // makemkv.WatchdogError, hung
	KindVerifySize     = "verify-size"     // the file isn't the size the scan gave
	KindVerifyNoVideo  = "verify-no-video" // no video track, or not a video file
	KindVerifyNoAudio  = "verify-no-audio" // no audio, though the title has some
	KindVerifyLossless = "verify-lossless" // a kept language lost its lossless track
)

// Limits on what is kept: messages come from the disc, so they are bounded.
const (
	maxFailureMessages = 50
	maxMessageText     = 300 // characters
)

// describe builds the Failure for an error from step what: its kind, and,
// from anywhere in its chain, MakeMKV's messages (MakeMKVMessages) and a
// saved diagnostics bundle (DiagnosticsBundle).
func describe(what string, err error) *Failure {
	f := &Failure{Kind: what}
	var (
		inc  *makemkv.IncompleteScanError
		exit *makemkv.ExitError
		dog  *makemkv.WatchdogError
		k    interface{ FailKind() string }
	)
	switch {
	case errors.Is(err, ErrDiscChanged):
		f.Kind = KindDiscChanged
	case errors.As(err, &inc):
		f.Kind = KindScanIncomplete
	case errors.As(err, &dog) && dog.Verdict == makemkv.Stalled:
		f.Kind = KindRipStalled
	case errors.As(err, &dog) && dog.Verdict == makemkv.Hung:
		f.Kind = KindRipHung
	case errors.As(err, &exit):
		f.Kind = KindMakeMKVExit
	case errors.As(err, &k):
		f.Kind = k.FailKind()
	}
	var m interface{ MakeMKVMessages() []makemkv.Message }
	if errors.As(err, &m) {
		for _, msg := range keptMessages(m.MakeMKVMessages()) {
			f.Messages = append(f.Messages, makemkv.Message{Code: msg.Code, Flags: msg.Flags, Text: bounded(msg.Text)})
		}
	}
	var d interface{ DiagnosticsBundle() string }
	if errors.As(err, &d) {
		f.Diagnostics = d.DiagnosticsBundle()
	}
	return f
}

// keptMessages picks at most maxFailureMessages of msgs, in their order:
// the known errors first (Message.Problem), since they explain the failure,
// then the last of the rest. A scan's few early errors aren't pushed out by
// the hundreds of routine lines after them.
func keptMessages(msgs []makemkv.Message) []makemkv.Message {
	if len(msgs) <= maxFailureMessages {
		return msgs
	}
	keep := make([]bool, len(msgs))
	n := 0
	for i, m := range msgs {
		if n < maxFailureMessages && m.Problem() {
			keep[i] = true
			n++
		}
	}
	for i := len(msgs) - 1; i >= 0 && n < maxFailureMessages; i-- {
		if !keep[i] {
			keep[i] = true
			n++
		}
	}
	var out []makemkv.Message
	for i, m := range msgs {
		if keep[i] {
			out = append(out, m)
		}
	}
	return out
}

// keepBundle is what a job keeps of its failure once it is retried or
// cancelled: only the diagnostics bundle's name, or nothing.
func keepBundle(f *Failure) *Failure {
	if f == nil || f.Diagnostics == "" {
		return nil
	}
	return &Failure{Diagnostics: f.Diagnostics}
}

func bounded(s string) string {
	s = strings.ToValidUTF8(s, "�")
	if utf8.RuneCountInString(s) <= maxMessageText {
		return s
	}
	return string([]rune(s)[:maxMessageText]) + "…"
}
