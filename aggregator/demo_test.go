package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// With DEMO_NOW set, the demo commands the simulator uses are reproducible, however long apart.
func TestDemoDeterministic(t *testing.T) {
	t.Setenv("DEMO_NOW", "1795032000")
	j1 := demoJSON()
	d1, d2 := t.TempDir(), t.TempDir()
	if err := writeDemoWeather(d1, demoNow()); err != nil {
		t.Fatal(err)
	}
	if err := writeDemoWeather(d2, demoNow()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond) // a live clock would tick over
	if !bytes.Equal(j1, demoJSON()) {
		t.Error("demo-json differs between runs")
	}
	files, _ := filepath.Glob(filepath.Join(d1, "*"))
	if len(files) == 0 {
		t.Fatal("no demo-weather output")
	}
	for _, f := range files {
		a, _ := os.ReadFile(f)
		b, err := os.ReadFile(filepath.Join(d2, filepath.Base(f)))
		if err != nil || !bytes.Equal(a, b) {
			t.Errorf("%s differs between runs", filepath.Base(f))
		}
	}
}

// general, light and profile cards, and gpu cards with a graph or a fan slot, are the ones with an entry in the
// cards document
func hasEntry(c Card) bool {
	if c.Type == "gpu" {
		_, _, as, _ := c.slots()
		return as == "graph" || c.usesFan()
	}
	return contains(generalTypes, c.Type) || c.Type == "light" || c.Type == "profile"
}

func TestDemoLayoutsIDsUnique(t *testing.T) {
	seen := map[string]string{}
	for n, l := range demoLayouts() {
		if _, err := parseLayout(handLayout(l)); err != nil {
			t.Errorf("%s: %v", n, err)
		}
		for _, tab := range l.Tabs {
			for _, c := range tab.Cards {
				if !hasEntry(c) {
					continue
				}
				if prev, dup := seen[c.ID]; dup {
					t.Errorf("id %s in both %s and %s", c.ID, prev, n)
				}
				seen[c.ID] = n
			}
		}
	}
}

func TestDemoCards(t *testing.T) {
	t.Setenv("DEMO_NOW", "1795032000")
	a, err := demoCardsDoc(demoNow())
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond)
	b, _ := demoCardsDoc(demoNow())
	if !bytes.Equal(a, b) {
		t.Error("two runs differ")
	}
	var d struct {
		Cards map[string]cardOut `json:"cards"`
	}
	if err := json.Unmarshal(a, &d); err != nil {
		t.Fatal(err)
	}
	for n, l := range demoLayouts() {
		for _, tab := range l.Tabs {
			for _, c := range tab.Cards {
				if hasEntry(c) {
					if o, ok := d.Cards[c.ID]; !ok {
						t.Errorf("%s: no entry for %s", n, c.ID)
					} else if o.St == stNone && !strings.HasPrefix(c.ID, "gl-many") {
						t.Errorf("%s is none", c.ID)
					}
				}
			}
		}
	}
	if d.Cards["pl-cpu"].St != stWarn || d.Cards["pl-cpu"].V != "87 %" {
		t.Errorf("server cpu: %+v", d.Cards["pl-cpu"])
	}
	if d.Cards["gs-st-2x1"].V != "down" || d.Cards["gs-st-1x1"].V != "up" {
		t.Error("status cards")
	}
	if v := d.Cards["gl-num-1x1"].V; len(v) != maxV {
		t.Errorf("long value %q", v)
	}
	if len(d.Cards["gp-vram-graph"].G) != 1 || len(d.Cards["gp-vram-graph"].G[0]) != histKeep {
		t.Errorf("gpu graph: %+v", d.Cards["gp-vram-graph"])
	}
	// the fan: spinning, zero-RPM, no reading; the vram graph beside a fan gauge stays the vram's
	ws, srv, nas, vr := d.Cards["gf-ws-graph"], d.Cards["gf-srv-bar"], d.Cards["gf-nas-text"], d.Cards["gf-ws-vram"]
	if ws.V != "52%" || ws.F == nil || *ws.F != 0.52 || len(ws.G) != 1 || len(ws.G[0]) != histKeep {
		t.Errorf("workstation fan: %+v", ws)
	}
	if srv.V != "0%" || srv.F == nil || *srv.F != 0 || nas.V != "--" || nas.F != nil {
		t.Errorf("server fan %+v, nas fan %+v", srv, nas)
	}
	if vr.V != ws.V || len(vr.G) != 1 || vr.G[0] != d.Cards["gp-vram-graph"].G[0] {
		t.Errorf("vram graph beside a fan gauge: %+v", vr)
	}
	dir := t.TempDir()
	if err := writeDemoCards(dir, demoNow()); err != nil {
		t.Fatal(err)
	}
	if f, _ := os.ReadFile(filepath.Join(dir, "cards.json")); !bytes.Equal(bytes.TrimSpace(f), a) {
		t.Error("cards.json differs")
	}
}

