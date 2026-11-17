#!/bin/sh
# Update the installed Magisk module from a computer and switch over without a
# reboot. The module must already be installed (the zip, from the Magisk app).
#
# usage: install.sh
set -e
A="adb${SERIAL:+ -s $SERIAL}"  # SERIAL picks a device when more than one is attached
HERE=$(cd "$(dirname "$0")" && pwd)
T=/data/local/tmp/astra-install
$A shell "rm -rf $T; mkdir -p $T"
(cd "$HERE/.." && GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -o build/astrad2 .)  # built as build-zip.sh builds it, so a release's binary and an installed one match
$A push "$HERE/../build/astrad2" "$HERE/service.sh" "$HERE/module.prop" "$HERE/switch.sh" $T/ >/dev/null
$A shell su -c "'sh $T/switch.sh; rm -rf $T'"
echo "installed; the daemon starts about 15 s from now"
