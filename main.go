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
//   - Its CPU knob is a frequency floor instead of the WALT boost, so the CPU
//     and GPU knobs are the same kind of thing and a step down can be predicted
//     for either. The control law's floor goes to the cluster the game's
//     busiest thread runs on; the other cluster gets only what its own busiest
//     game thread needs for the same headroom, so six idle cores are not held
//     at a high clock because two busy ones need it.
//
// With -heat it also keeps the tablet under the vendor's thermal cut in Rise
// (heat.go), by giving up GPU floor and then CPU ceiling a little at a time.
//
// With -base it also does one thing outside games, and nothing else there: it
// holds the performance cores' minimum clock a little above the vendor's
// while the screen is on (see governor.rest). No frames are watched and no
// app is singled out; the control law stays behind the vendor's game scene.
//
// Everything it writes is runtime sysfs state that disappears on reboot.
// The vendor's thermal limits are untouched: a floor never goes above the
// vendor's cap, and a ceiling of ours can only be lower than theirs.
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
	fanDir     = "/sys/kernel/fan/"
	backlight  = "/sys/class/backlight/panel0-backlight/brightness"
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
	// IFAS has a GPU policy of its own that would move the GPU's ceiling as it
	// moves the CPU's. The vendor ships it switched off (in 515 recorded runs the
	// ceiling never left the mode's own: 1100 MHz in Rise, 832 in Balance, 342 in
	// Eco), and our GPU floor is built on that. This keeps it off in games should
	// an update switch it on.
	"enable_gpu_policy": "0",
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
	picked   time.Time
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

