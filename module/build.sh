#!/bin/sh
# Test and build everything the module carries, into build/:
# astrad2 (the governor), panel-service and panel-mode.jar (display
# calibration) and app/astra.apk (the settings app).
# SKIP_APP=1 leaves the app out, for a tablet-side update of the daemons only.
set -e
ROOT=$(cd "$(dirname "$0")/.." && pwd)
SDK=${ANDROID_HOME:-/opt/homebrew/share/android-commandlinetools}
cd "$ROOT"
mkdir -p build
go test ./... >/dev/null
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -buildvcs=false -o build/astrad2 .
(cd panel && go test ./... >/dev/null && python3 test_panel_table.py >/dev/null 2>&1 &&
  GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags='-s -w' -o ../build/panel-service .)
# The helper that asks SurfaceFlinger for a brief mode change, run with app_process.
CLASSES=$(mktemp -d)
trap 'rm -rf "$CLASSES"' EXIT
javac --release 8 -Xlint:-options -classpath "$SDK/platforms/android-36/android.jar" -d "$CLASSES" panel/mode/PanelMode.java
"$SDK/build-tools/36.0.0/d8" --min-api 35 --lib "$SDK/platforms/android-36/android.jar" --output build/panel-mode.jar "$CLASSES/dev/auro/astra/panel/PanelMode.class"
[ -n "$SKIP_APP" ] || sh app/build.sh
