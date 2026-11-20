package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type preference struct {
	Enabled bool   `json:"enabled"`
	Rate    string `json:"rate"`
}
type status struct {
	preference
	State   string `json:"state"`
	Message string `json:"message"`
}

// Android owns the package directory's parent. Root confines path resolution
// inside that package and consumes only the supported preference records.
func readPreference(pkg string) (preference, error) {
	off := preference{Rate: "60"}
	root, err := os.OpenRoot(pkg)
	if errors.Is(err, os.ErrNotExist) {
		return off, nil
	}
	if err != nil {
		return off, err
	}
	defer root.Close()
	dir, err := root.Stat(".")
	if err != nil {
		return off, err
	}
	f, err := root.OpenFile("files/panel-calibration.conf", os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return off, nil
	}
	if err != nil {
		return off, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return off, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0002 != 0 || info.Sys().(*syscall.Stat_t).Uid != dir.Sys().(*syscall.Stat_t).Uid {
		return off, errors.New("Invalid calibration preference file")
	}
	data, err := io.ReadAll(io.LimitReader(f, 33))
	if err != nil {
		return off, err
	}
	enabled, rate, ok := strings.Cut(strings.TrimSuffix(string(data), "\n"), " ")
	if !ok || (enabled != "on" && enabled != "off") {
		return off, errors.New("Unrecognized calibration preference")
	}
	switch rate {
	case "60", "59.94", "gba", "nes":
		return preference{enabled == "on", rate}, nil
	default:
		return off, errors.New("Unrecognized calibration preference")
	}
}

type monitor struct {
	publish func(status)
	// The helper compares, programs changed words and verifies under one lock.
	run      func(string) (int, string)
	last     preference
	next     time.Time
	verified bool
	paused   string
	// Why calibration is kept off although the owner's choice is on; empty
	// when it is not. Factory timing is then restored as for a choice of off.
	held string
}

// off reports factory timing: as the owner's choice, or as a fault that says
// why when calibration is being kept off against that choice.
func (m *monitor) off(p preference) {
	if m.held != "" {
		m.report(p, "error", m.held)
		return
	}
	m.report(p, "off", "")
}

func (m *monitor) report(p preference, state, msg string) {
	m.publish(status{p, state, msg})
}

func temporaryConflict(msg string) bool {
	switch msg {
	case "A manual panel preset owns the display. Restore it to stock first.", "Panel calibration lock is busy.", "Panel is asleep.":
		return true
	}
	return false
}

func (m *monitor) retry(p preference, now time.Time, msg string) {
	if temporaryConflict(msg) {
		m.next = now.Add(15 * time.Second)
	} else {
		m.paused = msg + " Calibration paused until the next full sleep and wake."
		msg = m.paused
	}
	m.report(p, "error", msg)
}

// A transient lcd_state=5 while Android is still awake can be a failing
// driver's recovery loop. Only system sleep/suspend clears a hardware fault.
func (m *monitor) newWake() {
	m.next = time.Time{}
	m.verified = false
	m.paused = ""
}

type readiness struct {
	bootAt  time.Time
	onSince time.Time
	settled bool
}

// Stable samples involve only passive power state. No diagnostic DSI
// transaction is necessary to decide whether the display has settled.
func (g *readiness) ready(now time.Time, bootComplete, available bool) bool {
	if !bootComplete {
		g.bootAt = time.Time{}
		g.onSince = time.Time{}
		g.settled = false
		return false
	}
	if g.bootAt.IsZero() {
		g.bootAt = now
	}
	if !available {
		g.onSince = time.Time{}
		return false
	}
	if g.onSince.IsZero() {
		g.onSince = now
	}
	settle := 2 * time.Second
	if !g.settled {
		settle = 8 * time.Second
	}
	if now.Sub(g.bootAt) < 30*time.Second || now.Sub(g.onSince) < settle {
		return false
	}
	g.settled = true
	return true
}

func (m *monitor) reconcile(p preference, awake, owns bool, now time.Time) {
	if m.last != p {
		m.verified = false
		m.next = time.Time{}
		m.last = p
	}
	want := p.Enabled && m.held == ""
	if !want && !owns {
		m.verified = false
		m.off(p)
		return
	}
	if m.paused != "" {
		m.report(p, "error", m.paused)
		return
	}
	if !awake {
		m.verified = false
		m.report(p, "waiting", "Waiting for the display to settle")
		return
	}
	if now.Before(m.next) {
		return
	}
	if !want {
		m.report(p, "restoring", "Restoring factory timing")
		if code, msg := m.run("stock"); code != 0 {
			m.retry(p, now, msg)
			return
		}
		m.next = time.Time{}
		m.verified = false
		m.off(p)
		return
	}
	suffix := p.Rate
	if p.Rate == "59.94" {
		suffix = "5994"
	}
	if m.verified && owns {
		m.report(p, "active", "")
		return
	}
	m.report(p, "applying", "Applying calibration")
	if code, msg := m.run("apply" + suffix); code != 0 {
		m.retry(p, now, msg)
		return
	}
	m.verified = true
	m.next = time.Time{}
	m.report(p, "active", "")
}

// unexpectedRestart reports whether Android's reason for the last boot names a
// crash. The restarts calibration caused before 0.4 read "kernel_panic,..."
// and "watchdog_apps_bark". A requested reboot, a shutdown and a cold start do
// not count, and neither does a reset forced with the power key.
func unexpectedRestart(reason string) bool {
	return strings.HasPrefix(reason, "kernel_panic") || strings.HasPrefix(reason, "watchdog")
}

// restartHold keeps calibration off after the tablet restarted by itself with
// calibration switched on, until the owner changes the choice in the app.
//
// The service cannot tell whether calibration caused a restart, so it assumes
// the worst once and says so: a fault of ours then costs one restart instead of
// one at every wake. The hold lives in the service's own directory and
// survives reboots; the owner's saved choice is never rewritten.
type restartHold struct {
	dir string
}

func (h restartHold) seen() string { return filepath.Join(h.dir, "restart-seen") }
func (h restartHold) file() string { return filepath.Join(h.dir, "restart-hold") }

func (p preference) record() string {
	if p.Enabled {
		return "on " + p.Rate
	}
	return "off " + p.Rate
}

// judge looks at how this boot began, once per boot: a restart of the service
// must not put back a hold the owner has already cleared.
func (h restartHold) judge(bootID, reason string, p preference) error {
	if bootID == "" {
		return errors.New("no boot ID")
	}
	if b, _ := os.ReadFile(h.seen()); strings.TrimSpace(string(b)) == bootID {
		return nil
	}
	if unexpectedRestart(reason) && p.Enabled {
		// The reason comes from memory that survived a crash and can hold anything.
		clean := strings.Map(func(r rune) rune {
			if r < '!' || r > '~' {
				return -1
			}
			return r
		}, reason)
		if len(clean) > 40 {
			clean = clean[:40]
		}
		if err := writeSynced(h.file(), p.record()+"\n"+clean+"\n"); err != nil {
			return err
		}
	}
	return writeSynced(h.seen(), bootID+"\n")
}

// message is why calibration is held off for this choice, or empty when it is
// not. Any other choice than the one that was on at the restart ends the hold:
// switching off and on again in the app, or picking another rate.
func (h restartHold) message(p preference) string {
	b, err := os.ReadFile(h.file())
	if err != nil {
		return ""
	}
	choice, reason, _ := strings.Cut(strings.TrimSpace(string(b)), "\n")
	if choice != p.record() {
		os.Remove(h.file())
		return ""
	}
	return "The tablet restarted by itself (" + reason + "), so calibration is off. Switch it off and on to use it again."
}

func writeSynced(path, text string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	if _, err = f.WriteString(text); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}

func helperDiagnostic(output []byte, err error) string {
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	msg := strings.TrimSpace(lines[len(lines)-1])
	if msg == "" {
		msg = fmt.Sprintf("Calibration helper: %v", err)
	}
	if len(msg) > 230 {
		msg = msg[:230]
	}
	return msg
}
