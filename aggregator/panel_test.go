package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// Minimal nvidia-smi -q -x shapes seen on the real hosts: driver 550 (TrueNAS, RTX 2070 Super,
// ollama resident) and driver 610 (Windows, RTX 5060 Ti, desktop apps only).
const smi550 = `<?xml version="1.0" ?><!DOCTYPE nvidia_smi_log SYSTEM "nvsmi_device_v12.dtd">
<nvidia_smi_log><gpu id="00000000:01:00.0">
<product_name>NVIDIA GeForce RTX 2070 SUPER</product_name>
<fb_memory_usage><total>8192 MiB</total><reserved>225 MiB</reserved><used>4471 MiB</used><free>3497 MiB</free></fb_memory_usage>
<utilization><gpu_util>0 %</gpu_util></utilization>
<temperature><gpu_temp>47 C</gpu_temp></temperature>
<gpu_power_readings><power_draw>11.65 W</power_draw></gpu_power_readings>
<module_power_readings><power_draw>N/A</power_draw></module_power_readings>
<processes><process_info><pid>4033782</pid><type>C</type><process_name>/usr/lib/ollama/llama-server</process_name></process_info></processes>
</gpu></nvidia_smi_log>`

const smi610 = `<?xml version="1.0" ?><nvidia_smi_log><gpu id="00000000:01:00.0">
<product_name>NVIDIA GeForce RTX 5060 Ti</product_name>
<fb_memory_usage><total>16311 MiB</total><used>2328 MiB</used></fb_memory_usage>
<utilization><gpu_util>16 %</gpu_util></utilization>
<temperature><gpu_temp>43 C</gpu_temp></temperature>
<gpu_power_readings><average_power_draw>16.53 W</average_power_draw><instant_power_draw>18.38 W</instant_power_draw></gpu_power_readings>
<processes><process_info><type>C+G</type></process_info><process_info><type>C+G</type></process_info></processes>
</gpu></nvidia_smi_log>`

func TestParseSMI(t *testing.T) {
	for _, tc := range []struct {
		xml  string
		want gpuStat
	}{
		{smi550, gpuStat{Name: "RTX 2070 SUPER", Up: true, Temp: 47, Util: 0, VRAM: 4471, VRAMTotal: 8192, Power: 12, Procs: 1, State: "loaded"}},
		{smi610, gpuStat{Name: "RTX 5060 Ti", Up: true, Temp: 43, Util: 16, VRAM: 2328, VRAMTotal: 16311, Power: 18, Procs: 0, State: "idle"}},
	} {
		got, err := parseSMI([]byte(tc.xml))
		if err != nil || len(got) != 1 || got[0] != tc.want {
			t.Errorf("parseSMI = %+v, %v; want %+v", got, err, tc.want)
		}
	}
}

func TestParseSMIFan(t *testing.T) {
	for _, tc := range []struct {
		fan  string
		want int
		ok   bool
	}{{"45 %", 45, true}, {"0 %", 0, true}, {"N/A", 0, false}, {"[N/A]", 0, false}, {"[Not Supported]", 0, false}, {"Unknown Error", 0, false}, {"", 0, false}} {
		x := strings.Replace(smi610, "<temperature>", "<fan_speed>"+tc.fan+"</fan_speed><temperature>", 1)
		got, err := parseSMI([]byte(x))
		if err != nil || len(got) != 1 || got[0].Fan != tc.want || got[0].FanOK != tc.ok {
			t.Errorf("fan %q: %+v, %v; want %d,%v", tc.fan, got, err, tc.want, tc.ok)
		}
	}
}

