# Discern

Discern watches optical drives, works out which titles on a disc are the movie, rips them with MakeMKV (`makemkvcon`, run as a subprocess) and imports them into Radarr. It is written in Go 1.27 and is a single binary with a web UI.

The design, every decision (D1–D13) and the reason for each are in `docs/DESIGN.md`. Read the relevant section before changing a part. When a change alters behaviour the design describes, update the design doc in the same change. New decisions get the next D-number in the decision log (section 17), marked Proposed until the owner accepts them.

## Layout

| Path | What it is |
|---|---|
| `cmd/discern` | the server: reads settings, wires the parts, shuts down within Docker's 10 s |
| `cmd/fake-makemkvcon`, `cmd/fake-radarr` | stand-ins for running the app without a drive or Radarr (README: "Try it without a drive") |
| `internal/makemkv` | robot-output parser, the runner (`Info`, `Rip`) and the rip watchdog; the only package that knows MakeMKV |
| `internal/makemkv/fake` | the fake makemkvcon (scenarios in the `FAKE_MAKEMKVCON` JSON variable) |
| `internal/makemkv/install` | the first-start installer for `makemkvcon`, the key writer, and the MakeMKV pin (`pin.env`, the signed checksum file, the signing key) |
| `internal/jobs` | job states (a transition table), the engine, and the `Store` and `Steps` interfaces it needs |
| `internal/store` | SQLite (pure-Go driver), migrations in `migrations/*.sql` |
| `internal/steps` | the real `jobs.Steps`: scan, check disc, select, rip, verify, import |
| `internal/radarr` | Radarr v3 API client; `radarrtest` is an in-memory fake Radarr |
| `internal/importer` | Radarr import flow and the staging `PathMap` |
| `internal/identify` | disc label cleaning for the movie lookup |
| `internal/drive` | Linux tray status and eject (ioctls), the watcher and the tray-change tracker |
| `internal/app` | runtime glue: starts and resumes jobs, ejects, health |
| `internal/config` | settings from `DISCERN_*` environment variables |
| `internal/web` | server-rendered UI (`html/template` + vendored htmx), Basic auth, health endpoints |
| `internal/format` | shared size and length formatting |
| `testdata/makemkv` | robot-output scans used by tests and the demo |
| `Dockerfile`, `deploy/` | the image, and the compose example with its notes |
| `scripts/update-makemkv-pin.sh` | pins a MakeMKV release after checking its signature; the weekly workflow runs it |
| `.github/workflows` | CI, the release (tag → signed multi-arch image), the MakeMKV update check |

## Build and test

```sh
go build -o bin/ ./cmd/...
go vet ./...
GOOS=linux go vet ./...          # Linux-only files (drive, procfs) must compile too
go test -count=1 ./...
go test -race -count=1 ./...     # needs cgo; CI runs it
gofmt -l .                       # must print nothing
```

- Tests that start makemkvcon re-run the test binary as the fake (see `TestMain` in `internal/makemkv`, `internal/steps` and `internal/app`). There is no separate build step.
- The Linux-only tests (`*_linux_test.go`, and the `/proc` watchdog test) skip or don't build elsewhere. Only CI on Linux runs them.
- Timing-based tests (the watchdog, the runner) should pass many runs in a row. Run new ones 5–8 times before calling them done.

## Rules the code follows

- **Fail closed.** A scan that can't be read completely, a value that doesn't parse, or a rip that doesn't verify is an error, never a guess.
- **Every job step is safe to run again.** Recovery after a crash is just running the step the saved state names:
  - Rip starts from an empty folder.
  - Import checks whether it already happened.
  - Interrupted (shutdown, cancel) is not the same as failed.
- **The database is the source of truth.**
  - `Store.Move` is a compare-and-swap on the job's state, and records the history event in the same transaction.
  - Only `SetProgress` writes progress.
  - A partial unique index allows one active job per drive.
- **Never make Radarr download anything.** Movies are added unmonitored, with no search, and the client enforces it.
- **Secrets** come from environment variables or `*_FILE` files. They never appear in URLs, logs or error messages; the Radarr key goes in a header.
- **MakeMKV's proprietary part is never bundled** in the repo or the image. `makemkvcon` is installed at first start, after the user accepts its EULA (decisions D6, D14). Only the open-source `makemkv-oss` libraries are built into the image.
- **The MakeMKV pin changes only through `scripts/update-makemkv-pin.sh`,** which checks the signature. `TestPinMatchesSignedFile` fails if `pin.env`, the signed file and the key disagree.
- **Web UI.**
  - Nothing inline (no scripts, no `style=`): the Content-Security-Policy forbids it.
  - Changes need same-origin requests.
  - `/healthz` and `/readyz` are the only pages outside the login, and they reveal no details.
- **Untrusted input has limits.** Disc data counts as untrusted: it is validated, bounded and escaped.

## Working style

- Prefer small, readable code over clever code: the owner reads and reviews all of it.
- Commits: an imperative subject line, then a body that says what changed and why, and what is not yet verified. Commit only when asked.
- Third-party files are fetched from their official source and checked against published checksums, with origin, version and hash recorded (see `internal/web/static/THIRD_PARTY.md`).