func TestDemoPromSeriesEndsAtValue(t *testing.T) {
	now := time.Unix(1795032000, 0)
	p := &demoProm{start: now.Add(-time.Hour), now: func() time.Time { return now }}
	for _, q := range []string{valueSpec{Shortcut: "cpu"}.queries().Graph, valueSpec{Shortcut: "net"}.queries().Graph, "some_query", `up{job="x"}`} {
		s := p.series(q)
		if len(s.P) != 60 || s.St != stOK || s.P[59] != p.value(q).V {
			t.Errorf("%s: %d points, last %v, value %v", q, len(s.P), s.P[len(s.P)-1], p.value(q).V)
		}
	}
	if p.value(`up{job="jellyfin"}`).V != 0 || p.value(`up{job="a"}`).V != 1 {
		t.Error("up")
	}
	if s := p.series(valueSpec{Shortcut: "cpu"}.queries().Graph).P; s[0] < 20 || s[0] > 35 { // the mockup's rise
		t.Errorf("cpu starts the hour at %.1f, not about 27", s[0])
	}
}

func TestDemoCardsLongSub(t *testing.T) {
	b, err := demoCardsDoc(time.Unix(1795032000, 0))
	if err != nil {
		t.Fatal(err)
	}
	var d struct {
		Cards map[string]cardOut `json:"cards"`
	}
	json.Unmarshal(b, &d)
	// shortcuts that name an instance matching two series: the plain count (the simulator makes the
	// sub 23 bytes for its general-long shot)
	for _, id := range []string{"gl-many-1x1", "gl-many-1x2"} {
		if o := d.Cards[id]; o.St != stNone || o.Sub != "returns 2 series" {
			t.Errorf("%s: %+v", id, o)
		}
	}
}

func TestDemoJSONCards(t *testing.T) {
	b, err := demoCardsDoc(time.Unix(1795032000, 0))
	if err != nil {
		t.Fatal(err)
	}
	var d struct {
		Cards map[string]cardOut `json:"cards"`
	}
	json.Unmarshal(b, &d)
	if o := d.Cards["js-app"]; o.V != "up" || o.St != "" {
		t.Errorf("js-app: %+v", o)
	}
	if o := d.Cards["js-queue"]; o.V == "" || o.St == stNone || len(d.Cards["js-latency"].G) == 0 {
		t.Errorf("js-queue/js-latency: %+v %+v", o, d.Cards["js-latency"])
	}
}

func TestDemoHACards(t *testing.T) {
	b, err := demoCardsDoc(time.Unix(1795032000, 0))
	if err != nil {
		t.Fatal(err)
	}
	var d struct {
		Cards map[string]cardOut `json:"cards"`
	}
	json.Unmarshal(b, &d)
	if o := d.Cards["ha-temp"]; o.V != "22°" || o.St != "" {
		t.Errorf("ha-temp: %+v", o)
	}
	if o := d.Cards["ha-humid"]; o.V != "48 %" || o.F == nil || *o.F != 0.48 {
		t.Errorf("ha-humid: %+v", o)
	}
	if o := d.Cards["ha-light"]; o.V != "180" || o.F == nil || *o.F != 0.706 {
		t.Errorf("ha-light: %+v", o)
	}
	if o := d.Cards["ha-tv"]; o.V != "up" || o.St != "" {
		t.Errorf("ha-tv: %+v", o)
	}
	if o := d.Cards["ha-power"]; len(o.G) == 0 || !strings.HasSuffix(o.V, " W") {
		t.Errorf("ha-power: %+v", o)
	}
}

func TestEnvOn(t *testing.T) { // DEMO_WARNING=0 used to turn it on
	for v, want := range map[string]bool{"": false, "0": false, "false": false, "1": true, "true": true,
		"yes": true, "ON": true, "No": false, "off": false} {
		t.Setenv("DESKPANEL_TEST_FLAG", v)
		if envOn("DESKPANEL_TEST_FLAG") != want {
			t.Errorf("%q: got %v", v, !want)
		}
	}
}

// The sheet matches a text to its chip by key; the demo rebuilds the text at request time, so it must not drift.
func TestDemoAlertTextKeyStable(t *testing.T) {
	start := time.Unix(1800000123, 0)
	d := newDemoWeather(start, true)
	list, _, _ := d.alertsAt(start)
	d.clock = func() time.Time { return start.Add(70 * time.Second) }
	for i, a := range list {
		x, ok := d.text(i)
		if !ok || x.Key == "" || x.Key != a.Key || x.Event != a.Event {
			t.Errorf("text %d = %q/%q, list has %q/%q", i, x.Key, x.Event, a.Key, a.Event)
		}
	}
}
