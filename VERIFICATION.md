# Verification

Verified on 18–19 October 2026 with Go 1.27.2 on Linux x86-64, AMD Ryzen 5 5500U (6 cores / 12 logical CPUs). Results are development-machine measurements, not guarantees for the deployed Alpine/NAS server.

## Version 1.3: preview generation and quicker viewing

The renderer now reduces large images using area averaging before the final Catmull-Rom pass and applies EXIF rotation/mirroring to the smaller result. Tests cover all eight transforms, nonzero source bounds, fine-pattern antialiasing, alpha compositing, and YCbCr colors. Cache tests cover thumbnail reuse with the source library offline, background-job promotion without duplicate rendering, timestamp throttling, cache replacement accounting, and an image request completing while a dedicated video worker is busy. Embedded RAW tests cover numeric camera orientation, avoiding an unnecessary metadata probe/development, and rejection of corrupt or undersized JPEG candidates. In Alpine 3.24.2, real ExifTool extraction from `synthetic-preview.dng` verified its base64 JPEG and numeric Orientation 6; the cache produced a correctly rotated 96 × 128 preview. The existing DNG without a preview exercised LibRaw fallback. HEIC, MP4/WebM conversion, unchanged source bytes, and HTTP range delivery were rechecked using the real optional packages.

The viewer displays a thumbnail while loading detail, swaps in the loaded image node, and prefetches one next still at low priority. JavaScript tests verify the swap, stale-request guards, cancellation, video/bookmark exclusions, and retaining unchanged grid cards. Embedded web assets use gzip and representation-specific ETags; automated tests cover decompression, conditional requests, and compression negotiation. HTTP verification reduced `app.js` from 27,773 to 8,357 bytes (70% fewer transferred bytes).

Browser checks verified immediate 480-pixel thumbnails followed by 1600-pixel previews, repeated next-item navigation, crossing from item 60 to 61 and returning to page 2, a rotated photo and details at 390 × 844, and restoring a bookmarked viewer after reload. No browser warnings/errors were observed. These are viewport checks, not physical-device/NAS timings. Go race tests, vet, JavaScript tests, and shell syntax checks passed.

Preview benchmark command (Go 1.27.2, Linux amd64, Ryzen 5 5500U):

```sh
GOMAXPROCS=1 go test -run '^$' -bench BenchmarkPreview12MP -benchtime=3x -count=2 -benchmem
```

Each measurement decodes a synthetic 4000 × 3000 JPEG, resizes/orients it, and JPEG-encodes the output. The baseline uses the version 1.2 renderer in the same test executable. Results average two runs of three renders each, on one Go execution thread. This is a local CPU benchmark: it excludes source-file/NAS I/O, HTTP, cache hits, browser rendering, and video conversion.

| Output | Version 1.2 | Version 1.3 | Speedup | Allocated bytes/render, old → new |
| --- | ---: | ---: | ---: | ---: |
| Thumbnail, 480 px | 3.620 s | 0.561 s | 6.4× | 207.5 → 24.2 MiB |
| Thumbnail, rotated | 3.452 s | 0.567 s | 6.1× | 207.5 → 24.9 MiB |
| Viewer, 1600 px | 4.076 s | 0.956 s | 4.3× | 318.0 → 109.6 MiB |
| Viewer, rotated | 3.930 s | 0.967 s | 4.1× | 318.0 → 116.9 MiB |

Allocation figures are cumulative bytes allocated during one render, **not peak process RSS or a memory cap**. Actual photos, CPUs, NAS latency, and concurrent work change the timings. No deployed Proxmox/NAS measurements were available. The 100,000-entry index measurements below remain the version 1.0 baseline.

## Version 1.2: gallery password, version settings, and optional media

Settings now displays the executable version, scan controls, optional gallery password controls, and installed decoder status. Password tests cover salted hashing, incorrect passwords, session tampering/expiry, login throttling, changing/removing protection, previous-session invalidation, cross-origin/missing-header mutation rejection, and protection of browsing/search/tags/details/images/video/scan endpoints. Recovery configuration works without an available library root. Command-line reset was exercised against the running disposable demo; the next browser reload opened the gallery without a login.

