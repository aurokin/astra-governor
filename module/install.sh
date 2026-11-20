#!/bin/sh
# Update the installed Magisk module from a computer and switch over without a
# reboot. The module must already be installed (the zip, from the Magisk app).
# The app is not touched: install build/app/astra.apk with adb for that.
#
# usage: install.sh
set -e
A="adb${SERIAL:+ -s $SERIAL}"  # SERIAL picks a device when more than one is attached
HERE=$(cd "$(dirname "$0")" && pwd)
ROOT=$HERE/..
T=/data/local/tmp/astra-install
SKIP_APP=1 sh "$HERE/build.sh"  # built as build-zip.sh builds it, so a release's binaries and installed ones match
$A shell "rm -rf $T; mkdir -p $T"
$A push "$ROOT/build/astrad2" "$ROOT/build/panel-service" "$ROOT/build/panel-mode.jar" "$ROOT/panel/panel-table.sh" \
  "$HERE/service.sh" "$HERE/uninstall.sh" "$HERE/module.prop" "$HERE/switch.sh" $T/ >/dev/null
$A shell su -c "'sh $T/switch.sh; rc=\$?; rm -rf $T; exit \$rc'"
echo "installed; the governor starts about 15 s from now"
