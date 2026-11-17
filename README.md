# DISCERN

**D**isc **I**dentification, **S**election, **C**apture, **E**xtraction, **R**ipping and **N**aming.

Put a disc in the tray, get a correctly named movie in your library. Discern works with Radarr (and soon Sonarr), and can also file movies for Plex or Jellyfin on its own.

Discern watches your optical drive, works out which titles on a Blu-ray, UHD or DVD are the actual movie (even on discs that hide it among hundreds of fake playlists), rips only those with MakeMKV, and hands them to Radarr. You can also have it file them for Plex or Jellyfin itself. It imports on its own only when the evidence agrees. Otherwise it asks you.

> **Status: early development (v0.1 in progress).** It runs end to end against stand-ins for MakeMKV and Radarr, but it has not yet ripped a real disc. The design is in [docs/DESIGN.md](docs/DESIGN.md).

## Why another ripper?

Tools like [ARM](https://github.com/automatic-ripping-machine/automatic-ripping-machine), [Rexarr](https://github.com/MoonlightLaboratory/rexarr), [BluForge](https://github.com/JohnPostlethwait/BluForge) and [Engram](https://github.com/Jsakkos/engram) already rip discs. Discern focuses on two things none of them publish numbers for:

- **Picking the right title, measurably.** A benchmark over [TheDiscDB](https://thediscdb.com)'s catalogue scores every change, and CI fails a change that picks more wrong titles. On 2,818 movie discs, "take the longest title" gives the wrong film on 4.5%. Discern's selector, on discs TheDiscDB doesn't know, is wrong on none of the 2,512 it would pick alone, and asks you on 10.9%; on discs TheDiscDB knows, it is right on 99.5% ([details](docs/DESIGN.md#8-measuring-it-the-benchmark)). Unless you turn auto-import on, it only pre-selects: you approve every disc.
- **Safe automation.** Discern imports on its own only when the evidence agrees. A watchdog catches rips that stall without an error, and retries them a different way.

## Try it without a drive

Two stand-ins let you run and click through the whole app on any machine with Go 1.27. `fake-makemkvcon` plays a disc; `fake-radarr` plays Radarr and really moves the imported file into a library folder.

```sh
go build -o bin/ ./cmd/...
./bin/fake-radarr -listen 127.0.0.1:17878 -staging dev/staging -library dev/library &
```

```sh
export DISCERN_LISTEN=127.0.0.1:18790
export DISCERN_DATA_DIR=dev/config DISCERN_STAGING_DIR=dev/staging
export DISCERN_RADARR_URL=http://127.0.0.1:17878 DISCERN_RADARR_API_KEY=dev-key DISCERN_RADARR_ROOT_FOLDER=/movies
export DISCERN_INSECURE_NO_AUTH=true
export DISCERN_MAKEMKVCON=./bin/fake-makemkvcon
export FAKE_MAKEMKVCON='{"info":{"file":"testdata/makemkv/info-demo-small.txt"},"mkv":{"title_name":"The Matrix","total_bytes":39845888,"chunk_bytes":262144,"chunk_delay_ms":40}}'
./bin/discern
```

Open http://127.0.0.1:18790 and press **Scan disc**. The demo disc is identified as The Matrix and title 0 is suggested. Press **Rip and import**: the progress bar runs for a few seconds, then the job imports into `dev/library` and shows **In Radarr**. The demo fetches The Matrix's poster from TMDb's image server; offline, the card shows the title's first letter instead.

## Run it with Docker

The image is `ghcr.io/danvanton/discern`, for amd64 and arm64. [`deploy/compose.yaml`](deploy/compose.yaml) is a complete example, and [`deploy/README.md`](deploy/README.md) explains the drive passthrough, the folders and TrueNAS SCALE.

At the first start, Discern downloads MakeMKV's `makemkvcon` from makemkv.com into `/config/makemkv`. It does this only if you set `MAKEMKV_ACCEPT_EULA=yes`, and it checks the download against a hash pinned in the release. The image holds only MakeMKV's open-source libraries.

## Settings

What the container needs to start is set with environment variables, below. Secrets can also come from a file: set `NAME_FILE` instead of `NAME`.

Everything else is on the web UI's **Settings** page, saved in Discern's database: auto-import, whether to open the tray when a disc is done, and which audio and subtitle languages to keep (until you choose, MakeMKV's own default selection applies). Auto-import starts off. On trial, every disc still waits for you, and the dashboard shows how often you chose what Discern would have; on, a disc TheDiscDB knows, whose own name finds the same movie, with nothing in doubt and not yet in Radarr, is ripped and imported without asking. `DISCERN_EJECT` was replaced by the Settings page in v0.1.3; if it is still set, its value is copied to the page once.

| Variable | Default | |
|---|---|---|
| `DISCERN_RADARR_URL` | (required) | e.g. `http://radarr:7878` |
| `DISCERN_RADARR_API_KEY` | (required) | or `_FILE` |
| `DISCERN_RADARR_ROOT_FOLDER` | (required) | where Radarr adds new movies, as Radarr sees it |
| `DISCERN_USERNAME`, `DISCERN_PASSWORD` | (required) | web UI login; the password needs at least 12 characters; or `_FILE` |
| `DISCERN_LISTEN` | `:8790` | |
| `DISCERN_DATA_DIR` | `/config` | the database |
| `DISCERN_STAGING_DIR` | `/media/staging` | rips wait here; put it on the same filesystem as the library. Relative paths are made absolute |
| `DISCERN_RADARR_STAGING_DIR` | same as staging | the staging folder as Radarr's container sees it; must be absolute |
| `DISCERN_LIBRARY_DIR` | (none) | the movie library as Discern sees it; set it and Discern warns at start-up if staging is on another filesystem |
| `DISCERN_RADARR_QUALITY_PROFILE` | `Any` | profile for movies Discern adds (always unmonitored) |
| `MAKEMKV_ACCEPT_EULA` | (none) | set to `yes` once you have read and accept [MakeMKV's licence](https://www.makemkv.com/eula/); until then Discern installs nothing and stops |
| `MAKEMKV_KEY` | (none) | your MakeMKV key, written into MakeMKV's settings at start; or `_FILE` |
| `DISCERN_MAKEMKV_DIR` | `/config/makemkv` | where Discern installs MakeMKV |
| `DISCERN_MAKEMKVCON` | (none) | use this makemkvcon instead of installing MakeMKV (development, with the fake) |
| `DISCERN_DRIVES` | `/dev/sr0` | comma-separated |
| `DISCERN_DRIVE_POLL` | `2s` | |
| `DISCERN_DISCDB_URL` | Discern's daily TheDiscDB index | where to download [TheDiscDB](https://github.com/TheDiscDb/data)'s disc data from, once a day (a few MB, `https://` only); `off` to not download it. The index must be signed with Discern's own key, so another address can only be a copy of Discern's published files (index, `.sha256` and `.sig`), not an index built elsewhere |
| `DISCERN_INSECURE_NO_AUTH` | `false` | development only: turns the login off |

## Legal

Discern is for backing up discs you own. It does not decrypt anything itself and does not include MakeMKV's program. The image contains only MakeMKV's open-source libraries, and each release links their source. Each user installs MakeMKV through Discern, accepts [MakeMKV's licence](https://www.makemkv.com/eula/) and supplies their own key. Discern has no download or sharing features.

## How it's built

The design and every decision in it are the author's; the code is written with [Claude Code](https://claude.com/claude-code). Every design decision and its reasoning is in [docs/DESIGN.md](docs/DESIGN.md#17-decision-log).

## Licence

[GPL-3.0](LICENSE). Disc metadata from [TheDiscDB](https://thediscdb.com) (MIT). MakeMKV is proprietary software by GuinpinSoft and is not part of this project.
