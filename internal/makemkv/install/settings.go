package install

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// SettingsPath is MakeMKV's settings file for the given home folder.
// makemkvcon reads it from $HOME/.MakeMKV; the image sets HOME=/config.
func SettingsPath(home string) string { return filepath.Join(home, ".MakeMKV", "settings.conf") }

// maxSettings bounds the settings file read. MakeMKV's own is a few hundred
// bytes.
const maxSettings = 1 << 20

// WriteKey sets app_Key in MakeMKV's settings file, keeping MakeMKV's other
// settings, and reports whether the file changed. It is safe to run at every
// start: an unchanged key writes nothing.
//
// Errors never contain the key.
func WriteKey(path, key string) (changed bool, err error) {
	if err := checkKey(key); err != nil {
		return false, err
	}
	return writeSetting(path, "app_Key", key)
}

// WriteSetting sets one of MakeMKV's settings, such as
// app_DefaultSelectionString, keeping the others, and reports whether the
// file changed. An empty value removes the setting, so MakeMKV's own default
// applies. The value goes inside quotes, so quotes, backslashes and control
// characters are refused.
func WriteSetting(path, name, value string) (changed bool, err error) {
	if !settingName.MatchString(name) {
		return false, fmt.Errorf("%q is not a MakeMKV setting name", name)
	}
	if len(value) > 4096 {
		return false, fmt.Errorf("%s: value longer than 4096 bytes", name)
	}
	for _, c := range value {
		if c < ' ' || c > '~' || c == '"' || c == '\\' {
			return false, fmt.Errorf("%s: only printable ASCII without quotes or backslashes", name)
		}
	}
	return writeSetting(path, name, value)
}

var settingName = regexp.MustCompile(`^app_[A-Za-z]+$`)

// writeSetting replaces or adds the line for name ("" removes it) and writes
// the file only if it changed.
func writeSetting(path, name, value string) (changed bool, err error) {
	old, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	if len(old) > maxSettings {
		return false, fmt.Errorf("%s is larger than %d bytes", path, maxSettings)
	}

	want := name + ` = "` + value + `"`
	var lines []string
	found := false
	if len(old) > 0 {
		for _, line := range strings.Split(strings.TrimRight(string(old), "\n"), "\n") {
			if isSettingLine(line, name) {
				if found || value == "" {
					continue // keep just one line for name, or none to remove it
				}
				line, found = want, true
			}
			lines = append(lines, line)
		}
	}
	if !found && value != "" {
		lines = append(lines, want)
	}
	next := ""
	if len(lines) > 0 {
		next = strings.Join(lines, "\n") + "\n"
	}
	if next == string(old) {
		return false, nil
	}

	// Write a temporary file and rename it over the old one, so a crash never
	// leaves MakeMKV a half-written settings file. os.CreateTemp makes it
	// 0600, readable only by its owner, which matters: it holds the key.
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false, err
	}
	tmp, err := os.CreateTemp(dir, ".settings-*.conf")
	if err != nil {
		return false, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(next); err != nil {
		tmp.Close()
		return false, err
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	return true, os.Rename(tmp.Name(), path)
}

// isSettingLine matches MakeMKV's "name = ..." line: "app_Key = ...", not
// app_KeyHash.
func isSettingLine(line, name string) bool {
	rest, ok := strings.CutPrefix(strings.TrimSpace(line), name)
	return ok && strings.HasPrefix(strings.TrimSpace(rest), "=")
}

// checkKey refuses anything that could break out of the quoted value:
// quotes, backslashes, spaces and control characters. Keys are short ASCII
// strings such as "T-..." (the beta key) or "M-..." (a bought key).
func checkKey(key string) error {
	if len(key) < 8 || len(key) > 256 {
		return errors.New("MAKEMKV_KEY must be 8 to 256 characters")
	}
	for _, c := range key {
		if c <= ' ' || c > '~' || c == '"' || c == '\\' {
			return errors.New("MAKEMKV_KEY may only contain printable ASCII without spaces, quotes or backslashes")
		}
	}
	return nil
}
