// astrad2 is the second frame-rate governor for the RedMagic Astra (NP05J).
//
// Like the first one it only acts while the vendor's frame governor (IFAS) is
// active, which is while a Game Space game is in front outside Diablo mode, and
// it hands everything back when that ends. It differs in three ways:
//
//   - The control law (control.go) starts from ample clocks and works down,
//     instead of starting low and reacting to late frames.
//   - It sees every presented frame (SurfaceFlinger's per-layer history, twice
//     a second) instead of a once-a-second histogram.
//   - Its CPU knob is a frequency floor on both clusters instead of the WALT
//     boost, so the CPU and GPU knobs are the same kind of thing and a step
//     down can be predicted for either.
//
// Everything it writes is runtime sysfs state that disappears on reboot.
// Thermal limits are untouched: a floor never goes above the vendor's cap.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	ifasParams = "/sys/module/ifas/parameters/"
	ifasEnable = "/sys/kernel/ifas/ifas_enable"
	kgsl       = "/sys/class/kgsl/kgsl-3d0/"
	cpufreq    = "/sys/devices/system/cpu/cpufreq/"
	tick       = 500 * time.Millisecond
)

// The two CPU clusters: six performance cores and two prime cores.
var policies = []string{"policy0", "policy6"}

// Values that stop IFAS lowering its CPU cap, as in the first daemon: the cap
// becomes a guard rail at the mode's ceiling and our floor works beneath it.
var ratchet = map[string]string{
	"normal_frame_keep_count": "100000",
	"scaling_a":               "5",
	"rescue_freq_level_up":    "15",
	"cpu_loading_enable":      "0",
}

