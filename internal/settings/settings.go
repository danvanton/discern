// Package settings holds what the web UI's Settings page changes (decision
// D21), as opposed to the container's environment variables, which configure
// what Discern needs before it starts.
package settings

import "github.com/danvanton/discern/internal/lang"

// Settings is every setting on the page.
type Settings struct {
	// Eject opens the tray when a job is done (decision D25).
	Eject bool
	// Languages chooses the tracks MakeMKV keeps. Nil means MakeMKV's own
	// default selection, as before any choice was saved (decision D24).
	Languages *Languages
}

// Languages is the Settings page's track choice: a lang.Choice, kept under
// this name so the page's code reads as before.
type Languages = lang.Choice

// Defaults are the settings before anything is saved.
func Defaults() Settings { return Settings{Eject: true} }