Built a static test executable and ran the opt-in media integration test inside Alpine 3.24.2 with FFmpeg 8.1.2, libheif 1.23.4, LibRaw 0.22.1, and ExifTool 13.55 installed from Alpine repositories. A separate user/mount/process namespace provided `/proc` for verified descriptor input. Synthetic DNG, HEIC, MP4 (with audio), and WebM sources were indexed and their JPEG thumbnails generated. Windows XPKeywords and XMP Unicode tags in DNG/HEIC were decoded and deduplicated. Both video containers were converted to seekable MP4, and preview delivery returned correct HTTP 206 range responses. File bytes and directory entries were checked after conversion to confirm sources were unchanged. The browser demo used a read-only bind mount of its synthetic source library. GitHub Actions also runs these decoder integration tests inside Alpine before publishing releases.

Browser checks verified Settings/version/tool status at desktop and 390 × 844, the whole-gallery login screen, signing in to the selected folder, signing out, and CLI recovery. The video element loaded a four-second 640 × 480 MP4 with `readyState=4` and no codec error. The preview screen showed native video controls. Physical-device touch gestures, a full Proxmox/OpenRC boot, and real camera-model/codec collections were not tested. Camera RAW/HEIC/video support depends on the installed tool versions; the included DNG is a generated Bayer test fixture, not a representative camera corpus. Large-library performance has not been re-benchmarked for this version, and the measurements below remain the 1.0 baseline.

## Version 1.1.1: public releases and Alpine updates

Re-ran the Go race tests, `go vet`, JavaScript helper tests, and shell syntax checks. Built both Linux release archives with the updater included. In an isolated Alpine 3.24.2 root filesystem, exercised the production updater using local release downloads and a mock OpenRC command: successful checksum verification and binary replacement, preservation of the previous binary/config/state, restarting a running service, leaving a stopped service stopped, and rejecting failed downloads or invalid checksums before touching the service/binary. Network downloading and a real OpenRC lifecycle are separate deployment checks; the mock does not verify those.

GitHub Actions verifies changes and publishes static amd64/arm64 packages and checksums for version tags. Release download/update instructions are in `README.md`. Updating changes the executable; it does not change an existing service or configuration file.

## Version 1.1 update: navbar search and cached tags

The search form now lives solely in the top navbar, with folder scope alongside it and sorting in the folder heading. A persistent SQLite tag catalog caches unique labels and image counts, backfills existing indexes without reading image files, and updates inside metadata/deletion transactions. `/api/tags` provides bounded, Unicode-aware prefix suggestions for the selected folder scope or whole library. The browser caches at most 32 responses for 30 seconds and clears those during/after scans.

Verified migration/restart, tag additions/replacements, cascade deletion, transactional rollback, scoped counts, exact `tag:` searches (including spaces and escaped characters), AND combinations, malformed-query errors, and suggestion endpoint validation. `go test -race ./...` and `go vet ./...` pass. The standalone Node helper tests cover active-token completion, quoted/spaced tags, escaped quotes, and cursor edits without requiring a browser or new runtime dependency.

Browser checks at the normal desktop size and 390 × 844 verify navbar-only search, Unicode autocomplete, counts, keyboard selection, pointer selection, and combining `tag:"Ærø" tag:"Family"` (7 matching demo images). The previously entered unquoted `tag:Ærø` URL also now works (13 matches in the demo folder).

The large-library measurements below are the **version 1.0 baseline**, before the tag catalog was added. Full scan duration/storage have not been re-benchmarked for version 1.1. Alpine compatibility is separately rechecked against the updated release.

## Automated checks

