# Verification

Verified on 18 October 2026 with Go 1.27.2 on Linux x86-64, AMD Ryzen 5 5500U (6 cores / 12 logical CPUs). Results are development-machine measurements, not guarantees for an undecided Alpine server.

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
