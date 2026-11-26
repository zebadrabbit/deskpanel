package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"image"
	"image/color"
	"maps"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Demo mode (DEMO=1): every real source is replaced by lively fake data, so the panel can be tried
// with nothing but the board and Docker:
//   - GPUs working
//   - a pool creeping past 85 %
//   - a service that drops out now and then
//   - a phone coming and going on the tailnet
//   - two task boards where Done works
//   - a Claude-style note every few minutes
// The real event watcher runs on it, so demo events reach the bell like real ones would.
// `deskpanel demo-json` prints one such document (the PC simulator renders it for screenshots).

// wave moves smoothly between lo and hi with the given period (seconds).
func wave(t, period, lo, hi float64) int {
	return int(math.Round(lo + (hi-lo)*(0.5+0.5*math.Sin(2*math.Pi*t/period))))
}

type demoSource struct {
	key   string
	every time.Duration
	start time.Time
	gen   func(t float64) any // t: seconds since start
	clock func() time.Time    // nil = time.Now
}

func (d *demoSource) Section() string         { return "homelab" }
func (d *demoSource) Key() string             { return d.key }
func (d *demoSource) Interval() time.Duration { return d.every }
func (d *demoSource) Collect(context.Context) (any, error) {
	if d.clock != nil {
		return d.gen(d.clock().Sub(d.start).Seconds()), nil
	}
	return d.gen(time.Since(d.start).Seconds()), nil
}

func demoSources(start time.Time) []Collector {
	src := func(key string, every time.Duration, gen func(float64) any) Collector {
		return &demoSource{key: key, every: every, start: start, gen: gen}
	}
	return []Collector{
		src("nas", 10*time.Second, demoNAS),
		src("gpu", 5*time.Second, demoGPUs),
		src("ts", 15*time.Second, demoPeers),
		src("svc", 15*time.Second, demoServices),
	}
}

func demoNAS(t float64) any {
	mk := func(name string, sizeGiB, pct float64) pool {
		used := int64(sizeGiB * pct / 100)
		return pool{name, "ONLINE", true, used, int64(sizeGiB) - used}
	}
	return nas{
		Pools: []pool{
			mk("tank", 14900, float64(wave(t, 900, 61, 63))),
			mk("backup", 7450, 80+math.Mod(t/60, 10)), // 80 → 90 % every 10 min: crosses 85 %
			mk("boot-pool", 220, 9),
		},
		Alerts: []alert{
			{"WARNING", "Disk sdc in tank is running warm (52 °C)"},
			{"INFO", `An update is available for "jellyfin" application.`},
		},
		Info: 1,
	}
}

// demoGPUs: the workstation's fan swings slowly (smooth at one sample a minute); the server's idles at 0 %
// (zero-RPM). The fan never reaches /panel. (The simulator's gpu-fan shots add a passively cooled GPU, "nas",
// which has no fan reading.)
func demoGPUs(t float64) any {
	return []gpuStat{
		{Host: "workstation", Name: "RTX 4070", Up: true, Temp: wave(t, 90, 58, 79), Util: wave(t, 37, 35, 98),
			VRAM: wave(t, 1500, 6200, 9800), VRAMTotal: 12282, Power: wave(t, 37, 110, 215), Procs: 1, State: "busy",
			Fan: wave(t, 1200, 38, 66), FanOK: true},
		{Host: "server", Name: "RTX 3060", Up: true, Temp: wave(t, 300, 41, 46), Util: wave(t, 20, 0, 3),
			VRAM: 7104, VRAMTotal: 12288, Power: wave(t, 40, 17, 24), Procs: 1, State: "loaded", FanOK: true},
	}
}

func demoPeers(t float64) any {
	return []peer{{"laptop", true}, {"nas", true}, {"phone", int(t/180)%2 == 0}, {"pi-hole", true},
		{"server", true}, {"workstation", true}}
}

func demoServices(t float64) any {
	return []peer{{"home-assistant", true}, {"jellyfin", true}, {"pihole", math.Mod(t, 300) < 240}, {"grafana", true}}
}

// demoBoards stands in for Planka: two boards whose cards can really be marked done (in memory).
type demoBoards struct {
	mu     sync.Mutex
	boards []pkBoard
}

func (d *demoBoards) Section() string         { return "planka" }
func (d *demoBoards) Key() string             { return "boards" }
func (d *demoBoards) Interval() time.Duration { return 5 * time.Second }

func (d *demoBoards) Collect(context.Context) (any, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]pkBoard, len(d.boards))
	for i, b := range d.boards {
		out[i] = b
		out[i].Cards = append([]pkCard(nil), b.Cards...)
	}
	return out, nil
}