func read(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func readInt(path string) int {
	n, _ := strconv.Atoi(strings.Fields(read(path) + " 0")[0])
	return n
}

func write(path, v string) {
	if err := os.WriteFile(path, []byte(v), 0); err != nil {
		log.Printf("write %s=%s: %v", path, v, err)
	}
}

func run(timeout time.Duration, name string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, _ := exec.CommandContext(ctx, name, args...).Output()
	return string(out)
}

// frames follows the presenting layer of the game in front and keeps the last
// few seconds of its present times.
type frames struct {
	layer    string
	presents []int64 // CLOCK_MONOTONIC ns, ascending, about the last 7 s
	periodNs float64 // the panel's real refresh period, as SurfaceFlinger measured it
	periodAt time.Time
	curHold  int   // refreshes the most recent frame was held for
	holdFrom int64 // present time since which every frame has held curHold refreshes
	stale    int   // polls in a row that brought nothing new
}

var layerRe = regexp.MustCompile(`RequestedLayerState\{(.*?#\d+)(?: parentId=| relativeParentId=| z=|\})`)
var periodRe = regexp.MustCompile(`mPeriod: (\d+) ns`)

func latency(layer string) []int64 {
	var res []int64
	lines := strings.Split(run(2*time.Second, "/system/bin/dumpsys", "SurfaceFlinger", "--latency", layer), "\n")
	for _, l := range lines[min(1, len(lines)):] {
		f := strings.Fields(l)
		if len(f) != 3 {
			continue
		}
		if a, _ := strconv.ParseInt(f[1], 10, 64); a > 0 && a < 1<<62 { // pending frames carry INT64_MAX
			res = append(res, a)
		}
	}
	sort.Slice(res, func(i, j int) bool { return res[i] < res[j] })
	return res
}

// pick chooses the layer presenting most recently, preferring the package the
// vendor says the game scene belongs to. Surfaces are recreated on resume and
// on some scene changes, so this runs again whenever the current one goes quiet.
func (f *frames) pick() {
	pkg := strings.TrimSpace(run(2*time.Second, "/system/bin/settings", "get", "system", "nubia_game_scene_package_name"))
	var mine, others []string
	for _, m := range layerRe.FindAllStringSubmatch(run(2*time.Second, "/system/bin/dumpsys", "SurfaceFlinger", "--list"), -1) {
		switch {
		case pkg != "" && pkg != "null" && strings.Contains(m[1], pkg):
			mine = append(mine, m[1])
		case strings.Contains(m[1], "SurfaceView["):
			others = append(others, m[1])
		}
	}
	if len(mine) == 0 {
		mine = others
	}
	best, newest := "", int64(0)
	for _, l := range mine {
		if p := latency(l); len(p) > 0 && p[len(p)-1] > newest {
			best, newest = l, p[len(p)-1]
		}
	}
	if best != "" && best != f.layer {
		f.layer, f.presents, f.curHold = best, nil, 0
	}
}

func (f *frames) poll() {
	if f.periodNs == 0 || time.Since(f.periodAt) > 30*time.Second {
		f.periodAt = time.Now()
		if m := periodRe.FindStringSubmatch(run(2*time.Second, "/system/bin/dumpsys", "SurfaceFlinger")); m != nil {
			if ns, _ := strconv.ParseFloat(m[1], 64); ns > 4e6 && ns < 5e7 {
				f.periodNs = ns
			}
		}
	}
	if f.layer == "" || f.stale >= 3 {
		f.pick()
		f.stale = 0
	}
	if f.layer == "" || f.periodNs == 0 {
		return
	}
	newest := int64(0)
	if n := len(f.presents); n > 0 {
		newest = f.presents[n-1]
	}
	added := false
	for _, t := range latency(f.layer) {
		if t <= newest {
			continue
		}
		if newest != 0 {
			if hold := max(1, int(math.Round(float64(t-newest)/f.periodNs))); hold != f.curHold {
				f.curHold, f.holdFrom = hold, newest
			}
		}
		f.presents = append(f.presents, t)
		newest, added = t, true
	}
	if added {
		f.stale = 0
	} else {
		f.stale++
	}
	for len(f.presents) > 0 && f.presents[0] < newest-7e9 {
		f.presents = f.presents[1:]
	}
}

// rate returns frames per second over the last `window` of frames, and how many frames that was.
func (f *frames) rate(window time.Duration) (fps float64, n int) {
	if len(f.presents) < 2 {
		return 0, len(f.presents)
	}
	last := f.presents[len(f.presents)-1]
	i := sort.Search(len(f.presents), func(i int) bool { return f.presents[i] >= last-window.Nanoseconds() })
	n = len(f.presents) - i
	if span := last - f.presents[i]; n >= 2 && span > 0 {
		fps = float64(n-1) * 1e9 / float64(span)
	}
	return fps, n
}

// residency returns the average MHz over the time between two cumulative
// `MHz-or-kHz ticks` tables, which is exact where sampling the current
// frequency twice a second would not be.
type table map[int]int

func cpuResidency(policy string) table {
	t := table{}
	for _, l := range strings.Split(read(cpufreq+policy+"/stats/time_in_state"), "\n") {
		if f := strings.Fields(l); len(f) == 2 {
			khz, _ := strconv.Atoi(f[0])
			n, _ := strconv.Atoi(f[1])
			t[khz/1000] = n
		}
	}
	return t
}

func gpuResidency(freqsMHz []int) table {
	t := table{}
	for i, f := range strings.Fields(read(kgsl + "gpu_clock_stats")) {
		if n, _ := strconv.Atoi(f); i < len(freqsMHz) {
			t[freqsMHz[i]] = n
		}
	}
	return t
}

func meanMHz(prev, cur table) int {
	sum, total := 0, 0
	for mhz, n := range cur {
		if d := n - prev[mhz]; d > 0 {
			sum, total = sum+mhz*d, total+d
		}
	}
	if total == 0 {
		return 0
	}
	return sum / total
}

type threadTicks struct {
	ticks int
	cpu   int
	name  string
}

func threadsOf(pid int) map[int]threadTicks {
	res := map[int]threadTicks{}
	tasks, _ := os.ReadDir(fmt.Sprintf("/proc/%d/task", pid))
	for _, t := range tasks {
		tid, _ := strconv.Atoi(t.Name())
		s := read(fmt.Sprintf("/proc/%d/task/%d/stat", pid, tid))
		open, close := strings.IndexByte(s, '('), strings.LastIndexByte(s, ')')
		if open < 0 || close < open {
			continue
		}
		f := strings.Fields(s[close+1:]) // f[0] is the state, stat field 3
		if len(f) < 37 {
			continue
		}
		ut, _ := strconv.Atoi(f[11])
		st, _ := strconv.Atoi(f[12])
		cpu, _ := strconv.Atoi(f[36])
		res[tid] = threadTicks{ut + st, cpu, s[open+1 : close]}
	}
	return res
}

var layerPkgRe = regexp.MustCompile(`([A-Za-z0-9_]+(?:\.[A-Za-z0-9_]+)+)/`)

func pidOf(pkg string) int {
	entries, _ := os.ReadDir("/proc")
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		b, _ := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		if strings.SplitN(string(b), "\x00", 2)[0] == pkg {
			return pid
		}
	}
	return 0
}

