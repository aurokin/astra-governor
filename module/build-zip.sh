#!/bin/sh
# Build a Magisk module zip for a fresh install from the Magisk app:
# build/astra-governor-<version>.zip
set -e
HERE=$(cd "$(dirname "$0")" && pwd)
ROOT=$HERE/..
VERSION=$(sed -n 's/^version=//p' "$HERE/module.prop")
(cd "$ROOT" && go test ./... >/dev/null && GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -o build/astrad2 .)
STAGE=$(mktemp -d)
trap 'rm -rf "$STAGE"' EXIT
cp -R "$HERE/zip/META-INF" "$STAGE/"
cp "$HERE/module.prop" "$HERE/service.sh" "$HERE/customize.sh" "$STAGE/"
cp "$ROOT/build/astrad2" "$STAGE/astrad2"
OUT=$ROOT/build/astra-governor-$VERSION.zip
rm -f "$OUT"
(cd "$STAGE" && zip -qr -X "$OUT" META-INF module.prop service.sh customize.sh astrad2)
echo "$OUT"