// markDone moves the card to "Done" and to the top, as the real collector would show it next.
func (d *demoBoards) markDone(_ context.Context, id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	for bi := range d.boards {
		cards := d.boards[bi].Cards
		for ci, c := range cards {
			if c.ID == id {
				c.Done, c.List, c.Due = true, "Done", 0
				d.boards[bi].Cards = append([]pkCard{c}, append(cards[:ci:ci], cards[ci+1:]...)...)
				return nil
			}
		}
	}
	return httpErr{http.StatusNotFound, "no such card"}
}

func newDemoBoards(now time.Time) *demoBoards {
	day := int64(24 * time.Hour / time.Second)
	label := func(name, color string) []pkLabel { return []pkLabel{{name, labelColor(color)}} }
	return &demoBoards{boards: []pkBoard{
		{ID: "1001", Name: "Homelab", CanDone: true, Cards: []pkCard{
			{ID: "1101", Name: "Replace the failing disk in backup", List: "Doing", Due: now.Unix() + day, Labels: label("Hardware", "berry-red")},
			{ID: "1102", Name: "Upgrade Jellyfin to 10.10", List: "To do", Labels: label("Software", "lagoon-blue")},
			{ID: "1103", Name: "Set up offsite backups — rclone + B2", List: "Done", Done: true},
		}},
		{ID: "1002", Name: "Weekend", CanDone: true, Cards: []pkCard{
			{ID: "1201", Name: "Fix the garden gate", List: "To do", Due: now.Unix() - day, Labels: label("House", "bright-moss")},
			{ID: "1202", Name: "Print a stand for the deskpanel", List: "Doing", Labels: label("Making", "sugar-plum")},
			{ID: "1203", Name: "Order filament — PETG, black", List: "To do", Labels: label("Shopping", "pumpkin-orange")},
		}},
	}}
}

// demoMessages, oldest first; demo mode posts one every few minutes starting with the last.
var demoMessages = []struct{ src, msg, lvl string }{
	{"Grafana", "HighCPU on server: 92% for 10 min", "warn"},
	{"Uptime Kuma", "jellyfin is back up (down 2 min)", "info"},
	{"Claude", "homelab: Claude needs your permission to use Bash", "warn"},
	{"Claude", "deskpanel: Claude finished (14 min)", "info"},
}

func demoNotes(post func(src, msg, lvl string) note) {
	for i := len(demoMessages) - 1; ; i-- {
		m := demoMessages[(i%len(demoMessages)+len(demoMessages))%len(demoMessages)]
		post(m.src, m.msg, m.lvl)
		time.Sleep(4 * time.Minute)
	}
}

// demoNow is the "now" of the demo-json / demo-weather commands: DEMO_NOW (unix seconds) when set, so
// the simulator's frames are reproducible, else the real time. The DEMO=1 server keeps the live clock.
func demoNow() time.Time {
	if s := os.Getenv("DEMO_NOW"); s != "" {
		if sec, err := strconv.ParseInt(s, 10, 64); err == nil {
			return time.Unix(sec, 0)
		}
	}
	return time.Now()
}

// newDemoAstro is the DEMO=1 sky: Greenwich at the live clock.
func newDemoAstro() *astro { return newAstro(51.4769, -0.0005, time.UTC, time.Now) }

// demoJSON is one demo panel document: every demo source collected once, with a note history where
// only the newest note is unread.
func demoJSON() []byte { return encodePanel(demoSlots(demoNow())) }

// demoSlots is every demo collector of the panel document, collected once at now (t = 0).
func demoSlots(now time.Time) []*slot {
	notes := newNotifier("")
	notes.clock = func() time.Time { return now }
	notes.next = int(now.Unix()) // newNotifier seeded it from the live clock
	for i, m := range demoMessages {
		nt := notes.add(m.src, m.msg, m.lvl, "")
		if i == len(demoMessages)-2 {
			notes.markRead(nt.ID)
		}
	}
	var slots []*slot
	srcs := demoSources(now)
	for _, c := range srcs {
		c.(*demoSource).clock = func() time.Time { return now } // t = 0, not a few ms of wall time
	}
	for _, c := range append(srcs, Collector(newDemoBoards(now)), Collector(notes)) {
		data, _ := c.Collect(context.Background())
		slots = append(slots, &slot{c: c, data: data, at: now, ok: true, ran: true})
	}
	return slots
}

// editorDemoNow is the editor demo's clock: the simulator's DEMO_NOW, so demo-data.json is the same on every run.
const editorDemoNow = 1795032000

