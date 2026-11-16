package jobs

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/danvanton/discern/internal/makemkv"
)

type kindErr struct{ kind string }

func (e kindErr) Error() string    { return "kinded" }
func (e kindErr) FailKind() string { return e.kind }

// describe takes the kind, MakeMKV's last messages and the bundle from
// anywhere in the error's chain, and names any other error by its step.
func TestDescribe(t *testing.T) {
	if f := describe("import", errors.New("radarr said no")); f.Kind != "import" || f.Messages != nil || f.Diagnostics != "" {
		t.Errorf("a plain error: %+v", f)
	}
	if f := describe("check disc", fmt.Errorf("check: %w", ErrDiscChanged)); f.Kind != KindDiscChanged {
		t.Errorf("disc changed: %+v", f)
	}
	if f := describe("verify", fmt.Errorf("title 3: %w", kindErr{KindVerifyNoAudio})); f.Kind != KindVerifyNoAudio {
		t.Errorf("a kinded error: %+v", f)
	}

	// The last 50 messages, each bounded.
	var msgs []makemkv.Message
	for i := 0; i < 60; i++ {
		msgs = append(msgs, makemkv.Message{Code: 1000 + i, Text: fmt.Sprint("message ", i), Format: "%1", Params: []string{"p"}})
	}
	msgs[59].Text = strings.Repeat("é", 1000)
	exit := &makemkv.ExitError{Op: "mkv", Code: 1, Messages: msgs}
	f := describe("rip", fmt.Errorf("title 0: %w", exit))
	if f.Kind != KindMakeMKVExit || len(f.Messages) != 50 || f.Messages[0].Code != 1010 {
		t.Fatalf("exit error: kind %q, %d messages from %d", f.Kind, len(f.Messages), f.Messages[0].Code)
	}
	if last := f.Messages[49].Text; len([]rune(last)) != maxMessageText+1 || !strings.HasSuffix(last, "…") {
		t.Errorf("a long message kept %d characters", len([]rune(last)))
	}
	if f.Messages[0].Format != "" || f.Messages[0].Params != nil {
		t.Error("only the code, flags and text need keeping")
	}
}

// describe recognises the makemkv errors, wherever they are in the chain.
func TestMakeMKVErrorKinds(t *testing.T) {
	for want, err := range map[string]error{
		KindScanIncomplete: &makemkv.IncompleteScanError{},
		KindMakeMKVExit:    &makemkv.ExitError{},
		KindRipStalled:     &makemkv.WatchdogError{Verdict: makemkv.Stalled},
		KindRipHung:        &makemkv.WatchdogError{Verdict: makemkv.Hung},
	} {
		if got := describe("rip", fmt.Errorf("title 3: %w", err)).Kind; got != want {
			t.Errorf("%T: kind %q; want %q", err, got, want)
		}
	}
}

// The known errors are kept first, then the last of the rest, in order: a
// scan's early errors aren't pushed out by the routine lines after them.
func TestKeptMessagesPutErrorsFirst(t *testing.T) {
	var msgs []makemkv.Message
	msgs = append(msgs, makemkv.Message{Code: 1005, Text: "started"})
	msgs = append(msgs, makemkv.Message{Code: 2003, Text: "read error"}, makemkv.Message{Code: 3016, Text: "title skipped"})
	for i := 0; i < 200; i++ {
		msgs = append(msgs, makemkv.Message{Code: 3307, Text: fmt.Sprint("title info ", i)})
	}
	msgs = append(msgs, makemkv.Message{Code: 5011, Text: "Operation successfully completed"})
	got := keptMessages(msgs)
	if len(got) != maxFailureMessages || got[0].Code != 2003 || got[1].Code != 3016 || got[len(got)-1].Code != 5011 {
		t.Fatalf("kept %d, starting %d %d, ending %d", len(got), got[0].Code, got[1].Code, got[len(got)-1].Code)
	}
	if got[2].Text != "title info 153" {
		t.Errorf("after the errors: %q; want the last 48 lines (47 routine, then the closing one), from title info 153", got[2].Text)
	}
	// More errors than room: the first ones, in order.
	var many []makemkv.Message
	for i := 0; i < 80; i++ {
		many = append(many, makemkv.Message{Code: 2003, Text: fmt.Sprint("read error ", i)})
	}
	if got := keptMessages(many); len(got) != maxFailureMessages || got[0].Text != "read error 0" {
		t.Errorf("many errors: kept %d starting %q", len(got), got[0].Text)
	}
	if got := keptMessages(msgs[:10]); len(got) != 10 {
		t.Errorf("a short list was cut to %d", len(got))
	}
}
