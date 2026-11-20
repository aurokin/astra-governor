package dev.auro.astra;

import android.content.Context;
import android.content.res.ColorStateList;
import android.graphics.Typeface;
import android.graphics.drawable.GradientDrawable;
import android.graphics.drawable.RippleDrawable;
import android.view.View;
import android.widget.LinearLayout;
import android.widget.Switch;
import android.widget.TextView;

/** The app's colours and type, and the few view shapes it is built from. */
final class Ui {
    static final int INK = 0xFF0F1B21;    // the page
    static final int LINE = 0xFF24363D;   // rules and resting outlines
    static final int TEXT = 0xFFECE5D6;
    static final int DIM = 0xFF9DB0B3;    // explanations
    static final int AMBER = 0xFFF4B63F;  // what is measured, and what is chosen
    static final int TEAL = 0xFF74C7B8;   // working as intended
    static final int CORAL = 0xFFEC6A4D;  // needs attention

    private static final int[] CHECKED = {android.R.attr.state_checked};
    private static final int[] DISABLED = {-android.R.attr.state_enabled};
    private static final int[] ANY = {};

    final Context context;
    private final float density;

    Ui(Context context) {
        this.context = context;
        density = context.getResources().getDisplayMetrics().density;
    }

    int dp(float value) {
        return Math.round(density * value);
    }

    static Typeface face(String family, int weight) {
        return Typeface.create(Typeface.create(family, Typeface.NORMAL), weight, false);
    }

    TextView text(CharSequence value, float sp, int color, int weight) {
        TextView v = new TextView(context);
        v.setText(value);
        v.setTextSize(sp);
        v.setTextColor(color);
        v.setTypeface(face("sans-serif", weight));
        v.setLineSpacing(0, 1.25f);
        return v;
    }

    /** Digits that keep their width as they change. */
    TextView figure(float sp, int color, int weight) {
        TextView v = text("", sp, color, weight);
        v.setFontFeatureSettings("tnum");
        return v;
    }

    TextView heading(String value) {
        TextView v = text(value, 22, TEXT, 500);
        v.setAccessibilityHeading(true);
        return v;
    }

    LinearLayout column() {
        LinearLayout v = new LinearLayout(context);
        v.setOrientation(LinearLayout.VERTICAL);
        return v;
    }

    LinearLayout row() {
        LinearLayout v = new LinearLayout(context);
        v.setOrientation(LinearLayout.HORIZONTAL);
        return v;
    }

    static LinearLayout.LayoutParams fill() {
        return new LinearLayout.LayoutParams(LinearLayout.LayoutParams.MATCH_PARENT, LinearLayout.LayoutParams.WRAP_CONTENT);
    }

    static LinearLayout.LayoutParams wrap() {
        return new LinearLayout.LayoutParams(LinearLayout.LayoutParams.WRAP_CONTENT, LinearLayout.LayoutParams.WRAP_CONTENT);
    }

    static LinearLayout.LayoutParams share() {
        return new LinearLayout.LayoutParams(0, LinearLayout.LayoutParams.WRAP_CONTENT, 1);
    }

    LinearLayout.LayoutParams below(LinearLayout.LayoutParams params, float gapDp) {
        params.topMargin = dp(gapDp);
        return params;
    }

    Switch toggle(String label) {
        Switch v = new Switch(context);
        v.setContentDescription(label);
        v.setMinimumHeight(dp(48));
        v.setMinimumWidth(dp(56));
        v.setThumbTintList(new ColorStateList(new int[][]{DISABLED, CHECKED, ANY}, new int[]{0xFF4A5B60, AMBER, 0xFFB9C4C4}));
        v.setTrackTintList(new ColorStateList(new int[][]{DISABLED, CHECKED, ANY}, new int[]{LINE, 0x80F4B63F, 0xFF3A4D54}));
        // The saved choice is authoritative across rotation and process recreation.
        v.setSaveEnabled(false);
        return v;
    }

    /** A title with a line of explanation and a switch at the end; the whole row flips the switch. */
    LinearLayout toggleRow(String title, String explanation, Switch toggle) {
        LinearLayout words = column();
        words.addView(text(title, 17, TEXT, 500));
        words.addView(text(explanation, 14, DIM, 400), below(fill(), 2));
        LinearLayout row = row();
        row.setGravity(android.view.Gravity.CENTER_VERTICAL);
        row.addView(words, share());
        LinearLayout.LayoutParams end = wrap();
        end.leftMargin = dp(16);
        row.addView(toggle, end);
        row.setOnClickListener(v -> {
            if (toggle.isEnabled()) toggle.toggle();
        });
        row.setBackground(ripple(0));
        return row;
    }

    RippleDrawable ripple(float radiusDp) {
        GradientDrawable mask = new GradientDrawable();
        mask.setColor(0xFFFFFFFF);
        mask.setCornerRadius(dp(radiusDp));
        return new RippleDrawable(ColorStateList.valueOf(0x22ECE5D6), null, mask);
    }

    View rule() {
        View v = new View(context);
        v.setBackgroundColor(LINE);
        return v;
    }

    /** A note set off by a coloured rule at its start: something to know before relying on what is above. */
    LinearLayout note(CharSequence value, int color) {
        LinearLayout row = row();
        View bar = new View(context);
        bar.setBackgroundColor(color);
        row.addView(bar, new LinearLayout.LayoutParams(dp(3), LinearLayout.LayoutParams.MATCH_PARENT));
        TextView words = text(value, 14, DIM, 400);
        words.setPadding(dp(12), dp(2), 0, dp(2));
        row.addView(words, share());
        return row;
    }
}