// editorDemo is `deskpanel editor-demo`: the editor's layout, sources, panel and cards answers from the demo data
// at a pinned clock, for the public demo page (committed as editor/demo-data.json; TestDemoDataCurrent keeps it so).
func editorDemo() ([]byte, error) {
	now := time.Unix(editorDemoNow, 0)
	slots := demoSlots(now)
	l := editorDemoLayout(foundSources(slots))
	parsed, err := parseLayout(handLayout(l))
	if err != nil {
		return nil, err
	}
	dp := &demoProm{start: now, now: func() time.Time { return now }}
	doc := struct {
		Layout  Layout          `json:"layout"`
		Sources map[string]any  `json:"sources"`
		Panel   json.RawMessage `json:"panel"`
		Cards   json.RawMessage `json:"cards"`
	}{l, sourcesDoc(slots, demoPromListing(), demoHAListing(dp), []string{"CARD_APP_TOKEN"}),
		encodePanel(slots), cardsDocWith(parsed, readers{prom: dp, json: dp}, demoHistory(now), newDemoLights())}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	err = enc.Encode(doc)
	return b.Bytes(), err
}

// editorDemoLayout is what the editor demo opens on: the default tabs for the demo collectors with a Prometheus tab,
// a Home Assistant tab and the Lights tab between them, a GPU with a graph slot, a card that jumps to a tab, and a tour that skips
// Planka. Default taps are left out, as the editor saves.
func editorDemoLayout(f found) Layout {
	num := func(x float64) *float64 { return &x }
	gen := func(id, typ string, col, row int, size, title, value string) Card {
		return Card{ID: id, Type: typ, At: [2]int{col, row}, Size: cardSize(size), Title: title, Value: json.RawMessage(value)}
	}
	cpu := gen("cpu", "graph", 1, 1, "2x3", "server CPU", `"cpu@server:9100"`)
	cpu.Unit, cpu.Warn, cpu.Bad, cpu.Tap = "%", num(80), num(95), "tab:Planka"
	server := Tab{Name: "server", Cards: []Card{
		cpu,
		gen("temp", "gauge", 3, 1, "1x2", "Temp", `"temp@server:9100"`),
		gen("load", "number", 4, 1, "1x2", "Load", `"load@server:9100"`),
		gen("uptime", "number", 3, 3, "1x1", "Up", `"uptime@server:9100"`),
		gen("prometheus", "status", 4, 3, "1x1", "Prometheus", `{"prometheus":"up{job=\"prometheus\"}"}`),
		gen("ram", "bar", 1, 4, "1x2", "RAM", `"ram@server:9100"`),
		gen("disk", "bar", 2, 4, "1x2", "Disk /", `"disk:/@server:9100"`),
		gen("net", "graph", 3, 4, "2x2", "Network", `"net@server:9100"`),
		{ID: "dots-2", Type: "dots", At: [2]int{1, 6}, Size: "4x1", Src: "both"},
	}}
	light := gen("kitchen-light", "bar", 4, 1, "1x1", "Kitchen", `{"ha":"light.kitchen","attr":"brightness"}`)
	light.Min, light.Max = num(0), num(255)
	queue := gen("app-queue", "number", 3, 4, "2x2", "Queue", `{"json":{"url":"http://app.lan/api/status","path":"queue.depth"}}`)
	queue.Unit = "jobs"
	home := Tab{Name: "Home", Cards: []Card{
		gen("living-room", "number", 1, 1, "2x2", "Living room", `{"ha":"sensor.living_room_temperature"}`),
		gen("humidity", "gauge", 3, 1, "1x2", "Humidity", `{"ha":"sensor.living_room_humidity"}`),
		light,
		gen("washer", "graph", 1, 3, "2x2", "Washer", `{"ha":"sensor.washer_power"}`),
		gen("tv", "status", 3, 3, "2x1", "TV", `{"ha":"media_player.living_room_tv"}`),
		queue,
		gen("app-status", "status", 1, 5, "2x1", "App", `{"json":{"url":"http://app.lan/api/status","path":"status","every":60}}`),
	}}
	def := defaultLayout(f)
	def.Tabs[0].Cards[1].Slots = &gpuSlots{Line1: &slotLine{Value: "vram", As: "graph"}} // the second GPU's line 1 as a graph
	// the simulator's Lights tab: newDemoLights knows its ids
	lights := demoGeneralLayouts(def)["lights-demo"].Tabs[0]
	l := Layout{Tour: &Tour{Skip: []string{"Planka"}}, Tabs: []Tab{def.Tabs[0], server, home, lights, def.Tabs[1], def.Tabs[2]}}
	for _, t := range l.Tabs {
		for j := range t.Cards {
			if t.Cards[j].Tap == defaultTap(t.Cards[j].Type) {
				t.Cards[j].Tap = ""
			}
		}
	}
	return l
}

