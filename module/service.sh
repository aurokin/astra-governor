#!/system/bin/sh
# Starts the governor daemon once Android has booted and restarts it if it exits.
# It runs late and outside the boot sequence, so a broken daemon cannot hold up boot.
MODDIR=${0%/*}
until [ "$(getprop sys.boot_completed)" = 1 ]; do sleep 2; done
sleep 15
mkdir -p /data/adb/astra
while [ ! -e "$MODDIR/disable" ] && [ ! -e "$MODDIR/remove" ]; do
  "$MODDIR/astrad2" -listen 127.0.0.1:7878 -log /data/adb/astra/astrad2.log
  sleep 5
done
