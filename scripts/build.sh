#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
mkdir -p dist
for arch in amd64 arm64; do
    CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath -ldflags='-s -w' -o "dist/folderlight-linux-$arch" .
done
cp README.md config.example.json LICENSE dist/
cp deploy/folderlight.openrc dist/
cp scripts/update.sh dist/folderlight-update
if [ -f VERIFICATION.md ]; then cp VERIFICATION.md dist/; fi
mkdir -p dist/licenses
cp "$(go env GOROOT)/LICENSE" dist/licenses/Go-LICENSE
go list -deps -f '{{with .Module}}{{.Path}} {{.Dir}}{{end}}' . | sort -u | while read -r module module_dir; do
    [ -n "$module_dir" ] || continue
    [ "$module" != folderlight ] || continue
    safe_name=$(printf '%s' "$module" | tr '/' '_')
    for license in "$module_dir"/LICENSE* "$module_dir"/COPYING* "$module_dir"/NOTICE*; do
        [ -f "$license" ] || continue
        cp -f "$license" "dist/licenses/${safe_name}-$(basename "$license")"
    done
done
go version -m dist/folderlight-linux-amd64 > dist/BUILD-INFO.txt
cd dist
for arch in amd64 arm64; do
    tar -czf "folderlight-linux-$arch.tar.gz" "folderlight-linux-$arch" README.md VERIFICATION.md config.example.json folderlight.openrc folderlight-update LICENSE licenses BUILD-INFO.txt
done
sha256sum folderlight-linux-amd64 folderlight-linux-arm64 folderlight-linux-amd64.tar.gz folderlight-linux-arm64.tar.gz > SHA256SUMS
