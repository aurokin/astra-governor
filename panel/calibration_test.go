package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestAppPreference(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "files"), 0700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "files", "panel-calibration.conf")
	get := func(want preference) {
		t.Helper()
		got, err := readPreference(dir)
		if err != nil || got != want {
			t.Fatalf("got %v %v, want %v", got, err, want)
		}
	}
	get(preference{false, "60"})
	for _, v := range []struct {
		text string
		p    preference
	}{
		{"on 60\n", preference{true, "60"}}, {"off 59.94", preference{false, "59.94"}},
		{"on 59.94\n", preference{true, "59.94"}}, {"off 60\n", preference{false, "60"}},
		{"on gba\n", preference{true, "gba"}}, {"on nes\n", preference{true, "nes"}}, {"off gba\n", preference{false, "gba"}}, {"off nes\n", preference{false, "nes"}},
	} {
		if err := os.WriteFile(p, []byte(v.text), 0600); err != nil {
			t.Fatal(err)
		}
		get(v.p)
	}
	for _, bad := range [][]byte{[]byte("on 165\n"), []byte("on 60\n\n"), bytes.Repeat([]byte("on 60 "), 20)} {
		if err := os.WriteFile(p, bad, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readPreference(dir); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if err := os.Rename(p, p+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(p+".old", p); err != nil {
		t.Fatal(err)
	}
	if _, err := readPreference(dir); err == nil {
		t.Fatal("accepted symlink")
	}
}

func TestFullWakeAndChoiceChanges(t *testing.T) {
	var calls []string
	var last status
	m := monitor{publish: func(s status) { last = s }, run: func(arg string) (int, string) {
		calls = append(calls, arg)
		return 0, ""
	}}
	start := time.Unix(12345, 0)
	cases := []struct {
		p            preference
		awake, owned bool
		sec          int
		calls        []string
		state        string
	}{
		{preference{false, "60"}, true, false, 0, nil, "off"},
		{preference{true, "60"}, false, false, 1, nil, "waiting"},
		{preference{true, "60"}, true, false, 2, []string{"apply60"}, "active"},
		{preference{true, "60"}, true, true, 3, nil, "active"},
		{preference{true, "60"}, false, true, 4, nil, "waiting"},
		{preference{true, "60"}, true, true, 5, []string{"apply60"}, "active"},
		{preference{true, "59.94"}, true, true, 6, []string{"apply5994"}, "active"},
		{preference{false, "59.94"}, false, true, 7, nil, "waiting"},
		{preference{false, "59.94"}, true, true, 8, []string{"stock"}, "off"},
		{preference{false, "59.94"}, true, false, 9, nil, "off"},
	}
	for _, c := range cases {
		calls = nil
		m.reconcile(c.p, c.awake, c.owned, start.Add(time.Duration(c.sec)*time.Second))
		if !reflect.DeepEqual(calls, c.calls) || last.State != c.state || last.preference != c.p {
			t.Fatalf("at %ds: calls %v state %+v", c.sec, calls, last)
		}
	}
}

func TestSteadyDisplayNeverOpensController(t *testing.T) {
	p := preference{true, "60"}
	calls := 0
	m := monitor{publish: func(status) {}, run: func(string) (int, string) { calls++; return 0, "" }}
	now := time.Unix(600, 0)
	for i := 0; i <= 3600; i++ {
		m.reconcile(p, true, true, now.Add(time.Duration(i)*time.Second))
	}
	if calls != 1 {
		t.Fatalf("steady display ran %d helpers", calls)
	}
	m.newWake()
	m.reconcile(p, true, true, now.Add(2*time.Hour))
	if calls != 2 {
		t.Fatal("did not verify after wake")
	}
}

func TestPassiveWakeTimestamps(t *testing.T) {
	sample := func(s string) powerSample { return parsePower("mWakefulness=Awake\n" + s) }
	first := sample("mLastWakeTime=70 (2 ms ago)\nmLastSleepTime=60 (12 ms ago)")
	older := sample("mLastWakeTime=70 (800 ms ago)\nmLastSleepTime=60 (810 ms ago)")
	missed := sample("mLastWakeTime=250 (2 ms ago)\nmLastSleepTime=200 (52 ms ago)")
	if older.cycledSince(first) || missed.cycledSince(powerSample{}) || !missed.cycledSince(first) {
		t.Fatal("passive wake history did not distinguish full sleep/wake")
	}
}

func TestConflictRetriesWithoutTouchingHardwareWhileBusy(t *testing.T) {
	calls := 0
	m := monitor{publish: func(status) {}, run: func(string) (int, string) {
		calls++
		if calls == 1 {
			return 2, "Panel calibration lock is busy."
		}
		return 0, ""
	}}
	p := preference{true, "60"}
	start := time.Now()
	for _, d := range []time.Duration{0, time.Second, 15 * time.Second, time.Hour} {
		m.reconcile(p, true, true, start.Add(d))
	}
	if calls != 2 || !m.verified {
		t.Fatal("conflict retry failed")
	}
}

func TestReceiptRecoveryWithSavedOff(t *testing.T) {
	var called string
	m := monitor{publish: func(status) {}, run: func(arg string) (int, string) { called = arg; return 0, "" }}
	m.reconcile(preference{false, "60"}, true, true, time.Now())
	if called != "stock" {
		t.Fatalf("called %q", called)
	}
}

func TestReadOnlyStatus(t *testing.T) {
	s := &state{current: status{preference{true, "59.94"}, "active", ""}}
	for _, method := range []string{"GET", "POST"} {
		w := httptest.NewRecorder()
		s.handler(w, httptest.NewRequest(method, "/calibration", nil))
		if method == "POST" {
			if w.Code != http.StatusMethodNotAllowed {
				t.Fatal(w.Code)
			}
			continue
		}
		var fields map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &fields); err != nil {
			t.Fatal(err)
		}
		if fields["rate"] != "59.94" || fields["enabled"] != true || fields["state"] != "active" {
			t.Fatal(fields)
		}
	}
}

func TestBootAndWakeGate(t *testing.T) {
	var g readiness
	start := time.Unix(1234, 0)
	for _, x := range []struct {
		sec            int
		boot, on, want bool
	}{
		{0, false, true, false}, {32, false, true, false},
		{35, true, true, false}, {62, true, true, false}, {64, true, false, false},
		{65, true, true, false}, {71, true, true, false}, {73, true, true, true},
		{80, true, false, false}, {81, true, true, false}, {82, true, true, false}, {83, true, true, true},
	} {
		if got := g.ready(start.Add(time.Duration(x.sec)*time.Second), x.boot, x.on); got != x.want {
			t.Fatalf("at %ds ready %v want %v", x.sec, got, x.want)
		}
	}
}

func TestRepeatedDemandAndDriverRecoveryDoNotRearm(t *testing.T) {
	p := preference{true, "60"}
	calls := 0
	m := monitor{publish: func(status) {}, run: func(string) (int, string) { calls++; return 2, "Panel checks failed." }}
	start := time.Unix(12345, 0)
	m.reconcile(p, true, true, start)
	m.reconcile(p, false, true, start.Add(time.Second))
	m.reconcile(p, true, true, start.Add(2*time.Second))
	if calls != 1 {
		t.Fatal("driver transition bypassed cooldown")
	}
	m.reconcile(p, true, true, start.Add(15*time.Second))
	for _, enabled := range []bool{true, false, true} {
		p.Enabled = enabled
		m.reconcile(p, false, true, start.Add(time.Minute))
		m.reconcile(p, true, true, start.Add(2*time.Minute))
	}
	if calls != 1 || m.paused == "" {
		t.Fatal("hardware failure continued probing")
	}
	m.newWake()
	m.reconcile(p, true, true, start.Add(3*time.Minute))
	if calls != 2 {
		t.Fatal("full wake failed to rearm")
	}
}

func TestRetroCommandSelection(t *testing.T) {
	for _, r := range []string{"gba", "nes"} {
		var cmds []string
		m := monitor{publish: func(status) {}, run: func(arg string) (int, string) {
			cmds = append(cmds, arg)
			return 0, ""
		}}
		m.reconcile(preference{true, r}, true, false, time.Now())
		if !reflect.DeepEqual(cmds, []string{"apply" + r}) {
			t.Fatal(cmds)
		}
	}
}
