# Folderlight

A compact, folder-first photo gallery for Alpine Linux. Point it at an existing, read-only directory. Browse folders, preview images, and search the filenames and tags already embedded in your photographs.

The server is one statically linked executable with its web interface and SQLite engine included. There is no runtime requirement for Go, Node.js, Python, SQLite CLI, ExifTool, FFmpeg, Docker, or an external database. The browser loads no external scripts, fonts, analytics, or services.

Download Linux x86-64 and ARM64 packages from [GitHub Releases](https://github.com/imkowalski/folderlight/releases/latest). Source code is available at [imkowalski/folderlight](https://github.com/imkowalski/folderlight).

## Quick start

Choose `folderlight-linux-amd64` for an x86-64 server or `folderlight-linux-arm64` for ARM64. Check `uname -m`: `x86_64` means amd64, `aarch64` means arm64.

Create `config.json` next to your executable:

```json
{
  "library": "/srv/photos",
  "state": "/var/lib/folderlight",
  "listen": "127.0.0.1:8080"
}
```

Make sure the account running Folderlight can read the library, traverse its directories, and write to the state directory. Then run:

```sh
chmod +x ./folderlight-linux-amd64
./folderlight-linux-amd64 -config ./config.json
```

Open `http://127.0.0.1:8080`. Startup automatically scans the library in the background; browsing becomes available as images are indexed. Subsequent starts reuse the database and inspect only changed image metadata. Thumbnails are generated when viewed, rather than during the scan.

For direct LAN access, explicitly set `"listen": "0.0.0.0:8080"` and browse `http://SERVER_IP:8080`. For remote access, use a VPN or your authenticated HTTPS reverse proxy. There are no built-in accounts. The complete configured library is available to every connected visitor. Robots directives discourage search-engine indexing; they are not access control.

## Native Alpine service

Run these commands as root from the extracted release directory. Substitute the ARM64 executable when appropriate.

```sh
addgroup -S folderlight
adduser -S -D -H -s /sbin/nologin -G folderlight folderlight
install -m 0755 folderlight-linux-amd64 /usr/local/bin/folderlight
install -d -m 0755 /etc/folderlight
install -d -m 0700 -o folderlight -g folderlight /var/lib/folderlight
install -m 0644 config.example.json /etc/folderlight/config.json
install -m 0755 folderlight.openrc /etc/init.d/folderlight
install -m 0755 folderlight-update /usr/local/bin/folderlight-update
```

The source checkout stores the service at `deploy/folderlight.openrc`; release archives put it in the top-level directory.

Edit `/etc/folderlight/config.json` to set your photo path and listening address. Give the service read access using your existing photo-reader group, for example:

```sh
# Only if an existing group named "photos" has read/traverse access:
addgroup folderlight photos

# Check the directory is readable under the service account:
su -s /bin/sh folderlight -c 'ls /srv/photos >/dev/null'
rc-update add folderlight default
rc-service folderlight start
rc-service folderlight status
tail -n 30 /var/log/folderlight.log
```

For network-mounted libraries, ensure the mount is ready before starting the service; add the relevant mount/service dependency to the OpenRC script. Rotate `/var/log/folderlight.log` through your normal logging setup.

Mount the library read-only at the OS/container layer for an additional guarantee. Folderlight itself opens source files for reading and rejects state directories that overlap the library. It neither uploads, edits, deletes, nor reorganizes source images. Its HTTP API serves generated JPEG previews, not arbitrary source files.

## Update from GitHub on Alpine / Proxmox

Run updates **inside the Alpine container**, as root. For Proxmox container 105, enter it from the Proxmox host with `pct enter 105`.

Install HTTPS download support once:

```sh
apk add --no-cache ca-certificates wget
```

If you installed an older package without the updater, download its source, inspect it, then install it:

```sh
wget -O /tmp/folderlight-update https://raw.githubusercontent.com/imkowalski/folderlight/v1.1.1/scripts/update.sh
cat /tmp/folderlight-update
install -m 0755 /tmp/folderlight-update /usr/local/bin/folderlight-update
```

Update to the latest published release:

```sh
folderlight-update
folderlight -version
rc-service folderlight status
tail -n 30 /var/log/folderlight.log
```

Or install a particular published release with `folderlight-update v1.1.1`. The updater selects amd64 or arm64, downloads the release archive and its SHA-256 manifest, checks the archive, verifies the executable can run, saves the previous executable as `/usr/local/bin/folderlight.previous`, and replaces the binary. It stops/restarts the service only if it was already running. Download or checksum failures occur before stopping the service. SHA-256 detects corruption; the release and checksum both rely on trust in this GitHub repository and HTTPS.

The updater changes only `/usr/local/bin/folderlight` and its previous-binary copy. It does not overwrite your config, OpenRC service, updater script, state directory, or NAS mount. Normal application startup can migrate the index when a new version requires it. Read release notes before upgrading, and back up configuration/state with the app stopped before a major upgrade or planned downgrade. A previous executable alone cannot undo database migrations.

For existing config/service files that need changes in a future version, follow that version's release notes. Do not use `git pull` as a server update: the server needs the compiled release executable, and does not need Git or Go installed. Updating starts the usual incremental library scan.

Maintainers: pushes to `main` run verification. To publish a release, update the version in `main.go`, commit the changes, create a matching `vX.Y.Z` tag, and push it. GitHub Actions runs the checks, builds both static executables, and publishes their archives and checksums. Keep the tag and executable version consistent.

## Configuration

Paths may be absolute or relative to the configuration file. Unknown keys are rejected.

| Key | Default | Purpose |
| --- | --- | --- |
| `library` | Required | Existing photo directory, including nested folders |
| `state` | Required | Separate writable directory for SQLite, cache, and temporary renders |
| `listen` | `127.0.0.1:8080` | HTTP listening address |
| `cache_bytes` | `5368709120` | 5 GiB maximum for cached thumbnails and previews |
| `workers` | `1` | Concurrent image renderers, from 1 to 8 |
| `max_pixels` | `40000000` | Maximum source image pixel count decoded for a preview |

The cache limit excludes the SQLite database and its WAL. Old cache entries are evicted using recency of access. A growing state directory is expected as the index is built. Do not share a state directory between running instances. Back it up only with the app stopped, or use a SQLite-aware backup tool; copying just `library.sqlite` while it runs can miss WAL data. The index and cache are completely rebuildable. The SQLite tag catalog caches unique tags and counts; it is backfilled once when upgrading an existing index and maintained transactionally on tag changes/deletions. No extra metadata scan is needed for the upgrade. The browser caches up to 32 autocomplete responses for 30 seconds and invalidates them during/after scans.

The app sets a soft Go memory target of 256 MiB unless `GOMEMLIMIT` is supplied. This is **not** a hard process memory limit. Decoding a single large photo can temporarily exceed it. On a small server, retain one worker and lower `max_pixels`, for example to `12000000`. Raising workers raises peak memory use. Images above the limit remain indexed and searchable but show a preview error. Metadata packets are limited separately.

## Browsing and search

- The default view shows immediate subfolders first and images directly in the selected folder. Names use natural sorting, so `photo2` precedes `photo10`.
- Folder counts and covers include descendants. Folder and photo lists are independently paginated, with at most 60 of each shown per page.
- **Newest first** uses embedded capture dates where available and falls back to the file modification date. Folder date sorting uses the newest descendant image.
- Search defaults to **This folder + subfolders**. Switch to **This folder only** or **Entire library** as needed.
- Search lives in the top navbar. Plain words are case-insensitive filename/tag fragments; all terms must match. For example, `holiday 2025` can match a tag and a filename together.
- Focus the search field to see cached tags and image counts for the selected search scope. Start typing a tag to autocomplete it, then click a suggestion or press Down/Up and Enter. Escape dismisses suggestions; Enter without a selection submits the typed query.
- Use `tag:Ærø` for an exact tag, or `tag:"Family holiday"` for a tag containing spaces. Combine tags and words using AND, for example `lake tag:Ærø tag:Sommer`. Autocomplete inserts the correct quoting and preserves other search terms. Double quotes/backslashes inside a tag are escaped automatically.
- Click a tag in an image's Details panel to insert an exact tag filter in the navbar.
- Folder, search, scope, sort, page, and the open viewer image are bookmarkable in the URL.
- Use the viewer's arrows, left/right keys, or horizontal swipes to move through the current result set, including across page boundaries. Escape closes the viewer.

JPEG/JPG, PNG, WebP, and GIF are supported. GIFs and animated WebP/PNG use a static frame for previews. Thumbnails fit inside 480 px; viewer previews fit inside 1600 px. EXIF orientation is applied without modifying originals. Transparency is composited onto a light background. Previews are JPEG and contain no copied EXIF/GPS metadata. This is a preview gallery rather than a color-managed editing application.

Embedded tag sources are Windows `XPKeywords` (UTF-16LE, semicolon separated), IPTC Keywords, and XMP Dublin Core subjects in JPEG/PNG/WebP containers where those fields are supported. GIF metadata is not parsed. PNG international-text XMP and compressed XMP are supported. Tags are trimmed, Unicode-normalized for matching, and deduplicated without replacing their display spelling. Standard PNG `Keywords` text is also recognized. Legacy IPTC uses its declared character set, falling back to Latin-1 when no encoding is declared. No tags are guessed from the directory name, and no sidecar files are read.

## Rescanning and unavailable mounts

Use **Library status → Scan library**. Check **Re-read all metadata** if tags were changed by another program without changing file size or modification time. From the command line, with the web service stopped:

```sh
folderlight -config /etc/folderlight/config.json -scan
folderlight -config /etc/folderlight/config.json -scan -force
```

There are no timers, filesystem watchers, or periodic library scans. The browser polls scan status, not the library filesystem.

Unreadable directories are reported and their previous index entries are preserved. A scan reconciles deletions only after it successfully reads a directory's complete contents. A cancelled scan can be restarted safely. Damaged image/metadata headers produce warnings while filenames remain indexed. Errors are shown in Library status and the server log; stored metadata warnings appear in image Details.

The app remembers the library root's path, filesystem device, and inode. If the root changes, or a previously indexed subdirectory moves to a different filesystem, scanning preserves the old index and reports a warning. This protects against the common case of an unavailable NAS mount exposing its empty mountpoint. An empty but otherwise indistinguishable directory on the same filesystem cannot reliably be identified as an unavailable mount; validate mount readiness in your service setup.

If you intentionally replace or relocate the library root, stop the app and configure a **new empty state directory** to rebuild the index for the new root. Keep the old state directory until you have verified the new setup. Restore the original mount/path to reuse its existing state. Symlinked entries are skipped and cannot be served; the configured root path itself is resolved at startup.

## HTTP interface

| Method and endpoint | Parameters / result |
| --- | --- |
| `GET /api/browse` or `/api/search` | `folder`, `q`, `scope=tree\|folder\|all`, `sort=name\|date`, `tag`, `page`, `limit` (1–120); paginated folders/photos and counts |
| `GET /api/tags` | `prefix`, `folder`, `scope=tree\|folder\|all`, `limit` (1–20); cached/scoped tag suggestions with image counts |
| `GET /api/photo` | `id`; filename, relative folder/path, dimensions, dates, tags, warning |
| `GET /api/image` | `id`, `size=thumb\|preview`; cached JPEG, private ETag |
| `GET /api/status` | Current/last scan progress, counters, and most recent warning |
| `POST /api/rescan` | Optional `force=1`; requires `X-Folderlight-Request: 1` and a matching Origin when supplied |

Folder paths are relative POSIX paths. Dates are Unix seconds except `mtime`, which is Unix nanoseconds. Image IDs remain stable for existing paths during normal rescans. API failures return JSON `{"error":"…"}`. Image generation can return 503 with `Retry-After: 2` when its bounded queue is full; clients should retry. Invalid or oversized images return 422. Deploy the app at the root of its hostname; URL subpath hosting is not supported.

## Build and verify

Build on a development machine with Go 1.26 or newer and network access for the pinned module dependencies:

```sh
go mod download
CGO_ENABLED=0 go test ./...
go vet ./...
# Optional UI helper tests on the development machine (Node.js only):
node --test scripts/search-ui.test.cjs
sh scripts/build.sh
```

Both executables, release archives, checksums, and dependency licenses are written to `dist/`. No C compiler is needed. For a race check on a development machine with a C compiler, use `go test -race ./...`.

Run the opt-in large-library benchmark:

```sh
FOLDERLIGHT_BENCH=1 CGO_ENABLED=0 go test -run '^TestLargeLibrary$' -v -timeout 15m
```

It creates 100,000 hardlinked, tiny tagged JPEGs in a temporary directory, scans them, and reports warm search latency and process memory. It measures index/metadata work rather than full-size photo reading, NAS speed, or thumbnail generation. See `VERIFICATION.md` for measured results and verification limits.
