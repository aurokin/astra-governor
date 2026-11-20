#!/system/bin/sh
# Run by Magisk at the boot after the module is removed. Nothing of the module
# survives a reboot in the kernel or the panel; this clears its files and app.
rm -f /data/adb/astra/astrad2.log /data/adb/astra/astrad2.log.1 /data/adb/astra/governor
rmdir /data/adb/astra 2>/dev/null
rm -rf /data/adb/astra-panel-rate
(
  until [ "$(getprop sys.boot_completed)" = 1 ]; do sleep 2; done
  pm uninstall dev.auro.astra
) >/dev/null 2>&1 &
