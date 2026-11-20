package dev.auro.astra;

import org.json.JSONObject;

import java.io.InputStream;
import java.net.HttpURLConnection;
import java.net.URL;
import java.nio.charset.StandardCharsets;

/** The two root daemons' loopback HTTP endpoints. Every call blocks briefly; none belongs on the main thread. */
final class Daemons {
    private static final String GOVERNOR = "http://127.0.0.1:7878/";
    private static final String CALIBRATION = "http://127.0.0.1:7879/calibration";

    private Daemons() {}

    /** The response body, or null when nothing answers. */
    private static String get(String url) {
        HttpURLConnection c = null;
        try {
            c = (HttpURLConnection) new URL(url).openConnection();
            c.setConnectTimeout(400);
            c.setReadTimeout(800);
            try (InputStream in = c.getInputStream()) {
                return new String(in.readNBytes(16384), StandardCharsets.UTF_8);
            }
        } catch (Exception e) {
            return null;
        } finally {
            if (c != null) c.disconnect();
        }
    }

    private static JSONObject json(String body) {
        try {
            return body == null ? null : new JSONObject(body);
        } catch (Exception e) {
            return null;
        }
    }

    /** The governor's state, or null when it is not running. */
    static JSONObject governor() {
        return json(get(GOVERNOR + "status"));
    }

    /** Switches the governor's control on or off; it remembers the choice across reboots. */
    static boolean setGovernor(boolean on) {
        return get(GOVERNOR + (on ? "enable" : "disable")) != null;
    }

    /** What the calibration service is doing, or null when it is not running. Read-only. */
    static JSONObject calibration() {
        return json(get(CALIBRATION));
    }
}
