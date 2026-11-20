package dev.auro.astra.panel;

import android.os.IBinder;
import android.os.SystemClock;

import java.io.DataInputStream;
import java.io.DataOutputStream;
import java.io.FileOutputStream;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.Paths;
import java.nio.file.StandardCopyOption;
import java.lang.reflect.Field;
import java.lang.reflect.Method;

/** Briefly select a different native mode so the panel latches a newly written table. */
public final class PanelMode {
    private static final Path RECEIPT = Paths.get("/data/adb/astra-panel-rate/mode-policy");
    private static final Path BOOT_ID = Paths.get("/proc/sys/kernel/random/boot_id");
    private final Class<?> surface = Class.forName("android.view.SurfaceControl");
    private final Class<?> specs = nested("DesiredDisplayModeSpecs");
    private final Method readPolicy = surface.getMethod("getDesiredDisplayModeSpecs", IBinder.class);
    private final Method writePolicy = surface.getMethod("setDesiredDisplayModeSpecs", IBinder.class, specs);
    private final Method displayInfo = surface.getMethod("getDynamicDisplayInfo", long.class);
    private final long displayId;
    private final IBinder token;

    private Class<?> nested(String name) throws Exception {
        return Class.forName(surface.getName() + "$" + name);
    }

    private PanelMode() throws Exception {
        Class<?> factory = Class.forName("com.android.internal.os.ClassLoaderFactory");
        Method create = factory.getDeclaredMethod("createClassLoader", String.class, String.class,
                String.class, ClassLoader.class, int.class, boolean.class, String.class);
        ClassLoader loader = (ClassLoader) create.invoke(null, "/system/framework/services.jar",
                null, null, ClassLoader.getSystemClassLoader().getParent(), 36, true, null);
        Class<?> control = loader.loadClass("com.android.server.display.DisplayControl");
        Method load = Runtime.class.getDeclaredMethod("loadLibrary0", Class.class, String.class);
        load.setAccessible(true);
        load.invoke(Runtime.getRuntime(), control, "android_servers");
        long[] displays = (long[]) control.getMethod("getPhysicalDisplayIds").invoke(null);
        if (displays.length != 1) throw new IllegalStateException("Expected one physical display");
        displayId = displays[0];
        token = (IBinder) control.getMethod("getPhysicalDisplayToken", long.class).invoke(null, displayId);
        if (token == null) throw new IllegalStateException("Internal display unavailable");
    }

    private static Field field(Object value, String name) throws Exception {
        return value.getClass().getField(name);
    }

    private int activeMode() throws Exception {
        Object info = displayInfo.invoke(null, displayId);
        return field(info, "activeDisplayModeId").getInt(info);
    }

    private static Object otherMode(Object info, int active) throws Exception {
        Object[] modes = (Object[]) field(info, "supportedDisplayModes").get(info);
        Object current = null;
        for (Object mode : modes) {
            if (field(mode, "id").getInt(mode) == active) current = mode;
        }
        if (current == null) throw new IllegalStateException("Unknown active mode");
        int other = Math.round(field(current, "vsyncRate").getFloat(current)) == 60 ? 120 : 60;
        for (Object mode : modes) {
            if (Math.round(field(mode, "vsyncRate").getFloat(mode)) == other
                    && field(mode, "group").getInt(mode) == field(current, "group").getInt(current)
                    && field(mode, "width").getInt(mode) == field(current, "width").getInt(current)
                    && field(mode, "height").getInt(mode) == field(current, "height").getInt(current)) return mode;
        }
        throw new IllegalStateException("No alternate native mode in active group");
    }

    private Object fixedPolicy(Object original, Object mode) throws Exception {
        Object fixed = specs.getConstructor(specs).newInstance(original);
        field(fixed, "defaultMode").setInt(fixed, field(mode, "id").getInt(mode));
        float hz = field(mode, "vsyncRate").getFloat(mode);
        for (String policy : new String[]{"primaryRanges", "appRequestRanges"}) {
            Object ranges = field(fixed, policy).get(fixed);
            for (String type : new String[]{"physical", "render"}) {
                Object range = field(ranges, type).get(ranges);
                // A narrow render interval selects the same unique native mode and
                // distinguishes this brief vote from an OEM fixed-rate policy.
                field(range, "min").setFloat(range, type.equals("render") ? hz - 0.125f : hz);
                field(range, "max").setFloat(range, hz);
            }
        }
        return fixed;
    }

    private Object policy() throws Exception {
        return readPolicy.invoke(null, token);
    }

    private void apply(Object policy) throws Exception {
        if (!Boolean.TRUE.equals(writePolicy.invoke(null, token, policy))) {
            throw new IllegalStateException("SurfaceFlinger rejected the mode policy");
        }
    }

