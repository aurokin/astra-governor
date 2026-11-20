package dev.auro.astra;

import android.graphics.drawable.GradientDrawable;
import android.graphics.drawable.LayerDrawable;
import android.view.Gravity;
import android.view.View;
import android.view.accessibility.AccessibilityNodeInfo;
import android.widget.LinearLayout;
import android.widget.RadioButton;
import android.widget.TextView;

/** One choice of what the 60 Hz mode is matched to: its name, its rate, and what runs at that rate. */
final class PresetRow extends LinearLayout {
    final CalibrationStore.Preset preset;
    private final Ui ui;
    private final TextView name, hz, use;
    private final View mark;
    private boolean chosen;

    PresetRow(Ui ui, CalibrationStore.Preset preset) {
        super(ui.context);
        this.ui = ui;
        this.preset = preset;
        setOrientation(HORIZONTAL);
        setGravity(Gravity.CENTER_VERTICAL);
        setMinimumHeight(ui.dp(54));
        setPadding(0, ui.dp(6), 0, ui.dp(6));
        setFocusable(true);
        setClickable(true);
        setBackground(ui.ripple(6));

        mark = new View(getContext());
        LayoutParams markParams = new LayoutParams(ui.dp(18), ui.dp(18));
        markParams.leftMargin = ui.dp(2);
        markParams.rightMargin = ui.dp(14);
        addView(mark, markParams);

        LinearLayout words = ui.column();
        name = ui.text(preset.label, 17, Ui.TEXT, 500);
        use = ui.text(preset.use, 14, Ui.DIM, 400);
        words.addView(name);
        words.addView(use);
        addView(words, Ui.share());

        hz = ui.figure(17, Ui.DIM, 400);
        hz.setText(preset.hz + " Hz");
        LayoutParams hzParams = Ui.wrap();
        hzParams.leftMargin = ui.dp(12);
        addView(hz, hzParams);
        setContentDescription(preset.label + ", " + preset.hz + " hertz. " + preset.use);
        render();
    }

    void setChosen(boolean chosen) {
        if (this.chosen != chosen) {
            this.chosen = chosen;
            render();
        }
    }

    @Override
    public void setEnabled(boolean enabled) {
        if (isEnabled() != enabled) {
            super.setEnabled(enabled);
            render();
        }
    }

    @Override
    public void onInitializeAccessibilityNodeInfo(AccessibilityNodeInfo info) {
        super.onInitializeAccessibilityNodeInfo(info);
        info.setClassName(RadioButton.class.getName());
        info.setCheckable(true);
        info.setChecked(chosen);
    }

    private void render() {
        boolean lit = chosen && isEnabled();
        setAlpha(isEnabled() ? 1f : 0.45f);
        GradientDrawable ring = new GradientDrawable();
        ring.setShape(GradientDrawable.OVAL);
        ring.setStroke(ui.dp(2), chosen ? Ui.AMBER : 0xFF5B6E73);
        GradientDrawable dot = new GradientDrawable();
        dot.setShape(GradientDrawable.OVAL);
        dot.setColor(chosen ? Ui.AMBER : 0);
        LayerDrawable both = new LayerDrawable(new GradientDrawable[]{ring, dot});
        int inset = ui.dp(5);
        both.setLayerInset(1, inset, inset, inset, inset);
        mark.setBackground(both);
        hz.setTextColor(lit ? Ui.AMBER : Ui.DIM);
    }
}
