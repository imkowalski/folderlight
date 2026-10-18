#!/bin/sh
# Update an existing native Alpine/OpenRC installation from a GitHub release.
set -eu

repository='imkowalski/folderlight'
install_dir='/usr/local/bin'
release="${1:-latest}"
if [ "$#" -gt 1 ]; then
    echo "Usage: folderlight-update [vVERSION]" >&2
    exit 1
fi
case "$release" in
    latest) base="https://github.com/$repository/releases/latest/download" ;;
    v[0-9]*)
        case "$release" in *[!A-Za-z0-9._-]*) echo 'Invalid release tag' >&2; exit 1 ;; esac
        base="https://github.com/$repository/releases/download/$release"
        ;;
    *) echo 'Use latest or a release tag such as v1.1.1' >&2; exit 1 ;;
esac
[ "$(id -u)" -eq 0 ] || { echo 'Run as root inside the Alpine container.' >&2; exit 1; }
case "$(uname -m)" in
    x86_64) arch=amd64 ;;
    aarch64) arch=arm64 ;;
    *) echo 'Unsupported architecture' >&2; exit 1 ;;
esac
for utility in wget sha256sum tar install mktemp rc-service; do
    command -v "$utility" >/dev/null || { echo "Missing command: $utility" >&2; exit 1; }
done
[ -x "$install_dir/folderlight" ] || { echo 'Install Folderlight first.' >&2; exit 1; }

lock='/run/folderlight-update.lock'
mkdir "$lock" 2>/dev/null || { echo "An update is already running (lock: $lock)." >&2; exit 1; }
temporary=''
staged=''
cleanup() {
    [ -z "$temporary" ] || rm -rf "$temporary"
    [ -z "$staged" ] || rm -f "$staged"
    rmdir "$lock"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM
temporary=$(mktemp -d)
asset="folderlight-linux-$arch"
archive="$asset.tar.gz"
echo "Downloading $repository ($release, $arch)…"
wget -q -O "$temporary/$archive" "$base/$archive"
wget -q -O "$temporary/SHA256SUMS" "$base/SHA256SUMS"
awk -v name="$archive" '$2 == name { print; count++ } END { if (count != 1) exit 1 }' \
    "$temporary/SHA256SUMS" > "$temporary/selected.sha256"
(cd "$temporary" && sha256sum -c selected.sha256)
tar -xzf "$temporary/$archive" -C "$temporary" "$asset"
[ -f "$temporary/$asset" ] && [ ! -L "$temporary/$asset" ]
chmod 0755 "$temporary/$asset"
"$temporary/$asset" -version

# All downloads and validation finish before interrupting the running service.
staged=$(mktemp "$install_dir/.folderlight-update.XXXXXX")
install -m 0755 "$temporary/$asset" "$staged"
cp -p "$install_dir/folderlight" "$install_dir/folderlight.previous"
running=0
if rc-service folderlight status >/dev/null 2>&1; then
    running=1
    rc-service folderlight stop
fi
mv -f "$staged" "$install_dir/folderlight"
staged=''
if [ "$running" -eq 1 ]; then
    if ! rc-service folderlight start; then
        echo 'Update installed, but the service failed to start. Inspect /var/log/folderlight.log.' >&2
        echo "Previous executable: $install_dir/folderlight.previous" >&2
        exit 1
    fi
    rc-service folderlight status
fi
echo 'Updated. Configuration, index, cache, and source photos were preserved.'
