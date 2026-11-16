package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) Getenv { return func(k string) string { return m[k] } }

func files(m map[string]string) ReadFile {
	return func(p string) ([]byte, error) {
		if v, ok := m[p]; ok {
			return []byte(v), nil
		}
		return nil, os.ErrNotExist
	}
}

var minimal = map[string]string{
	"DISCERN_RADARR_URL":         "http://radarr:7878",
	"DISCERN_RADARR_API_KEY":     "abc",
	"DISCERN_RADARR_ROOT_FOLDER": "/movies",
	"DISCERN_USERNAME":           "dan",
	"DISCERN_PASSWORD":           "correct horse battery",
}

func with(extra map[string]string) map[string]string {
	m := map[string]string{}
	for k, v := range minimal {
		m[k] = v
	}
	for k, v := range extra {
		if v == "" {
			delete(m, k)
		} else {
			m[k] = v
		}
	}
	return m
}

func TestDefaults(t *testing.T) {
	c, err := Load(env(minimal), files(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != ":8790" || c.DataDir != abs(t, "/config") || c.StagingDir != abs(t, "/media/staging") ||
		c.MakeMKVCon != "" || c.MakeMKVDir != abs(t, "/config/makemkv") || c.AcceptEULA || c.MakeMKVKey != "" ||
		len(c.Drives) != 1 || c.Drives[0] != "/dev/sr0" ||
		c.DrivePoll != 2*time.Second || c.RadarrQualityProfile != "Any" {
		t.Errorf("defaults = %+v", c)
	}
	if c.RadarrStagingDir != filepath.ToSlash(abs(t, "/media/staging")) {
		t.Errorf("Radarr's staging dir defaults to %q, want the same as Discern's", c.RadarrStagingDir)
	}
}

func TestSettings(t *testing.T) {
	c, err := Load(env(with(map[string]string{
		"DISCERN_DRIVES":             " /dev/sr0, /dev/sr1 ,",
		"DISCERN_DRIVE_POLL":         "5s",
		"DISCERN_RADARR_STAGING_DIR": "/data/staging",
	})), files(nil))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(c.Drives, "|") != "/dev/sr0|/dev/sr1" || c.DrivePoll != 5*time.Second ||
		c.RadarrStagingDir != "/data/staging" {
		t.Errorf("got %+v", c)
	}
}

func TestSecretsFromFiles(t *testing.T) {
	c, err := Load(env(with(map[string]string{
		"DISCERN_RADARR_API_KEY":      "",
		"DISCERN_RADARR_API_KEY_FILE": "/run/secrets/radarr",
		"DISCERN_PASSWORD":            "",
		"DISCERN_PASSWORD_FILE":       "/run/secrets/pw",
	})), files(map[string]string{
		"/run/secrets/radarr": "key-from-file\n", // trailing newline, as editors leave it
		"/run/secrets/pw":     "a long password from a file\r\n",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.RadarrAPIKey != "key-from-file" || c.Password != "a long password from a file" {
		t.Errorf("secrets = %q, %q", c.RadarrAPIKey, c.Password)
	}
}

// Every problem is reported at once.
func TestProblemsAreAllReported(t *testing.T) {
	_, err := Load(env(map[string]string{
		"DISCERN_DRIVE_POLL":       "fast",
		"DISCERN_INSECURE_NO_AUTH": "maybe",
		"DISCERN_PASSWORD":         "short",
		"DISCERN_PASSWORD_FILE":    "/x",
	}), files(nil))
	if err == nil {
		t.Fatal("loaded a config with nothing set")
	}
	for _, want := range []string{
		"DISCERN_RADARR_URL is required", "DISCERN_RADARR_API_KEY is required",
		"DISCERN_RADARR_ROOT_FOLDER is required", "DISCERN_USERNAME is required",
		"not a duration", "not true or false", "DISCERN_PASSWORD or DISCERN_PASSWORD_FILE, not both",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error is missing %q:\n%v", want, err)
		}
	}
}

func TestPasswordRules(t *testing.T) {
	if _, err := Load(env(with(map[string]string{"DISCERN_PASSWORD": "short"})), files(nil)); err == nil ||
		!strings.Contains(err.Error(), "at least 12") {
		t.Errorf("short password: err = %v", err)
	}
	// Login can be turned off explicitly for development; then no password is needed.
	if _, err := Load(env(with(map[string]string{
		"DISCERN_USERNAME": "", "DISCERN_PASSWORD": "", "DISCERN_INSECURE_NO_AUTH": "true",
	})), files(nil)); err != nil {
		t.Errorf("no-auth mode: %v", err)
	}
}

func TestMissingSecretFile(t *testing.T) {
	_, err := Load(env(with(map[string]string{"DISCERN_RADARR_API_KEY": "", "DISCERN_RADARR_API_KEY_FILE": "/nope"})), files(nil))
	if err == nil || !errors.Is(err, os.ErrNotExist) {
		t.Errorf("err = %v, want the missing file reported", err)
	}
}

func abs(t *testing.T, p string) string {
	t.Helper()
	a, err := filepath.Abs(p)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// Relative folders become absolute, so Radarr is never handed a path that
// only makes sense in Discern's working directory.
func TestRelativeFoldersBecomeAbsolute(t *testing.T) {
	c, err := Load(env(with(map[string]string{
		"DISCERN_STAGING_DIR": "dev/staging", "DISCERN_DATA_DIR": "dev/config", "DISCERN_LIBRARY_DIR": "dev/library",
	})), files(nil))
	if err != nil {
		t.Fatal(err)
	}
	for name, p := range map[string]string{"staging": c.StagingDir, "data": c.DataDir, "library": c.LibraryDir} {
		if !filepath.IsAbs(p) {
			t.Errorf("%s dir %q is not absolute", name, p)
		}
	}
	if c.RadarrStagingDir != filepath.ToSlash(c.StagingDir) {
		t.Errorf("Radarr's staging dir = %q, want the absolute staging dir", c.RadarrStagingDir)
	}
	if _, err := Load(env(with(map[string]string{"DISCERN_RADARR_STAGING_DIR": "data/staging"})), files(nil)); err == nil ||
		!strings.Contains(err.Error(), "must be an absolute path") {
		t.Errorf("relative Radarr staging dir: err = %v", err)
	}
}

func TestMakeMKVSettings(t *testing.T) {
	c, err := Load(env(with(map[string]string{
		"MAKEMKV_ACCEPT_EULA": "YES",
		"MAKEMKV_KEY_FILE":    "/run/secrets/makemkv",
		"DISCERN_DATA_DIR":    "/data/discern",
	})), files(map[string]string{"/run/secrets/makemkv": "T-key-from-file\n"}))
	if err != nil {
		t.Fatal(err)
	}
	if !c.AcceptEULA || c.MakeMKVKey != "T-key-from-file" || c.MakeMKVDir != abs(t, "/data/discern/makemkv") {
		t.Errorf("got EULA %v, key %q, dir %q", c.AcceptEULA, c.MakeMKVKey, c.MakeMKVDir)
	}
}

func TestEULAOnlyAcceptsYes(t *testing.T) {
	for _, v := range []string{"true", "1", "y", "no"} {
		_, err := Load(env(with(map[string]string{"MAKEMKV_ACCEPT_EULA": v})), files(nil))
		if err == nil || !strings.Contains(err.Error(), "MAKEMKV_ACCEPT_EULA") {
			t.Errorf("MAKEMKV_ACCEPT_EULA=%s: err = %v; want an error", v, err)
		}
	}
}

// TheDiscDB's index comes from Discern's release by default (decision D38);
// "off" turns the download off, and only https is accepted.
func TestDiscDBURL(t *testing.T) {
	c, err := Load(env(minimal), files(nil))
	if err != nil || c.DiscDBURL != "https://github.com/danvanton/discern/releases/download/discdb-index/discdb-index.json.gz" {
		t.Errorf("default: %q, %v", c.DiscDBURL, err)
	}
	if c, err := Load(env(with(map[string]string{"DISCERN_DISCDB_URL": "OFF"})), files(nil)); err != nil || c.DiscDBURL != "" {
		t.Errorf("off: %q, %v", c.DiscDBURL, err)
	}
	if _, err := Load(env(with(map[string]string{"DISCERN_DISCDB_URL": "http://example.com/i.json.gz"})), files(nil)); err == nil {
		t.Error("a plain-http index address was accepted")
	}
}
