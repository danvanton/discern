# Trayarr

Put a disc in the tray, get a correctly named movie in your library.

Trayarr watches your optical drive, works out which titles on a Blu-ray, UHD or DVD are the actual movie (even on discs that hide it among hundreds of fake playlists), rips only those with MakeMKV, and hands them to Radarr. You can also have it file them for Plex or Jellyfin itself. It imports on its own only when the evidence agrees. Otherwise it asks you.

> **Status: early development (v0.1 in progress).** It runs end to end against stand-ins for MakeMKV and Radarr, but it has not yet ripped a real disc. The design is in [docs/DESIGN.md](docs/DESIGN.md).

## Why another ripper?

Tools like [ARM](https://github.com/automatic-ripping-machine/automatic-ripping-machine), [Rexarr](https://github.com/MoonlightLaboratory/rexarr), [BluForge](https://github.com/JohnPostlethwait/BluForge) and [Engram](https://github.com/Jsakkos/engram) already rip discs. Trayarr focuses on two things none of them publish numbers for:

- **Picking the right title, measurably.** A benchmark over [TheDiscDB](https://thediscdb.com)'s catalogue scores every change. The baseline, "take the longest title", is wrong on 7.3% of 2,627 movie discs.
- **Safe automation.** Trayarr imports on its own only when the evidence agrees. A watchdog catches rips that stall without an error, and retries them a different way.

## Try it without a drive

Two stand-ins let you run and click through the whole app on any machine with Go 1.27. `fake-makemkvcon` plays a disc; `fake-radarr` plays Radarr and really moves the imported file into a library folder.

```sh
go build -o bin/ ./cmd/...
./bin/fake-radarr -listen 127.0.0.1:17878 -staging dev/staging -library dev/library &
```

```sh
export TRAYARR_LISTEN=127.0.0.1:18790
export TRAYARR_DATA_DIR=dev/config TRAYARR_STAGING_DIR=dev/staging
export TRAYARR_RADARR_URL=http://127.0.0.1:17878 TRAYARR_RADARR_API_KEY=dev-key TRAYARR_RADARR_ROOT_FOLDER=/movies
export TRAYARR_INSECURE_NO_AUTH=true
export TRAYARR_MAKEMKVCON=./bin/fake-makemkvcon
export FAKE_MAKEMKVCON='{"info":{"file":"testdata/makemkv/info-demo-small.txt"},"mkv":{"title_name":"The Matrix","total_bytes":39845888,"chunk_bytes":262144,"chunk_delay_ms":40}}'
./bin/trayarr
```

Open http://127.0.0.1:18790 and press **Scan disc**. The demo disc is identified as The Matrix and title 0 is suggested. Press **Rip and import**: the progress bar runs for a few seconds, then the job imports into `dev/library` and shows **Done**.

## Settings

Everything is an environment variable. Secrets can also come from a file: set `NAME_FILE` instead of `NAME`.

| Variable | Default | |
|---|---|---|
| `TRAYARR_RADARR_URL` | (required) | e.g. `http://radarr:7878` |
| `TRAYARR_RADARR_API_KEY` | (required) | or `_FILE` |
| `TRAYARR_RADARR_ROOT_FOLDER` | (required) | where Radarr adds new movies, as Radarr sees it |
| `TRAYARR_USERNAME`, `TRAYARR_PASSWORD` | (required) | web UI login; the password needs at least 12 characters; or `_FILE` |
| `TRAYARR_LISTEN` | `:8790` | |
| `TRAYARR_DATA_DIR` | `/config` | the database |
| `TRAYARR_STAGING_DIR` | `/media/staging` | rips wait here; put it on the same filesystem as the library. Relative paths are made absolute |
| `TRAYARR_RADARR_STAGING_DIR` | same as staging | the staging folder as Radarr's container sees it; must be absolute |
| `TRAYARR_LIBRARY_DIR` | (none) | the movie library as Trayarr sees it; set it and Trayarr warns at start-up if staging is on another filesystem |
| `TRAYARR_RADARR_QUALITY_PROFILE` | `Any` | profile for movies Trayarr adds (always unmonitored) |
| `TRAYARR_MAKEMKVCON` | `makemkvcon` | |
| `TRAYARR_DRIVES` | `/dev/sr0` | comma-separated |
| `TRAYARR_DRIVE_POLL` | `2s` | |
| `TRAYARR_EJECT` | `true` | eject the disc when its job is done |
| `TRAYARR_INSECURE_NO_AUTH` | `false` | development only: turns the login off |

## Legal

Trayarr is for backing up discs you own. It does not decrypt anything itself and does not include MakeMKV. Each user installs MakeMKV through Trayarr, accepts [MakeMKV's licence](https://www.makemkv.com/eula/) and supplies their own key. Trayarr has no download or sharing features.

## How it's built

The design and every decision in it are the author's; the code is written with [Claude Code](https://claude.com/claude-code). Every design decision and its reasoning is in [docs/DESIGN.md](docs/DESIGN.md#17-decision-log).

## Licence

[GPL-3.0](LICENSE). Disc metadata from [TheDiscDB](https://thediscdb.com) (MIT). MakeMKV is proprietary software by GuinpinSoft and is not part of this project.
