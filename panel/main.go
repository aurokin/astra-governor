// This Magisk service owns the timing helper's wake/retry policy. The app may
// choose a calibration through device-protected storage; HTTP is read-only.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	packageDir = "/data/user_de/0/dev.auro.astra"
	stateDir   = "/data/adb/astra-panel-rate"
	receipt    = stateDir + "/table-session"
	idlePoll   = 10 * time.Second
)

type state struct {
	mu      sync.RWMutex
	current status
	// A status request means the app is open: the loop looks at the
	// preference at once instead of at its next slow check.
	asked chan struct{}
}

func (s *state) set(v status) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current != v {
		s.current = v
		log.Printf("enabled=%t rate=%s state=%s %s", v.Enabled, v.Rate, v.State, v.Message)
	}
}
func (s *state) handler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/calibration" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "GET required", 405)
		return
	}
	select {
	case s.asked <- struct{}{}:
	default:
	}
	s.mu.RLock()
	v := s.current
	s.mu.RUnlock()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

func panelAwake() bool {
	on, _ := os.ReadFile("/sys/class/drm/card0-DSI-1/dpms")
	if strings.TrimSpace(string(on)) != "On" {
		return false
	}
	lcd, _ := os.ReadFile("/proc/driver/lcd_state")
	return strings.TrimSpace(string(lcd)) == "0"
}

func prop(key string) string {
	out, _ := exec.Command("/system/bin/getprop", key).Output()
	return strings.TrimSpace(string(out))
}
func supported() bool {
	if prop("ro.product.model") != "NP05J" {
		return false
	}
	// The timing words were measured at the panel's pin on these builds only.
	switch prop("ro.build.display.id") {
	case "REDMAGICOS11.0.7_NP05J_GB", "REDMAGICOS11.0.8MR1_NP05J_GB":
		return true
	}
	return false
}

// Linux exposes suspend-inclusive boottime here; Go's monotonic timer on
// this device excludes suspend. This comparison ignores wall-clock changes.
func bootElapsed() (time.Duration, bool) {
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0, false
	}
	field, _, ok := strings.Cut(string(b), " ")
	seconds, err := strconv.ParseFloat(field, 64)
	return time.Duration(seconds * 1e9), ok && err == nil
}

type powerSample struct {
	state       string
	sleep, wake string
}

func parsePower(out string) (p powerSample) {
	for _, line := range strings.Split(out, "\n") {
		key, value, _ := strings.Cut(strings.TrimSpace(line), "=")
		switch key {
		case "mWakefulness":
			p.state = value
		case "mLastSleepTime", "mLastWakeTime":
			stamp, _, _ := strings.Cut(value, " ")
			if _, err := strconv.ParseUint(stamp, 10, 64); err == nil {
				if key == "mLastSleepTime" {
					p.sleep = stamp
				} else {
					p.wake = stamp
				}
			}
		}
	}
	return p
}

// Both timestamps must advance to rearm after a fault while the display
// appeared continuously on. Ignore the changing "(... ms ago)" suffix.
func (p powerSample) cycledSince(old powerSample) bool {
	return old.sleep != "" && old.wake != "" && p.sleep != "" && p.wake != "" &&
		p.sleep != old.sleep && p.wake != old.wake && p.state == "Awake"
}

func powerState(ctx context.Context) powerSample {
	bounded, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(bounded, "/system/bin/dumpsys", "power").Output()
	if err != nil {
		return powerSample{}
	}
	return parsePower(string(out))
}

