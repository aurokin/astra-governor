package dev.auro.astra;

import android.content.Context;
import android.util.AtomicFile;

import java.io.File;
import java.io.FileNotFoundException;
import java.io.FileOutputStream;
import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.util.Arrays;

/**
 * The display calibration choice. The app's only part in calibration is this
 * one file in device-protected storage, which the root service reads; the
 * service alone touches the panel. Device-protected so that the service can
 * read it after a reboot, before the tablet is first unlocked.
 */
final class CalibrationStore {
    /** What the 60 Hz mode is matched to. {@code value} is the word the service knows it by. */
    enum Preset {
        MODERN("60", "Modern", "60.000", "Android games, Switch, 60 fps video, game streaming"),
        NTSC("59.94", "NTSC", "59.940", "GameCube, Wii, PlayStation 2, PSP, Dreamcast, TV video"),
        GBA("gba", "GBA", "59.727", "Game Boy, Game Boy Color, Game Boy Advance"),
        NES("nes", "NES", "60.099", "NES and Super NES");

        final String value, label, hz, use;

        Preset(String value, String label, String hz, String use) {
            this.value = value;
            this.label = label;
            this.hz = hz;
            this.use = use;
        }

        static Preset of(String value) {
            for (Preset p : values()) if (p.value.equals(value)) return p;
            return null;
        }
    }

    static final class Choice {
        static final Choice DEFAULT = new Choice(false, Preset.MODERN);
        final boolean on;
        final Preset preset;

        Choice(boolean on, Preset preset) {
            this.on = on;
            this.preset = preset;
        }

        byte[] record() {
            return ((on ? "on " : "off ") + preset.value + "\n").getBytes(StandardCharsets.US_ASCII);
        }
    }

    private final AtomicFile file;

    CalibrationStore(Context context) {
        file = new AtomicFile(new File(context.createDeviceProtectedStorageContext().getFilesDir(),
                "panel-calibration.conf"));
    }

    /** The saved choice, or the default when none was ever saved. Throws when a saved one cannot be read. */
    Choice read() throws IOException {
        String line;
        try {
            line = new String(file.readFully(), StandardCharsets.UTF_8).trim();
        } catch (FileNotFoundException e) {
            if (file.getBaseFile().exists()) throw e;
            return Choice.DEFAULT;
        }
        int space = line.indexOf(' ');
        Preset preset = space < 0 ? null : Preset.of(line.substring(space + 1));
        String on = space < 0 ? "" : line.substring(0, space);
        if (preset == null || !(on.equals("on") || on.equals("off"))) throw new IOException("unrecognised record");
        return new Choice(on.equals("on"), preset);
    }

    /** Replaces the saved choice and reads it back; the old one stays if anything fails. */
    void write(Choice choice) throws IOException {
        byte[] record = choice.record();
        FileOutputStream out = null;
        try {
            out = file.startWrite();
            out.write(record);
            file.finishWrite(out);
            out = null;
            if (!Arrays.equals(record, file.readFully())) throw new IOException("readback differs");
        } finally {
            if (out != null) file.failWrite(out);
        }
    }
}
