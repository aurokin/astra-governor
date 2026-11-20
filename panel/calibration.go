package main

import (
	"errors"
	"fmt"
	"io"
	"os"
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
	if !p.Enabled && !owns {
		m.verified = false
		m.report(p, "off", "")
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
	if !p.Enabled {
		m.report(p, "restoring", "Restoring factory timing")
		if code, msg := m.run("stock"); code != 0 {
			m.retry(p, now, msg)
			return
		}
		m.next = time.Time{}
		m.verified = false
		m.report(p, "off", "")
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