// A service or kernel crash during controller traffic must not lead to an
// automatic repeat of the same transaction on every restart. Persist the
// directory entry before the first access. A clean result releases it.
func armHardwareGuard(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := f.Sync(); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func has(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func serve(ctx context.Context, moddir string, s *state) {
	helper := filepath.Join(moddir, "panel-table.sh")
	guard := filepath.Join(stateDir, "controller-in-flight")
	m := monitor{publish: s.set, run: func(action string) (int, string) {
		bounded, cancel := context.WithTimeout(ctx, 65*time.Second)
		defer cancel()
		ctl, err := exec.CommandContext(bounded, "/system/bin/getprop", "sys.powerctl").Output()
		if err != nil || strings.TrimSpace(string(ctl)) != "" {
			return 2, "Android is shutting down or its power service is unavailable"
		}
		if err := armHardwareGuard(guard); err != nil {
			return 2, "Could not persist controller access guard."
		}
		log.Printf("helper %s", action)
		cmd := exec.CommandContext(bounded, "/system/bin/sh", helper, action)
		cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
		cmd.WaitDelay = 5 * time.Second
		out, err := cmd.CombinedOutput()
		if err == nil {
			if err := os.Remove(guard); err != nil {
				return 2, "Could not clear controller access guard."
			}
			return 0, ""
		}
		var exit *exec.ExitError
		if bounded.Err() == nil && errors.As(err, &exit) {
			msg := helperDiagnostic(out, err)
			if temporaryConflict(msg) {
				if err := os.Remove(guard); err != nil {
					return 2, "Could not clear controller access guard."
				}
			}
			return exit.ExitCode(), msg
		}
		return 2, helperDiagnostic(out, err)
	}}
	if has(guard) {
		m.paused = "Previous controller access was interrupted. Calibration paused until the next full sleep and wake."
	}
	ok := supported()
	tick := time.NewTimer(0)
	<-tick.C
	defer tick.Stop()
	var gate readiness
	bootDone := false
	previous := time.Now()
	previousBoot, haveBoot := bootElapsed()
	var nextPowerCheck, nextWakeAudit time.Time
	var previousPower powerSample
	for {
		delay := time.Second
		now := time.Now()
		currentBoot, validBoot := bootElapsed()
		if haveBoot && validBoot && currentBoot-previousBoot-now.Sub(previous) > time.Second {
			m.newWake()
			gate.onSince = time.Time{}
			nextPowerCheck = time.Time{}
		}
		haveBoot, previousBoot = validBoot, currentBoot
		if !bootDone {
			bootDone = prop("sys.boot_completed") == "1"
		}
		previous = now
		p, err := readPreference(packageDir)
		disabled := has(filepath.Join(moddir, "disable")) || has(filepath.Join(moddir, "remove"))
		if disabled {
			p.Enabled = false
			err = nil
		}
		owns := has(receipt)
		if err != nil {
			m.verified = false
			m.report(m.last, "error", err.Error())
		} else if !ok && (p.Enabled || owns) {
			m.report(p, "error", "Unsupported tablet or firmware build")
		} else {
			need := p.Enabled || owns
			physical := need && panelAwake()
			if need && bootDone && !now.Before(nextPowerCheck) && ((physical && (gate.onSince.IsZero() || !now.Before(nextWakeAudit))) || (!physical && m.paused != "")) {
				power := powerState(ctx)
				nextWakeAudit = now.Add(time.Minute)
				if power.cycledSince(previousPower) {
					m.newWake()
					gate.onSince = time.Time{}
				}
				if power.sleep != "" && power.wake != "" {
					previousPower = power
				}
				if power.state == "Asleep" || power.state == "Dozing" {
					m.newWake()
				}
				if physical && power.state != "Awake" {
					physical = false
				}
				if !physical {
					nextPowerCheck = now.Add(2 * time.Second)
				}
			} else if physical && gate.onSince.IsZero() && now.Before(nextPowerCheck) {
				physical = false
			}
			ready := gate.ready(now, bootDone, physical)
			if physical && !ready {
				delay = 250 * time.Millisecond
			}
			wasPaused := m.paused != ""
			m.reconcile(p, ready, owns, now)
			if !wasPaused && m.paused != "" {
				// Only a cycle after this fault may clear the guard.
				previousPower = powerState(ctx)
				previous = time.Now()
				previousBoot, haveBoot = bootElapsed()
			}
		}
		if disabled && (!has(receipt) || !ok) {
			return
		}
		if err == nil && !p.Enabled && !owns {
			// Calibration is off and nothing of ours is in the controller:
			// the only thing to notice is the app switching it on.
			delay = idlePoll
		}
		tick.Reset(delay)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-s.asked:
		}
	}
}

func main() {
	exe, err := os.Executable()
	if err != nil {
		log.Fatal(err)
	}
	if err = os.MkdirAll(stateDir, 0700); err != nil {
		log.Fatal(err)
	}
	lock, err := os.OpenFile(stateDir+"/service.lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		log.Fatal(err)
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			os.Exit(73)
		}
		log.Fatal(err)
	}
	defer lock.Close()
	log.SetOutput(boundedLog{stateDir + "/service.log"})
	log.Printf("Starting panel service. Waiting for boot completion and a stable display.")
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	s := &state{current: status{preference{Rate: "60"}, "waiting", "Starting calibration service"}, asked: make(chan struct{}, 1)}
	l, err := net.Listen("tcp4", "127.0.0.1:7879")
	if err != nil {
		log.Printf("Calibration status unavailable: %v", err)
	} else {
		server := &http.Server{Handler: http.HandlerFunc(s.handler), ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 3 * time.Second, WriteTimeout: 3 * time.Second, IdleTimeout: 5 * time.Second, MaxHeaderBytes: 4096}
		defer server.Close()
		go func() {
			if err := server.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Print(err)
			}
		}()
	}
	serve(ctx, filepath.Dir(exe), s)
}
