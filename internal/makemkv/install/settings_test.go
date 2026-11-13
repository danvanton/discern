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
