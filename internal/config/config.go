// Package config reads Discern's settings from environment variables, so a
// container can be configured entirely from its compose file. Secrets can
// come from files instead (Docker secrets): set NAME_FILE to a path, and the
// file's content is used for NAME.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Config is every setting, after defaults.
type Config struct {
	Listen     string        // address for the web UI, e.g. ":8790"
	DataDir    string        // database and other state
	StagingDir string        // where rips wait to be imported; same filesystem as the library
	LibraryDir string        // optional: the movie library as Discern sees it, to check the filesystem
	MakeMKVCon string        // path to makemkvcon
	Drives     []string      // optical drives to watch
	DrivePoll  time.Duration // how often drives are checked
	Eject      bool          // eject the disc when its job is done

	RadarrURL            string
	RadarrAPIKey         string
	RadarrRootFolder     string // as Radarr sees it
	RadarrStagingDir     string // StagingDir as Radarr sees it
	RadarrQualityProfile string

	Username string // web UI login
	Password string
	NoAuth   bool // development only: turn the login off
}

// Getenv and ReadFile are what Load uses to read the environment and secret
// files; tests pass their own.
type (
	Getenv   func(string) string
	ReadFile func(string) ([]byte, error)
)

// Load reads the settings and checks them. All errors are reported at once,
// so a fresh install can be fixed in one pass.
func Load(getenv Getenv, readFile ReadFile) (Config, error) {
	r := reader{getenv: getenv, readFile: readFile}
	c := Config{
		Listen:     r.str("DISCERN_LISTEN", ":8790"),
		DataDir:    r.str("DISCERN_DATA_DIR", "/config"),
		StagingDir: r.str("DISCERN_STAGING_DIR", "/media/staging"),
		LibraryDir: r.str("DISCERN_LIBRARY_DIR", ""),
		MakeMKVCon: r.str("DISCERN_MAKEMKVCON", "makemkvcon"),
		Drives:     r.list("DISCERN_DRIVES", "/dev/sr0"),
		DrivePoll:  r.duration("DISCERN_DRIVE_POLL", 2*time.Second),
		Eject:      r.boolean("DISCERN_EJECT", true),

		RadarrURL:            r.str("DISCERN_RADARR_URL", ""),
		RadarrAPIKey:         r.secret("DISCERN_RADARR_API_KEY"),
		RadarrRootFolder:     r.str("DISCERN_RADARR_ROOT_FOLDER", ""),
		RadarrQualityProfile: r.str("DISCERN_RADARR_QUALITY_PROFILE", "Any"),

		Username: r.str("DISCERN_USERNAME", ""),
		Password: r.secret("DISCERN_PASSWORD"),
		NoAuth:   r.boolean("DISCERN_INSECURE_NO_AUTH", false),
	}
	// Folders are made absolute: the staging path is handed to Radarr, which
	// can't resolve a path relative to Discern's working directory.
	c.DataDir = r.abs("DISCERN_DATA_DIR", c.DataDir)
	c.StagingDir = r.abs("DISCERN_STAGING_DIR", c.StagingDir)
	if c.LibraryDir != "" {
		c.LibraryDir = r.abs("DISCERN_LIBRARY_DIR", c.LibraryDir)
	}
	// Radarr usually sees the staging folder at the same path; set this when
	// its container mounts it elsewhere. It must be absolute: Radarr has no
	// idea of Discern's working directory.
	c.RadarrStagingDir = r.str("DISCERN_RADARR_STAGING_DIR", filepath.ToSlash(c.StagingDir))
	if !strings.HasPrefix(c.RadarrStagingDir, "/") && !filepath.IsAbs(filepath.FromSlash(c.RadarrStagingDir)) {
		r.fail("DISCERN_RADARR_STAGING_DIR must be an absolute path, as Radarr sees it (got %q)", c.RadarrStagingDir)
	}

	r.require("DISCERN_RADARR_URL", c.RadarrURL)
	r.require("DISCERN_RADARR_API_KEY", c.RadarrAPIKey)
	r.require("DISCERN_RADARR_ROOT_FOLDER", c.RadarrRootFolder)
	if !c.NoAuth {
		r.require("DISCERN_USERNAME", c.Username)
		r.require("DISCERN_PASSWORD", c.Password)
		if c.Password != "" && len(c.Password) < 12 {
			r.fail("DISCERN_PASSWORD must be at least 12 characters")
		}
	}
	if len(c.Drives) == 0 {
		r.fail("DISCERN_DRIVES lists no drives")
	}
	if c.DrivePoll < 500*time.Millisecond {
		r.fail("DISCERN_DRIVE_POLL must be at least 500ms")
	}
	if len(r.errs) > 0 {
		return Config{}, errors.Join(r.errs...)
	}
	return c, nil
}

// FromEnvironment loads from the real environment.
func FromEnvironment() (Config, error) { return Load(os.Getenv, os.ReadFile) }

// reader collects every problem instead of stopping at the first.
type reader struct {
	getenv   Getenv
	readFile ReadFile
	errs     []error
}

func (r *reader) fail(format string, args ...any) {
	r.errs = append(r.errs, fmt.Errorf(format, args...))
}

func (r *reader) str(name, def string) string {
	if v := strings.TrimSpace(r.getenv(name)); v != "" {
		return v
	}
	return def
}

// secret reads NAME, or the file NAME_FILE points at. Setting both is an
// error: it would be unclear which one wins.
func (r *reader) secret(name string) string {
	v, file := r.getenv(name), r.getenv(name+"_FILE")
	switch {
	case v != "" && file != "":
		r.fail("set %s or %s_FILE, not both", name, name)
		return ""
	case file != "":
		b, err := r.readFile(file)
		if err != nil {
			r.fail("%s_FILE: %w", name, err)
			return ""
		}
		return strings.TrimRight(string(b), "\r\n")
	}
	return v
}

// abs makes a folder absolute, relative to the working directory.
func (r *reader) abs(name, p string) string {
	a, err := filepath.Abs(p)
	if err != nil {
		r.fail("%s: %v", name, err)
		return p
	}
	return a
}

func (r *reader) list(name, def string) []string {
	var out []string
	for _, p := range strings.Split(r.str(name, def), ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (r *reader) duration(name string, def time.Duration) time.Duration {
	v := r.str(name, "")
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		r.fail("%s: %q is not a duration like 2s or 500ms", name, v)
		return def
	}
	return d
}

func (r *reader) boolean(name string, def bool) bool {
	v := r.str(name, "")
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		r.fail("%s: %q is not true or false", name, v)
		return def
	}
	return b
}

func (r *reader) require(name, v string) {
	if v == "" {
		r.fail("%s is required", name)
	}
}
