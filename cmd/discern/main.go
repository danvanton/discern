// Command discern is the Discern server: it watches optical drives, rips the
// chosen titles with MakeMKV and imports them into Radarr. Settings come from
// environment variables (internal/config).
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/danvanton/discern/internal/app"
	"github.com/danvanton/discern/internal/config"
	"github.com/danvanton/discern/internal/drive"
	"github.com/danvanton/discern/internal/importer"
	"github.com/danvanton/discern/internal/jobs"
	"github.com/danvanton/discern/internal/makemkv"
	"github.com/danvanton/discern/internal/makemkv/install"
	"github.com/danvanton/discern/internal/radarr"
	"github.com/danvanton/discern/internal/steps"
	"github.com/danvanton/discern/internal/store"
	"github.com/danvanton/discern/internal/web"
)

// checkStagingFilesystem warns when staging and the library are on different
// filesystems. Then Radarr's "move" is a full copy of every rip (tens of
// gigabytes, possibly over the network) instead of an instant rename, and a
// long copy can outlast the import timeout.
func checkStagingFilesystem(log *slog.Logger, staging, library string) {
	if library == "" {
		log.Info("DISCERN_LIBRARY_DIR is not set, so Discern can't check that staging and the library share a filesystem")
		return
	}
	same, err := sameFilesystem(staging, library)
	switch {
	case err != nil:
		log.Warn("could not compare the staging and library filesystems", "err", err)
	case !same:
		log.Warn("staging and the library are on different filesystems: every import will copy the whole rip instead of moving it",
			"staging", staging, "library", library)
	}
}

// setUpMakeMKV installs the pinned MakeMKV release unless DISCERN_MAKEMKVCON
// names a makemkvcon to use instead (the fake, in development), writes the
// key into MakeMKV's settings, and returns the makemkvcon to run.
//
// It runs before the web server starts, so a container that can't install
// MakeMKV stops with the reason in its log, as a bad setting does, and
// Docker's restart policy tries again.
func setUpMakeMKV(ctx context.Context, log *slog.Logger, cfg config.Config) (string, error) {
	makemkvcon := cfg.MakeMKVCon
	if makemkvcon != "" {
		log.Info("using the makemkvcon in DISCERN_MAKEMKVCON; Discern installs nothing", "path", makemkvcon)
	} else {
		pin, err := install.Pinned()
		if err != nil {
			return "", err
		}
		in := &install.Installer{Dir: cfg.MakeMKVDir, Release: pin, AcceptEULA: cfg.AcceptEULA, Arch: runtime.GOARCH}
		did, err := in.Ensure(ctx)
		if err != nil {
			return "", fmt.Errorf("MakeMKV %s: %w", pin.Version, err)
		}
		makemkvcon = in.Makemkvcon()
		if did {
			log.Info("MakeMKV installed", "version", pin.Version, "path", makemkvcon)
		} else {
			log.Info("MakeMKV is installed", "version", pin.Version)
		}
	}
	if cfg.MakeMKVKey != "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("finding MakeMKV's settings: %w", err)
		}
		changed, err := install.WriteKey(install.SettingsPath(home), cfg.MakeMKVKey)
		if err != nil {
			return "", fmt.Errorf("writing the MakeMKV key: %w", err)
		}
		if changed {
			log.Info("MakeMKV key saved in its settings")
		}
	}
	return makemkvcon, nil
}

// version is set at build time with -ldflags "-X main.version=v0.1.0".
var version = "dev"

func main() {
	if len(os.Args) > 1 {
		if os.Args[1] == "healthcheck" {
			os.Exit(healthcheck())
		}
		fmt.Fprintf(os.Stderr, "usage: discern [healthcheck]\nunknown command %q\n", os.Args[1])
		os.Exit(2)
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("discern stopped", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.FromEnvironment()
	if err != nil {
		return fmt.Errorf("settings:\n%w", err)
	}
	log.Info("starting", "version", version, "listen", cfg.Listen, "drives", cfg.Drives)

	// SIGTERM is what `docker stop` sends. On it: stop taking new work, let
	// running jobs stop at a safe point, then exit.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	for _, dir := range []string{cfg.DataDir, cfg.StagingDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	checkStagingFilesystem(log, cfg.StagingDir, cfg.LibraryDir)
	st, err := store.Open(ctx, filepath.Join(cfg.DataDir, "discern.db"))
	if err != nil {
		return fmt.Errorf("opening the database: %w", err)
	}
	defer st.Close()

	rc, err := radarr.New(cfg.RadarrURL, cfg.RadarrAPIKey)
	if err != nil {
		return err
	}
	imp, err := importer.NewRadarr(rc, importer.RadarrConfig{
		RootFolder:     cfg.RadarrRootFolder,
		QualityProfile: cfg.RadarrQualityProfile,
		Staging:        importer.PathMap{Local: cfg.StagingDir, Remote: cfg.RadarrStagingDir},
	})
	if err != nil {
		return err
	}
	makemkvcon, err := setUpMakeMKV(ctx, log, cfg)
	if err != nil {
		if ctx.Err() != nil {
			// Stopped mid-install (docker stop): an interruption, not a
			// failure. The next start installs from scratch.
			log.Info("stopped while installing MakeMKV; the install starts again at the next start")
			return nil
		}
		return err
	}
	runner := makemkv.NewRunner(makemkvcon)
	tracker := drive.NewTracker()
	jobSteps := steps.New(runner, imp)
	jobSteps.Tray = tracker
	jobSteps.WaitReady = func(ctx context.Context, d string) error {
		return drive.WaitReady(ctx, d, 2*time.Minute, time.Second)
	}
	engine := jobs.NewEngine(st, jobSteps, cfg.StagingDir, log)

	a := app.New(ctx, st, engine, rc, log)
	a.Drives, a.MakeMKVCon = cfg.Drives, makemkvcon
	if cfg.Eject {
		a.Eject = drive.Eject
	}
	if err := a.Resume(ctx); err != nil {
		return fmt.Errorf("resuming jobs: %w", err)
	}

	watcher := &drive.Watcher{Drives: cfg.Drives, Every: cfg.DrivePoll, OnDisc: a.OnDisc, Tracker: tracker, Log: log}
	go watcher.Run(ctx)

	handler, err := web.New(a, web.Options{
		Username: cfg.Username, Password: cfg.Password, NoAuth: cfg.NoAuth, Version: version, Log: log,
	})
	if err != nil {
		return err
	}
	if cfg.NoAuth {
		log.Warn("the web UI has no login (DISCERN_INSECURE_NO_AUTH): use this for development only")
	}
	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.ListenAndServe() }()
	log.Info("web UI listening", "addr", cfg.Listen)

	select {
	case <-ctx.Done():
		log.Info("shutting down")
	case err := <-serveErr:
		if !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("web server: %w", err)
		}
	}

	// Stop the web UI first, so nobody starts new work, then wait for the
	// running jobs. They were told to stop when ctx ended: a rip stops and
	// resumes at the next start, and an import finishes.
	//
	// The whole shutdown fits in 8 s, inside the 10 s Docker allows after
	// SIGTERM before it kills the container. A job that isn't done by then is
	// safe to cut off: its state is saved, and it resumes at the next start.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Warn("web server shutdown", "err", err)
	}
	if !a.Wait(6 * time.Second) {
		log.Warn("some jobs were still running at exit; they resume at the next start")
	}
	log.Info("stopped")
	return nil
}
