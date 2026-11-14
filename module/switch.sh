#!/system/bin/sh
# Runs on the tablet as root, from the directory holding the files to install:
# stops the running governor and its supervisor, copies the files into the
# module and starts the new supervisor.
set -e
M=/data/adb/modules/astra
T=${0%/*}
[ -x $M/astrad ] || { echo "the astra module is not installed"; exit 1; }
# The supervisor goes first so it does not restart the daemon under us. The
# daemon restores every setting it holds when it gets the signal.
for p in $(ps -A -o PID,ARGS | grep "$M/service.sh" | grep -v grep | awk '{print $1}'); do kill $p; done
pkill -TERM -x astrad || true
pkill -TERM -x astrad2 || true
sleep 2
for f in astrad2 service.sh module.prop; do [ -f $T/$f ] && cp $T/$f $M/$f; done
chmod 755 $M/service.sh
[ -f $M/astrad2 ] && chmod 755 $M/astrad2
chmod 644 $M/module.prop
nohup sh $M/service.sh >/dev/null 2>&1 &