// pick chooses the layer that presented most frames in its last second,
// preferring the package the vendor says the game scene belongs to. Surfaces
// are recreated on resume and on some scene changes, so this runs again
// whenever the current one goes quiet. The newest frame alone is no guide: an
// app's own readout drawn over the picture (Artemis's statistics, once a
// second) is a layer of the same package and as recent as any.
func (f *frames) pick() {
	f.picked = time.Now()
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
	best, most, now := "", 0, monoNs()
	for _, l := range mine {
		p := latency(l)
		if len(p) == 0 || p[len(p)-1] < now-3e9 {
			continue // nothing shown for three seconds: not what is on screen
		}
		n := len(p) - sort.Search(len(p), func(i int) bool { return p[i] >= p[len(p)-1]-1e9 })
		if n > most {
			best, most = l, n
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
	// A layer that shows a frame or two a second is either a paused game or the
	// wrong layer; every few seconds, see whether another has more to show.
	slow := false
	if _, n := f.rate(time.Second); n < 5 && time.Since(f.picked) > 5*time.Second {
		slow = true
	}
	if f.layer == "" || f.stale >= 3 || slow {
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

// longer counts the frames within the last `window` that followed the one
// before by more than `ns`.
func (f *frames) longer(window time.Duration, ns float64) (n int) {
	for i := len(f.presents) - 1; i > 0 && f.presents[i] >= f.presents[len(f.presents)-1]-window.Nanoseconds(); i-- {
		if float64(f.presents[i]-f.presents[i-1]) > ns {
			n++
		}
	}
	return n
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
	// Read by the on-screen readout app, which predates this daemon.
	Package    string  `json:"package"`
	LongFrames int     `json:"long_frames"`    // frames in the last second shown later than the target rate allows
	CPUCur     [2]int  `json:"cpu_cur_mhz"`    // average clock of each cluster over the last tick
	CPUCap     [2]int  `json:"cpu_cap_mhz"`    // the vendor's current ceiling for each cluster
	CPUFloors  [2]int  `json:"cpu_floors_mhz"` // the floor each cluster holds
	Demand     int     `json:"demand_mhz"`     // where the stock governor reaches above the busiest cluster's floor, 0 if nowhere
	LoadSum    float64 `json:"load_sum"`
	SkinC      float64 `json:"skin_c"`
	BattC      float64 `json:"batt_c"`
	XoC        float64 `json:"xo_c"`        // the sensor the vendor's thermal cut watches (41 C)
	HeatLevel  int     `json:"heat_level"`  // steps of CPU ceiling given up to stay under it
	HeatSaving bool    `json:"heat_saving"` // spare GPU floor is being given up for the same reason
	FanLevel   int     `json:"fan_level"`
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
	leftAt  time.Time          // when the last game scene ended
	hot     string             // the cluster the game's busiest thread runs on
	hotFor  int                // ticks in a row the busiest thread was seen on the other one
	floor   map[string]int     // the floor each cluster currently holds, kHz
	since   map[string][]table // each cluster's clock residency per tick since its floor last changed, newest last
	side    []float64          // recent ratios of the other cluster's heaviest game thread to the busiest one, as work
	heat    heat               // CPU ceilings against the vendor's thermal cut
	heatC   float64            // the temperature to hold in Rise, 0 for none
	thrifty bool               // give up spare GPU floor whatever the temperature
	capKHz  map[string]int     // the ceiling we last asked for on each cluster, 0 if none
	fanMin  int                // the slowest fan level to allow in games, 1-5; 0 leaves the fan to the vendor
	fanHot  bool               // run the fan at its top level while the tablet is at or near a thermal cut
	nearCut bool               // it is
	fanWas  int                // the fan level found before our first write in this game, 0 if we have not written
	fanOurs int                // the level we last wrote
	hold    []int              // for experiments: floors in MHz (prime cores, GPU, other cores) to hold in games instead of running the control law
	base    int                // the performance cores' minimum outside games while the screen is on, kHz; 0 leaves it to the vendor
	rested  bool               // that minimum is in place and it was this instance that put it there
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
		switch {
		case g.hold != nil:
			khz = g.cpuFloorKHz(p, g.hold[map[string]int{"policy6": 0, "policy0": 2}[p]])
		case p != g.hot:
			khz = g.sideFloor(p, g.c.cpuMHz[g.c.cpu])
		}
		if c := g.capKHz[p]; c != 0 {
			khz = min(khz, c)
		}
		if khz != g.floor[p] {
			g.floor[p], g.since[p] = khz, nil
		}
		if readInt(cpufreq+p+"/scaling_min_freq") != khz {
			write(cpufreq+p+"/scaling_min_freq", strconv.Itoa(khz))
		}
	}
}

// reached returns the clock a cluster was at or above for a tenth of the time
// since its floor last changed (at most the last ten seconds), in kHz, or 0
// before three seconds of that have passed. The stock governor climbs for
// every burst of work, a frame too late to help that one; where it climbs to
// says what the cluster is being asked for.
func (g *governor) reached(policy string) int {
	ticks := g.since[policy]
	if len(ticks) < 6 {
		return 0
	}
	sum, total := table{}, 0
	for _, t := range ticks {
		for mhz, n := range t {
			sum[mhz] += n
			total += n
		}
	}
	freqs := g.cpuFreqs[policy]
	above := 0
	for i := len(freqs) - 1; i >= 0; i-- {
		if above += sum[freqs[i]/1000]; above*10 >= total && total > 0 {
			return freqs[i]
		}
	}
	return 0
}

// sideFloor is the floor for the cluster the busiest thread is not on, given
// the floor of the one it is on: the clock at which that cluster's heaviest game
// thread would be as busy as the busiest thread is at its floor. The ratio is
// the largest of the last ten seconds, so a thread that comes and goes keeps
// its clock.
func (g *governor) sideFloor(policy string, hotMHz int) int {
	ratio := 0.0
	for _, r := range g.side {
		ratio = max(ratio, r)
	}
	return g.cpuFloorKHz(policy, int(float64(hotMHz)*min(ratio, 1)))
}

// leaveGame hands the floors and IFAS parameters back. Each floor goes back to
// where every mode except Diablo keeps it: the bottom of each cluster's table
// and the GPU's base level. (The levels found on entry are not trustworthy:
// after a visit to Diablo the vendor itself leaves the GPU floor raised until
// the next mode change.) Diablo pins every floor to its ceiling, and switching
// to it is one way a game scene ends, so in Diablo nothing is restored. The
// floors are not compared with what we wrote first: a thermal limit can make
// them read back lower, and a floor skipped for that reason would stay raised.
func (g *governor) leaveGame() {
	if !g.game {
		return
	}
	g.game = false
	g.leftAt = time.Now()
	g.uncap()
	if g.fanOurs != 0 && readInt(fanDir+"fan_speed_level") == g.fanOurs {
		write(fanDir+"fan_speed_level", strconv.Itoa(g.fanWas))
	}
	g.fanWas, g.fanOurs = 0, 0
	if !diablo() {
		for _, p := range policies {
			write(cpufreq+p+"/scaling_min_freq", strconv.Itoa(g.cpuFreqs[p][0]))
		}
		if readInt(kgsl+"min_pwrlevel") < g.baseLevel() {
			write(kgsl+"min_pwrlevel", strconv.Itoa(g.baseLevel()))
		}
	}
	for k, v := range g.stock {
		if read(ifasParams+k) != v {
			write(ifasParams+k, v)
		}
	}
	g.c.reset()
}

// rest sets the performance cores' minimum for an app that is not a game: at
// -base while the screen is on, back at the bottom of the table otherwise.
//
// Outside games the vendor lets these six cores idle at 384 MHz (it keeps the
// two prime cores at 1018 MHz itself), and the stock governor is slow to leave
// it for work that comes in short bursts once per frame. A 60 fps film then
// loses a frame a second. Held at 1152 MHz, the clock the vendor itself falls
// back to when a browser sits untouched, it loses a fifth of that for no power
// that could be measured.
//
// It only ever raises a minimum that is below -base, and only lowers one that
// it raised itself and that still reads -base. A minimum someone else holds
// (Diablo pins it to the top; a second instance under test may hold this very
// clock) is never touched.
func (g *governor) rest(on bool) {
	if g.base == 0 {
		return
	}
	path := cpufreq + policies[0] + "/scaling_min_freq"
	switch cur := readInt(path); {
	case on && cur < g.base:
		write(path, strconv.Itoa(g.base))
		g.rested = true
	case !on && g.rested:
		if g.rested = false; cur == g.base {
			write(path, strconv.Itoa(g.cpuFreqs[policies[0]][0]))
		}
	}
}

// screenOn reports whether the panel is lit.
func screenOn() bool { return readInt(backlight) > 0 }

// uncap puts our ceiling request on each cluster back at the top of its table,
// where it limits nothing. The vendor's own limits are separate requests and
// stay as they are.
func (g *governor) uncap() {
	for _, p := range policies {
		t := g.cpuFreqs[p]
		write(cpufreq+p+"/scaling_max_freq", strconv.Itoa(t[len(t)-1]))
	}
	g.capKHz = map[string]int{}
}

// cool sets the fan and the CPU ceilings for the board sensor's temperature and
// the ceiling level it calls for.
func (g *governor) cool(now time.Time, xoC float64, level int) {
	g.fan(xoC, level)
	for _, p := range policies {
		t, cut := g.cpuFreqs[p], firstCut[p]
		// Each cluster gives up the same share of the way down to the vendor's first cut.
		steps := 0
		for i := len(t) - 1; i > 0 && t[i] > cut; i-- {
			steps++
		}
		khz := t[len(t)-1-(level*steps+g.heat.max/2)/max(g.heat.max, 1)]
		if level == 0 {
			khz = 0
		}
		if khz != g.capKHz[p] {
			if g.capKHz[p] = khz; khz == 0 {
				khz = t[len(t)-1]
			}
			write(cpufreq+p+"/scaling_max_freq", strconv.Itoa(khz))
			log.Printf("%s ceiling %d MHz at %.1f C (heading for %.1f C)", p, khz/1000, xoC, g.heat.ahead(now))
		}
	}
}

// fanTop is the fan's fastest level: 20,000 rpm, where the vendor gives a game
// level 2 (9,000 rpm) and goes to 4 once the tablet is hot.
const fanTop = 5

// fan runs the fan faster than the vendor does where that is asked for: never
// slower than -fan, and with -fanhot at its top level while clocks are being
// cut for heat or are about to be.
//
// "About to be" is within four degrees of the vendor's first cut on the board
// sensor (49 C in Rise, 41 C in the other modes; given up again a degree
// lower). "Being cut" is a ceiling of ours in place, or the vendor's thermal
// cap on the GPU.
//
// The fan is the user's first: it is only ever made faster, only while it is
// running (the vendor starts it for a game and stops it afterwards; a user who
// switched it off has fan_enable at 0, and it stays off), and the level found
// before our first write is put back when the reason has gone. In the vendor's
// "Fast cooling" mode there is nothing left to raise.
func (g *governor) fan(xoC float64, level int) {
	cut := 49.0
	if readInt(ifasParams+"gpu_max_pwrlevel_ceiling") != 0 { // 0 is Rise; Balance and Eco hold the GPU lower
		cut = 41.0
	}
	g.nearCut = level > 0 || readInt(kgsl+"thermal_pwrlevel") > 0 || xoC >= cut-4 || (g.nearCut && xoC >= cut-5)
	want := g.fanMin
	if g.fanHot && g.nearCut {
		want = fanTop
	}
	if read(fanDir+"fan_enable") != "1" {
		return
	}
	cur := readInt(fanDir + "fan_speed_level")
	set, ours, was := fanStep(cur, want, g.fanOurs, g.fanWas)
	if set != cur {
		write(fanDir+"fan_speed_level", strconv.Itoa(set))
		log.Printf("fan level %d (was %d, the vendor's %d) at %.1f C", set, cur, was, xoC)
	}
	g.fanOurs, g.fanWas = ours, was
}

// fanStep decides the fan's level from the level it is at, the slowest we want
// now (0 for no wish), the level we last wrote (0 if none stands) and the
// vendor's level that write replaced. It never goes below the vendor's own
// level, and takes a level that is neither ours nor slower than we want as the
// vendor's new choice.
func fanStep(cur, want, ours, was int) (set, newOurs, newWas int) {
	if ours == 0 || cur != ours {
		ours, was = 0, cur // nothing of ours stands: what is there is the vendor's
	}
	switch {
	case want > cur:
		return want, want, was
	case ours != 0 && want < cur: // ours is faster than is still wanted
		if back := max(want, was); back > was {
			return back, back, was
		}
		return was, 0, was
	}
	return cur, ours, was
}

// firstCut is the ceiling the vendor's thermal cut puts on each cluster at 41 C, kHz.
var firstCut = map[string]int{"policy0": 2400000, "policy6": 2841600}

// reclaim undoes floors a previous instance left behind if it was killed
// before it could restore them. Outside Diablo, which pins both, nothing else
// on this tablet raises a cluster's minimum or keeps the GPU floor above the
// base level, so anything found raised at start-up is ours. A game in front
// gets its floors back on the first tick.
func (g *governor) reclaim() {
	if diablo() {
		return
	}
	g.uncap() // our ceiling request cannot be read back by itself, and at the top it limits nothing
	for _, p := range policies {
		if bottom := g.cpuFreqs[p][0]; readInt(cpufreq+p+"/scaling_min_freq") > bottom {
			log.Printf("%s minimum was left raised; restoring", p)
			write(cpufreq+p+"/scaling_min_freq", strconv.Itoa(bottom))
		}
	}
	if readInt(kgsl+"min_pwrlevel") < g.baseLevel() {
		log.Printf("GPU floor was left raised; restoring")
		write(kgsl+"min_pwrlevel", strconv.Itoa(g.baseLevel()))
	}
}

func diablo() bool {
	return strings.TrimSpace(run(2*time.Second, "/system/bin/settings", "get", "global", "performance_mode_value")) == "4"
}

// repin covers the one ordering leaveGame cannot see: the vendor pins the
// clocks for Diablo and reports the mode only afterwards, so our restore lands
// on top of the pins. For a few seconds after leaving, put lost pins back.
func (g *governor) repin() {
	if g.leftAt.IsZero() || time.Since(g.leftAt) > 5*time.Second || !diablo() {
		return
	}
	if lo, hi := readInt(kgsl+"min_pwrlevel"), readInt(kgsl+"max_pwrlevel"); lo != hi {
		write(kgsl+"min_pwrlevel", strconv.Itoa(hi))
		log.Printf("put the Diablo GPU pin back (level %d)", hi)
	}
	for _, p := range policies {
		if top := g.cpuFreqs[p][len(g.cpuFreqs[p])-1]; readInt(cpufreq+p+"/scaling_min_freq") < min(top, readInt(cpufreq+p+"/scaling_max_freq")) {
			write(cpufreq+p+"/scaling_min_freq", strconv.Itoa(top))
			log.Printf("put the Diablo %s pin back", p)
		}
	}
}

func (g *governor) enterGame() {
	g.game = true
	g.c.reset()
	g.f = frames{}
	g.cpuPrev, g.gpuPrev, g.threads = nil, nil, nil
	g.hot, g.hotFor, g.floor, g.since, g.side = policies[0], 0, map[string]int{}, map[string][]table{}, nil
}

// limits returns the highest floors the vendor currently allows, as controller indices.
func (g *governor) limits() (cpu, gpu int) {
	capMHz := readInt(cpufreq+g.hot+"/scaling_max_freq") / 1000
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
	st.SkinC, st.BattC = float64(readInt(zone("skin-msm-therm")))/1000, float64(readInt("/sys/class/power_supply/battery/temp"))/10
	st.XoC = float64(readInt(zone("xo-therm"))) / 1000
	// The ceiling level follows the sensor between games too: it is still there
	// for a game that is left for a moment, and gone once the tablet has cooled.
	if g.heat.level == 0 {
		// With no ceiling of ours in the way, both clusters open to the top of their
		// tables means Rise with the vendor's cut off: the one place this acts.
		g.heat.target = g.heatC
		for _, p := range policies {
			if t := g.cpuFreqs[p]; readInt(cpufreq+p+"/scaling_max_freq") < t[len(t)-1] {
				g.heat.target = 0
			}
		}
	}
	saving := g.heat.saving
	st.HeatLevel = g.heat.step(now, st.XoC)
	if st.HeatSaving = g.heat.saving; saving != g.heat.saving && g.game {
		log.Printf("giving up spare GPU floor: %v at %.1f C", g.heat.saving, st.XoC)
	}
	if !g.game {
		g.repin()
		g.rest(g.enabled && screenOn())
		g.st = st
		return
	}
	g.cool(now, st.XoC, st.HeatLevel)
	st.FanLevel = readInt(fanDir + "fan_speed_level")
	g.applyRatchet()
	g.f.poll()

	fps, n := g.f.rate(time.Second)
	fps6, _ := g.f.rate(6 * time.Second)
	fpsNow, nNow := g.f.rate(400 * time.Millisecond)
	if nNow < 4 {
		fpsNow = fps // too few frames to judge by themselves
	}
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
	var clusterBusy [2]float64 // the busiest game thread last seen on each cluster
	if g.pid != 0 {
		thr := threadsOf(g.pid)
		if len(thr) == 0 {
			g.pkg, g.pid = "", 0
		} else if dt := now.Sub(g.lastThr).Seconds(); g.threads != nil && dt > 0 {
			for tid, t := range thr {
				if p, ok := g.threads[tid]; ok {
					busy := float64(t.ticks-p.ticks) / 100 / dt
					clusterBusy[min(t.cpu/6, 1)] = max(clusterBusy[min(t.cpu/6, 1)], busy)
					if busy > hotBusy {
						hotBusy, hotCPU, hotName = busy, t.cpu, t.name
					}
				}
			}
		}
		g.threads, g.lastThr = thr, now
	}
	// Follow the busiest thread to its cluster once it has been there two ticks
	// running; a thread caught mid-migration should not swap the floors over.
	if seen := policies[min(hotCPU/6, 1)]; hotName == "" || seen == g.hot {
		g.hotFor = 0
	} else if g.hotFor++; g.hotFor >= 2 {
		// What the stock governor did on that cluster while it was the idle one
		// says nothing about what the busiest thread needs there: start counting afresh.
		g.hot, g.hotFor, g.since = seen, 0, map[string][]table{}
	}
	policy := g.hot
	cpuCur := map[string]table{}
	for _, p := range policies {
		cpuCur[p] = cpuResidency(p)
	}
	gpuCur := gpuResidency(g.gpuMHz)
	cpuNow, gpuNow := 0, 0
	prev := g.cpuPrev
	if prev != nil {
		cpuNow, gpuNow = meanMHz(prev[policy], cpuCur[policy]), meanMHz(g.gpuPrev, gpuCur)
		for _, p := range policies {
			delta := table{}
			for mhz, n := range cpuCur[p] {
				if d := n - prev[p][mhz]; d > 0 {
					delta[mhz] = d
				}
			}
			if g.since[p] = append(g.since[p], delta); len(g.since[p]) > 20 {
				g.since[p] = g.since[p][1:]
			}
		}
	}
	// The other cluster's share of the work, for its floor.
	if prev != nil && hotBusy > 0.02 {
		hot, other := 0, 1
		if g.hot == policies[1] {
			hot, other = 1, 0
		}
		ratio := 1.0
		if clusterBusy[hot] > 0 {
			ratio = clusterBusy[other] * float64(meanMHz(prev[policies[other]], cpuCur[policies[other]])) / (clusterBusy[hot] * float64(max(cpuNow, 1)))
		}
		if g.side = append(g.side, ratio); len(g.side) > 20 {
			g.side = g.side[1:]
		}
	}
	// What the busiest thread's cluster is being asked for, if that is more than our floor gives.
	demand := 0
	if r := g.reached(policy); r > g.floor[policy] {
		demand = r / 1000
	}
	g.cpuPrev, g.gpuPrev = cpuCur, gpuCur
	gpuBusy := g.busySum / float64(max(g.busyN, 1))
	g.busySum, g.busyN = 0, 0

	cpuLimit, gpuLimit := g.limits()
	panelHz := 0.0
	if g.f.periodNs > 0 {
		panelHz = 1e9 / g.f.periodNs
	}
	action := "hold"
	if g.hold == nil {
		action = g.c.step(sample{now: now, flowing: flowing, fps: fps, fps6: fps6, fpsNow: fpsNow, panelHz: panelHz,
			gpuBusy: gpuBusy, hotBusy: hotBusy, cpuNow: cpuNow, gpuNow: gpuNow, demand: demand,
			lockK: g.f.curHold, lockedFor: lockedFor, cpuLimit: cpuLimit, gpuLimit: gpuLimit, thrifty: g.heat.saving || g.thrifty})
	} else {
		g.c.cpu, g.c.gpu = 0, 0
		for i, f := range g.c.cpuMHz[:cpuLimit+1] {
			if f <= g.hold[0] {
				g.c.cpu = i
			}
		}
		for i, f := range g.c.gpuMHz[:gpuLimit+1] {
			if f <= g.hold[1] {
				g.c.gpu = i
			}
		}
	}
	g.apply()
	if action != "" && action != "learning" && action != "probing" && action != "idle" && action != "watching" && action != "hold" {
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
	st.Package, st.LoadSum, st.Demand = g.pkg, hotBusy+gpuBusy, demand
	if g.c.target > 0 {
		st.LongFrames = g.f.longer(time.Second, 1.5e9/g.c.target)
	}
	for i, p := range policies {
		st.CPUCap[i], st.CPUFloors[i] = readInt(cpufreq+p+"/scaling_max_freq")/1000, g.floor[p]/1000
		if prev != nil {
			st.CPUCur[i] = meanMHz(prev[p], cpuCur[p])
		}
	}
	g.st = st
}

var zonePaths = map[string]string{}

// zone finds a thermal zone's temperature file by the zone's type.
func zone(name string) string {
	if zonePaths[name] == "" {
		zonePaths[name] = "/dev/null"
		zones, _ := filepath.Glob("/sys/class/thermal/thermal_zone*/type")
		for _, z := range zones {
			if read(z) == name {
				zonePaths[name] = filepath.Join(filepath.Dir(z), "temp")
			}
		}
	}
	return zonePaths[name]
}

// logFile appends to a file and starts a new one when it reaches 2 MB, keeping
// the previous one as path.1. The daemon runs for weeks and logs every decision.
type logFile struct {
	path string
	f    *os.File
	size int64
}

func (l *logFile) Write(b []byte) (int, error) {
	if l.f != nil && l.size > 2<<20 {
		l.f.Close()
		l.f = nil
		os.Rename(l.path, l.path+".1")
	}
	if l.f == nil {
		f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return 0, err
		}
		l.f, l.size = f, 0
		if info, err := f.Stat(); err == nil {
			l.size = info.Size()
		}
	}
	n, err := l.f.Write(b)
	l.size += int64(n)
	return n, err
}

func main() {
	listen := flag.String("listen", "127.0.0.1:7879", "status/control HTTP address")
	monitor := flag.Bool("monitor", false, "start with control disabled (observe only)")
	logPath := flag.String("log", "", "write the log to this file, keeping one previous file, instead of stderr")
	hold := flag.String("hold", "", "for experiments: hold these floors in games (\"cpuMHz,gpuMHz[,otherCoresMHz]\": the first is for the prime cores, and for the rest unless a third is given) instead of running the control law")
	fanMin := flag.Int("fan", 0, "the slowest fan level to allow in games, 1-5 (the vendor runs 2 until the tablet is hot); 0 leaves the fan to the vendor")
	heatC := flag.Float64("heat", 0, "in Rise, hold the board sensor (xo-therm) at or under this many C by giving up spare GPU floor and then CPU ceilings a step at a time; the vendor cuts the ceilings hard at 49. 0 leaves both alone")
	base := flag.Int("base", 0, "outside games, hold the six performance cores' minimum at this many MHz while the screen is on (the vendor's is 384); 0 leaves it alone")
	fanHot := flag.Bool("fanhot", false, "run the fan at its top level while clocks are being cut for heat, or the board is within four degrees of the vendor's first cut; only ever faster than the vendor has it, and never when it is switched off")
	statePath := flag.String("state", "", "remember /enable and /disable in this file, so a restart or a reboot keeps the choice; without it every start is enabled")
	thrifty := flag.Bool("thrifty", false, "give up GPU floor the GPU's busy share does not call for at any temperature, not only near -heat's target (content at the panel's rate whose CPU floor stays at its limit; see sample.thrifty)")
	flag.Parse()
	if *logPath != "" {
		log.SetOutput(&logFile{path: *logPath})
	}

	g := &governor{enabled: !*monitor && (*statePath == "" || read(*statePath) != "off"), stock: map[string]string{}, cpuFreqs: map[string][]int{}, fanMin: *fanMin, fanHot: *fanHot, thrifty: *thrifty}
	g.heatC = *heatC
	if *hold != "" {
		g.hold = []int{0, 0, 0}
		if n, _ := fmt.Sscanf(*hold, "%d,%d,%d", &g.hold[0], &g.hold[1], &g.hold[2]); n < 2 {
			log.Fatalf("-hold %q: want cpuMHz,gpuMHz[,otherCoresMHz]", *hold)
		} else if n == 2 {
			g.hold[2] = g.hold[0]
		}
	}
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
	for k, v := range map[string]string{"normal_frame_keep_count": "8", "scaling_a": "310", "rescue_freq_level_up": "0", "cpu_loading_enable": "1", "enable_gpu_policy": "0"} {
		if _, ok := g.stock[k]; !ok {
			g.stock[k] = v
		}
	}

	// One step of ceiling per entry of the prime cores' table down to the vendor's first cut.
	for t, i := g.cpuFreqs["policy6"], len(g.cpuFreqs["policy6"])-1; i > 0 && t[i] > firstCut["policy6"]; i-- {
		g.heat.max++
	}
	if *base != 0 {
		g.base = g.cpuFloorKHz(policies[0], *base)
	}
	g.reclaim()

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
			g.st.Enabled = on // a status read before the next tick already shows the change
			g.mu.Unlock()
			if *statePath != "" {
				if err := os.WriteFile(*statePath, []byte(map[bool]string{true: "on\n", false: "off\n"}[on]), 0600); err != nil {
					log.Printf("remember %s: %v", *statePath, err)
				}
			}
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
	log.Printf("astrad2 up: cpu floors %v MHz, gpu floors %v MHz, control=%v, fan=%d, heat=%v C in %d steps, base=%d MHz", cpuMHz, gpuMHz, g.enabled, g.fanMin, g.heatC, g.heat.max, g.base/1000)
	for {
		select {
		case <-sig:
			g.mu.Lock()
			g.leaveGame()
			g.rest(false)
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