// demoPromListing is the editor demo's Prometheus: two node_exporter machines.
func demoPromListing() promListing {
	m := []string{"go_goroutines", "go_memstats_alloc_bytes", "node_boot_time_seconds", "node_context_switches_total",
		"node_cpu_seconds_total", "node_disk_io_time_seconds_total", "node_disk_read_bytes_total",
		"node_disk_written_bytes_total", "node_entropy_available_bits", "node_filefd_allocated", "node_filesystem_avail_bytes",
		"node_filesystem_files_free", "node_filesystem_free_bytes", "node_filesystem_size_bytes", "node_forks_total",
		"node_hwmon_temp_celsius", "node_intr_total", "node_load1", "node_load15", "node_load5", "node_memory_Buffers_bytes",
		"node_memory_Cached_bytes", "node_memory_MemAvailable_bytes", "node_memory_MemFree_bytes", "node_memory_MemTotal_bytes",
		"node_memory_SwapFree_bytes", "node_memory_SwapTotal_bytes", "node_network_receive_bytes_total",
		"node_network_receive_errs_total", "node_network_transmit_bytes_total", "node_network_transmit_errs_total",
		"node_network_up", "node_procs_blocked", "node_procs_running", "node_time_seconds", "node_uname_info",
		"process_cpu_seconds_total", "process_resident_memory_bytes", "prometheus_tsdb_head_series", "scrape_duration_seconds", "up"}
	sort.Strings(m)
	return promListing{Configured: true, Needs: []string{}, Instances: []string{"server:9100", "workstation:9100"}, Mounts: []string{"/", "/mnt/data"}, Metrics: m}
}

// demoHAListing is the editor demo's entity list: demoHA as Home Assistant's /api/states would send it, read by the
// same code as the real list. An entity whose state is a wave gets its value at dp's clock.
func demoHAListing(dp *demoProm) haListing {
	var states []map[string]any
	for _, id := range slices.Sorted(maps.Keys(demoHA)) {
		h := demoHA[id]
		attrs := map[string]any{"friendly_name": h.name}
		maps.Copy(attrs, h.attrs)
		if h.unit != "" {
			attrs["unit_of_measurement"] = h.unit
		}
		st := h.state
		if st == "" {
			st = fixedDec(dp.value(jsonSpec{HA: id}.key()).V, 1)
		}
		states = append(states, map[string]any{"entity_id": id, "state": st, "attributes": attrs})
	}
	b, _ := json.Marshal(states)
	list, truncated, _ := haStates(b)
	return haListing{Configured: true, Needs: []string{}, Truncated: truncated, Entities: list}
}

// demoShape is how one query moves: base +- wob over a period, plus grow per second, and a swing
// and a rise; each only shapes the history (both are zero at "now", so a series ends at the
// current value): the hour starts rise below where it ends.
type demoShape struct{ base, wob, swing, grow, phase, rise float64 }

// demoLongQ is the query behind the general-long cards: the longest value text a card can hold.
const demoLongQ = "demo_long_value"

// demoShapes holds the mockup's numbers, keyed by the exact shortcut query strings, for all machines and for the
// editor demo's server:9100.
var demoShapes = func() map[string]demoShape {
	m := map[string]demoShape{demoLongQ: {base: -123456, wob: 400, swing: 20000}}
	for _, inst := range []string{"", "server:9100"} {
		sq := func(short, mount string) promQueries {
			return valueSpec{Shortcut: short, Mount: mount, Instance: inst}.queries()
		}
		m[sq("cpu", "").Value] = demoShape{base: 87, wob: 2, swing: 3, rise: 57} // the mockup's: about 27 % up to 87 %
		ram := sq("ram", "")
		m[ram.Value], m[ram.Second], m[ram.Graph] = demoShape{base: 9.1e9, wob: 1e8, swing: 1e9}, demoShape{base: 76e9}, demoShape{base: 12, wob: .3, swing: 1.5}
		disk := sq("disk", "/")
		m[disk.Value], m[disk.Second], m[disk.Graph] = demoShape{base: 330e9, wob: 1e9, swing: 5e9}, demoShape{base: 500e9}, demoShape{base: 66, wob: .2, swing: 1}
		m[sq("temp", "").Value] = demoShape{base: 50, wob: 1, swing: 4}
		load := sq("load", "")
		m[load.Value], m[load.Second] = demoShape{base: 3.07, wob: .1, swing: 1.2}, demoShape{base: 8}
		net := sq("net", "")
		m[net.Value], m[net.Second] = demoShape{base: 30000, wob: 6000, swing: 15000}, demoShape{base: 40000, wob: 8000, swing: 20000}
		m[sq("uptime", "").Value] = demoShape{base: 50 * 86400, grow: 1}
	}
	return m
}()

