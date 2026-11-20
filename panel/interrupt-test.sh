#!/system/bin/sh
# Cuts calibration runs short on purpose, to check that a packet meeting the
# panel's power-down does no harm. Run on the tablet as root with calibration
# switched on in the app; watch the kernel log from a computer meanwhile
# (adb exec-out su -c "dmesg -w"), because the fault this looks for only shows
# at the wake after the one that caused it, and ends in a restart.
#
# Each cycle wakes the screen, waits for the helper to start and sends the
# sleep key at a random moment within 4.5 s of that, then leaves the screen
# off for OFF seconds. With MODE alt, every other cycle lets the helper finish.
#
# usage: interrupt-test.sh N OFF [interrupt|alt|complete]
N=$1; OFF=$2; MODE=${3:-interrupt}
log() { echo "$(date +%H:%M:%S) $(cut -d' ' -f1 /proc/uptime) $*"; }
helper() { pgrep -f panel-table.sh | head -1; }
log start n=$N off=$OFF mode=$MODE boot=$(cat /proc/sys/kernel/random/boot_id)
i=0
while [ "$i" -lt "$N" ]; do
  i=$((i+1))
  kind=$MODE
  [ "$MODE" = alt ] && { if [ $((i % 2)) = 1 ]; then kind=complete; else kind=interrupt; fi; }
  log cycle $i $kind
  input keyevent 224
  log woke
  t=0; pid=
  while [ "$t" -lt 300 ]; do pid=$(helper); [ -n "$pid" ] && break; usleep 100000; t=$((t+1)); done
  if [ -z "$pid" ]; then
    # After a run that was cut short the service waits for a full sleep and
    # wake, and a short one does not always count: nothing to cut short here.
    log no-helper
    input keyevent 223
  elif [ "$kind" = interrupt ]; then
    log helper-start
    d=$(( $(od -An -N2 -tu2 /dev/urandom) % 4500 ))
    usleep $((d * 1000))
    alive=$(helper)
    log sleep-key delay=${d}ms helper=${alive:+running}
    input keyevent 223
  else
    log helper-start
    t=0
    while [ "$t" -lt 700 ] && [ -n "$(helper)" ]; do usleep 100000; t=$((t+1)); done
    log helper-end
    sleep 8
    input keyevent 223
  fi
  sleep "$OFF"
done
log done boot=$(cat /proc/sys/kernel/random/boot_id)
