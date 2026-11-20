#!/system/bin/sh
# Root helper for the NP05J calibration service. check* returns 0 when ready,
# 1 when the requested table needs applying, and 2 for a conflict or unavailable panel.
set -efu

case "${1:-}" in
    check60|apply60) profile=60 ;;
    check5994|apply5994) profile=5994 ;;
    checkgba|applygba) profile=gba ;;
    checknes|applynes) profile=nes ;;
    stock|checkstock) profile=stock ;;
    *) echo 'Usage: panel-table.sh {apply60|apply5994|applygba|applynes|check60|check5994|checkgba|checknes|stock|checkstock}' >&2; exit 2 ;;
esac
[ "$#" -eq 1 ] || exit 2
command=$1
blocked() { echo "$*" >&2; exit 2; }
[ "$(id -u)" = 0 ] || blocked 'Root access is required.'
[ "$(getprop ro.product.model)" = NP05J ] || blocked 'Unsupported tablet or build.'
case "$(getprop ro.build.display.id)" in
    REDMAGICOS11.0.7_NP05J_GB|REDMAGICOS11.0.8MR1_NP05J_GB) ;;
    *) blocked 'Unsupported tablet or build.' ;;
esac
umask 077
dir=/data/adb/astra-panel-rate
journal=$dir/table-session
mkdir -p "$dir" || blocked 'Cannot open the calibration state directory.'
exec 9>"$dir/lock"
flock -n 0 <&9 || blocked 'Panel calibration lock is busy.'
if [ "$profile" = stock ] && [ ! -e "$journal" ]; then exit 0; fi
[ ! -e "$dir/session" ] || blocked 'A manual panel preset owns the display. Restore it to stock first.'

panel_on() {
    [ "$(cat /sys/class/drm/card0-DSI-1/dpms)" = On ] &&
        [ "$(cat /proc/driver/lcd_state)" = 0 ]
}
awake() { dumpsys power | grep 'mWakefulness=Awake' >/dev/null && panel_on; }
# The controller is reached through the display driver's own command files in
# debugfs. A command sent that way waits for the lock the driver holds while
# it powers the panel up or down, and is refused once the display link is off.
# The vendor's /sys/reg_debug nodes take no lock: a packet that met the panel's
# off command left the link's interrupt raised, and the tablet hung at the
# next wake (2026-11-19, docs/display.md).
# --- link to the controller ---
debug=/dev/astra-panel-debug
dsi=$debug/dri/0/DSI-1
if [ ! -e "$dsi/tx_cmd" ]; then
    mkdir -p "$debug" && chmod 700 "$debug" && mount -t debugfs debugfs "$debug" 2>/dev/null || :
    [ -e "$dsi/tx_cmd" ] && [ -e "$dsi/rx_cmd" ] || blocked 'The display driver has no command interface here.'
fi
# A packet header for the driver: type, unused, channel 0, low-power mode, no
# wait, then the payload's length in two bytes.
write_packet() {
    left=$1 bytes=
    while [ -n "$left" ]; do
        rest=${left#??}
        bytes="$bytes 0x${left%"$rest"}"
        left=$rest
    done
    panel_on || return 1
    # Generic long write. The file takes the request whatever becomes of it;
    # reading it back says whether the driver sent it.
    echo "0x29 0x00 0x00 0x02 0x00 0x00 $(printf '0x%02x' $((${#1} / 2)))$bytes" > "$dsi/tx_cmd" || return 1
    [ "$(cat "$dsi/tx_cmd")" = 'last_cmd_tx_sts:0x1' ]
}
# Select the address, then a generic read of four bytes. Never more than four:
# longer reads overrun a buffer in the controller's firmware protocol.
read_word() {
    write_packet "90$1" || return 1
    panel_on || return 1
    echo '0x04 0x14 0x00 0x00 0x02 0x00 0x00 0x01 0x91' > "$dsi/rx_cmd" 2>/dev/null || return 1
    reply=$(cat "$dsi/rx_cmd" 2>/dev/null) || return 1
    set -- $reply
    [ "$#" -eq 4 ] || return 1
    case "$1 $2 $3 $4" in
        '0x'??' 0x'??' 0x'??' 0x'??) ;;
        *) return 1 ;;
    esac
    value="${4#0x}${3#0x}${2#0x}${1#0x}"
    case "$value" in *[!0-9a-f]*) return 1 ;; esac
    echo "$value"
}