type status struct {
	Time      string  `json:"time"`
	Enabled   bool    `json:"enabled"`
	Game      bool    `json:"game"`
	Layer     string  `json:"layer"`
	State     string  `json:"state"`
	Action    string  `json:"action"`
	FPS       float64 `json:"fps"`
	FPS6      float64 `json:"fps6"`
	Target    float64 `json:"target_fps"`
	PanelHz   float64 `json:"panel_hz"`
	LockK     int     `json:"lock_k"`
	LockedS   float64 `json:"locked_s"`
	CPUFloor  int     `json:"cpu_floor_mhz"`
	GPUFloor  int     `json:"gpu_floor_mhz"`
	CPULimit  int     `json:"cpu_limit_mhz"`
	GPULimit  int     `json:"gpu_limit_mhz"`
	CPUNow    int     `json:"cpu_mhz"`
	GPUNow    int     `json:"gpu_mhz"`
	GPUBusy   float64 `json:"gpu_busy"`
	HotThread string  `json:"hot_thread"`
	HotBusy   float64 `json:"hot_busy"`
	HotCPU    int     `json:"hot_cpu"`
}

type governor struct {
	mu      sync.Mutex
	st      status
	enabled bool

	c        *controller
	gpuMHz   []int            // KGSL frequencies by power level (index = level; lower level is faster)
	cpuFreqs map[string][]int // each cluster's frequency table in kHz, ascending
	stock    map[string]string
	game     bool

	f       frames
	pid     int
	pkg     string
	threads map[int]threadTicks
	lastThr time.Time
	cpuPrev map[string]table
	gpuPrev table
	busySum float64
	busyN   int
	leftAt  time.Time // when the last game scene ended
}

// gpuIndexToLevel maps the controller's ascending GPU table onto KGSL levels:
// index 0 is the slowest floor (the vendor's base), the last is level 0.
func (g *governor) gpuLevel(index int) int { return g.baseLevel() - index }

func (g *governor) baseLevel() int {
	for i, f := range g.gpuMHz {
		if f <= 222 {
			return i
		}
	}
	return len(g.gpuMHz) - 1
}

// cpuFloorKHz picks, in a cluster's own table, the lowest frequency at or above
// the controller's choice for the performance cluster.
func (g *governor) cpuFloorKHz(policy string, mhz int) int {
	t := g.cpuFreqs[policy]
	for _, f := range t {
		if f/1000 >= mhz {
			return f
		}
	}
	return t[len(t)-1]
}

func (g *governor) applyRatchet() {
	for k, v := range ratchet {
		if read(ifasParams+k) != v {
			write(ifasParams+k, v)
		}
	}
}

func (g *governor) apply() {
	if lvl := g.gpuLevel(g.c.gpu); readInt(kgsl+"min_pwrlevel") != lvl {
		write(kgsl+"min_pwrlevel", strconv.Itoa(lvl))
	}
	for _, p := range policies {
		khz := g.cpuFloorKHz(p, g.c.cpuMHz[g.c.cpu])
		if readInt(cpufreq+p+"/scaling_min_freq") != khz {
			write(cpufreq+p+"/scaling_min_freq", strconv.Itoa(khz))
		}
	}
}

// leaveGame hands the floors and IFAS parameters back. The GPU floor goes back
// to the vendor's base level, which is where every mode except Diablo keeps it.
// (The level found on entry is not trustworthy: after a visit to Diablo the
// vendor itself leaves the floor raised until the next mode change.) Diablo
// pins the floor to the ceiling, so there it is left alone; the first daemon
// could undo that pin when its own floor happened to sit on the same level.
func (g *governor) leaveGame() {
	if !g.game {
		return
	}
	g.game = false
	g.leftAt = time.Now()
	// Stock leaves each cluster's minimum at the bottom of its table.
	for _, p := range policies {
		write(cpufreq+p+"/scaling_min_freq", strconv.Itoa(g.cpuFreqs[p][0]))
	}
	if !diablo() && readInt(kgsl+"min_pwrlevel") == g.gpuLevel(g.c.gpu) {
		write(kgsl+"min_pwrlevel", strconv.Itoa(g.baseLevel()))
	}
	for k, v := range g.stock {
		if read(ifasParams+k) != v {
			write(ifasParams+k, v)
		}
	}
	g.c.reset()
}

func diablo() bool {
	return strings.TrimSpace(run(2*time.Second, "/system/bin/settings", "get", "global", "performance_mode_value")) == "4"
}

