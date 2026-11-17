# Run by Magisk while installing the module.
DEVICE=$(getprop ro.product.device)
BUILD=$(getprop ro.build.display.id)
ui_print "- Astra governor for the RedMagic Astra (NP05J)"
case "$(getprop ro.product.model)$(getprop ro.product.name)$DEVICE" in
  *NP05J*|*np05j*) ;;
  *) ui_print "! This is not an NP05J ($DEVICE): the module installs, but the daemon will refuse to start" ;;
esac
case "$BUILD" in
  *11.0.7*) ;;
  *) ui_print "! Built and measured on RedMagicOS 11.0.7; this is $BUILD" ;;
esac
set_perm "$MODPATH/astrad2" 0 0 0755
set_perm "$MODPATH/service.sh" 0 0 0755
ui_print "- The governor starts about 15 s after the next boot"