# H then V for each native mode, in wire order. Content profiles share the four higher modes.
addresses="240181e7 280181e7 2c0181e7 300181e7 340181e7 380181e7 3c0181e7 400181e7 440181e7 480181e7"
field() {
    ntsc= gba= nes=
    case "$1" in
        240181e7) mode=1; factory=004000e4; corrected=004000d8; ntsc=004000b8; gba=004000d0; nes=004000e8 ;;
        280181e7) mode=1; factory=01200aed; corrected=01200b14; ntsc=01200b4d; gba=01200b35; nes=01200af3 ;;
        2c0181e7) mode=2; factory=004000e4; corrected=004000d8 ;;
        300181e7) mode=2; factory=0120054d; corrected=01200567 ;;
        340181e7) mode=3; factory=004000e4; corrected=004000f4 ;;
        380181e7) mode=3; factory=0120027d; corrected=0120027a ;;
        3c0181e7) mode=4; factory=004000e4; corrected=004000d4 ;;
        400181e7) mode=4; factory=01200115; corrected=01200128 ;;
        440181e7) mode=5; factory=004000e4; corrected=004000c4 ;;
        480181e7) mode=5; factory=0120002d; corrected=0120004a ;;
        *) return 1 ;;
    esac
    case "$profile" in
        stock) wanted=$factory ;;
        60) wanted=$corrected ;;
        5994) wanted=${ntsc:-$corrected} ;;
        gba) wanted=${gba:-$corrected} ;;
        nes) wanted=${nes:-$corrected} ;;
    esac
}
valid_word() {
    case "$1" in
        '') return 1 ;;
        "$factory"|"$corrected"|"$ntsc"|"$gba"|"$nes") return 0 ;;
        *) return 1 ;;
    esac
}
write_word() {
    number=$((0x$2))
    wire=$(printf '%02x%02x%02x%02x' "$((number & 255))" "$(((number >> 8) & 255))" \
        "$(((number >> 16) & 255))" "$(((number >> 24) & 255))") || return 1
    write_packet "80${1}${wire}" &&
        write_packet 806c0181e701000000 &&
        write_packet 807c0181e700000000 &&
        [ "$(read_word c80086e6)" = e781017c ] &&
        [ "$(read_word cc0086e6)" = 00000000 ] &&
        [ "$(read_word "$1")" = "$2" ]
}