// demoHA is the demo's Home Assistant: entity -> friendly name, state, unit and attributes ("" state: a wave, as any
// other key). The editor demo lists them all.
var demoHA = map[string]struct {
	name, state, unit string
	attrs             map[string]any
}{
	"sensor.living_room_temperature": {name: "Living room temperature", state: "21.5", unit: "°C", attrs: map[string]any{"device_class": "temperature"}},
	"sensor.living_room_humidity":    {name: "Living room humidity", state: "48", unit: "%", attrs: map[string]any{"device_class": "humidity"}},
	"sensor.washer_power":            {name: "Washer power", unit: "W", attrs: map[string]any{"device_class": "power"}},
	"media_player.living_room_tv":    {name: "Living room TV", state: "playing", attrs: map[string]any{"volume_level": 0.35, "source": "HDMI 1"}},
	"light.kitchen":                  {name: "Kitchen", state: "on", attrs: map[string]any{"brightness": 180.0, "color_temp_kelvin": 3000.0}},
	"binary_sensor.front_door":       {name: "Front door", state: "off", attrs: map[string]any{"device_class": "door"}},
	"sensor.outdoor_temperature":     {name: "Outdoor temperature", state: "8.5", unit: "°C", attrs: map[string]any{"device_class": "temperature"}},
	"sensor.bedroom_temperature":     {name: "Bedroom temperature", state: "19.5", unit: "°C", attrs: map[string]any{"device_class": "temperature"}},
	"sensor.office_co2":              {name: "Office CO2", state: "640", unit: "ppm", attrs: map[string]any{"device_class": "carbon_dioxide"}},
	"sensor.fridge_power":            {name: "Fridge power", state: "92", unit: "W", attrs: map[string]any{"device_class": "power"}},
	"sensor.solar_power":             {name: "Solar power", state: "1840", unit: "W", attrs: map[string]any{"device_class": "power"}},
	"sensor.energy_today":            {name: "Energy today", state: "7.42", unit: "kWh", attrs: map[string]any{"device_class": "energy"}},
	"sensor.phone_battery":           {name: "Phone battery", state: "76", unit: "%", attrs: map[string]any{"device_class": "battery"}},
	"switch.coffee_maker":            {name: "Coffee maker", state: "off"},
	// the three lights of newDemoLights (lights.go), as they start
	"light.desk":          {name: "Desk", state: "on", attrs: map[string]any{"brightness": 38.0, "color_temp_kelvin": 2700.0}},
	"light.shelf":         {name: "Shelf", state: "on", attrs: map[string]any{"brightness": 153.0}},
	"switch.fan":          {name: "Fan", state: "off"},
	"cover.garage_door":   {name: "Garage door", state: "closed", attrs: map[string]any{"current_position": 0.0}},
	"climate.living_room": {name: "Living room", state: "heat", attrs: map[string]any{"current_temperature": 21.5, "temperature": 22.0, "hvac_action": "heating"}},
}

// demoProm answers any query with a believable moving value: the mockup's numbers for the shortcut
// queries, a wave keyed by an FNV hash of the query for everything else. A series is 60 points a
// minute apart that end at the current value. It depends only on now() and start.
type demoProm struct {
	start time.Time
	now   func() time.Time
}

func (d *demoProm) shape(q string) demoShape {
	if sh, ok := demoShapes[q]; ok {
		return sh
	}
	h := fnv.New32a()
	h.Write([]byte(q))
	n := h.Sum32()
	return demoShape{base: 20 + float64(n%60), wob: 3, swing: 12, phase: float64(n%628) / 100}
}

func (d *demoProm) at(q string, t float64) float64 {
	if strings.HasPrefix(q, "up{") {
		if strings.Contains(q, "jellyfin") {
			return 0
		}
		return 1
	}
	sh := d.shape(q)
	h := fnv.New32a()
	h.Write([]byte(q))
	period := 240 + float64(h.Sum32()%360)
	v := sh.base + sh.wob*math.Sin(2*math.Pi*t/period+sh.phase) + sh.grow*t
	if v < 0 && sh.base >= 0 {
		v = 0
	}
	return v
}

func (d *demoProm) t() float64 { return d.now().Sub(d.start).Seconds() }

// demoManyInstance makes a query answer like a selector that matches two series (a none card).
const demoManyInstance = `instance="many:9100"`

