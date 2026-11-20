"""Exercise ownership, packet sequencing and native selection with an emulated HX8874."""
import fcntl
from itertools import permutations
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

SOURCE = Path(__file__).resolve().parent / "panel-table.sh"
FACTORY = dict(zip("24 28 2c 30 34 38 3c 40 44 48".split(), """
    004000e4 01200aed 004000e4 0120054d 004000e4 0120027d
    004000e4 01200115 004000e4 0120002d""".split()))
CALIBRATED = dict(zip(FACTORY, """
    004000d8 01200b14 004000d8 01200567 004000f4 0120027a
    004000d4 01200128 004000c4 0120004a""".split()))
CONTENT = {
    "60": ("004000d8", "01200b14"),
    "5994": ("004000b8", "01200b4d"),
    "gba": ("004000d0", "01200b35"),
    "nes": ("004000e8", "01200af3"),
}

HARDWARE = r'''
read_word() {
    if [ -e "$MOCK/tripped" ]; then echo "read $1" >> "$MOCK/after-error"; fi
    case "$1" in
        080080e7) cat "$MOCK/chip" ;;
        c80086e6) echo e781017c ;;
        cc0086e6) echo 00000000 ;;
        940080e7)
            case "$(cat "$MOCK/active")" in
                1) echo 0001003c ;; 2) echo 0001005a ;; 3) echo 00010078 ;;
                4) echo 00010090 ;; 5) echo 000100a5 ;;
            esac ;;
        ??0181e7) cat "$MOCK/${1%0181e7}" ;;
        *) return 1 ;;
    esac
}
write_packet() {
    if [ -e "$MOCK/tripped" ]; then echo "write $1" >> "$MOCK/after-error"; fi
    echo "$1" >> "$MOCK/packets"
    case "$1" in
        806c0181e701000000) touch "$MOCK/armed" ;;
        807c0181e700000000)
            [ -f "$MOCK/armed" ] || return 1
            read -r bank value < "$MOCK/staged" || return 1
            if [ -f "$MOCK/fail" ] && [ "$(cat "$MOCK/fail")" = "$bank" ]; then
                touch "$MOCK/tripped"
                rm "$MOCK/fail"
                return 1
            fi
            echo "$value" > "$MOCK/$bank"
            rm "$MOCK/armed" "$MOCK/staged" ;;
        80??0181e7????????)
            raw=${1#??????????}; low=${raw%????}; high=${raw#????}; bank=${1#80}; bank=${bank%0181e7????????}
            echo "$bank ${high#??}${high%??}${low#??}${low%??}" > "$MOCK/staged" ;;
        *) return 1 ;;
    esac
}
'''


class PanelTableTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.bin = self.root / "bin"
        self.bin.mkdir()
        self.cmd("id", "echo 0")
        self.cmd("getprop", 'case "$1" in ro.product.model) echo NP05J ;; *) echo REDMAGICOS11.0.7_NP05J_GB ;; esac')
        self.cmd("am", "echo 0")
        self.cmd("flock", """exec python3 -c 'import fcntl; fcntl.flock(0,fcntl.LOCK_EX|fcntl.LOCK_NB)'""")
        self.cmd("settings", '''
            case "$3" in get) cat "$MOCK/pref"; exit ;; delete) choice=null ;; put) choice=$6 ;; *) exit 1 ;; esac
            echo "$choice" >> "$MOCK/selections"
            if [ -f "$MOCK/concurrent" ]; then choice=$(cat "$MOCK/concurrent"); rm "$MOCK/concurrent"; fi
            echo "$choice" > "$MOCK/pref"
            case "$choice" in null|0) choice=3 ;; esac
            echo "$choice" > "$MOCK/active"
        ''')
        self.cmd("app_process", """
            test "$1" = /system/bin && test "$2" = dev.auro.astra.panel.PanelMode || exit 2
            echo "$3" >> "$MOCK/pulses"
            if [ "$3" = recover ]; then rm -f "$MOCK/state/mode-policy"; exit 0; fi
            test "$3" = pulse || exit 2
            if [ -f "$MOCK/concurrent" ]; then
                cp "$MOCK/concurrent" "$MOCK/pref"
                cp "$MOCK/concurrent" "$MOCK/active"
                rm "$MOCK/concurrent"
            fi
            test ! -f "$MOCK/pulse-fault"
        """)
        self.cmd("dumpsys", '''
            case "$1" in power) echo mWakefulness=Awake ;;
                display) case "$(cat "$MOCK/active")" in
                    1) hz=60 ;; 2) hz=90 ;; 3) hz=120 ;; 4) hz=144 ;; 5) hz=165 ;;
                esac
                echo "mActiveSfDisplayMode=DisplayMode{vsyncRate=$hz.0,}" ;;
            esac
        ''')
        for path, value in {**FACTORY, "chip": "0008874b", "pref": "0", "active": "3", "dpms": "On", "lcd": "0", "packets": "", "selections": "", "pulses": ""}.items():
            self.put(path, value)
        source = SOURCE.read_text()
        start, end = source.index("reg=/sys/reg_debug"), source.index("# H then V")
        source = (source[:start] + HARDWARE + source[end:]).replace(
            "/data/adb/astra-panel-rate", str(self.root / "state")).replace(
            "/sys/class/drm/card0-DSI-1/dpms", str(self.root / "dpms")).replace(
            "/proc/driver/lcd_state", str(self.root / "lcd"))
        self.put("run.sh", source)
        self.receipt = self.root / "state" / "table-session"

    def cmd(self, name, program):
        path = self.bin / name
        path.write_text("#!/bin/sh\n" + program + "\n")
        path.chmod(0o755)

    def put(self, name, value):
        (self.root / name).write_text(value + "\n")

    def get(self, name):
        return (self.root / name).read_text().strip()

    def invoke(self, name, status=0):
        result = subprocess.run(["sh", str(self.root / "run.sh"), name], capture_output=True, text=True,
                                env={**os.environ, "MOCK": str(self.root), "PATH": f"{self.bin}:{os.environ['PATH']}"})
        self.assertEqual(result.returncode, status, (result.stdout, result.stderr))

    def table(self):
        return {bank: self.get(bank) for bank in FACTORY}

    def test_auto_profiles_and_idempotence(self):
        self.invoke("check60", 1)
        self.invoke("apply60")
        self.assertEqual(self.table(), CALIBRATED)
        self.assertEqual(self.get("selections"), "")
        self.assertEqual(self.get("pulses").splitlines(), ["pulse"])
        packets = self.get("packets").splitlines()
        self.assertEqual(len(packets), 30)
        for index, (bank, word) in enumerate(CALIBRATED.items()):
            self.assertEqual(packets[3*index:3*index+3], [
                f"80{bank}0181e7{bytes.fromhex(word)[::-1].hex()}", "806c0181e701000000", "807c0181e700000000"])
        self.invoke("check60")
        self.invoke("apply60")
        self.invoke("check5994", 1)
        self.assertEqual(self.get("packets").splitlines(), packets)
        self.invoke("apply5994")
        self.assertEqual(self.table(), {**CALIBRATED, "24": "004000b8", "28": "01200b4d"})
        self.assertEqual(self.get("selections"), "")
        self.assertEqual(self.get("pulses").splitlines(), ["pulse"])
        self.put("active", "1")
        self.invoke("apply60")
        self.assertEqual(self.get("selections"), "")
        self.assertEqual(self.get("pulses").splitlines(), ["pulse", "pulse"])
        self.invoke("stock")
        self.assertEqual(self.table(), FACTORY)
        self.assertFalse(self.receipt.exists())

    def test_reset_sleep_and_saved_absence(self):
        self.put("pref", "null")
        self.invoke("apply5994")
        self.assertEqual(self.get("pref"), "null")
        for key, word in FACTORY.items():
            self.put(key, word)
        self.put("dpms", "Off")
        self.invoke("check5994", 2)
        self.invoke("apply5994", 2)
        self.put("dpms", "On")
        self.invoke("check5994", 1)
        self.invoke("apply5994")
        self.invoke("check5994")
        self.invoke("stock")
        self.assertEqual(self.get("pref"), "null")

    def test_all_content_transitions_replace_only_the_60_hz_pair(self):
        self.put("pref", "1")
        self.put("active", "1")
        for previous, following in permutations(CONTENT, 2):
            with self.subTest(previous=previous, following=following):
                self.invoke("apply" + previous)
                self.put("packets", "")
                self.put("pulses", "")
                self.invoke("check" + following, 1)
                self.invoke("apply" + following)
                self.invoke("check" + following)
                self.invoke("apply" + following)
                h, v = CONTENT[following]
                self.assertEqual(self.table(), {**CALIBRATED, "24": h, "28": v})
                self.assertEqual(len(self.get("packets").splitlines()), 6)
                self.assertEqual(self.get("selections"), "")
                self.assertEqual(self.get("pulses").splitlines(), ["pulse"])
        self.invoke("stock")
        self.assertEqual(self.table(), FACTORY)

    def test_interrupted_game_profile_change_defers_stock_until_next_access(self):
        for previous, following in [("gba", "nes"), ("nes", "gba")]:
            with self.subTest(previous=previous, following=following):
                self.invoke("apply" + previous)
                self.put("fail", "28")
                self.put("packets", "")
                self.invoke("apply" + following, 1)
                self.assertEqual(len(self.get("packets").splitlines()), 6)
                self.assertIn("pending", self.receipt.read_text())
                self.invoke("stock")
                self.assertEqual(self.table(), FACTORY)
                self.assertEqual(self.get("pref"), "0")
                self.assertFalse(self.receipt.exists())

    def test_failed_transfer_stops_diagnostic_io_in_both_directions(self):
        for action in ["apply60", "stock"]:
            with self.subTest(action=action):
                if action == "stock":
                    self.invoke("apply60")
                self.put("fail", "38")
                self.invoke(action, 1)
                self.assertTrue((self.root / "tripped").exists())
                self.assertFalse((self.root / "after-error").exists())
                self.assertIn("pending", self.receipt.read_text())
                (self.root / "tripped").unlink()
                self.invoke("stock")
                self.assertEqual(self.table(), FACTORY)

    def test_interrupted_preference_is_restored_with_unavailable_controller(self):
        self.receipt.parent.mkdir()
        self.receipt.write_text("format 1\nselect 0 null 1\n")
        self.put("pref", "1")
        self.put("chip", "ffffffff")
        self.invoke("checkgba", 1)
        self.invoke("applygba", 2)
        self.assertEqual(self.get("pref"), "null")
        self.assertEqual(self.get("packets"), "")
        self.assertNotIn("select ", self.receipt.read_text())

    def test_interrupted_word_and_selection(self):
        self.put("fail", "38")
        self.invoke("apply60", 1)
        self.assertIn("pending", self.receipt.read_text())
        self.invoke("stock")
        self.assertEqual(self.table(), FACTORY)
        self.assertFalse(self.receipt.exists())
        self.receipt.write_text("format 1\nreload1 1\n240181e7 pending 004000e4 004000d8\nselect 0 null 1\n")
        self.put("pref", "1")
        self.put("active", "1")
        self.invoke("stock")
        self.assertEqual(self.get("pref"), "null")
        self.assertFalse(self.receipt.exists())
        self.receipt.write_text("format 1\nselect 0 null 1\n")
        self.put("pref", "1")
        self.put("3c", "33445566")
        self.invoke("check60", 1)
        self.invoke("apply60", 2)
        self.assertEqual(self.get("pref"), "null")
        self.assertEqual(self.get("3c"), "33445566")

    def test_conflict_preflight_and_owned_only_restore(self):
        self.put("3c", "00112233")
        self.invoke("apply60", 2)
        self.assertFalse(self.receipt.exists())
        self.assertEqual(self.get("packets"), "")
        self.invoke("stock")
        self.put("3c", FACTORY["3c"])
        self.invoke("apply60")
        self.put("3c", "33445566")
        self.invoke("stock", 1)
        self.assertEqual(self.table(), {**FACTORY, "3c": "33445566"})
        self.assertTrue(self.receipt.exists())
        self.put("3c", CALIBRATED["3c"])
        self.invoke("stock")
        self.assertFalse(self.receipt.exists())

    def test_manual_and_competing_lock(self):
        self.invoke("check60", 1)
        self.put("state/session", "manual")
        self.invoke("apply60", 2)
        self.invoke("stock")
        (self.root / "state/session").unlink()
        with (self.root / "state/lock").open() as lock:
            fcntl.flock(lock, fcntl.LOCK_EX)
            self.invoke("apply60", 2)
        self.assertEqual(self.get("packets"), "")

    def test_interrupted_mode_pulse_keeps_active_row_pending(self):
        self.put("active", "5")
        self.put("pulse-fault", "1")
        self.invoke("apply60", 1)
        self.assertIn("reload5 1", self.receipt.read_text())
        self.assertEqual(self.get("selections"), "")
        (self.root / "pulse-fault").unlink()
        self.put("state/mode-policy", "interrupted")
        self.invoke("check60", 1)
        self.invoke("apply60")
        self.assertEqual(self.get("pulses").splitlines(), ["pulse", "recover", "pulse"])
        self.invoke("check60")

    def test_concurrent_user_preference(self):
        self.put("concurrent", "4")
        self.invoke("apply60")
        self.assertEqual(self.get("pref"), "4")
        self.invoke("stock")
        self.assertEqual(self.get("pref"), "4")


if __name__ == "__main__":
    unittest.main()
