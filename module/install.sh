#!/bin/sh
# Put the second governor in place of the first inside the installed Magisk
# module and switch over without a reboot. The first governor's binary stays in
# the module directory, so `install.sh --first` switches back.
#
# usage: install.sh [--first]
set -e
S=${SERIAL:-PDNP05J000047054}
HERE=$(cd "$(dirname "$0")" && pwd)
T=/data/local/tmp/astra-install
adb -s $S shell "rm -rf $T; mkdir -p $T"
if [ "$1" = --first ]; then
  FIRST=$HERE/../../../governor/magisk-module
  adb -s $S push "$FIRST/service.sh" "$FIRST/module.prop" "$HERE/switch.sh" $T/ >/dev/null
else
  (cd "$HERE/.." && GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o build/astrad2 .)
  adb -s $S push "$HERE/../build/astrad2" "$HERE/service.sh" "$HERE/module.prop" "$HERE/switch.sh" $T/ >/dev/null
fi
adb -s $S shell su -c "'sh $T/switch.sh; rm -rf $T'"
echo "installed; the daemon starts about 15 s from now"