func TestAgentWireFan(t *testing.T) {
	f45, f0 := 45, 0
	b, _ := json.Marshal([]gpuWire{{gpuStat{Name: "x", Temp: 50}, &f45}, {gpuStat{Name: "y"}, &f0}, {gpuStat{Name: "z"}, nil}})
	if !strings.Contains(string(b), `"fan":45`) || !strings.Contains(string(b), `"fan":0`) || strings.Count(string(b), `"fan"`) != 2 {
		t.Fatalf("wire = %s", b)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(b) }))
	defer srv.Close()
	got := newGPU("h="+srv.URL).fetch(context.Background(), "h", srv.URL)
	if len(got) != 3 || got[0].Fan != 45 || !got[0].FanOK || got[1].Fan != 0 || !got[1].FanOK || got[2].FanOK || got[0].Host != "h" || got[0].Temp != 50 {
		t.Fatalf("decoded = %+v", got)
	}
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[{"n":"x","up":true}]`)) }))
	defer old.Close()
	if got := newGPU("h="+old.URL).fetch(context.Background(), "h", old.URL); len(got) != 1 || got[0].FanOK {
		t.Fatalf("old agent = %+v", got)
	}
}

func TestPanelHasNoFan(t *testing.T) {
	gpus := []gpuStat{{Host: "a", Name: "x", Up: true, Fan: 45, FanOK: true}}
	body := encodePanel([]*slot{{c: fixed{"homelab", "gpu", gpus}, data: gpus, at: time.Now(), ok: true}})
	if strings.Contains(string(body), "fan") || !strings.Contains(string(body), `"n":"x"`) {
		t.Errorf("panel = %s", body)
	}
}

func TestTrunc(t *testing.T) {
	s := trunc(strings.Repeat("—", 30), 20) // 3-byte runes
	if len(s) > 20 || !utf8.ValidString(s) || !strings.HasSuffix(s, "...") {
		t.Errorf("trunc = %q (%d bytes)", s, len(s))
	}
	if got := trunc("  Risks &\n Notes ", 20); got != "Risks & Notes" {
		t.Errorf("trunc = %q", got)
	}
}

type fixed struct {
	sec, key string
	data     any
}

func (f fixed) Section() string                      { return f.sec }
func (f fixed) Key() string                          { return f.key }
func (f fixed) Interval() time.Duration              { return time.Hour }
func (f fixed) Collect(context.Context) (any, error) { return f.data, nil }

// A failed collector keeps serving its last good data and marks only its own section stale.
func TestStaleKeepsData(t *testing.T) {
	then := time.Unix(1700000000, 0)
	slots := []*slot{
		{c: fixed{"homelab", "nas", nil}, data: "last good", at: then, ok: false},
		{c: fixed{"homelab", "ts", nil}, data: []peer{}, at: then.Add(time.Minute), ok: true},
		{c: fixed{"planka", "boards", nil}, data: []pkBoard{}, at: then, ok: true},
	}
	p := panel(slots)
	if p["homelab"]["stale"] != true || p["homelab"]["nas"] != "last good" || p["homelab"]["updated_at"] != then.Unix() {
		t.Errorf("homelab = %v", p["homelab"])
	}
	if p["planka"]["stale"] != false {
		t.Errorf("planka = %v", p["planka"])
	}
}

// The watcher only speaks on real changes: never on the baseline, only rising for fill levels,
// with hysteresis, once per new TrueNAS alert, and for a source only after it's been down a while.
func TestWatcher(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	nasSlot := &slot{c: fixed{"homelab", "nas", nil}, ok: true, at: now}
	svcSlot := &slot{c: fixed{"homelab", "svc", nil}, ok: true, at: now}
	gpuSlot := &slot{c: fixed{"homelab", "gpu", nil}, ok: true, at: now}
	var got []string
	w := newWatcher([]*slot{nasSlot, svcSlot, gpuSlot}, func(src, msg, lvl, act string) { got = append(got, lvl+" "+msg) })
	w.now, w.start = func() time.Time { return now }, now
	step := func(pct int, health string, alerts []alert, jellyfin bool, temp int) []string {
		got = nil
		nasSlot.data = nas{Pools: []pool{{Name: "tank", Health: health, OK: health == "ONLINE", Used: int64(pct), Avail: int64(100 - pct)}}, Alerts: alerts}
		svcSlot.data = []peer{{"jellyfin", jellyfin}}
		gpuSlot.data = []gpuStat{{Host: "pc", Name: "RTX", Up: true, Temp: temp}}
		w.tick()
		return got
	}
	expect := func(what string, got []string, want ...string) {
		t.Helper()
		if !slices.Equal(got, want) {
			t.Errorf("%s: got %q, want %q", what, got, want)
		}
	}
	hot := []alert{{"WARNING", "Disk sda is hot"}}
	expect("baseline is silent", step(90, "DEGRADED", hot, false, 95))
	expect("recoveries", step(80, "ONLINE", nil, true, 60), "info Pool tank is healthy again", "info jellyfin is back up")
	expect("service down", step(80, "ONLINE", nil, false, 60), "alert jellyfin is down")
	expect("pool past 85%", step(86, "ONLINE", nil, false, 60), "warn Pool tank is 86% full")
	expect("hysteresis holds", step(83, "ONLINE", nil, false, 60))
	expect("falling is silent", step(70, "ONLINE", nil, false, 60))
	expect("straight to 96%", step(96, "ONLINE", nil, false, 60), "alert Pool tank is 96% full")
	expect("unhealthy", step(96, "FAULTED", nil, false, 60), "alert Pool tank is FAULTED")
	expect("new TrueNAS warning", step(96, "FAULTED", hot, false, 60), "warn Disk sda is hot")
	expect("same warning again", step(96, "FAULTED", hot, false, 60))
	expect("info alerts ignored", step(96, "FAULTED", []alert{{"INFO", "update"}}, false, 60))
	expect("GPU hot", step(96, "FAULTED", nil, false, 91), "alert pc RTX is at 91 °C")
	expect("GPU hysteresis", step(96, "FAULTED", nil, false, 88))
	expect("GPU cools quietly", step(96, "FAULTED", nil, false, 70))

	nasSlot.ok = false
	now = now.Add(time.Minute)
	expect("brief outage is silent", step(96, "FAULTED", nil, false, 70))
	now = now.Add(2 * time.Minute)
	expect("long outage", step(96, "FAULTED", nil, false, 70), "warn Can't reach TrueNAS for 2+ min")
	nasSlot.ok, nasSlot.at = true, now
	expect("source back", step(96, "FAULTED", nil, false, 70), "info TrueNAS is answering again")
}

// NWS alerts: silent baseline, a new takeover warning, an escalation, then "ended"; the key's
// event and level ride in the stored state.
func TestWatcherWeatherAlerts(t *testing.T) {
	alerts := &slot{c: fixed{"weather", "alerts", nil}, ok: true, at: time.Now()}
	var got []string
	w := newWatcher([]*slot{alerts}, func(src, msg, lvl, act string) { got = append(got, src+"|"+lvl+"|"+act+"|"+msg) })
	step := func(l ...wxAlert) []string {
		got = nil
		alerts.data = wxAlerts{List: l}
		w.tick()
		return got
	}
	watch := wxAlert{Key: "a", Event: "Tornado Watch", Cls: "watch", Until: 1_800_000_000}
	tor := wxAlert{Key: "b", Event: "Tornado Warning", Cls: "warning", Until: 1_800_000_000, Take: true}
	if g := step(watch); len(g) != 0 {
		t.Errorf("baseline: %q", g)
	}
	g := step(watch, tor)
	if len(g) != 1 || !strings.HasPrefix(g[0], "NWS|alert|wx|Tornado Warning until ") {
		t.Errorf("new warning: %q", g)
	}
	tor.Lvl = 1
	if g := step(watch, tor); !slices.Equal(g, []string{"NWS|alert|wx|Tornado Warning: tornado observed"}) {
		t.Errorf("escalation: %q", g)
	}
	if g := step(watch); !slices.Equal(g, []string{"NWS|info||Tornado Warning ended"}) {
		t.Errorf("ended: %q", g)
	}
}

// A dead NWS feed must not look like "no alerts": it's announced too, after 10 min (it hiccups often).
func TestWatcherNWSDown(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	alerts := &slot{c: fixed{"weather", "alerts", nil}, ok: true, at: now}
	var got []string
	w := newWatcher([]*slot{alerts}, func(src, msg, lvl, act string) { got = append(got, src+"|"+lvl+"|"+msg) })
	w.now, w.start = func() time.Time { return now }, now
	step := func(d time.Duration, ok bool) []string {
		got = nil
		now = now.Add(d)
		alerts.ok = ok
		if ok {
			alerts.at = now
		}
		w.tick()
		return got
	}
	if g := step(0, true); len(g) != 0 {
		t.Errorf("baseline: %q", g)
	}
	if g := step(5*time.Minute, false); len(g) != 0 {
		t.Errorf("5 min down: %q", g)
	}
	if g := step(6*time.Minute, false); !slices.Equal(g, []string{"NWS|warn|Can't reach NWS for 10+ min"}) {
		t.Errorf("11 min down: %q", g)
	}
	if g := step(time.Minute, true); !slices.Equal(g, []string{"NWS|info|NWS is answering again"}) {
		t.Errorf("back: %q", g)
	}
}

func TestWatcherWeatherUpdateIsQuiet(t *testing.T) {
	alerts := &slot{c: fixed{"weather", "alerts", nil}, ok: true, at: time.Now()}
	var got []string
	w := newWatcher([]*slot{alerts}, func(src, msg, lvl, act string) { got = append(got, msg) })
	a := wxAlert{Key: "b", Event: "Tornado Warning", Cls: "warning", Until: 1_800_000_000, Take: true}
	alerts.data = wxAlerts{List: []wxAlert{a}}
	w.tick()
	a.Until += 1800
	alerts.data = wxAlerts{List: []wxAlert{a}}
	w.tick()
	if len(got) != 0 {
		t.Errorf("update spoke: %q", got)
	}
}

// A source that first answers after the watcher's first tick still gets a silent baseline.
func TestWatcherSlowStartIsSilent(t *testing.T) {
	alerts := &slot{c: fixed{"weather", "alerts", nil}}
	nasSlot := &slot{c: fixed{"homelab", "nas", nil}}
	var got []string
	w := newWatcher([]*slot{alerts, nasSlot}, func(src, msg, lvl, act string) { got = append(got, msg) })
	w.tick() // nothing has answered yet
	watch := wxAlert{Key: "a", Event: "Tornado Watch", Cls: "watch", Until: 1_800_000_000}
	alerts.data, alerts.ok, alerts.at = wxAlerts{List: []wxAlert{watch}}, true, time.Now()
	nasSlot.data, nasSlot.ok, nasSlot.at = nas{Alerts: []alert{{"WARNING", "Disk sda is hot"}}}, true, time.Now()
	w.tick()
	if len(got) != 0 {
		t.Fatalf("late first answers spoke: %q", got)
	}
	tor := wxAlert{Key: "b", Event: "Tornado Warning", Cls: "warning", Until: 1_800_000_000, Take: true}
	alerts.data = wxAlerts{List: []wxAlert{watch, tor}}
	w.tick()
	if len(got) != 1 || !strings.HasPrefix(got[0], "Tornado Warning until ") {
		t.Errorf("new warning after slow start: %q", got)
	}
}

// A chain whose event changes (or whose take flips) is announced; a note with no end has no "until".
func TestWatcherWeatherEventChange(t *testing.T) {
	alerts := &slot{c: fixed{"weather", "alerts", nil}, ok: true, at: time.Now()}
	var got []string
	w := newWatcher([]*slot{alerts}, func(src, msg, lvl, act string) { got = append(got, lvl+"|"+act+"|"+msg) })
	step := func(l ...wxAlert) []string {
		got = nil
		alerts.data = wxAlerts{List: l}
		w.tick()
		return got
	}
	svr := wxAlert{Key: "c", Event: "Severe Thunderstorm Warning", Cls: "warning", Until: 1_800_000_000}
	tor := svr
	tor.Event, tor.Take = "Tornado Warning", true
	if g := step(svr); len(g) != 0 {
		t.Errorf("baseline: %q", g)
	}
	if g := step(tor); len(g) != 1 || !strings.HasPrefix(g[0], "alert|wx|Tornado Warning until ") {
		t.Errorf("upgrade: %q", g)
	}
	if g := step(svr); len(g) != 2 || g[0] != "info||Tornado Warning ended" ||
		!strings.HasPrefix(g[1], "alert||Severe Thunderstorm Warning until ") {
		t.Errorf("downgrade: %q", g)
	}
	sps := wxAlert{Key: "d", Event: "Special Weather Statement", Cls: "advisory"}
	if g := step(svr, sps); !slices.Equal(g, []string{"info||Special Weather Statement"}) {
		t.Errorf("no end time: %q", g)
	}
}

// Pins shipped behaviour: only a rising level speaks.
func TestWatcherWeatherLevels(t *testing.T) {
	alerts := &slot{c: fixed{"weather", "alerts", nil}, ok: true, at: time.Now()}
	var got []string
	w := newWatcher([]*slot{alerts}, func(src, msg, lvl, act string) { got = append(got, lvl+"|"+act+"|"+msg) })
	tor := wxAlert{Key: "b", Event: "Tornado Warning", Cls: "warning", Until: 1_800_000_000, Take: true}
	for _, c := range []struct {
		lvl  int
		want []string
	}{
		{0, nil},
		{2, []string{"alert|wx|Tornado Warning: considerable damage threat"}},
		{3, []string{"alert|wx|TORNADO EMERGENCY"}},
		{1, nil},
	} {
		got, tor.Lvl = nil, c.lvl
		alerts.data = wxAlerts{List: []wxAlert{tor}}
		w.tick()
		if !slices.Equal(got, c.want) {
			t.Errorf("lvl %d: %q, want %q", c.lvl, got, c.want)
		}
	}
}

func TestBootNote(t *testing.T) {
	for _, c := range []struct{ h, note string }{
		{"12 task_wdt 273600", "Panel restarted after a crash (task watchdog), up 3 d 4 h before"},
		{"3 brownout 0", "Panel restarted after a brownout (power dip)"},
		{"1 poweron 0", ""}, {"5 sw 120", ""}, {"2 usb 30", ""}, {"4 ext 9", ""},
		{"7 odd_new 60", "Panel restarted after an unexpected reset (odd_new), up 1 min before"},
	} {
		line, note, ok := bootNote(c.h)
		if !ok || note != c.note || line == "" {
			t.Errorf("bootNote(%q) = %q, %q, %v; want note %q", c.h, line, note, ok, c.note)
		}
	}
	if line, _, _ := bootNote("12 task_wdt 273600"); line != "panel: boot 12 after task_wdt, previous run up 3 d 4 h" {
		t.Errorf("log line = %q", line)
	}
	for _, h := range []string{"", "12", "x panic 1", "12 Panic 1", "12 panic 1 2", "12 panic -1", "12 panic 1" + strings.Repeat("0", 60)} {
		if _, _, ok := bootNote(h); ok {
			t.Errorf("bootNote(%q) ok, want not", h)
		}
	}
	for _, tok := range []string{"panic", "int_wdt", "task_wdt", "wdt", "cpu_lockup", "brownout", "pwr_glitch", "efuse", "sdio", "unknown", "abcdefghijklmnop"} {
		if _, note, ok := bootNote("4294967295 " + tok + " 4294967295"); !ok || note == "" || len(note) > noteMax {
			t.Errorf("%s: note %q (%d bytes), ok %v", tok, note, len(note), ok)
		}
	}
}

func TestBootWatch(t *testing.T) {
	var got []string
	b := &bootWatch{post: func(src, msg, lvl, act string) { got = append(got, src+"|"+msg+"|"+lvl+"|"+act) }}
	for _, h := range []string{"5 panic 60", "5 panic 60"} {
		b.seen(h)
	}
	if len(got) != 0 {
		t.Fatalf("baseline posted %q", got)
	}
	b.seen("6 task_wdt 3600")
	want := "Panel|Panel restarted after a crash (task watchdog), up 1 h before|warn|"
	if len(got) != 1 || got[0] != want {
		t.Fatalf("got %q, want [%q]", got, want)
	}
	// ponytail: "again" is checked right after the first (last == header); after a different header it would count as new.
	for _, h := range []string{"6 task_wdt 3600", "1 poweron 0", "", "junk"} {
		b.seen(h)
	}
	if len(got) != 1 {
		t.Fatalf("extra posts: %q", got)
	}
	// junk first after a start: no baseline taken, so the first valid header is silent and the next different one notes.
	var got2 []string
	b2 := &bootWatch{post: func(src, msg, lvl, act string) { got2 = append(got2, msg) }}
	b2.seen("junk")
	b2.seen("5 panic 60")
	if len(got2) != 0 {
		t.Fatalf("first valid header after junk posted %q", got2)
	}
	b2.seen("6 panic 60")
	if len(got2) != 1 {
		t.Fatalf("next crash header: got %q", got2)
	}
}

// ntfy publishes: the topic path with Title/Priority headers, JSON publish, priority names.
func TestNtfyNote(t *testing.T) {
	h := http.Header{}
	h.Set("Title", "Backup failed")
	h.Set("Priority", "urgent")
	if src, msg, lvl := ntfyNote("backups", h, []byte("rsync exited 23\n")); src != "backups" || msg != "Backup failed: rsync exited 23" || lvl != "alert" {
		t.Errorf("header publish: %q %q %q", src, msg, lvl)
	}
	h = http.Header{}
	h.Set("X-Priority", "4")
	if _, msg, lvl := ntfyNote("ci", h, []byte("build #42 is green")); msg != "build #42 is green" || lvl != "warn" {
		t.Errorf("x-priority: %q %q", msg, lvl)
	}
	src, msg, lvl := ntfyNote("", http.Header{}, []byte(`{"topic":"kuma","title":"Jellyfin","message":"is down","priority":3}`))
	if src != "kuma" || msg != "Jellyfin: is down" || lvl != "info" {
		t.Errorf("json publish: %q %q %q", src, msg, lvl)
	}
}

// Alertmanager / Grafana webhooks: severity picks the colour, resolved is blue, max 5 per delivery.
func TestAlertmanagerNotes(t *testing.T) {
	body := `{"status":"firing","alerts":[
	  {"status":"firing","labels":{"alertname":"HostDown","severity":"critical","instance":"nas:9100"},"annotations":{"summary":"Host is down"}},
	  {"status":"firing","labels":{"alertname":"DiskFilling"},"annotations":{"description":"tank is 91% full"}},
	  {"status":"resolved","labels":{"alertname":"HighCPU","severity":"warning"},"annotations":{"summary":"CPU high"}}]}`
	got, err := alertmanagerNotes([]byte(body))
	want := [][3]string{
		{"HostDown", "Host is down (nas:9100)", "alert"},
		{"DiskFilling", "tank is 91% full", "warn"},
		{"HighCPU", "Resolved: CPU high", "info"},
	}
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("got %q, %v", got, err)
	}
	many := `{"alerts":[` + strings.Repeat(`{"status":"firing","labels":{"alertname":"X"}},`, 9) + `{"status":"firing"}]}`
	if got, _ := alertmanagerNotes([]byte(many)); len(got) != 5 {
		t.Errorf("storm: %d notes, want 5", len(got))
	}
	if _, err := alertmanagerNotes([]byte("nope")); err == nil {
		t.Error("garbage accepted")
	}
}

// Demo mode: the document fits the budget and parses; Done works on the demo boards.
func TestDemo(t *testing.T) {
	body := demoJSON()
	var doc struct {
		Homelab struct{ Gpu []gpuStat }
		Planka  struct{ Boards []pkBoard }
		Notify  struct{ Notes []note }
	}
	if err := json.Unmarshal(body, &doc); err != nil || len(body) > 8192 {
		t.Fatalf("demo doc: %d bytes, %v", len(body), err)
	}
	if len(doc.Homelab.Gpu) != 2 || len(doc.Planka.Boards) != 2 || len(doc.Notify.Notes) != 4 || doc.Notify.Notes[0].Read || !doc.Notify.Notes[1].Read {
		t.Fatalf("demo content: %+v", doc)
	}
	b := newDemoBoards(time.Now())
	if err := b.markDone(context.Background(), "1102"); err != nil {
		t.Fatal(err)
	}
	got, _ := b.Collect(context.Background())
	if c := got.([]pkBoard)[0].Cards[0]; c.ID != "1102" || !c.Done || c.List != "Done" {
		t.Errorf("after Done: %+v", c)
	}
	if err := b.markDone(context.Background(), "9999"); err == nil {
		t.Error("unknown card accepted")
	}
}

// Notes survive a restart through NOTES_FILE, read flags included, and ids keep rising.
func TestNotesPersist(t *testing.T) {
	file := filepath.Join(t.TempDir(), "notes.json")
	a := newNotifier(file)
	id := a.add("x", "hello", "info", "").ID
	a.markRead(id)
	b := newNotifier(file)
	if len(b.notes) != 1 || !b.notes[0].Read || b.notes[0].Msg != "hello" || b.add("x", "y", "info", "").ID <= id {
		t.Fatalf("reloaded notes: %+v", b.notes)
	}
}

// Notes: ids keep rising, the ring keeps the newest, the panel gets the newest first, reads stick.
func TestNotifier(t *testing.T) {
	n := newNotifier("")
	first := n.add("Claude", "one", "info", "").ID
	for i := 0; i < keepNotes+5; i++ {
		n.add("x", fmt.Sprint(i), "info", "")
	}
	if len(n.notes) != keepNotes || n.notes[0].ID <= first {
		t.Fatalf("ring: %d notes, oldest id %d", len(n.notes), n.notes[0].ID)
	}
	got, _ := n.Collect(context.Background())
	list := got.([]note)
	if len(list) != panelNotes || list[0].ID != n.next || list[0].Msg != fmt.Sprint(keepNotes+4) {
		t.Fatalf("panel view: %+v", list)
	}
	n.markRead(list[1].ID)
	got, _ = n.Collect(context.Background())
	if list = got.([]note); list[0].Read || !list[1].Read || !list[4].Read {
		t.Fatalf("read flags: %+v", list)
	}
}

// The device polls one document that must stay under ~8 KB. Fill every section to its caps with
// 3-byte UTF-8 text (Planka list names use em dashes) and check the encoded size.
func TestPanelUnder8KB(t *testing.T) {
	long := strings.Repeat("—", 100)
	var boards []pkBoard
	for i := range maxBoards {
		var b boardResp
		b.Item.ID, b.Item.Name = fmt.Sprintf("19106837321765%05d", i), long
		b.Included.Lists = append(b.Included.Lists, struct {
			ID, Name, Type string
			Position       float64
		}{"1", long, "closed", 1})
		for l := range 4 {
			b.Included.Labels = append(b.Included.Labels, struct{ ID, Name, Color string }{fmt.Sprint(l), long, "pumpkin-orange"})
		}
		due := time.Unix(1794963787, 0)
		for c := range maxCards + 2 {
			id := fmt.Sprintf("19106837360101%05d", c)
			b.Included.Cards = append(b.Included.Cards, struct {
				ID, Name, ListID string
				Position         float64
				DueDate          *time.Time
				CreatedAt        time.Time
				UpdatedAt        *time.Time
			}{id, long, "1", 1, &due, due, &due})
			for l := range 4 {
				b.Included.CardLabels = append(b.Included.CardLabels, struct{ CardID, LabelID string }{id, fmt.Sprint(l)})
			}
		}
		boards = append(boards, summarise(b))
	}
	if len(boards[0].Cards) != maxCards || len(boards[0].Cards[0].Labels) != maxLabels || !boards[0].Cards[0].Done {
		t.Fatalf("summarise caps not applied: %+v", boards[0])
	}

	n := nas{Info: 99}
	for range 3 {
		n.Pools = append(n.Pools, pool{trunc(long, poolNameMx), "DEGRADED", false, 99999, 99999})
	}
	for range maxAlerts {
		n.Alerts = append(n.Alerts, alert{"EMERGENCY", trunc(long, alertMax)})
	}
	var peers []peer
	for range maxPeers {
		peers = append(peers, peer{trunc(long, peerMax), false})
	}
	gpus := []gpuStat{
		{Host: "workstation", Name: "RTX 5060 Ti", Up: true, Temp: 100, Util: 100, VRAM: 16311, VRAMTotal: 16311, Power: 180, Procs: 9, State: "loaded"},
		{Host: "truenas", Name: "RTX 2070 SUPER", Up: true, Temp: 100, Util: 100, VRAM: 8192, VRAMTotal: 8192, Power: 215, Procs: 9, State: "loaded"},
	}

	svcs := peers[:maxServices]
	nt := newNotifier("")
	for range keepNotes {
		nt.add(long, long, "alert", "wx")
	}
	nt.markRead(nt.next)
	notes, _ := nt.Collect(context.Background())

	var slots []*slot
	for _, c := range []fixed{{"homelab", "nas", n}, {"homelab", "gpu", gpus}, {"homelab", "ts", peers}, {"homelab", "svc", svcs},
		{"planka", "boards", boards}, {"notify", "notes", notes}} {
		slots = append(slots, &slot{c: c, data: c.data, at: time.Now(), ok: true})
	}
	body := encodePanel(slots)
	if len(body) > 8192 {
		t.Errorf("worst-case panel is %d bytes, budget 8192", len(body))
	}
	var round map[string]map[string]json.RawMessage
	if err := json.Unmarshal(body, &round); err != nil || round["planka"]["stale"] == nil {
		t.Errorf("panel JSON malformed: %v", err)
	}
	t.Logf("worst-case panel: %d bytes", len(body))
}
