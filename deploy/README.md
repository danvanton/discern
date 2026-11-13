# Running Discern

[`compose.yaml`](compose.yaml) is a complete example. This page explains the values it asks for.

## Before the first start

1. **Read MakeMKV's licence** at <https://www.makemkv.com/eula/>. Setting `MAKEMKV_ACCEPT_EULA=yes` says you accept it. Without that, Discern downloads nothing and stops with a message saying so.
2. **Get a MakeMKV key.** Use a bought key or the current beta key from MakeMKV's forum. The beta key expires every few months; when it does, put the new one in the secret file and restart.
3. **Create the secret files.** Put each value on its own line, in a file only the container user can read (`chmod 640`):
   - `secrets/makemkv_key`
   - `secrets/radarr_api_key`
   - `secrets/discern_password` (at least 12 characters)

At the first start Discern downloads `makemkvcon` (about 19 MB) from makemkv.com, checks its SHA-256 against the version pinned in this release, and installs it in `/config/makemkv/<version>`. Later starts reuse it. A new Discern release that pins a newer MakeMKV installs that version and removes the old one.

## The drive

MakeMKV needs two device nodes for one drive: the block device (`/dev/sr0`) and its SCSI generic node (`/dev/sgN`). To find the `sg` that belongs to `sr0`:

```sh
ls /sys/block/sr0/device/scsi_generic/
```

The `sg` number follows the order the kernel finds SCSI devices in, with disks first. It can change after a reboot or a USB re-plug, and then the container won't start ("error gathering device information"). Update the `devices:` entry when that happens, or run a small script at boot that reads the number from the path above and updates the app.

The container needs no `--privileged` and no extra capabilities: only the two devices, and the group that owns them (`group_add`). `ls -ln /dev/sr0 /dev/sgN` shows that group's number.

## Folders

- **`/config`**: the database, the installed MakeMKV and its settings, including the key. Back it up.
- **Staging and the library on one filesystem.** Mount the folder that holds both, and set `DISCERN_STAGING_DIR` inside it. Radarr then imports with a rename instead of copying tens of gigabytes. Set `DISCERN_LIBRARY_DIR` too, and Discern warns at start-up if the two are on different filesystems.
- **The same paths as Radarr.** If Discern mounts the media folder at the same path as Radarr's container does, Radarr finds staging without extra settings. Otherwise set `DISCERN_RADARR_STAGING_DIR` to staging as Radarr sees it.
- **The user** (`user:`) should be the owner of the media folders, the same one Radarr uses, so Radarr can move the rips.

## TrueNAS SCALE

Install it as a custom app from YAML (Apps → Discover Apps → ⋮ → Install via YAML) and paste the edited compose file.

- Use absolute host paths. The `./config` and `./secrets` paths only work with `docker compose` in a folder. For example, use `/mnt/<pool>/apps/discern/config`, with the secret files next to it.
- Only one app can use the drive at a time. Stop any other MakeMKV app first.
- Put staging on the same dataset as the library.

## First-deployment check

No automated test can put a disc in a real drive, so check these once on a new install:

1. The log shows `MakeMKV installed` (first start) or `MakeMKV is installed` (later starts), and the dashboard's health list is all green.
2. Open and close the tray, then put a disc in. The log shows `disc inserted`, and the dashboard shows a new job.
3. When the job finishes, the tray opens.
