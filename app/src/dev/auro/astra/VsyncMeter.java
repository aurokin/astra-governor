package dev.auro.astra;

import android.view.Choreographer;

/**
 * Measures the display's real refresh rate from vsync timestamps. The mode the
 * system reports is nominal: this panel's "60 Hz" mode runs at 60.27 Hz until
 * it is calibrated.
 */
final class VsyncMeter implements Choreographer.FrameCallback {
    private static final long WINDOW_NS = 2_000_000_000L;
    private long windowStart, last;
    private int intervals;
    private volatile double hz;
    private boolean running;

    void start() {
        running = true;
        windowStart = 0;
        hz = 0;
        Choreographer.getInstance().postFrameCallback(this);
    }

    void stop() {
        running = false;
        Choreographer.getInstance().removeFrameCallback(this);
    }

    /** The rate over the last full window, or 0 before the first one completes. */
    double hz() {
        return hz;
    }

    @Override
    public void doFrame(long frameTimeNanos) {
        if (!running) return;
        if (windowStart == 0) {
            windowStart = frameTimeNanos;
            intervals = 0;
        } else {
            // A skipped callback spans several refreshes; count them so a busy main thread does not read as a slow panel.
            long gap = frameTimeNanos - last;
            long typical = intervals > 0 ? (last - windowStart) / intervals : gap;
            intervals += Math.max(1, Math.round((double) gap / Math.max(typical, 1)));
            if (frameTimeNanos - windowStart >= WINDOW_NS) {
                hz = intervals * 1e9 / (frameTimeNanos - windowStart);
                windowStart = frameTimeNanos;
                intervals = 0;
            }
        }
        last = frameTimeNanos;
        Choreographer.getInstance().postFrameCallback(this);
    }
}
