# Trayarr

Put a disc in the tray, get a correctly named movie in your library.

Trayarr watches your optical drive, works out which titles on a Blu-ray, UHD or DVD are the actual movie (even on discs that hide it among hundreds of fake playlists), rips only those with MakeMKV, and hands them to Radarr. You can also have it file them for Plex or Jellyfin itself. It imports on its own only when the evidence agrees. Otherwise it asks you.

> **Status: early development.** Nothing here is usable yet. The design is in [docs/DESIGN.md](docs/DESIGN.md).

## Why another ripper?

Tools like [ARM](https://github.com/automatic-ripping-machine/automatic-ripping-machine), [Rexarr](https://github.com/MoonlightLaboratory/rexarr), [BluForge](https://github.com/JohnPostlethwait/BluForge) and [Engram](https://github.com/Jsakkos/engram) already rip discs. Trayarr focuses on two things none of them publish numbers for:

- **Picking the right title, measurably.** A benchmark over [TheDiscDB](https://thediscdb.com)'s catalogue scores every change. The baseline, "take the longest title", is wrong on 7.3% of 2,627 movie discs.
- **Safe automation.** Trayarr imports on its own only when the evidence agrees. A watchdog catches rips that stall without an error, and retries them a different way.

## Legal

Trayarr is for backing up discs you own. It does not decrypt anything itself and does not include MakeMKV. Each user installs MakeMKV through Trayarr, accepts [MakeMKV's licence](https://www.makemkv.com/eula/) and supplies their own key. Trayarr has no download or sharing features.

## How it's built

The design and every decision in it are the author's; the code is written with [Claude Code](https://claude.com/claude-code). Every design decision and its reasoning is in [docs/DESIGN.md](docs/DESIGN.md#17-decision-log).

## Licence

[GPL-3.0](LICENSE). Disc metadata from [TheDiscDB](https://thediscdb.com) (MIT). MakeMKV is proprietary software by GuinpinSoft and is not part of this project.
