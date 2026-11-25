package main

import (
	"cmp"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"
)

// watcher turns changes in the collectors' results into notes on the panel's bell: a service down
// or back, a pool unhealthy or healthy again, a pool filling past a threshold, a new TrueNAS
// warning, a GPU running hot, a source not answering. The first look after a start only records
// the baseline, so a restart never announces what was already true. Thresholds have a little
// hysteresis so a value sitting on a line doesn't flap.
type watcher struct {
	slots  []*slot
	post   func(src, msg, lvl, act string)
	now    func() time.Time
	start  time.Time
	seen   map[string]string // event key -> state as of the last look
	primed bool
	fed    map[*slot]bool // slots that have delivered data at least once (their first data is a silent baseline)
}

// ponytail: fixed thresholds; move to .env if they need tuning per install.
const (
	poolWarnPct = 85
	poolBadPct  = 95
	gpuHotC     = 90
	hysteresis  = 3               // a level clears this many units below its threshold
	downAfter   = 2 * time.Minute // a source must fail this long before it's announced
	watchEvery  = 10 * time.Second
)

var (
	sourceNames = map[string]string{"nas": "TrueNAS", "ts": "Tailscale", "boards": "Planka", "alerts": "NWS"}
	// these must fail this long, not downAfter, before they're announced: api.weather.gov is often briefly flaky
	slowSources = map[string]time.Duration{"alerts": 10 * time.Minute}
	levels      = map[string]int{"1": 1, "2": 2} // stored level state -> int ("" / "0" -> 0)
)

func newWatcher(slots []*slot, post func(src, msg, lvl, act string)) *watcher {
	return &watcher{slots: slots, post: post, now: time.Now, start: time.Now(), seen: map[string]string{}, fed: map[*slot]bool{}}
}

func (w *watcher) run() {
	for {
		time.Sleep(watchEvery)
		w.tick()
	}
}

// level maps a value to 0 (fine), 1 (warn) or 2 (bad), keeping the previous level until the value
// drops `hysteresis` below that level's threshold.
func level(v, warn, bad int, prev string) int {
	l := 0
	if v >= bad {
		l = 2
	} else if v >= warn {
		l = 1
	}
	p := levels[prev]
	if p > l && v > [3]int{0, warn, bad}[p]-hysteresis {
		return p
	}
	return l
}

func (w *watcher) tick() {
	next := map[string]string{}
	// set records key's state and reports the old one and whether this is a real change (not the
	// baseline pass, not a key seen for the first time).
	set := func(key, state string) (string, bool) {
		next[key] = state
		old, had := w.seen[key]
		return old, w.primed && had && old != state
	}

	for _, s := range w.slots {
		s.mu.Lock()
		data, ok, at := s.data, s.ok, s.at
		s.mu.Unlock()
		key := s.c.Key()
		first := data != nil && !w.fed[s] // a slow starter's first answer is its baseline, not news
		if data != nil {
			w.fed[s] = true
		}

		if name, watched := sourceNames[key]; watched {
			if at.IsZero() {
				at = w.start
			}
			state, after := "ok", cmp.Or(slowSources[key], downAfter)
			if !ok && w.now().Sub(at) > after {
				state = "down"
			}
			if _, changed := set("src:"+key, state); changed {
				if state == "down" {
					w.post(name, fmt.Sprintf("Can't reach %s for %d+ min", name, int(after.Minutes())), "warn", "")
				} else {
					w.post(name, name+" is answering again", "info", "")
				}
			}
		}

		switch d := data.(type) {
		case nas:
			for _, p := range d.Pools {
				health := "ok"
				if !p.OK {
					health = p.Health
				}
				if _, changed := set("pool:"+p.Name, health); changed {
					if p.OK {
						w.post("TrueNAS", "Pool "+p.Name+" is healthy again", "info", "")
					} else {
						w.post("TrueNAS", "Pool "+p.Name+" is "+p.Health, "alert", "")
					}
				}
				pct := 0
				if p.Used+p.Avail > 0 {
					pct = int(p.Used * 100 / (p.Used + p.Avail))
				}
				fk := "fill:" + p.Name
				l := level(pct, poolWarnPct, poolBadPct, w.seen[fk])
				if old, changed := set(fk, fmt.Sprint(l)); changed && l > levels[old] { // only announce rising
					w.post("TrueNAS", fmt.Sprintf("Pool %s is %d%% full", p.Name, pct), [3]string{"", "warn", "alert"}[l], "")
				}
			}
			for _, a := range d.Alerts {
				sev := severity[a.Level]
				if sev == 0 {
					continue // INFO/NOTICE: the tile's "+N info" is enough
				}
				k := "nas:" + a.Msg
				next[k] = "1"
				if _, had := w.seen[k]; w.primed && !had && !first {
					lvl := "warn"
					if sev >= 2 {
						lvl = "alert"
					}
					w.post("TrueNAS", a.Msg, lvl, "")
				}
			}
		case wxAlerts:
			// state "<lvl>|<take>|<event>": new key or changed event announces, rising Lvl escalates,
			// a vanished Take key ends (so does a take key whose event drops take).
			now := map[string]bool{}
			for _, a := range d.List {
				k := "wx:" + a.Key
				now[k] = true
				next[k] = fmt.Sprintf("%d|%t|%s", a.Lvl, a.Take, a.Event)
				act := ""
				if a.Take {
					act = "wx"
				}
				old, had := w.seen[k]
				if !w.primed || first {
					continue
				}
				f := strings.SplitN(old, "|", 3)
				announce := func() {
					lvl := map[string]string{"warning": "alert", "watch": "warn"}[a.Cls]
					if lvl == "" {
						lvl = "info"
					}
					msg := a.Event
					if a.Until > 0 { // a zero time.Time is negative Unix time
						msg += " until " + time.Unix(a.Until, 0).Local().Format("3:04 PM")
					}
					w.post("NWS", msg, lvl, act)
				}
				if !had {
					announce()
				} else if len(f) == 3 && f[2] != a.Event {
					if f[1] == "true" && !a.Take {
						w.post("NWS", f[2]+" ended", "info", "")
					}
					announce()
				} else if ol, _ := strconv.Atoi(f[0]); a.Lvl > ol {
					msg := a.Event + ": " + [4]string{"", "tornado observed", "considerable damage threat"}[min(a.Lvl, 2)]
					if a.Lvl >= 3 {
						msg = "TORNADO EMERGENCY"
					}
					w.post("NWS", msg, "alert", act)
				}
			}
			for k, st := range w.seen {
				if f := strings.SplitN(st, "|", 3); strings.HasPrefix(k, "wx:") && !now[k] && w.primed && len(f) == 3 && f[1] == "true" {
					w.post("NWS", f[2]+" ended", "info", "")
				}
			}
		case []gpuStat:
			for i, g := range d {
				if !g.Up {
					continue // off/asleep machines are normal; the tile already says OFF
				}
				hk := fmt.Sprintf("hot:%s#%d", g.Host, i)
				l := level(g.Temp, gpuHotC, 1000, w.seen[hk])
				if _, changed := set(hk, fmt.Sprint(l)); changed && l > 0 {
					w.post("GPU", fmt.Sprintf("%s %s is at %d °C", g.Host, g.Name, g.Temp), "alert", "")
				}
			}
		case []peer:
			if key != "svc" {
				continue // Tailscale devices come and go; services should stay up
			}
			for _, sv := range d {
				state := map[bool]string{true: "up", false: "down"}[sv.On]
				if _, changed := set("svc:"+sv.Name, state); changed {
					if sv.On {
						w.post(sv.Name, sv.Name+" is back up", "info", "")
					} else {
						w.post(sv.Name, sv.Name+" is down", "alert", "")
					}
				}
			}
		}
	}
	w.seen, w.primed = next, true
}