# One receipt contains both the completed fields and any interrupted word.
# Replacing it atomically keeps the manual helper and the service in agreement.
get_record() {
    if [ -f "$journal" ]; then awk -v key="$1" '$1 == key { $1=""; print substr($0,2) }' "$journal"; fi
}
set_record() {
    awk -v key="$1" '$1 != key' "$journal" > "$journal.new" || return 1
    if [ "$#" -gt 1 ]; then printf '%s\n' "$*" >> "$journal.new" || return 1; fi
    # On disk before it takes the old one's name: a restart in between must
    # find one receipt or the other, not an empty file under the name.
    sync "$journal.new" && mv -f "$journal.new" "$journal"
}
valid_receipt() {
    [ -f "$journal" ] || return 0
    [ "$(get_record format)" = 1 ] && awk 'seen[$1]++ {exit 1}' "$journal" || return 1
    while read -r key stage first last extra; do
        case "$key" in
            select)
                case "$stage" in ''|*[!0-9]*) return 1 ;; esac
                valid_pref "$first" && [ -z "$extra" ] || return 1
                case "$last" in 1|2|3|4|5) ;; *) return 1 ;; esac ;;
            format|reload[1-5]) [ "$stage" = 1 ] && [ -z "$first$last$extra" ] || return 1 ;;
            *)
                field "$key" && [ -z "$extra" ] && valid_word "$first" && valid_word "$last" || return 1
                case "$stage" in done) [ "$first" = "$last" ] || return 1 ;; pending) ;; *) return 1 ;; esac ;;
        esac
    done < "$journal"
}
inspect() {
    foreign=0
    field "$1" || return 1
    record=$(get_record "$1") || return 1
    if [ -z "$record" ]; then
        stage=free; first=$factory; last=$factory
        if [ "$profile" = stock ]; then observed=$factory; return 0; fi
    else
        read -r stage first last <<EOF
$record
EOF
    fi
    observed=$(read_word "$1") || { echo "Cannot read controller word $1." >&2; return 1; }
    case "$observed" in "$factory"|"$first"|"$last") return 0 ;; esac
    foreign=1
    echo "Unowned controller value $observed at $1." >&2
    return 1
}
scan() {
    needed=0
    for addr in $addresses; do
        inspect "$addr" || return 1
        if [ "$observed" != "$wanted" ] || [ "$stage" = pending ]; then needed=1; fi
    done
    if [ -f "$journal" ] && grep -Eq '^(reload|select )' "$journal"; then needed=1; fi
}
program_field() {
    inspect "$1" || return 1
    if [ "$stage" = pending ]; then
        # A staged write can still read as factory until the apply commands run.
        # Clear an abandoned candidate before taking ownership of another one.
        if [ "$last" = "$wanted" ]; then settle=$wanted; else settle=$factory; fi
        write_word "$1" "$settle" && set_record "$1" done "$settle" "$settle" || return 1
        observed=$settle; stage=done
    fi
    if [ "$observed" != "$wanted" ]; then
        [ "$(get_record "reload$mode")" = 1 ] || set_record "reload$mode" 1 || return 1
        set_record "$1" pending "$observed" "$wanted" &&
            write_word "$1" "$wanted" && set_record "$1" done "$wanted" "$wanted" || return 1
    fi
}
valid_pref() {
    case "$1" in null) return 0 ;; ''|*[!0-9]*) return 1 ;; *) return 0 ;; esac
}
pref() { settings --user "$owner" get system refresh_rate_mode; }
set_pref() {
    if [ "$1" = null ]; then
        settings --user "$owner" delete system refresh_rate_mode >/dev/null
    else
        settings --user "$owner" put system refresh_rate_mode "$1"
    fi
}
active_mode() {
    case "$(read_word 940080e7)" in
        0001003c) echo 1 ;; 0001005a) echo 2 ;; 00010078) echo 3 ;;
        00010090) echo 4 ;; 000100a5) echo 5 ;; *) return 1 ;;
    esac
}
restore_pref() {
    choice=$(get_record select) || return 1
    [ -n "$choice" ] || return 0
    read -r owner saved temporary <<EOF
$choice
EOF
    current=$(pref) && valid_pref "$current" || return 1
    if [ "$current" = "$temporary" ]; then
        set_pref "$saved" && [ "$(pref)" = "$saved" ] || return 1
    elif [ "$current" != "$saved" ]; then
        preference_changed=1
    fi
    # A newer, different preference belongs to the user.
    set_record select
}
mode_policy() {
    CLASSPATH="${0%/*}/panel-mode.jar" app_process /system/bin dev.auro.astra.panel.PanelMode "$1"
}
finish_reload() {
    if ! grep -q '^reload' "$journal"; then return 0; fi
    active=$(active_mode) || return 1
    if [ -n "$(get_record "reload$active")" ]; then
        [ "$preference_changed" = 0 ] || return 1
        case " $conflicts " in *" $active "*) return 1 ;; esac
        awake && mode_policy pulse || { echo 'Could not confirm a native mode transition.' >&2; return 1; }
    fi
    for m in 1 2 3 4 5; do
        if [ -n "$(get_record "reload$m")" ]; then set_record "reload$m" || return 1; fi
    done
}
program() {
    restore_pref || return 1
    conflicts=
    for addr in $addresses; do
        if ! program_field "$addr"; then
            conflicts="$conflicts $mode"
            [ "$profile" = stock ] && [ "$foreign" = 1 ] && panel_on || return 1
        fi
    done
    finish_reload && [ -z "$conflicts" ] || return 1
    scan && [ "$needed" = 0 ] || return 1
    if [ "$profile" = stock ]; then rm "$journal" || return 1; fi
}
recover() {
    rc=$?
    trap - EXIT
    trap '' HUP INT TERM
    # Android preference recovery is independent of DSI. After a failed
    # transfer, leave the durable word journal for the next settled wake.
    if [ -f "$dir/mode-policy" ] && ! mode_policy recover; then
        echo 'Could not restore interrupted compositor policy. Receipt retained.' >&2
    fi
    if ! restore_pref; then
        echo 'Could not restore the temporary Android refresh preference. Receipt retained.' >&2
    else
        echo 'Panel operation interrupted. Receipt retained for the next wake.' >&2
    fi
    exit "$rc"
}
awake || blocked 'Panel is asleep.'
# The controller forgets everything when it loses power. A receipt that cannot
# be read and was last written before this boot describes nothing any more.
boot=$(cat /proc/sys/kernel/random/boot_id) || blocked 'Cannot read the boot identity.'
if ! valid_receipt; then
    [ "$(cat "$dir/boot" 2>/dev/null)" != "$boot" ] || blocked 'Invalid calibration receipt.'
    rm -f "$journal" "$journal.new" || blocked 'Invalid calibration receipt.'
    echo 'Discarded an unreadable receipt from before the last restart.' >&2
    if [ "$profile" = stock ]; then exit 0; fi
fi
preference_changed=0
if [ -f "$dir/mode-policy" ]; then
    case "$command" in check*) exit 1 ;; esac
    mode_policy recover || blocked 'Could not restore an interrupted display mode policy.'
fi
# Recover an interrupted Android preference even if panel diagnostics are unavailable.
case "$command" in
    check*)
        if [ -n "$(get_record select)" ]; then exit 1; fi ;;
    *) restore_pref || blocked 'Could not restore the interrupted Android refresh selection.' ;;
esac
[ "$(read_word 080080e7)" = 0008874b ] || blocked 'Panel controller identity is unavailable.'
case "$command" in check*) scan || blocked 'Panel checks failed.'; exit "$needed" ;; esac
if [ "$profile" != stock ]; then
    scan || blocked 'Panel checks failed.'
    if [ "$needed" = 0 ]; then exit 0; fi
fi
if [ "$(cat "$dir/boot" 2>/dev/null)" != "$boot" ]; then
    printf '%s\n' "$boot" > "$dir/boot.new" && sync "$dir/boot.new" && mv -f "$dir/boot.new" "$dir/boot" ||
        blocked 'Cannot save calibration receipt.'
fi
if [ ! -f "$journal" ]; then
    printf 'format 1\n' > "$journal.new" && sync "$journal.new" && mv "$journal.new" "$journal" ||
        blocked 'Cannot save calibration receipt.'
fi
trap recover EXIT
trap 'exit 1' INT HUP TERM
program || { echo 'Panel table update incomplete.' >&2; exit 1; }
trap - EXIT INT HUP TERM