// repin covers the one ordering leaveGame cannot see: the vendor pins the GPU
// for Diablo and reports the mode only afterwards, so our restore lands on top
// of the pin. For a few seconds after leaving, put a lost pin back.
func (g *governor) repin() {
	if g.leftAt.IsZero() || time.Since(g.leftAt) > 5*time.Second {
		return
	}
	if lo, hi := readInt(kgsl+"min_pwrlevel"), readInt(kgsl+"max_pwrlevel"); lo != hi && diablo() {
		write(kgsl+"min_pwrlevel", strconv.Itoa(hi))
		log.Printf("put the Diablo GPU pin back (level %d)", hi)
	}
}

func (g *governor) enterGame() {
	g.game = true
	g.c.reset()
	g.f = frames{}
	g.cpuPrev, g.gpuPrev, g.threads = nil, nil, nil
}

// limits returns the highest floors the vendor currently allows, as controller indices.
func (g *governor) limits() (cpu, gpu int) {
	capMHz := readInt(cpufreq+"policy0/scaling_max_freq") / 1000
	for i, f := range g.c.cpuMHz {
		if f <= capMHz {
			cpu = i
		}
	}
	gpu = max(0, g.baseLevel()-readInt(kgsl+"max_pwrlevel"))
	return cpu, min(gpu, len(g.c.gpuMHz)-1)
}

func (g *governor) step() {
	now := time.Now()
	active := g.enabled && read(ifasEnable) == "1"
	switch {
	case active && !g.game:
		g.enterGame()
		log.Printf("game scene on")
	case !active && g.game:
		g.leaveGame()
		log.Printf("game scene off")
	}
	st := status{Time: now.Format(time.RFC3339), Enabled: g.enabled, Game: g.game}
	if !g.game {
		g.repin()
		g.st = st
		return
	}
	g.applyRatchet()
	g.f.poll()

	fps, n := g.f.rate(time.Second)
	fps6, _ := g.f.rate(6 * time.Second)
	age := time.Duration(0)
	lockedFor := time.Duration(0)
	if m := len(g.f.presents); m > 0 {
		age = time.Duration(monoNs() - g.f.presents[m-1])
		lockedFor = time.Duration(g.f.presents[m-1] - g.f.holdFrom)
	}
	flowing := n >= 5 && age < time.Second

	// Busy share of the app's busiest thread, and the cluster it runs on.
	if m := layerPkgRe.FindStringSubmatch(g.f.layer); m != nil && m[1] != g.pkg {
		g.pkg, g.pid, g.threads = m[1], pidOf(m[1]), nil
	}
	hotBusy, hotCPU, hotName := 0.0, 0, ""
	if g.pid != 0 {
		thr := threadsOf(g.pid)
		if len(thr) == 0 {
			g.pkg, g.pid = "", 0
		} else if dt := now.Sub(g.lastThr).Seconds(); g.threads != nil && dt > 0 {
			for tid, t := range thr {
				if p, ok := g.threads[tid]; ok {
					if busy := float64(t.ticks-p.ticks) / 100 / dt; busy > hotBusy {
						hotBusy, hotCPU, hotName = busy, t.cpu, t.name
					}
				}
			}
		}
		g.threads, g.lastThr = thr, now
	}
	policy := policies[0]
	if hotCPU >= 6 {
		policy = policies[1]
	}
	cpuCur := map[string]table{}
	for _, p := range policies {
		cpuCur[p] = cpuResidency(p)
	}
	gpuCur := gpuResidency(g.gpuMHz)
	cpuNow, gpuNow := 0, 0
	if g.cpuPrev != nil {
		cpuNow, gpuNow = meanMHz(g.cpuPrev[policy], cpuCur[policy]), meanMHz(g.gpuPrev, gpuCur)
	}
	g.cpuPrev, g.gpuPrev = cpuCur, gpuCur
	gpuBusy := g.busySum / float64(max(g.busyN, 1))
	g.busySum, g.busyN = 0, 0

	cpuLimit, gpuLimit := g.limits()
	panelHz := 0.0
	if g.f.periodNs > 0 {
		panelHz = 1e9 / g.f.periodNs
	}
	action := g.c.step(sample{now: now, flowing: flowing, fps: fps, fps6: fps6, panelHz: panelHz,
		gpuBusy: gpuBusy, hotBusy: hotBusy, cpuNow: cpuNow, gpuNow: gpuNow,
		lockK: g.f.curHold, lockedFor: lockedFor, cpuLimit: cpuLimit, gpuLimit: gpuLimit})
	g.apply()
	if action != "" && action != "learning" && action != "probing" && action != "idle" {
		log.Printf("%s: %.1f fps (target %.1f) cpu floor %d MHz gpu floor %d MHz, busy %.2f+%.2f at %d/%d MHz",
			action, fps, g.c.target, g.c.cpuMHz[g.c.cpu], g.c.gpuMHz[g.c.gpu], hotBusy, gpuBusy, cpuNow, gpuNow)
	}

	st.Layer, st.State, st.Action = g.f.layer, g.c.state.String(), action
	st.FPS, st.FPS6, st.Target, st.PanelHz = fps, fps6, g.c.target, panelHz
	st.LockK, st.LockedS = g.f.curHold, lockedFor.Seconds()
	st.CPUFloor, st.GPUFloor = g.c.cpuMHz[g.c.cpu], g.c.gpuMHz[g.c.gpu]
	st.CPULimit, st.GPULimit = g.c.cpuMHz[cpuLimit], g.c.gpuMHz[gpuLimit]
	st.CPUNow, st.GPUNow, st.GPUBusy = cpuNow, gpuNow, gpuBusy
	st.HotThread, st.HotBusy, st.HotCPU = hotName, hotBusy, hotCPU
	g.st = st
}

