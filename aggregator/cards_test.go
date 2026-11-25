package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type mapProm struct {
	vals map[string]promVal
	sers map[string]promSeries
}

func newMap() *mapProm {
	return &mapProm{map[string]promVal{}, map[string]promSeries{}}
}

func (f *mapProm) value(q string) promVal {
	if v, ok := f.vals[q]; ok {
		return v
	}
	return promVal{St: stNone, Why: "no data"}
}

func (f *mapProm) series(q string) promSeries {
	if s, ok := f.sers[q]; ok {
		return s
	}
	return promSeries{St: stNone, Why: "no data"}
}

func (f *mapProm) v(q string, x float64) *mapProm {
	f.vals[q] = promVal{V: x, St: stOK}
	return f
}

func (f *mapProm) s(q string, p []float64) *mapProm {
	f.sers[q] = promSeries{P: p, St: stOK}
	return f
}

func qOf(short string) promQueries { return valueSpec{Shortcut: short}.queries() }

func mustLayout(t *testing.T, tabs string) Layout {
	t.Helper()
	l, err := parseLayout([]byte(`{"tabs":[` + tabs + `]}`))
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// one builds a layout with a single card x from its JSON fields and returns its entry in the document.
func one(t *testing.T, fields string, pr promReader, h *gpuHistory) string {
	t.Helper()
	l := mustLayout(t, `{"name":"t","cards":[{"id":"x","at":[1,1],`+fields+`}]}`)
	var d struct {
		Cards map[string]json.RawMessage `json:"cards"`
	}
	if err := json.Unmarshal(cardsDoc(l, pr, h), &d); err != nil {
		t.Fatal(err)
	}
	return string(d.Cards["x"])
}

func ramp(n int) []float64 {
	p := make([]float64, n)
	for i := range p {
		p[i] = float64(i) * 100 / float64(max(n-1, 1))
	}
	return p
}

func eq(t *testing.T, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestCardsDocTypes(t *testing.T) {
	cpu := qOf("cpu")
	pts := ramp(60)
	f := newMap().v(cpu.Value, 87.2).s(cpu.Graph, pts)
	eq(t, one(t, `"type":"graph","size":"2x3","value":"cpu","warn":80`, f, nil),
		`{"st":"warn","v":"87 %","g":["`+encodePoints(pts, 0, 100)+`"],"th":50}`)

	ram := qOf("ram")
	f = newMap().v(ram.Value, 9173499904).v(ram.Second, 75973189632)
	eq(t, one(t, `"type":"bar","size":"1x2","value":"ram"`, f, nil), `{"v":"12 %","f":0.121,"sub":"9.2 / 76 GB"}`)
	eq(t, one(t, `"type":"number","size":"1x2","value":"ram"`, f, nil), `{"v":"9.2 GB","sub":"of 76 GB"}`)

	load := qOf("load")
	f = newMap().v(load.Value, 3.07).v(load.Second, 8)
	eq(t, one(t, `"type":"number","size":"1x2","value":"load"`, f, nil), `{"v":"3.07","sub":"of 8 cores"}`)

	f = newMap().v(qOf("temp").Value, 50)
	eq(t, one(t, `"type":"gauge","size":"1x2","value":"temp"`, f, nil), `{"v":"50°","f":0.5}`)

	f = newMap().v(qOf("uptime").Value, 50*86400)
	eq(t, one(t, `"type":"number","size":"1x1","value":"uptime"`, f, nil), `{"v":"50 d"}`)

	net := qOf("net")
	rx, tx := []float64{1e5, 2e5, 1.4e5}, []float64{3e6, 3.2e6, 3e6}
	f = newMap().v(net.Value, 1.4e5).v(net.Second, 3.2e6).s(net.Graph, rx).s(net.Second, tx)
	hi := autoHi(append(append([]float64{}, rx...), tx...)...)
	eq(t, one(t, `"type":"graph","size":"2x2","value":"net"`, f, nil),
		`{"v":"0.14 Mb/s","v2":"3.2 Mb/s","g":["`+encodePoints(rx, 0, hi)+`","`+encodePoints(tx, 0, hi)+`"]}`)

	st := `"type":"status","size":"1x1","value":{"prometheus":"up{job=\"x\"}"}`
	f = newMap().v(`up{job="x"}`, 1)
	eq(t, one(t, st, f, nil), `{"v":"up"}`)
	f.v(`up{job="x"}`, 0)
	eq(t, one(t, st, f, nil), `{"st":"bad","v":"down"}`)

	h := &gpuHistory{}
	for i := 0; i < 3; i++ {
		h.record([]gpuStat{{Host: "server", Up: true, VRAM: 1000 * (i + 1), VRAMTotal: 4000}})
	}
	eq(t, one(t, `"type":"gpu","size":"2x3","src":"server","slots":{"line1":{"value":"vram","as":"graph"}}`, nil, h),
		`{"g":["`+encodePoints([]float64{25, 50, 75}, 0, 100)+`"]}`)
	eq(t, one(t, `"type":"gpu","size":"2x3","src":"server","slots":{"line1":{"value":"vram","as":"graph"}}`, nil, &gpuHistory{}), `{"st":"none"}`)
	eq(t, one(t, `"type":"gpu","size":"2x3","src":"server"`, nil, h), ``) // no graph slot: absent
}

func TestCardsDocStates(t *testing.T) {
	cpu := qOf("cpu").Value
	f := newMap()
	f.vals[cpu] = promVal{V: 87.2, St: stStale, Why: "prometheus 500"}
	eq(t, one(t, `"type":"number","size":"1x1","value":"cpu","warn":80`, f, nil), `{"st":"stale","v":"87 %"}`)

	// worst of a card's queries: the value is fine, the total is stale
	ram := qOf("ram")
	f = newMap().v(ram.Value, 1e9)
	f.vals[ram.Second] = promVal{V: 4e9, St: stStale}
	eq(t, one(t, `"type":"bar","size":"1x2","value":"ram"`, f, nil), `{"st":"stale","v":"25 %","f":0.25,"sub":"1.0 / 4.0 GB"}`)

	f = newMap()
	f.vals[cpu] = promVal{St: stNone, Why: "no data"}
	eq(t, one(t, `"type":"number","size":"1x1","value":"cpu"`, f, nil), `{"st":"none","sub":"no data"}`)

	f.vals[cpu] = promVal{St: stNone, Why: "returns 2 series"}
	eq(t, one(t, `"type":"number","size":"1x1","value":"cpu"`, f, nil), `{"st":"none","sub":"add @instance (2)"}`)
	f.vals[cpu] = promVal{St: stNone, Why: "returns 1234567 series"} // the hint fits 23 bytes for any real count
	eq(t, one(t, `"type":"number","size":"1x1","value":"cpu"`, f, nil), `{"st":"none","sub":"add @instance (1234567)"}`)
	// a shortcut that already names an instance: naming one isn't the fix
	at := valueSpec{Shortcut: "ram", Instance: "a:9100"}.queries().Value
	f.vals[at] = promVal{St: stNone, Why: "returns 2 series"}
	eq(t, one(t, `"type":"number","size":"1x1","value":"ram@a:9100"`, f, nil), `{"st":"none","sub":"returns 2 series"}`)
	f.vals["foo"] = promVal{St: stNone, Why: "returns 2 series"}
	eq(t, one(t, `"type":"number","size":"1x1","value":{"prometheus":"foo"}`, f, nil), `{"st":"none","sub":"returns 2 series"}`)

	eq(t, one(t, `"type":"number","size":"1x1","value":"cpu"`, nil, nil), `{"st":"none","sub":"set PROMETHEUS_URL"}`)
}

func TestCardsDocEdgeValues(t *testing.T) {
	ram := qOf("ram")
	f := newMap().v(ram.Value, 200).v(ram.Second, 100)
	if got := one(t, `"type":"bar","size":"1x2","value":"ram"`, f, nil); !strings.Contains(got, `"f":1,`) {
		t.Errorf("used > total: %s", got)
	}
	f = newMap().v(qOf("temp").Value, -5)
	eq(t, one(t, `"type":"gauge","size":"1x2","value":"temp"`, f, nil), `{"v":"-5°","f":0}`)

	f = newMap().v("q", 5).s("q", []float64{5, 5, 5, 5, 5, 5, 5, 5, 5, 5})
	eq(t, one(t, `"type":"graph","size":"2x2","value":{"prometheus":"q"},"min":5`, f, nil),
		`{"v":"5.00","g":["AAAAAAAAAA"]}`) // constant: no panic, 10 points stay 10 chars

	f = newMap().v("q", 0).s("q", make([]float64, 30)) // all zeros: hi = 1
	eq(t, one(t, `"type":"graph","size":"2x2","value":{"prometheus":"q"}`, f, nil), `{"v":"0.00","g":["`+strings.Repeat("A", 30)+`"]}`)

	disk := valueSpec{Shortcut: "disk", Mount: "/"}.queries()
	f = newMap().v(disk.Value, 1e15).v(disk.Second, 2e15)
	got := one(t, `"type":"number","size":"1x1","value":"disk:/"`, f, nil)
	var o cardOut
	if err := json.Unmarshal([]byte(got), &o); err != nil || len(o.V) > 15 {
		t.Errorf("1e15 disk: %s (%v)", got, err)
	}
}

func TestCardsDocQueryChange(t *testing.T) {
	f := newMap().v(qOf("cpu").Value, 87.2) // cached for the old value
	l := mustLayout(t, `{"name":"t","cards":[{"id":"x","at":[1,1],"type":"number","size":"1x1","value":"ram"}]}`)
	got := string(cardsDoc(l, f, nil))
	if strings.Contains(got, "87") || !strings.Contains(got, `"x":{"st":"none"`) {
		t.Errorf("old query leaked: %s", got)
	}
}

func TestCardsUnder16KB(t *testing.T) {
	// the contract's worst case through the real encoder: 144 cards at every cap
	cards := map[string]cardOut{}
	f := 0.123
	for i := 0; i < 144; i++ {
		cards[fmt.Sprintf("%023d", i)] = cardOut{St: stStale, V: strings.Repeat("v", maxV), Sub: strings.Repeat("s", maxSub), F: &f}
	}
	if n := len(encodeCards(cards)); n >= 16384 {
		t.Errorf("144 cards: %d bytes", n)
	}

	// and real documents: 6 tabs of 24 stale 1x1 bars, 6 tabs of six 2x2 two-line graphs
	pr := newMap()
	var tabs, gtabs []string
	for ti := 0; ti < 6; ti++ {
		var cs, gcs []string
		for i := 0; i < 24; i++ {
			cs = append(cs, fmt.Sprintf(`{"id":"%023d","type":"bar","at":[%d,%d],"size":"1x1","value":{"prometheus":"q%d"},"unit":"abcdefgh"}`, ti*24+i, i%4+1, i/4+1, i))
			pr.vals[fmt.Sprintf("q%d", i)] = promVal{V: 123456.789, St: stStale}
		}
		for i := 0; i < 6; i++ {
			gcs = append(gcs, fmt.Sprintf(`{"id":"%023d","type":"graph","at":[%d,%d],"size":"2x2","value":[{"prometheus":"a"},{"prometheus":"b"}]}`, ti*6+i, i%2*2+1, i/2*2+1))
		}
		tabs = append(tabs, fmt.Sprintf(`{"name":"t%d","cards":[%s]}`, ti, strings.Join(cs, ",")))
		gtabs = append(gtabs, fmt.Sprintf(`{"name":"t%d","cards":[%s]}`, ti, strings.Join(gcs, ",")))
	}
	pr.v("a", 1234.5).v("b", 1234.5).s("a", ramp(60)).s("b", ramp(60))
	if n := len(cardsDoc(mustLayout(t, strings.Join(tabs, ",")), pr, nil)); n >= 16384 {
		t.Errorf("bars: %d bytes", n)
	}
	d := cardsDoc(mustLayout(t, strings.Join(gtabs, ",")), pr, nil)
	if len(d) >= 16384 || !strings.Contains(string(d), `"v2"`) {
		t.Errorf("graphs: %d bytes", len(d))
	}
}

// The layout document's cap is the panel's buffer, lx_body[12 * 1024] in firmware/main/net.c: change both together.
func TestLayoutDocCapIsThePanels(t *testing.T) {
	if maxLayoutDoc != 12*1024 {
		t.Errorf("maxLayoutDoc %d, the panel's lx_body is 12 * 1024", maxLayoutDoc)
	}
}

func TestCardsRouteFast(t *testing.T) {
	stop := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(10 * time.Second):
		case <-stop:
		case <-r.Context().Done():
		}
	}))
	ctx, cancel := context.WithCancel(context.Background())
	defer srv.Close()
	defer cancel()
	defer close(stop)

	p := newProm(srv.URL)
	l := mustLayout(t, `{"name":"t","cards":[{"id":"x","at":[1,1],"type":"number","size":"1x1","value":"cpu"}]}`)
	qi, qr, _ := queriesFor(l)
	p.want(qi, qr)
	go p.run(ctx)
	time.Sleep(50 * time.Millisecond) // the first query is now in flight, hung

	mux := http.NewServeMux()
	addCardsRoute(mux, "tok", func() []byte { return cardsDoc(l, p, nil) })
	get := func(etag string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/api/v1/cards", nil)
		r.Header.Set("Authorization", "Bearer tok")
		if etag != "" {
			r.Header.Set("If-None-Match", etag)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	start := time.Now()
	w := get("")
	if time.Since(start) > 200*time.Millisecond || w.Code != 200 {
		t.Fatalf("code %d after %v", w.Code, time.Since(start))
	}
	if !strings.Contains(w.Body.String(), `"st":"none"`) {
		t.Errorf("body %s", w.Body)
	}
	if w2 := get(w.Header().Get("ETag")); w2.Code != 304 {
		t.Errorf("etag request: %d", w2.Code)
	}
	r := httptest.NewRequest("GET", "/api/v1/cards", nil)
	w3 := httptest.NewRecorder()
	mux.ServeHTTP(w3, r)
	if w3.Code != 401 {
		t.Errorf("no token: %d", w3.Code)
	}
}

func TestGPUHistory(t *testing.T) {
	h := &gpuHistory{}
	h.record([]gpuStat{
		{Host: "server", Up: true, Temp: 60, Util: 10, VRAM: 2000, VRAMTotal: 8000, Power: 90},
		{Host: "server", Up: true, Temp: 70},
		{Host: "off", Up: false, Temp: 99},
		{Host: "zero", Up: true, VRAM: 5, VRAMTotal: 0},
	})
	if got := h.series("server", "vram"); len(got) != 1 || got[0] != 25 {
		t.Errorf("vram %v", got)
	}
	if got := h.series("server#2", "temp"); len(got) != 1 || got[0] != 70 {
		t.Errorf("second gpu %v", got)
	}
	if got := h.series("off", "temp"); len(got) != 0 {
		t.Errorf("down gpu recorded: %v", got)
	}
	if got := h.series("zero", "vram"); len(got) != 1 || got[0] != 0 {
		t.Errorf("zero total %v", got)
	}
	for i := 1; i <= 60; i++ {
		h.record([]gpuStat{{Host: "server", Up: true, Temp: 100 + i}})
	}
	got := h.series("server", "temp")
	if len(got) != 60 || got[59] != 160 || got[0] != 101 {
		t.Errorf("61 records: len %d first %v last %v", len(got), got[0], got[len(got)-1])
	}

	// only the last hour: a GPU back after hours down doesn't join yesterday to today
	now := time.Unix(1e9, 0)
	h = &gpuHistory{now: func() time.Time { return now }}
	h.record([]gpuStat{{Host: "server", Up: true, Temp: 1}})
	now = now.Add(59 * time.Minute)
	h.record([]gpuStat{{Host: "server", Up: true, Temp: 2}})
	if got := fmt.Sprint(h.series("server", "temp")); got != "[1 2]" {
		t.Errorf("59 min apart: %s", got)
	}
	now = now.Add(3 * time.Hour)
	h.record([]gpuStat{{Host: "server", Up: true, Temp: 3}})
	if got := fmt.Sprint(h.series("server", "temp")); got != "[3]" {
		t.Errorf("after 3 h down: %s", got)
	}
}

// J7: a gpu card showing the fan in any slot carries it: "45%" and 0.45, "0%" and 0 (zero-RPM is a
// reading, not unknown), "--" and no f when unknown (no reading, an old agent, the GPU down or not there).
func TestGPUFan(t *testing.T) {
	live := []gpuStat{
		{Host: "ws", Up: true, Fan: 45, FanOK: true},
		{Host: "ws", Up: true, FanOK: true}, // ws#2: zero-RPM
		{Host: "nas", Up: true},             // N/A, or an old agent
		{Host: "off", Fan: 30, FanOK: true}, // down: no reading shown
	}
	h := &gpuHistory{live: func() []gpuStat { return live }}
	card := func(src, slots string) string {
		return one(t, `"type":"gpu","size":"2x3","src":"`+src+`","slots":`+slots, nil, h)
	}
	for _, slots := range []string{`{"gauge":"fan"}`, `{"line1":{"value":"fan","as":"bar"}}`,
		`{"line1":{"value":"fan","as":"text"}}`, `{"line2":["util","fan"]}`} {
		eq(t, card("ws", slots), `{"v":"45%","f":0.45}`)
	}
	eq(t, card("ws#2", `{"gauge":"fan"}`), `{"v":"0%","f":0}`)
	for _, src := range []string{"nas", "off", "gone"} {
		eq(t, card(src, `{"gauge":"fan"}`), `{"v":"--"}`)
	}
	eq(t, one(t, `"type":"gpu","size":"2x3","src":"ws","slots":{"gauge":"fan"}`, nil, nil), `{"v":"--"}`)

	// history: a fan only when known; a fan graph scales 0..100
	for _, f := range []int{20, 40} {
		h.record([]gpuStat{{Host: "ws", Up: true, Fan: f, FanOK: true}, {Host: "nas", Up: true, Temp: 50}})
	}
	if got := fmt.Sprint(h.series("ws", "fan")); got != "[20 40]" {
		t.Errorf("fan history %s", got)
	}
	if got := fmt.Sprint(h.series("nas", "fan"), h.series("nas", "temp")); got != "[] [50 50]" {
		t.Errorf("unknown fan: %s", got)
	}
	g := func(p ...float64) string { return `"g":["` + encodePoints(p, 0, 100) + `"]` }
	eq(t, card("ws", `{"line1":{"value":"fan","as":"graph"}}`), `{"v":"45%","f":0.45,`+g(20, 40)+`}`)
	eq(t, card("nas", `{"line1":{"value":"fan","as":"graph"}}`), `{"st":"none","v":"--"}`)
	// another value's graph keeps its own history; the fan comes by v and f (one g only)
	eq(t, card("nas", `{"gauge":"fan","line1":{"value":"temp","as":"graph"}}`), `{"v":"--",`+g(50, 50)+`}`)
	// no fan slot: as in Stage 2, the graph alone, or no entry at all
	eq(t, card("nas", `{"line1":{"value":"temp","as":"graph"}}`), `{`+g(50, 50)+`}`)
	eq(t, card("ws", `{"line2":["util","power"]}`), ``)
	eq(t, one(t, `"type":"gpu","size":"2x3","src":"ws"`, nil, h), ``)
}

// C5a: a graph whose value is fine but whose series is missing shows the value, stale, no plot;
// a stale series keeps its plot (a down source keeps its last value, greyed).
func TestCardsGraphSeriesTrouble(t *testing.T) {
	cpu := qOf("cpu")
	card := `"type":"graph","size":"2x2","value":"cpu"`
	f := newMap().v(cpu.Value, 40)
	eq(t, one(t, card, f, nil), `{"st":"stale","v":"40 %"}`)
	f.sers[cpu.Graph] = promSeries{P: ramp(10), St: stStale, Why: "prometheus 500"}
	eq(t, one(t, card, f, nil), `{"st":"stale","v":"40 %","g":["`+encodePoints(ramp(10), 0, 100)+`"]}`)
	f.sers[cpu.Graph] = promSeries{St: stOK} // empty
	eq(t, one(t, card, f, nil), `{"st":"stale","v":"40 %"}`)
	// no value at all stays none
	if got := one(t, card, newMap(), nil); !strings.Contains(got, `"st":"none"`) {
		t.Errorf("no value: %s", got)
	}
}

// C5b: the history records only while the gpu slot's last run succeeded.
type fakeGPU struct{}

func (fakeGPU) Section() string                      { return "homelab" }
func (fakeGPU) Key() string                          { return "gpu" }
func (fakeGPU) Interval() time.Duration              { return time.Second }
func (fakeGPU) Collect(context.Context) (any, error) { return nil, nil }

func TestRecordGPUOnlyWhenOK(t *testing.T) {
	s := &slot{c: fakeGPU{}, data: []gpuStat{{Host: "h", Up: true, Temp: 50}}, ok: true}
	h := &gpuHistory{}
	recordGPU(h, []*slot{s})
	s.ok = false // the collector fails: its old data is still there
	recordGPU(h, []*slot{s})
	recordGPU(h, []*slot{s})
	if n := len(h.series("h", "temp")); n != 1 {
		t.Errorf("%d samples, want 1", n)
	}
}

func TestCardsStatusTrim(t *testing.T) {
	st := `"type":"status","size":"1x1","value":{"prometheus":"s"}`
	f := newMap()
	f.vals["s"] = promVal{S: " running\n", St: stOK}
	eq(t, one(t, st, f, nil), `{"v":"up"}`)
	f.vals["s"] = promVal{S: " broken\n", St: stOK}
	eq(t, one(t, st, f, nil), `{"st":"bad","v":"broken"}`)
}