// bootNote reads a panel's X-Panel-Boot header ("<boot> <reason> <prev_up_s>"): the log line, and the bell note's
// text for an abnormal reset ("" for poweron, ext, sw, usb, jtag, deepsleep). ok is false unless it's exactly three
// fields: digits, 1-16 of [a-z_], digits (at most 64 bytes in all).
func bootNote(h string) (logLine, note string, ok bool) {
	f := strings.Split(h, " ")
	if len(h) > 64 || len(f) != 3 || len(f[1]) < 1 || len(f[1]) > 16 {
		return
	}
	for _, c := range []byte(f[1]) {
		if c != '_' && (c < 'a' || c > 'z') {
			return
		}
	}
	if _, err := strconv.ParseUint(f[0], 10, 64); err != nil || f[0][0] == '+' {
		return
	}
	up, err := strconv.ParseUint(f[2], 10, 64)
	if err != nil || f[2][0] == '+' {
		return
	}
	logLine = "panel: boot " + f[0] + " after " + f[1]
	if up > 0 {
		logLine += ", previous run up " + upFor(up)
	}
	var words string
	switch f[1] {
	case "poweron", "ext", "sw", "usb", "jtag", "deepsleep":
		return logLine, "", true
	case "panic":
		words = "a crash"
	case "int_wdt":
		words = "a crash (interrupt watchdog)"
	case "task_wdt":
		words = "a crash (task watchdog)"
	case "wdt":
		words = "a crash (watchdog)"
	case "cpu_lockup":
		words = "a crash (CPU lockup)"
	case "brownout":
		words = "a brownout (power dip)"
	case "pwr_glitch":
		words = "a power glitch"
	case "efuse":
		words = "an eFuse error"
	default:
		words = "an unexpected reset (" + f[1] + ")"
	}
	note = "Panel restarted after " + words
	if up > 0 {
		note += ", up " + upFor(up) + " before"
	}
	return logLine, note, true
}

// bootWatch logs each new header (the whole string changed). Past the first after a start, which is a silent
// baseline as for the watcher, so restarting the aggregator never repeats a note, it posts
// ("Panel", note, "warn", "") for an abnormal one. "" (older firmware) is ignored; an unreadable header is logged
// once per distinct value.
// ponytail: compares whole headers, so two power-ons in a row ("1 poweron 0" twice) log once; neither posts anything.
type bootWatch struct {
	mu     sync.Mutex
	last   string
	primed bool
	post   func(src, msg, lvl, act string)
}

func (b *bootWatch) seen(h string) {
	if h == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if h == b.last {
		return
	}
	b.last = h
	line, note, ok := bootNote(h)
	if !ok {
		log.Printf("panel: unreadable X-Panel-Boot %q", trunc(h, 64))
		return
	}
	first := !b.primed // the silent baseline is the first header that parses
	b.primed = true
	log.Print(line)
	if !first && note != "" {
		b.post("Panel", note, "warn", "")
	}
}

// upFor words a duration in seconds as the panel's boot log does: "3 d 4 h", "5 h 12 min", "42 min", "under a minute".
func upFor(s uint64) string {
	d, h, m := s/86400, s%86400/3600, s%3600/60
	switch {
	case d > 0 && h > 0:
		return fmt.Sprintf("%d d %d h", d, h)
	case d > 0:
		return fmt.Sprintf("%d d", d)
	case h > 0 && m > 0:
		return fmt.Sprintf("%d h %d min", h, m)
	case h > 0:
		return fmt.Sprintf("%d h", h)
	case m > 0:
		return fmt.Sprintf("%d min", m)
	}
	return "under a minute"
}