func main() {
	listen := flag.String("listen", "127.0.0.1:7879", "status/control HTTP address")
	monitor := flag.Bool("monitor", false, "start with control disabled (observe only)")
	flag.Parse()

	g := &governor{enabled: !*monitor, stock: map[string]string{}, cpuFreqs: map[string][]int{}}
	for _, f := range strings.Fields(read(kgsl + "gpu_available_frequencies")) {
		hz, _ := strconv.Atoi(f)
		g.gpuMHz = append(g.gpuMHz, hz/1_000_000)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(g.gpuMHz)))
	for _, p := range policies {
		for _, f := range strings.Fields(read(cpufreq + p + "/scaling_available_frequencies")) {
			khz, _ := strconv.Atoi(f)
			g.cpuFreqs[p] = append(g.cpuFreqs[p], khz)
		}
		sort.Ints(g.cpuFreqs[p])
	}
	if len(g.gpuMHz) < 2 || len(g.cpuFreqs["policy0"]) < 2 || read(ifasEnable) == "" {
		log.Fatal("KGSL, cpufreq or IFAS sysfs not found; this build only supports the NP05J vendor kernel")
	}
	// The controller's tables: the performance cluster's frequencies, and the GPU's from the vendor's base floor up.
	var cpuMHz, gpuMHz []int
	for _, khz := range g.cpuFreqs["policy0"] {
		cpuMHz = append(cpuMHz, khz/1000)
	}
	for lvl := g.baseLevel(); lvl >= 0; lvl-- {
		gpuMHz = append(gpuMHz, g.gpuMHz[lvl])
	}
	g.c = newController(cpuMHz, gpuMHz)
	for k, v := range ratchet {
		// If a previous instance died without restoring, do not adopt its values as stock.
		if cur := read(ifasParams + k); cur != v {
			g.stock[k] = cur
		}
	}
	for k, v := range map[string]string{"normal_frame_keep_count": "8", "scaling_a": "310", "rescue_freq_level_up": "0", "cpu_loading_enable": "1"} {
		if _, ok := g.stock[k]; !ok {
			g.stock[k] = v
		}
	}

	http.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(g.st)
	})
	set := func(on bool) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			g.mu.Lock()
			g.enabled = on
			g.mu.Unlock()
			fmt.Fprintln(w, map[bool]string{true: "enabled", false: "disabled"}[on])
		}
	}
	http.HandleFunc("/enable", set(true))
	http.HandleFunc("/disable", set(false))
	go func() { log.Print(http.ListenAndServe(*listen, nil)) }()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	busyTick := time.NewTicker(100 * time.Millisecond)
	stepTick := time.NewTicker(tick)
	log.Printf("astrad2 up: cpu floors %v MHz, gpu floors %v MHz, control=%v", cpuMHz, gpuMHz, g.enabled)
	for {
		select {
		case <-sig:
			g.mu.Lock()
			g.leaveGame()
			log.Print("restored and exiting")
			os.Exit(0)
		case <-busyTick.C:
			g.busySum += float64(readInt(kgsl+"gpu_busy_percentage")) / 100
			g.busyN++
		case <-stepTick.C:
			g.mu.Lock()
			g.step()
			g.mu.Unlock()
		}
	}
}