func (d *demoProm) value(q string) promVal {
	if e, ok := strings.CutPrefix(q, "ha:"); ok { // key: "ha:" entity NUL attr
		ent, attr, _ := strings.Cut(e, "\x00")
		h, ok := demoHA[ent]
		if !ok {
			return promVal{St: stNone, Why: "no entity"}
		}
		if attr == "" && h.state == "" {
			return promVal{V: d.at(q, d.t()), U: h.unit, St: stOK}
		}
		x := any(h.state)
		if attr != "" {
			x = h.attrs[attr]
		}
		v, ok := haValue(x)
		if !ok {
			return promVal{St: stNone, Why: "no attr " + attr}
		}
		v.St = stOK
		if attr == "" {
			v.U = h.unit
		}
		return v
	}
	// a JSON source key is "json:" url NUL path NUL header_env: a "status" path answers with a word, the rest
	// with the wave of any other key
	if p := strings.Split(q, "\x00"); strings.HasPrefix(q, "json:") && len(p) > 1 && p[1] == "status" {
		return promVal{S: "running", St: stOK}
	}
	if strings.Contains(q, demoManyInstance) {
		return promVal{St: stNone, Why: "returns 2 series"}
	}
	return promVal{V: d.at(q, d.t()), St: stOK}
}

func (d *demoProm) series(q string) promSeries {
	t, sh := d.t(), d.shape(q)
	p := make([]float64, promPoints)
	for i := range p {
		k := float64(promPoints - 1 - i)
		p[i] = d.at(q, t-promStep*k)
		if !strings.HasPrefix(q, "up{") {
			p[i] += sh.swing*(math.Sin(.4*k+1)-math.Sin(1)) - sh.rise*k/(promPoints-1)
			if p[i] < 0 && sh.base >= 0 {
				p[i] = 0
			}
		}
	}
	return promSeries{P: p, St: stOK}
}

// demoHistory is a GPU history pre-filled with an hour of samples ending at the demo's start
// (its t = 0), its live GPUs those at t = 0; the DEMO server goes on recording every minute.
func demoHistory(_ time.Time) *gpuHistory {
	h := &gpuHistory{live: func() []gpuStat { return demoGPUs(0).([]gpuStat) }}
	for i := 0; i < histKeep; i++ {
		h.record(demoGPUs(float64(i-histKeep+1) * 60).([]gpuStat))
	}
	return h
}

// demoCardsDoc is the cards document of every demo layout combined, at now. Card ids are unique
// across the demo layouts, so the documents merge without clashes.
func demoCardsDoc(now time.Time) ([]byte, error) {
	pr := &demoProm{start: now, now: func() time.Time { return now }}
	h := demoHistory(now)
	ls := demoLayouts()
	names := make([]string, 0, len(ls))
	for n := range ls {
		names = append(names, n)
	}
	sort.Strings(names)
	all := map[string]cardOut{}
	for _, n := range names {
		l, err := parseLayout(handLayout(ls[n]))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", n, err)
		}
		var d struct {
			Cards map[string]cardOut `json:"cards"`
		}
		if err := json.Unmarshal(cardsDocWith(l, pr, h, newDemoLights()), &d); err != nil {
			return nil, err
		}
		for id, c := range d.Cards {
			if _, dup := all[id]; dup {
				return nil, fmt.Errorf("%s: card id %q is used twice in the demo layouts", n, id)
			}
			all[id] = c
		}
	}
	return encodeCards(all), nil
}

// writeDemoCards writes <dir>/cards.json for the PC simulator.
func writeDemoCards(dir string, now time.Time) error {
	b, err := demoCardsDoc(now)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "cards.json"), append(b, '\n'), 0o644)
}

// demoWx serves a weather value computed at each Collect (section "weather").
type demoWx struct {
	key string
	get func() any
}

func (d demoWx) Section() string                      { return "weather" }
func (d demoWx) Key() string                          { return d.key }
func (d demoWx) Interval() time.Duration              { return time.Minute }
func (d demoWx) Collect(context.Context) (any, error) { return d.get(), nil }

// demoWeather is believable fake weather: five radar frames of drifting rain blobs on a dark map,
// a Tornado Watch and a Wind Advisory, and with warning also a Tornado Warning over home plus a
// Severe Thunderstorm Warning west of it. A frame's image depends only on its time (the panel caches
// frames by time), rendered on first request; times and alert expiries are derived from clock on
// every read, so a long DEMO run stays fresh.
type demoWeather struct {
	clock   func() time.Time
	warning bool
	cond    wxNow
	t0      int64 // the oldest frame's time at the start: the first five frames look as designed
	mu      sync.Mutex
	imgs    map[int64][]byte // rendered frames by time, the newest few
}

func (d *demoWeather) times() []int64 {
	newest := d.clock().Unix() / radarStep * radarStep
	ts := make([]int64, radarFrames)
	for k := range ts {
		ts[k] = newest - int64(radarFrames-1-k)*radarStep
	}
	return ts
}