- `CGO_ENABLED=0 go test -v ./...`: passed, including Windows UTF-16LE XPKeywords with Danish/Polish characters and a surrogate pair, IPTC UTF-8 keywords, XMP subjects, compressed/uncompressed PNG XMP, metadata decompression limits, and real WebP decoding.
- Scanner tests: first scan, unchanged scan, forced metadata refresh with preserved size/timestamp, deleted descendants, corrupt files, unavailable/replaced library root, unreadable subdirectory, cancelled scan, and persistent SQLite reuse.
- Image/API tests: all eight EXIF orientation transforms, simultaneous requests for the same preview, cache eviction, pixel limits, literal search, Unicode case folding, exact tags, folder/global boundaries, natural sorting, pagination, invalid API inputs, private ETags, cross-origin rescan rejection, traversal and symlink rejection.
- `go test -race ./...`: passed. No races reported.
- `go vet ./...`: passed.
- JavaScript syntax checked with `node --check web/app.js`; shell scripts checked with `sh -n`.

## Alpine compatibility

The x86-64 executable ran inside the official Alpine Linux **3.24.2** minirootfs, using a rootless user/mount namespace and chroot on the development host's kernel. The photo directory was bind-mounted **read-only**. No additional Alpine packages were installed.

Passed version output, a forced command-line scan, HTTP startup, scoped/global metadata search, generation and delivery of a JPEG preview, and delivery of the embedded web interface. A write probe to the read-only photo mount failed as expected. A Windows-tagged sample returned `Sommer`, `Ærø`, `Łódź`, and `Coast` in the JSON response.

Both Linux binaries were built with `CGO_ENABLED=0`. ELF inspection confirms static executables without a dynamic interpreter. ARM64 was cross-compiled; no ARM64 hardware execution was performed. The OpenRC file was syntax checked, but a complete OpenRC boot/service lifecycle was not exercised in the minimal chroot.

## Browser verification

Verified in the Codex in-app browser using 76 synthetic tagged JPEGs, nested directories, and enough search results for multiple pages:

- Compact desktop layout: small application header, inline folder/count heading, seven photo columns at the observed desktop width, compact folder covers, and sticky search controls.
- Phone breakpoint at **390 × 844**: three photo columns, two folder columns, wrapped search/sort controls, full-screen image viewer, and an overlay details panel.
- Folder browsing, Unicode keyword search, switching direct-folder/global scopes, cross-page viewer navigation, details/tags, saved viewer URL restoration, and Escape closing.
- No browser console errors or warnings during the checked flows.

These are browser/viewport checks rather than tests on a physical phone. Native touch swipe gestures still need a physical-device check. Demo photographs are synthetic fixtures and are not included in release binaries or the user's photo library.

## 100,000-image benchmark

Command:

```sh
FOLDERLIGHT_BENCH=1 CGO_ENABLED=0 go test -run '^TestLargeLibrary$' -v -timeout 15m
```

Corpus: 100,000 directory entries across 1,000 folders, hardlinked to one **790-byte JPEG with four embedded tags** (`Family`, `Sommer`, `Łódź`, `Ærø`). Each file's header/metadata was read and indexed separately. Queries include result counts and a first page of up to 60 items. Reported latencies are 20 warm queries per scenario.

| Operation | Median | p95 |
| --- | ---: | ---: |
| Root folder browsing | 2.724 ms | 2.944 ms |
| Exact tag within a folder tree | 1.538 ms | 1.766 ms |
| Global filename fragment | 1.209 ms | 1.895 ms |
| Global common tag + filename terms | 4.826 ms | 5.994 ms |
| Global exact tag matching all 100,000 images | 17.696 ms | 18.177 ms |
| Global two-character fragment | 60.937 ms | 63.003 ms |

| Scan/storage measurement | Result |
| --- | ---: |
| Initial metadata scan | 2 min 50.853 s |
| Unchanged incremental scan | 17.238 s |
| Sampled peak live Go heap | 3.7 MiB |
| Process peak resident memory (RSS) | 43.6 MiB |
| Go reserved memory at completion | 19.4 MiB |
| SQLite database, WAL, and shared-memory file | 125.8 MiB |

RSS includes SQLite/native allocations and other process memory that is not part of Go's live heap. The index is disk backed. Larger images, network storage, cold caches, additional tags, deeper paths, and thumbnail rendering can materially change scan duration and memory. The tiny hardlinked corpus does not measure representative NAS throughput, cold photo-file I/O, or full-size image decoding. No thumbnail rendering occurred during this benchmark. Some development checks/builds ran concurrently on the same workstation.