    private static Object[] ranges(Object spec) throws Exception {
        Object primary = field(spec, "primaryRanges").get(spec);
        Object app = field(spec, "appRequestRanges").get(spec);
        return new Object[]{field(primary, "physical").get(primary), field(primary, "render").get(primary),
                field(app, "physical").get(app), field(app, "render").get(app)};
    }

    private static void writeSpec(DataOutputStream out, Object spec) throws Exception {
        out.writeInt(field(spec, "defaultMode").getInt(spec));
        out.writeBoolean(field(spec, "allowGroupSwitching").getBoolean(spec));
        for (Object range : ranges(spec)) {
            out.writeFloat(field(range, "min").getFloat(range));
            out.writeFloat(field(range, "max").getFloat(range));
        }
        Object idle = field(spec, "idleScreenRefreshRateConfig").get(spec);
        out.writeBoolean(idle != null);
        if (idle != null) out.writeInt(field(idle, "timeoutMillis").getInt(idle));
    }

    private Object readSpec(DataInputStream in) throws Exception {
        Object spec = specs.getConstructor().newInstance();
        field(spec, "defaultMode").setInt(spec, in.readInt());
        field(spec, "allowGroupSwitching").setBoolean(spec, in.readBoolean());
        for (Object range : ranges(spec)) {
            field(range, "min").setFloat(range, in.readFloat());
            field(range, "max").setFloat(range, in.readFloat());
        }
        if (in.readBoolean()) {
            Object idle = nested("IdleScreenRefreshRateConfig").getConstructor(int.class).newInstance(in.readInt());
            field(spec, "idleScreenRefreshRateConfig").set(spec, idle);
        }
        return spec;
    }

    private static String bootId() throws Exception {
        return new String(Files.readAllBytes(BOOT_ID), StandardCharsets.US_ASCII).trim();
    }

    private void save(Object original, Object temporary) throws Exception {
        Path pending = RECEIPT.resolveSibling("mode-policy.new");
        try (FileOutputStream file = new FileOutputStream(pending.toFile());
             DataOutputStream out = new DataOutputStream(file)) {
            out.writeUTF(bootId());
            out.writeLong(displayId);
            writeSpec(out, original);
            writeSpec(out, temporary);
            out.flush();
            file.getFD().sync();
        }
        Files.move(pending, RECEIPT, StandardCopyOption.ATOMIC_MOVE);
    }

    private void recover() throws Exception {
        if (!Files.exists(RECEIPT)) return;
        try (DataInputStream in = new DataInputStream(Files.newInputStream(RECEIPT))) {
            if (in.readUTF().equals(bootId()) && in.readLong() == displayId) {
                Object original = readSpec(in);
                Object temporary = readSpec(in);
                if (in.read() != -1) throw new IllegalStateException("Invalid mode policy receipt");
                if (temporary.equals(policy())) {
                    apply(original);
                    System.out.println("Restored display policy");
                } else {
                    System.out.println("Display policy superseded by Android");
                }
            }
        }
        Files.delete(RECEIPT);
    }

    private void run(boolean pulse) throws Exception {
        Object info = displayInfo.invoke(null, displayId);
        int start = field(info, "activeDisplayModeId").getInt(info);
        Object original = policy();
        Object target = otherMode(info, start);
        Object temporary = fixedPolicy(original, target);
        int targetId = field(target, "id").getInt(target);
        System.out.println("active=" + start + " original=" + original + " temporary=" + temporary);
        if (!pulse) return;
        save(original, temporary);
        if (!original.equals(policy())) throw new IllegalStateException("Display policy changed before reload");
        try {
            apply(temporary);
            long until = SystemClock.uptimeMillis() + 1800;
            while (SystemClock.uptimeMillis() < until) {
                int active = activeMode();
                if (active != start) {
                    System.out.println("Observed native mode transition " + start + " -> " + active);
                    return;
                }
                SystemClock.sleep(15);
            }
            throw new IllegalStateException("Native mode transition not observed; requested " + targetId);
        } finally {
            recover();
        }
    }

    public static void main(String[] args) throws Exception {
        if (args.length != 1 || !(args[0].equals("probe") || args[0].equals("pulse") || args[0].equals("recover"))) {
            throw new IllegalArgumentException("Expected probe, pulse or recover");
        }
        try {
            PanelMode mode = new PanelMode();
            if (!args[0].equals("probe")) mode.recover();
            if (!args[0].equals("recover")) mode.run(args[0].equals("pulse"));
        } catch (Throwable error) {
            error.printStackTrace(System.err);
            System.exit(1);
        }
        System.exit(0);
    }
}
