package install

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const testKey = "T-abcdefGHIJ_1234@xyz"

func TestWriteKeyCreatesFile(t *testing.T) {
	path := SettingsPath(t.TempDir())
	changed, err := WriteKey(path, testKey)
	if err != nil || !changed {
		t.Fatalf("WriteKey = %v, %v; want true, nil", changed, err)
	}
	if got := read(t, path); got != `app_Key = "`+testKey+`"`+"\n" {
		t.Errorf("settings.conf = %q", got)
	}
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(path)
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("mode = %v; want 0600 (the file holds the key)", fi.Mode().Perm())
		}
	}
	// The same key again writes nothing.
	if changed, err := WriteKey(path, testKey); err != nil || changed {
		t.Errorf("second WriteKey = %v, %v; want false, nil", changed, err)
	}
}

func TestWriteKeyKeepsOtherSettings(t *testing.T) {
	path := SettingsPath(t.TempDir())
	os.MkdirAll(filepath.Dir(path), 0o700)
	old := "#\n# MakeMKV settings file\n#\n\napp_DestinationType = \"2\"\napp_Key = \"T-oldoldold\"\napp_KeyHash = \"abc\"\napp_Key=\"T-duplicate\"\n"
	os.WriteFile(path, []byte(old), 0o600)

	if _, err := WriteKey(path, testKey); err != nil {
		t.Fatal(err)
	}
	want := "#\n# MakeMKV settings file\n#\n\napp_DestinationType = \"2\"\napp_Key = \"" + testKey + "\"\napp_KeyHash = \"abc\"\n"
	if got := read(t, path); got != want {
		t.Errorf("settings.conf =\n%s\nwant\n%s", got, want)
	}
}

func TestWriteKeyRejectsUnsafeKeys(t *testing.T) {
	path := SettingsPath(t.TempDir())
	for _, key := range []string{"", "short", `T-abc"defghij`, "T-abc\ndefghij", `T-abc\defghij`, "T-abc defghij", strings.Repeat("k", 300)} {
		_, err := WriteKey(path, key)
		if err == nil {
			t.Errorf("WriteKey(%q) accepted it", key)
			continue
		}
		if key != "" && strings.Contains(err.Error(), key) {
			t.Errorf("the error shows the key: %v", err)
		}
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("a rejected key still wrote the settings file")
	}
}

func TestWriteSettingSetsAndRemoves(t *testing.T) {
	path := SettingsPath(t.TempDir())
	if _, err := WriteKey(path, testKey); err != nil {
		t.Fatal(err)
	}
	rule := "-sel:all,+sel:video,+sel:(audio&(eng|nolang|single)),=100:all"
	if changed, err := WriteSetting(path, "app_DefaultSelectionString", rule); err != nil || !changed {
		t.Fatalf("WriteSetting = %v, %v; want true, nil", changed, err)
	}
	want := `app_Key = "` + testKey + `"` + "\n" + `app_DefaultSelectionString = "` + rule + `"` + "\n"
	if got := read(t, path); got != want {
		t.Errorf("settings.conf =\n%s\nwant\n%s", got, want)
	}
	if changed, _ := WriteSetting(path, "app_DefaultSelectionString", rule); changed {
		t.Error("the same rule again rewrote the file")
	}
	// An empty value removes the line, so MakeMKV's default applies again.
	if changed, err := WriteSetting(path, "app_DefaultSelectionString", ""); err != nil || !changed {
		t.Fatalf("removing: %v, %v", changed, err)
	}
	if got := read(t, path); got != `app_Key = "`+testKey+`"`+"\n" {
		t.Errorf("after removing, settings.conf = %q", got)
	}
}

func TestWriteSettingRefusesUnsafeInput(t *testing.T) {
	path := SettingsPath(t.TempDir())
	for _, c := range []struct{ name, value string }{
		{"app_DefaultSelectionString", `x"y`},
		{"app_DefaultSelectionString", "x\ny"},
		{"app_DefaultSelectionString", `x\y`},
		{"not_a_setting", "x"},
		{"app_Key = \"evil\"\napp_X", "x"},
	} {
		if _, err := WriteSetting(path, c.name, c.value); err == nil {
			t.Errorf("WriteSetting(%q, %q) was accepted", c.name, c.value)
		}
	}
}