// frame accepts the current five times, and the one before them (a request that straddles a boundary).
func (d *demoWeather) frame(t int64) ([]byte, bool) {
	newest := d.clock().Unix() / radarStep * radarStep
	steps := (newest - t) / radarStep
	if t%radarStep != 0 || t > newest || steps > radarFrames {
		return nil, false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if b, ok := d.imgs[t]; ok {
		return b, true
	}
	b := demoFrame((t - d.t0) / radarStep)
	d.imgs[t] = b
	if len(d.imgs) > 8 {
		delete(d.imgs, slices.Min(slices.Collect(maps.Keys(d.imgs))))
	}
	return b, true
}

func (d *demoWeather) text(i int) (alertText, bool) {
	_, _, texts := d.alertsAt(d.clock())
	if i < 0 || i >= len(texts) {
		return alertText{}, false
	}
	return texts[i], true
}

func (d *demoWeather) collectors() []Collector {
	return []Collector{
		demoWx{"now", func() any { return d.cond }},
		demoWx{"frames", func() any { return d.times() }},
		demoWx{"alerts", func() any { l, p, _ := d.alertsAt(d.clock()); return wxAlerts{List: l, Polys: p} }},
	}
}

// demoBlobs: x, y, radius, hotness (1 = red core, 0 = green only) at step 0.
var demoBlobs = [][4]float64{{180, 270, 110, .9}, {300, 230, 80, .6}, {420, 300, 100, 1}, {520, 120, 90, .5}, {640, 260, 120, .8}, {120, 110, 70, .4}}

// rainRamp: red core, then orange, yellow, green at the rim (the mockup's gradient).
var rainRamp = []struct {
	at      float64
	r, g, b float64
}{{0, 255, 59, 59}, {.35, 255, 176, 46}, {.55, 242, 226, 53}, {.8, 47, 208, 90}, {1, 31, 158, 69}}

func rainColor(u float64) (r, g, b float64) {
	for i := 1; i < len(rainRamp); i++ {
		if a, b2 := rainRamp[i-1], rainRamp[i]; u <= b2.at {
			f := (u - a.at) / (b2.at - a.at)
			return a.r + (b2.r-a.r)*f, a.g + (b2.g-a.g)*f, a.b + (b2.b-a.b)*f
		}
	}
	last := rainRamp[len(rainRamp)-1]
	return last.r, last.g, last.b
}

// demoFrame draws the frame `step` 5-minute steps after the first: dark map, faint lines, blobs shifted
// (+34, -22) px per step. A blob that drifts out wraps round to the other side, out of sight (margin m),
// so the drift goes on for ever without a jump.
func demoFrame(step int64) []byte {
	img := image.NewRGBA(image.Rect(0, 0, frameW, frameH))
	bg, line := color.RGBA{0x0a, 0x0e, 0x13, 255}, color.RGBA{0x18, 0x20, 0x2b, 255}
	for y := 0; y < frameH; y++ {
		for x := 0; x < frameW; x++ {
			c := bg
			if x%92 == 0 || y%92 == 0 || (x+y)%260 == 0 {
				c = line
			}
			img.SetRGBA(x, y, c)
		}
	}
	k, m := float64(step), 120.0 // m: at least the largest radius
	wrap := func(v, size float64) float64 { return v - (size+2*m)*math.Floor((v+m)/(size+2*m)) }
	for _, b := range demoBlobs {
		cx, cy, r := wrap(b[0]+34*k, frameW), wrap(b[1]-22*k, frameH), b[2]
		for y := max(0, int(cy-r)); y < min(frameH, int(cy+r)+1); y++ {
			for x := max(0, int(cx-r)); x < min(frameW, int(cx+r)+1); x++ {
				d := math.Hypot(float64(x)-cx, float64(y)-cy) / r
				if d >= 1 {
					continue
				}
				a := .75 * math.Min(1, (1-d)/.2)
				cr, cg, cb := rainColor(math.Min(1, d+(1-b[3])*.8))
				p := img.RGBAAt(x, y)
				mix := func(old uint8, c float64) uint8 { return uint8(float64(old)*(1-a) + c*a) }
				img.SetRGBA(x, y, color.RGBA{mix(p.R, cr), mix(p.G, cg), mix(p.B, cb), 255})
			}
		}
	}
	z, _ := encodeFrame(img)
	return z
}

func newDemoWeather(now time.Time, warning bool) *demoWeather {
	d := &demoWeather{clock: func() time.Time { return now }, warning: warning,
		cond: wxNow{T: 72, RH: 64, Code: 2, Day: true, Text: "Partly cloudy", Hi: 78, Lo: 61, Wind: "S 14 mph"},
		imgs: map[int64][]byte{}}
	d.t0 = d.times()[0]
	return d
}

// alertsAt builds the list, outlines and full texts relative to t, so countdowns never run out.
func (d *demoWeather) alertsAt(t time.Time) (list []wxAlert, polys []wxPoly, texts []alertText) {
	n, h := t.Unix(), int64(3600)
	polys = []wxPoly{}
	add := func(a wxAlert, x alertText) {
		a.N = len(texts)
		list = append(list, a)
		texts = append(texts, x)
	}
	if d.warning {
		add(wxAlert{Key: "demo0001", Event: "Tornado Warning", Cls: "warning", Until: n + 38*60, Take: true},
			alertText{Key: "demo0001", Event: "Tornado Warning", Headline: "Tornado Warning issued for your area until 38 minutes from now by NWS Demo",
				Sent: n - 60, Expires: n + 38*60,
				Desc:  "At the time of issue, a severe thunderstorm capable of producing a tornado was located 9 miles southwest of your area, moving northeast at 35 mph.\n\nHAZARD... Tornado.\n\nSOURCE... Radar-indicated rotation.\n\nIMPACT... Flying debris will be dangerous to those caught without shelter. Mobile homes will be damaged or destroyed. Damage to roofs, windows and vehicles will occur. Tree damage is likely.",
				Instr: "TAKE COVER NOW! Move to a basement or an interior room on the lowest floor of a sturdy building. Avoid windows. If you are outdoors, in a mobile home, or in a vehicle, move to the closest substantial shelter and protect yourself from flying debris."})
		polys = []wxPoly{
			{Kind: "t", Home: true, Pts: [][2]int16{{350, 150}, {450, 130}, {475, 210}, {400, 245}, {340, 215}}},
			{Kind: "s", Pts: [][2]int16{{190, 140}, {300, 130}, {320, 200}, {250, 240}, {180, 210}}},
		}
	}
	add(wxAlert{Key: "demo0002", Event: "Tornado Watch", Cls: "watch", Until: n + 6*h},
		alertText{Key: "demo0002", Event: "Tornado Watch", Headline: "Tornado Watch issued for your county and 14 surrounding counties by NWS Demo",
			Sent: n - h, Expires: n + 6*h,
			Desc:  "WHAT... Conditions are favorable for tornadoes and severe thunderstorms in and close to the watch area.\n\nWHERE... Your county and 14 surrounding counties.\n\nWHEN... Until 9 PM this evening.\n\nIMPACTS... Tornadoes, damaging wind gusts to 70 mph and hail up to 2 inches in diameter are possible.",
			Instr: "Review your emergency plans and know where you would shelter. Stay tuned for possible warnings."})
	add(wxAlert{Key: "demo0003", Event: "Wind Advisory", Cls: "advisory", Until: n + 4*h},
		alertText{Key: "demo0003", Event: "Wind Advisory", Headline: "Wind Advisory issued for your county by NWS Demo",
			Sent: n - 5*h, Expires: n + 4*h,
			Desc:  "WHAT... South winds 20 to 30 mph with gusts up to 45 mph.\n\nWHERE... Your county.\n\nIMPACTS... Gusty winds will blow around unsecured objects. Tree limbs could be blown down and a few power outages may result.",
			Instr: "Use extra caution when driving, especially if operating a high-profile vehicle. Secure outdoor objects."})
	return
}

// writeDemoWeather writes what the PC simulator needs to render the weather tab: weather.json
// (no warning), warning.json, frame-<t>.z, alert-<n>.json and warning-alert-<n>.json.
func writeDemoWeather(dir string, now time.Time) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	put := func(name string, b []byte) error { return os.WriteFile(filepath.Join(dir, name), b, 0o644) }
	for _, warn := range []bool{false, true} {
		d, doc, pre := newDemoWeather(now, warn), "weather.json", "alert-"
		var slots []*slot
		for _, c := range d.collectors() {
			data, _ := c.Collect(context.Background())
			slots = append(slots, &slot{c: c, data: data, at: now, ok: true})
		}
		if warn {
			doc, pre = "warning.json", "warning-alert-"
		}
		if err := put(doc, weatherDoc(slots)); err != nil {
			return err
		}
		_, _, texts := d.alertsAt(now)
		for i, t := range texts {
			b, _ := json.Marshal(t)
			if err := put(pre+strconv.Itoa(i)+".json", b); err != nil {
				return err
			}
		}
		if !warn {
			for _, ts := range d.times() {
				b, _ := d.frame(ts)
				if err := put("frame-"+strconv.FormatInt(ts, 10)+".z", b); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
