// Package settings holds what the web UI's Settings page changes (decision
// D21), as opposed to the container's environment variables, which configure
// what Discern needs before it starts.
package settings

import (
	"fmt"

	"github.com/danvanton/discern/internal/lang"
)

// Settings is every setting on the page.
type Settings struct {
	// Eject opens the tray when a job is done (decision D25).
	Eject bool
	// Languages chooses the tracks MakeMKV keeps. Nil means MakeMKV's own
	// default selection, as before any choice was saved (decision D24).
	Languages *Languages
	// AutoImport says whether a sure choice is ripped without asking
	// (decisions D60-D63).
	AutoImport AutoImport
}

// AutoImport is the Settings page's auto-import mode (decision D63).
type AutoImport string

const (
	// AutoOff: every disc waits for the user's approval. The default.
	AutoOff AutoImport = "off"
	// AutoTrial: every disc still waits, and Discern records what it would
	// have done and whether the user agreed (decision D62).
	AutoTrial AutoImport = "trial"
	// AutoOn: a sure choice is ripped and imported without asking
	// (decision D60).
	AutoOn AutoImport = "on"
)

// Validate refuses a mode that isn't one of the three.
func (a AutoImport) Validate() error {
	switch a {
	case AutoOff, AutoTrial, AutoOn:
		return nil
	}
	return fmt.Errorf("auto-import %q isn't off, trial or on", string(a))
}

// Languages is the Settings page's track choice: a lang.Choice, kept under
// this name so the page's code reads as before.
type Languages = lang.Choice

// Defaults are the settings before anything is saved.
func Defaults() Settings { return Settings{Eject: true, AutoImport: AutoOff} }
