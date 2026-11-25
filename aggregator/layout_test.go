package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseLayoutOK(t *testing.T) {
	src := `{"tabs":[
 {"name":"Main","cards":[
  {"id":"g1","type":"gpu","at":[1,1],"size":"2x3","src":"studio"},
  {"id":"p1","type":"pool","at":[1,4],"size":"1x2","src":"tank","tap":"notifications"},
  {"id":"d1","type":"dots","at":[1,6],"size":"4x1","tap":"tab:Board"}
 ]},
 {"name":"Board","cards":[{"id":"b","type":"board","at":[1,1],"size":"4x6"}]}
]}`
	l, err := parseLayout([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Tabs) != 2 || len(l.Tabs[0].Cards) != 3 {
		t.Fatalf("bad shape: %+v", l)
	}
	if w, h := l.Tabs[0].Cards[0].wh(); w != 2 || h != 3 {
		t.Fatalf("wh = %d,%d", w, h)
	}
}

func tabJSON(name string, cards ...string) string {
	return fmt.Sprintf(`{"name":%q,"cards":[%s]}`, name, strings.Join(cards, ","))
}

func TestValidateErrors(t *testing.T) {
	many := func(n int) string {
		var cs []string
		for i := 0; i < n; i++ {
			// 24 distinct 1x1 gpu cells in a 4x6 grid
			cs = append(cs, fmt.Sprintf(`{"id":"c%d","type":"gpu","at":[%d,%d],"size":"1x1"}`, i, i%4+1, i/4+1))
		}
		return strings.Join(cs, ",\n")
	}
	var tabs7 []string
	for i := 0; i < 7; i++ {
		tabs7 = append(tabs7, tabJSON(fmt.Sprintf("T%d", i)))
	}
	tests := []struct {
		name, src, want string
		line            int
	}{
		{"syntax", "{\"tabs\":[\n{\"name\":\"A\",\n\"cards\":[],,]}]}", "", 3},
		{"dup id", "{\"tabs\":[{\"name\":\"A\",\"cards\":[\n" +
			`{"id":"x","type":"gpu","at":[1,1],"size":"1x1"},` + "\n" +
			`{"id":"x","type":"gpu","at":[2,1],"size":"1x1"}]}]}`, "duplicate", 3},
		{"unknown type", "{\"tabs\":[{\"name\":\"A\",\"cards\":[\n" +
			`{"id":"x","type":"clock","at":[1,1],"size":"1x1"}]}]}`, "unknown type", 2},
		{"bad size", "{\"tabs\":[{\"name\":\"A\",\"cards\":[\n" +
			`{"id":"x","type":"gpu","at":[1,1],"size":"3x3"}]}]}`, "size", 2},
		{"outside grid", "{\"tabs\":[{\"name\":\"A\",\"cards\":[\n" +
			`{"id":"x","type":"gpu","at":[4,1],"size":"2x3"}]}]}`, "outside", 2},
		{"overlap", "{\"tabs\":[{\"name\":\"A\",\"cards\":[\n" +
			`{"id":"cpu","type":"gpu","at":[1,4],"size":"2x2"},` + "\n" +
			`{"id":"ram","type":"gpu","at":[2,4],"size":"1x1"}]}]}`, "'ram' overlaps 'cpu' at col 2 row 4", 3},
		{"4x6 not alone", "{\"tabs\":[{\"name\":\"A\",\"cards\":[\n" +
			`{"id":"b","type":"board","at":[1,1],"size":"4x6"},` + "\n" +
			`{"id":"x","type":"gpu","at":[1,1],"size":"1x1"}]}]}`, "alone", 3},
		{"7 tabs", "{\"tabs\":[\n" + strings.Join(tabs7, ",\n") + "]}", "tabs", 8},
		{"25 cards", "{\"tabs\":[{\"name\":\"A\",\"cards\":[\n" + many(24) + ",\n" +
			`{"id":"extra","type":"gpu","at":[1,1],"size":"1x1"}]}]}`, "24 cards", 26},
		{"bad tab tap", "{\"tabs\":[{\"name\":\"A\",\"cards\":[\n" +
			`{"id":"x","type":"gpu","at":[1,1],"size":"1x1","tap":"tab:Nope"}]}]}`, "Nope", 2},
		{"bad dots src", "{\"tabs\":[{\"name\":\"A\",\"cards\":[\n" +
			`{"id":"x","type":"dots","at":[1,1],"size":"4x1","src":"bogus"}]}]}`, "src", 2},
		{"tap on radar", "{\"tabs\":[{\"name\":\"A\",\"cards\":[\n" +
			`{"id":"r","type":"radar","at":[1,1],"size":"4x6","tap":"notifications"}]}]}`, "'r' a radar card takes no tap", 2},
		{"tap on board", "{\"tabs\":[{\"name\":\"A\",\"cards\":[]},{\"name\":\"B\",\"cards\":[\n" +
			`{"id":"b","type":"board","at":[1,1],"size":"4x6","tap":"tab:A"}]}]}`, "board card takes no tap", 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseLayout([]byte(tc.src))
			var le *layoutError
			if !errors.As(err, &le) {
				t.Fatalf("want *layoutError, got %v", err)
			}
			if le.Line != tc.line {
				t.Errorf("line = %d, want %d (%v)", le.Line, tc.line, le)
			}
			if !strings.Contains(le.Msg, tc.want) {
				t.Errorf("msg %q lacks %q", le.Msg, tc.want)
			}
			if len(le.Error()) > 88 {
				t.Errorf("error too long (%d): %s", len(le.Error()), le)
			}
		})
	}
}

// Limits mirror firmware/main/layout.h (name[16], src/title[28], tap[24]).
func TestValidateFirmwareLimits(t *testing.T) {
	n := func(k int) string { return strings.Repeat("x", k) }
	doc := func(tab, extra string) string {
		return `{"tabs":[` + tabJSON(tab, `{"id":"a","type":"gpu","at":[1,1],"size":"1x1"`+extra+`}`) + `]}`
	}
	withID := func(id string) string {
		return `{"tabs":[` + tabJSON("A", `{"id":"`+id+`","type":"gpu","at":[1,1],"size":"1x1"}`) + `]}`
	}
	for name, s := range map[string]string{
		"tab15": doc(n(15), ""), "title27": doc("A", `,"title":"`+n(27)+`"`), "src27": doc("A", `,"src":"`+n(27)+`"`),
		"id23": withID(n(23)),
	} { // (a valid tap is at most "tab:"+15 = 19 bytes, so only the over-limit side is reachable)
		if _, err := parseLayout([]byte(s)); err != nil {
			t.Errorf("%s rejected: %v", name, err)
		}
	}
	for name, s := range map[string]string{
		"tab16": doc(n(16), ""), "title28": doc("A", `,"title":"`+n(28)+`"`), "src28": doc("A", `,"src":"`+n(28)+`"`),
		"tap24": doc("A", `,"tap":"tab:`+n(20)+`"`), "id24": withID(n(24)),
	} {
		if _, err := parseLayout([]byte(s)); err == nil {
			t.Errorf("%s accepted", name)
		} else if !strings.Contains(err.Error(), "over") {
			t.Errorf("%s: unexpected error %v", name, err)
		}
	}
}

func TestDefaultLayoutCutsLongSrc(t *testing.T) {
	l := defaultLayout(found{TrueNAS: true, Pools: []string{strings.Repeat("é", 20)}}) // 40 bytes
	for _, tab := range l.Tabs {
		for _, c := range tab.Cards {
			if len(c.Src) > maxSrcLen {
				t.Errorf("%s src is %d bytes", c.ID, len(c.Src))
			}
		}
	}
	if _, err := parseLayout(servedNoTour(t, l)); err != nil {
		t.Error(err)
	}
}

// servedNoTour is the served document minus its tour: the served tour (idle_s, skip as indexes) is the
// panel's form, not layout.json's, so parseLayout would not read it back.
func servedNoTour(t *testing.T, l Layout) []byte {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(encodeLayoutDoc(l), &m); err != nil {
		t.Fatal(err)
	}
	delete(m, "tour")
	b, _ := json.Marshal(m)
	return b
}

func TestParseLayoutBOMAndUTF16(t *testing.T) {
	src := `{"tabs":[{"name":"A","cards":[]}]}`
	if _, err := parseLayout(append([]byte{0xEF, 0xBB, 0xBF}, src...)); err != nil {
		t.Errorf("UTF-8 BOM: %v", err)
	}
	for _, bom := range [][]byte{{0xFF, 0xFE}, {0xFE, 0xFF}} {
		utf16 := append([]byte{}, bom...)
		for _, c := range []byte(src) { // the BOM's byte order is all the check looks at
			utf16 = append(utf16, c, 0)
		}
		if _, err := parseLayout(utf16); err == nil || err.Error() != "layout.json line 1: save as UTF-8" {
			t.Errorf("% x: %v", bom, err)
		}
	}
}

// The panel gets pool names as the TrueNAS collector cuts them (24 bytes, "..."); a pool src is cut the same
// way when served, so a hand-written src with the full name still matches. Other types keep their src.
func TestLayoutDocCutsPoolSrc(t *testing.T) {
	long := strings.Repeat("p", 27)
	l, err := parseLayout([]byte(`{"tabs":[{"name":"A","cards":[{"id":"p","type":"pool","at":[1,1],"size":"1x1","src":"` + long +
		`"},{"id":"g","type":"gpu","at":[2,1],"size":"1x1","src":"` + long + `"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	d := layoutDoc(l)
	if got, want := d.Tabs[0].Cards[0].Src, trunc(long, poolNameMx); got != want || len(got) != poolNameMx {
		t.Errorf("pool src %q, want %q", got, want)
	}
	if d.Tabs[0].Cards[1].Src != long {
		t.Errorf("gpu src cut: %q", d.Tabs[0].Cards[1].Src)
	}
	l.Tabs[0].Cards = l.Tabs[0].Cards[:1]         // the pool card: a served gpu card (flat slots) is not layout.json
	again, err := parseLayout(servedNoTour(t, l)) // serving the served document changes nothing
	if err != nil || string(encodeLayoutDoc(again)) != string(encodeLayoutDoc(l)) {
		t.Errorf("not idempotent: %v %s", err, encodeLayoutDoc(again))
	}
}

func TestValidateUnknownSourceIsFine(t *testing.T) {
	src := `{"tabs":[{"name":"A","cards":[{"id":"g","type":"gpu","at":[1,1],"size":"1x1","src":"nosuchhost"}]}]}`
	if _, err := parseLayout([]byte(src)); err != nil {
		t.Fatal(err)
	}
}

func TestValidateMore(t *testing.T) {
	long := strings.Repeat("x", 22) // ids over 16 bytes are shortened in messages
	// extra fields before/inside tabs must not shift card lines
	extra := "{\n\"meta\":{\"a\":{}},\n\"tabs\":[\n{\"name\":\"A\",\"x\":[{}],\"cards\":[\n" +
		"{\"id\":\"a\",\"type\":\"gpu\",\"at\":[1,1],\"size\":\"1x1\"}]},\n" +
		"{\"name\":\"B\",\"cards\":[\n" +
		"{\"id\":\"b\",\n \"type\":\"gpu\",\n \"at\":[1,1],\"size\":\"9x9\"}]}\n]}"
	card := func(id, at, size string) string {
		return fmt.Sprintf(`{"id":%q,"type":"gpu","at":%s,"size":%q}`, id, at, size)
	}
	tests := []struct {
		name, src, want string
		line            int
	}{
		{"long ids", "{\"tabs\":[{\"name\":\"A\",\"cards\":[\n" +
			`{"id":"` + long + `","type":"gpu","at":[1,4],"size":"2x2"},` + "\n" +
			`{"id":"` + long + `y","type":"gpu","at":[2,4],"size":"1x1"}]}]}`, "at col 2 row 4", 3},
		{"extra fields", extra, "size", 7},
		{"3rd tab", "{\"tabs\":[" + tabJSON("A") + ",\n" + tabJSON("B") + ",\n{\"name\":\"C\",\"cards\":[\n\n" +
			card("z", "[1,1]", "7x7") + "]}]}", "size", 5},
		{"after multiline cards", "{\"tabs\":[{\"name\":\"A\",\"cards\":[\n{\n\"id\":\"a\",\n\"type\":\"gpu\",\n\"at\":[1,1],\n\"size\":\"1x1\"},\n" +
			card("a", "[2,1]", "1x1") + "]}]}", "duplicate", 7},
		{"syntax msg", "{\"tabs\":[,]}", "bad JSON", 1},
		{"dup tab", "{\"tabs\":[" + tabJSON("A") + ",\n" + tabJSON("A") + "]}", "tab 2", 2},
		{"empty tab name", "{\"tabs\":[\n" + tabJSON("") + "]}", "tab 1", 2},
		{"no tabs", `{"tabs":[]}`, "1-6 tabs", 1},
		{"tap tab:", "{\"tabs\":[{\"name\":\"A\",\"cards\":[\n" +
			`{"id":"x","type":"gpu","at":[1,1],"size":"1x1","tap":"tab:"}]}]}`, "no tab", 2},
		{"at zero", "{\"tabs\":[{\"name\":\"A\",\"cards\":[\n" + card("x", "[0,0]", "1x1") + "]}]}", "outside", 2},
		{"at huge", "{\"tabs\":[{\"name\":\"A\",\"cards\":[\n" + card("x", "[9223372036854775807,1]", "1x1") + "]}]}", "outside", 2},
		{"size 2X3", "{\"tabs\":[{\"name\":\"A\",\"cards\":[\n" + card("x", "[1,1]", "2X3") + "]}]}", "size", 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseLayout([]byte(tc.src))
			var le *layoutError
			if !errors.As(err, &le) {
				t.Fatalf("want *layoutError, got %v", err)
			}
			if le.Line != tc.line || !strings.Contains(le.Msg, tc.want) || len(le.Error()) > 88 {
				t.Errorf("got %q (line %d), want %q line %d", le.Error(), le.Line, tc.want, tc.line)
			}
		})
	}
}

func TestHandLayout(t *testing.T) {
	l := defaultLayout(found{GPUs: []string{"<gpu host>"}, Pools: []string{`we"ird, name: x`}, TrueNAS: true})
	l.Tabs[0].Cards = append(l.Tabs[0].Cards, Card{ID: "cpu", Type: "number", At: [2]int{3, 1}, Size: "1x1", Value: json.RawMessage(`"cpu"`), Unit: "%", Warn: new(float64), Tap: "none"})
	b := handLayout(l)
	got, err := parseLayout(b)
	if err != nil {
		t.Fatalf("handLayout output doesn't parse: %v\n%s", err, b)
	}
	for i := range got.Tabs {
		for j := range got.Tabs[i].Cards {
			got.Tabs[i].Cards[j].parsed = nil // set by parseLayout only
		}
	}
	if !reflect.DeepEqual(got, l) {
		t.Fatalf("round trip changed the layout:\n%s", b)
	}
	for _, want := range []string{
		`        {"id": "gpu--gpu-host-", "type": "gpu", "at": [1, 1], "size": "2x3", "src": "<gpu host>", "tap": "none"},`,
		`"src": "we\"ird, name: x"`,
	} {
		if !strings.Contains(string(b), want) {
			t.Errorf("missing %s in:\n%s", want, b)
		}
	}
}

func TestDefaultLayout(t *testing.T) {
	names := func(p string, n int) []string {
		var s []string
		for i := 1; i <= n; i++ {
			s = append(s, fmt.Sprintf("%s%d", p, i))
		}
		return s
	}
	type want struct {
		id, size string
		at       [2]int
	}
	tests := []struct {
		name string
		f    found
		want []want // Homelab tab, in order
	}{
		{"2 gpu 3 pools", found{GPUs: names("g", 2), Pools: names("p", 3), TrueNAS: true}, []want{
			{"gpu-g1", "2x3", [2]int{1, 1}}, {"gpu-g2", "2x3", [2]int{3, 1}},
			{"pool-p1", "1x2", [2]int{1, 4}}, {"pool-p2", "1x2", [2]int{2, 4}}, {"pool-p3", "1x2", [2]int{3, 4}},
			{"nas-alerts", "1x2", [2]int{4, 4}}, {"dots", "4x1", [2]int{1, 6}}}},
		{"1 gpu", found{GPUs: names("g", 1), Pools: names("p", 1), TrueNAS: true}, []want{
			{"gpu-g1", "2x3", [2]int{1, 1}}, {"pool-p1", "1x2", [2]int{1, 4}},
			{"nas-alerts", "1x2", [2]int{4, 4}}, {"dots", "4x1", [2]int{1, 6}}}},
		{"4 gpus (6 given)", found{GPUs: names("g", 6)}, []want{
			{"gpu-g1", "1x3", [2]int{1, 1}}, {"gpu-g2", "1x3", [2]int{2, 1}}, {"gpu-g3", "1x3", [2]int{3, 1}},
			{"gpu-g4", "1x3", [2]int{4, 1}}, {"dots", "4x1", [2]int{1, 4}}}},
		{"0 gpus", found{Pools: names("p", 2), TrueNAS: true}, []want{
			{"pool-p1", "1x2", [2]int{1, 1}}, {"pool-p2", "1x2", [2]int{2, 1}},
			{"nas-alerts", "1x2", [2]int{4, 1}}, {"dots", "4x1", [2]int{1, 3}}}},
		{"5 pools", found{GPUs: names("g", 2), Pools: names("p", 5), TrueNAS: true}, []want{
			{"gpu-g1", "2x3", [2]int{1, 1}}, {"gpu-g2", "2x3", [2]int{3, 1}},
			{"pool-p1", "1x1", [2]int{1, 4}}, {"pool-p2", "1x1", [2]int{2, 4}}, {"pool-p3", "1x1", [2]int{3, 4}},
			{"pool-p4", "1x1", [2]int{4, 4}}, {"pool-p5", "1x1", [2]int{1, 5}},
			{"nas-alerts", "1x1", [2]int{2, 5}}, {"dots", "4x1", [2]int{1, 6}}}},
		{"no truenas", found{GPUs: names("g", 2)}, []want{
			{"gpu-g1", "2x3", [2]int{1, 1}}, {"gpu-g2", "2x3", [2]int{3, 1}}, {"dots", "4x1", [2]int{1, 4}}}},
		{"nothing", found{}, []want{{"dots", "4x1", [2]int{1, 1}}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			l := defaultLayout(tc.f)
			var got []want
			for _, c := range l.Tabs[0].Cards {
				got = append(got, want{c.ID, string(c.Size), c.At})
			}
			if fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Errorf("got %v\nwant %v", got, tc.want)
			}
			if l.Tabs[1].Name != "Planka" || l.Tabs[2].Name != "Weather" || l.Tabs[1].Cards[0].ID != "board" || l.Tabs[2].Cards[0].ID != "radar" {
				t.Errorf("tabs: %+v", l.Tabs[1:])
			}
			data, _ := json.Marshal(l)
			if _, err := parseLayout(data); err != nil {
				t.Errorf("does not validate: %v", err)
			}
		})
	}
	// caps and taps
	l := defaultLayout(found{GPUs: names("g", 2), Pools: names("p", 9), TrueNAS: true})
	if n := len(l.Tabs[0].Cards); n != 2+7+1+1 {
		t.Errorf("9 pools: %d cards", n)
	}
	data, _ := json.Marshal(l)
	if _, err := parseLayout(data); err != nil {
		t.Errorf("9 pools: %v", err)
	}
	for _, c := range l.Tabs[0].Cards {
		if (c.Tap == "alert-details") != (c.Type == "nas-alerts") || (c.Type != "nas-alerts" && c.Tap != "none") {
			t.Errorf("tap %s on %s", c.Tap, c.ID)
		}
	}
}

func TestFoundSourcesDemo(t *testing.T) {
	var slots []*slot
	for _, c := range demoSources(time.Now()) {
		d, _ := c.Collect(context.Background())
		slots = append(slots, &slot{c: c, data: d})
	}
	f := foundSources(slots)
	if fmt.Sprint(f.GPUs) != "[workstation server]" || fmt.Sprint(f.Pools) != "[tank backup boot-pool]" || !f.TrueNAS {
		t.Errorf("%+v", f)
	}
}

// Waiting until the GPU and TrueNAS slots have each finished a run; other collectors don't count.
func TestFoundSourcesWaiting(t *testing.T) {
	g, n := &slot{c: &gpuCollector{}}, &slot{c: &trueNAS{}}
	slots := []*slot{g, n, {c: &planka{}}}
	if !foundSources(slots).Waiting {
		t.Error("not waiting before any run")
	}
	g.ran = true
	if !foundSources(slots).Waiting {
		t.Error("not waiting for TrueNAS")
	}
	n.ran = true // (n failed: no data, but it ran)
	if foundSources(slots).Waiting {
		t.Error("still waiting after both ran")
	}
}

func TestDefaultLayoutMultiGPUAndIDs(t *testing.T) {
	var slots []*slot
	slots = append(slots, &slot{c: &gpuCollector{}, data: []gpuStat{{Host: "a"}, {Host: "a"}, {Host: "b"}, {Host: "b"}, {Host: "c"}}})
	f := foundSources(slots)
	if fmt.Sprint(f.GPUs) != "[a a#2 b b#2 c]" {
		t.Fatalf("%v", f.GPUs)
	}
	l := defaultLayout(f)
	var got []string
	for _, c := range l.Tabs[0].Cards[:4] {
		got = append(got, c.ID+"|"+c.Src+"|"+string(c.Size))
	}
	if fmt.Sprint(got) != "[gpu-a|a|1x3 gpu-a-2|a#2|1x3 gpu-b|b|1x3 gpu-b-2|b#2|1x3]" {
		t.Errorf("%v", got)
	}

	l = defaultLayout(found{Pools: []string{"tank/media 2", "Tank-Media-2", "tank/media-2", "a-very-long-pool-name-indeed"}, TrueNAS: true})
	var ids []string
	for _, c := range l.Tabs[0].Cards {
		ids = append(ids, c.ID)
		if len(c.ID) > 23 {
			t.Errorf("id too long: %s", c.ID)
		}
	}
	if fmt.Sprint(ids[:3]) != "[pool-tank-media-2 pool-tank-media-2-2 pool-tank-media-2-3]" || l.Tabs[0].Cards[0].Src != "tank/media 2" {
		t.Errorf("%v src=%s", ids, l.Tabs[0].Cards[0].Src)
	}
	data, _ := json.Marshal(l)
	if _, err := parseLayout(data); err != nil {
		t.Error(err)
	}
}

func storeFor(t *testing.T) (*layoutStore, string, *[]string) {
	path := filepath.Join(t.TempDir(), "layout.json")
	var notes []string
	return newLayoutStore(path, func(src, msg, lvl string) {
		if src != "deskpanel" || lvl != "warn" {
			t.Errorf("note %q %q", src, lvl)
		}
		notes = append(notes, msg)
	}), path, &notes
}

// write bumps the size too, so the version key changes even within one mtime tick.
func writeLayout(t *testing.T, path, s string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

const oneTab = `{"tabs":[{"name":"%s","cards":[]}]}`

func TestLayoutStoreDefaultWhenMissing(t *testing.T) {
	s, _, _ := storeFor(t)
	f := found{GPUs: []string{"a"}, Pools: []string{"tank"}, TrueNAS: true}
	s.refresh(f)
	if want := encodeLayoutDoc(defaultLayout(f)); string(s.doc()) != string(want) {
		t.Errorf("doc = %s", s.doc())
	}
	if s.refresh(f) {
		t.Error("unchanged found reported changed")
	}
	if !s.refresh(found{GPUs: []string{"a", "b"}, TrueNAS: true}) {
		t.Error("changed found not noticed")
	}
}

// settle runs n refreshes (a new file version needs two to be accepted).
func settle(s *layoutStore, n int) {
	for i := 0; i < n; i++ {
		s.refresh(found{})
	}
}

func TestLayoutStoreReload(t *testing.T) {
	s, p, _ := storeFor(t)
	writeLayout(t, p, fmt.Sprintf(oneTab, "One"))
	settle(s, 2)
	if !strings.Contains(string(s.doc()), `"One"`) {
		t.Fatalf("first: %s", s.doc())
	}
	writeLayout(t, p, fmt.Sprintf(oneTab, "Second"))
	settle(s, 1)
	if !strings.Contains(string(s.doc()), `"One"`) {
		t.Fatal("new version served before debounce")
	}
	if settle(s, 1); !strings.Contains(string(s.doc()), `"Second"`) {
		t.Fatalf("second: %s", s.doc())
	}
	os.Remove(p) // gone for good: back to the generated default after 3 checks
	settle(s, 2)
	if !strings.Contains(string(s.doc()), `"Second"`) {
		t.Fatal("reset too early")
	}
	if settle(s, 1); !strings.Contains(string(s.doc()), `"Homelab"`) {
		t.Fatalf("removed: %s", s.doc())
	}
}

// Just started: a file is served at the first look (nothing on screen to protect); later versions wait.
func TestLayoutStoreFirstLoadNoWait(t *testing.T) {
	s, p, _ := storeFor(t)
	writeLayout(t, p, fmt.Sprintf(oneTab, "Mine"))
	if s.refresh(found{Waiting: true}); !strings.Contains(string(s.doc()), `"Mine"`) {
		t.Fatalf("first refresh: %s", s.doc())
	}
	writeLayout(t, p, fmt.Sprintf(oneTab, "Later"))
	if s.refresh(found{}); !strings.Contains(string(s.doc()), `"Mine"`) {
		t.Fatal("a later version served without the second look")
	}
	if s.refresh(found{}); !strings.Contains(string(s.doc()), `"Later"`) {
		t.Fatalf("later: %s", s.doc())
	}
}

// No file: no default (503) until the GPU and TrueNAS collectors have run, or layoutWait has passed.
func TestLayoutStoreWaitsForCollectors(t *testing.T) {
	code := func(s *layoutStore) int {
		mux := http.NewServeMux()
		addLayoutRoute(mux, "tok", s)
		r := httptest.NewRequest("GET", "/api/v1/layout", nil)
		r.Header.Set("Authorization", "Bearer tok")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w.Code
	}
	waiting := found{GPUs: []string{"a"}, Waiting: true}
	s, _, _ := storeFor(t)
	s.clock = func() time.Time { return s.start.Add(layoutWait - time.Second) }
	if s.refresh(waiting); s.doc() != nil || code(s) != 503 {
		t.Fatalf("served while waiting: %s", s.doc())
	}
	if s.refresh(found{GPUs: []string{"a", "b"}}); !strings.Contains(string(s.doc()), "gpu-b") || code(s) != 200 {
		t.Fatalf("not served once the collectors ran: %s", s.doc())
	}
	s, _, _ = storeFor(t)
	s.clock = func() time.Time { return s.start.Add(layoutWait) }
	if s.refresh(waiting); !strings.Contains(string(s.doc()), "gpu-a") || code(s) != 200 {
		t.Fatalf("not served after layoutWait: %s", s.doc())
	}
}

func TestLayoutStoreBadFileOneNote(t *testing.T) {
	s, p, notes := storeFor(t)
	writeLayout(t, p, fmt.Sprintf(oneTab, "Good"))
	settle(s, 2)
	writeLayout(t, p, "{\n\"tabs\": [oops\n]}")
	settle(s, 1)
	if len(*notes) != 0 {
		t.Fatalf("noted before debounce: %q", *notes)
	}
	settle(s, 3)
	if !strings.Contains(string(s.doc()), `"Good"`) {
		t.Error("old layout not kept")
	}
	if len(*notes) != 1 || !strings.Contains((*notes)[0], "line") {
		t.Fatalf("notes = %q", *notes)
	}
	writeLayout(t, p, fmt.Sprintf(oneTab, "Fixed"))
	settle(s, 3)
	if !strings.Contains(string(s.doc()), `"Fixed"`) || len(*notes) != 1 {
		t.Errorf("after fix: %s notes %q", s.doc(), *notes)
	}
	writeLayout(t, p, "{{{ and a second bad version")
	settle(s, 2)
	if len(*notes) != 2 {
		t.Errorf("new bad version should note again: %q", *notes)
	}
}

func TestLayoutStoreHalfWrittenAndMissing(t *testing.T) {
	s, p, notes := storeFor(t)
	writeLayout(t, p, fmt.Sprintf(oneTab, "Old")) // not full's size: writes this close share an mtime
	settle(s, 2)
	full := fmt.Sprintf(oneTab, "Done")
	writeLayout(t, p, "")
	settle(s, 3) // empty is never parsed
	writeLayout(t, p, full[:20])
	settle(s, 1)
	writeLayout(t, p, full) // completed before the second look: no note, new layout
	settle(s, 2)
	if !strings.Contains(string(s.doc()), `"Done"`) || len(*notes) != 0 {
		t.Fatalf("doc %s notes %q", s.doc(), *notes)
	}
	os.Remove(p) // delete then recreate within 2 checks: no flip to the default
	settle(s, 2)
	writeLayout(t, p, full)
	settle(s, 3)
	if !strings.Contains(string(s.doc()), `"Done"`) || len(*notes) != 0 {
		t.Fatalf("flipped: %s notes %q", s.doc(), *notes)
	}
}

func TestLayoutDocDefaultTap(t *testing.T) {
	l, err := parseLayout([]byte(`{"tabs":[{"name":"A","cards":[
  {"id":"a","type":"nas-alerts","at":[1,1],"size":"1x2"},
  {"id":"g","type":"gpu","at":[2,1],"size":"1x2"},
  {"id":"n","type":"nas-alerts","at":[3,1],"size":"1x2","tap":"none"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	var d struct {
		Tabs []struct{ Cards []struct{ ID, Tap string } }
	}
	if err := json.Unmarshal(encodeLayoutDoc(l), &d); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range d.Tabs[0].Cards {
		got = append(got, c.ID+":"+c.Tap)
	}
	if s := strings.Join(got, " "); s != "a:alert-details g:none n:none" {
		t.Errorf("taps: %s", s)
	}
}

func TestCardSizeArrayLength(t *testing.T) {
	src := `{"tabs":[{"name":"A","cards":[{"id":"g","type":"gpu","at":[1,1],"size":[2,3,4]}]}]}`
	if _, err := parseLayout([]byte(src)); err == nil {
		t.Error("3-element size accepted")
	}
}

func TestDemoLayoutsDeterministic(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	if writeDemoLayouts(a) != nil || writeDemoLayouts(b) != nil {
		t.Fatal("write")
	}
	es, _ := os.ReadDir(a)
	for _, e := range es {
		x, _ := os.ReadFile(filepath.Join(a, e.Name()))
		y, _ := os.ReadFile(filepath.Join(b, e.Name()))
		if string(x) != string(y) {
			t.Errorf("%s differs", e.Name())
		}
	}
}

func TestLayoutRoute(t *testing.T) {
	s, _, _ := storeFor(t)
	s.refresh(found{})
	mux := http.NewServeMux()
	addLayoutRoute(mux, "tok", s)
	get := func(hdr map[string]string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/api/v1/layout", nil)
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	if c := get(nil).Code; c != 401 {
		t.Fatalf("no token: %d", c)
	}
	auth := map[string]string{"Authorization": "Bearer tok"}
	w := get(auth)
	etag := w.Header().Get("ETag")
	if w.Code != 200 || etag == "" {
		t.Fatalf("%d %q", w.Code, etag)
	}
	auth["If-None-Match"] = etag
	if c := get(auth).Code; c != 304 {
		t.Errorf("etag: %d", c)
	}
}

func TestDemoLayouts(t *testing.T) {
	dir := t.TempDir()
	if err := writeDemoLayouts(dir); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"default", "gpu1", "gpu4", "gpu0", "custom", "noradar", "radar-moved", "sizes", "longnames", "sizes-longnames"} {
		b, err := os.ReadFile(filepath.Join(dir, n+".json"))
		if err != nil {
			t.Fatal(err)
		}
		servedShape(t, n, b)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "noradar.json"))
	if strings.Contains(string(b), "Weather") {
		t.Error("noradar has Weather")
	}
}

func TestLayoutExampleParses(t *testing.T) {
	b, err := os.ReadFile("layout.example.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseLayout(b); err != nil {
		t.Error(err)
	}
}

// one card of a tab, for the Stage 2 validation tests
func oneCard(card string) string {
	return `{"tabs":[{"name":"A","cards":[` + "\n" + card + `]}]}`
}

func TestValidateGeneral(t *testing.T) {
	sizes := map[string][]string{
		"number": {"1x1", "1x2", "2x2"}, "gauge": {"1x2", "2x3"}, "bar": {"1x1", "1x2", "2x1"},
		"graph": {"2x2", "2x3", "4x2", "4x3"}, "status": {"1x1", "2x1"},
	}
	for typ, ss := range sizes {
		val, thr := `"cpu"`, `,"warn":70,"bad":90`
		if typ == "status" {
			val, thr = `{"prometheus":"up"}`, ""
		}
		for _, sz := range ss {
			src := oneCard(fmt.Sprintf(`{"id":"x","type":%q,"at":[1,1],"size":%q,"value":%s,"unit":"%%"%s}`, typ, sz, val, thr))
			l, err := parseLayout([]byte(src))
			if err != nil {
				t.Errorf("%s %s: %v", typ, sz, err)
			} else if len(l.Tabs[0].Cards[0].specs()) != 1 {
				t.Errorf("%s %s: specs not kept", typ, sz)
			}
		}
	}
	for _, ok := range []string{
		`{"id":"x","type":"graph","at":[1,1],"size":"2x2","value":["cpu","ram"]}`,
		`{"id":"x","type":"gauge","at":[1,1],"size":"1x2","value":{"prometheus":"q"},"min":-10,"max":10}`,
		`{"id":"x","type":"graph","at":[1,1],"size":"2x2","value":"temp","min":20,"max":90}`, // a shortcut graph honours them
		`{"id":"x","type":"number","at":[1,1],"size":"1x1","value":"cpu","below":true}`,
	} {
		if _, err := parseLayout([]byte(oneCard(ok))); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	if (Card{}).specs() != nil {
		t.Error("hand-built card has specs")
	}
}

func TestValidateGeneralErrors(t *testing.T) {
	c := func(typ, size, rest string) string {
		return oneCard(fmt.Sprintf(`{"id":"x","type":%q,"at":[1,1],"size":%q%s}`, typ, size, rest))
	}
	tests := []struct{ name, src, want string }{
		{"missing value", c("number", "1x1", ""), "'x' needs a value"},
		{"unknown shortcut", c("number", "1x1", `,"value":"cpux"`), "'x' value: unknown shortcut 'cpux'"},
		{"src", c("number", "1x1", `,"value":"cpu","src":"a"`), "'x' takes no src"},
		{"warn above bad", c("number", "1x1", `,"value":"cpu","warn":95,"bad":80`), "'x' warn must not be above bad"},
		{"warn below bad", c("number", "1x1", `,"value":"cpu","warn":10,"bad":80,"below":true`), "'x' warn must not be below bad"},
		{"long unit", c("number", "1x1", `,"value":"cpu","unit":"123456789"`), "'x' unit over 8 bytes"},
		{"graph 1x1", c("graph", "1x1", `,"value":"cpu"`), "'x' size 1x1 not allowed for graph"},
		{"slots on number", c("number", "1x1", `,"value":"cpu","slots":{}`), "'x' takes no slots"},
		{"value on pool", c("pool", "1x1", `,"value":"cpu"`), "'x' takes no value"},
		{"slots on pool", c("pool", "1x1", `,"slots":{}`), "'x' takes no slots"},
		{"value number", c("number", "1x1", `,"value":5`), "'x' value: value must be text or an object"},
		{"value null", c("number", "1x1", `,"value":null`), "'x' value:"},
		{"json value", c("number", "1x1", `,"value":{"json":{}}`), "'x' value: json url must be http:// or https://"},
		// fields a card would ignore
		{"unit on gpu", c("gpu", "2x3", `,"unit":"%"`), "'x' takes no unit, thresholds or min/max"},
		{"warn on pool", c("pool", "1x1", `,"warn":80`), "'x' takes no unit, thresholds or min/max"},
		{"max on dots", c("dots", "4x1", `,"max":5`), "'x' takes no unit, thresholds or min/max"},
		{"below on gpu", c("gpu", "1x1", `,"below":true`), "'x' takes no unit, thresholds or min/max"},
		{"bad on status", c("status", "1x1", `,"value":{"prometheus":"up"},"bad":0`), "'x' status takes no thresholds"},
		{"min = max", c("graph", "2x2", `,"value":"cpu","min":5,"max":5`), "'x' min must be below max"},
		{"min > max", c("bar", "1x1", `,"value":{"prometheus":"q"},"min":9,"max":1`), "'x' min must be below max"},
		{"max on shortcut gauge", c("gauge", "1x2", `,"value":"temp","max":90`), "'x' min/max need a prometheus value"},
		{"min on shortcut bar", c("bar", "1x1", `,"value":"ram","min":0`), "'x' min/max need a prometheus value"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseLayout([]byte(tc.src))
			var le *layoutError
			if !errors.As(err, &le) {
				t.Fatalf("want *layoutError, got %v", err)
			}
			if le.Line != 2 || !strings.HasPrefix(le.Msg, tc.want) {
				t.Errorf("line %d, %q; want line 2, %q", le.Line, le.Msg, tc.want)
			}
			if len(le.Error()) > 88 {
				t.Errorf("too long (%d): %s", len(le.Error()), le)
			}
		})
	}
	// wrong JSON types are reported with a line, not accepted
	for _, rest := range []string{`,"value":"cpu","warn":"80"`, `,"value":"cpu","below":"yes"`, `,"value":"cpu","unit":5`, `,"value":"cpu","max":"x"`} {
		_, err := parseLayout([]byte(c("number", "1x1", rest)))
		var le *layoutError
		if !errors.As(err, &le) || le.Line < 1 || !strings.Contains(le.Msg, "bad value for") {
			t.Errorf("%s: %v", rest, err)
		}
	}
}

func TestValidateSlots(t *testing.T) {
	g := func(slots string) string {
		return oneCard(`{"id":"g","type":"gpu","at":[1,1],"size":"2x3","slots":` + slots + `}`)
	}
	for _, ok := range []string{`{"line1":{"value":"vram","as":"graph"}}`, `{"line2":[]}`, `{"gauge":"util"}`, `{}`,
		`{"line1":{"value":"power","as":"text"},"line2":["procs","temp"]}`,
		`{"gauge":"fan"}`, `{"line1":{"value":"fan","as":"bar"}}`, `{"line1":{"value":"fan","as":"graph"}}`,
		`{"line1":{"value":"fan","as":"text"},"line2":["fan","temp"]}`} {
		if _, err := parseLayout([]byte(g(ok))); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	tests := []struct{ slots, want string }{
		{`{"gauge":"power"}`, "'g' slot gauge: power can't be a gauge"},
		{`{"gauge":"procs"}`, "'g' slot gauge: procs can't be a gauge"},
		{`{"gauge":"fans"}`, "'g' slot gauge: unknown value 'fans'"},
		{`{"line1":{"value":"procs","as":"text"}}`, "'g' slot line1: procs can't be a text"},
		{`{"line1":{"value":"power","as":"bar"}}`, "'g' slot line1: power can't be a bar"},
		{`{"line1":{"value":"vram","as":"pie"}}`, "'g' slot line1: as must be bar, graph or text"},
		{`{"line2":["temp","util","vram"]}`, "'g' slot line2: at most 2 values"},
		{`{"line2":["rpm"]}`, "'g' slot line2: unknown value 'rpm'"},
		{`"temp"`, "bad value for"},
		{`{"line1":"vram"}`, "bad value for"},
		{`{"line1":5}`, "bad value for"},
		{`{"line1":{"value":5}}`, "bad value for"},
		{`{"line1":"vram","as":"graph"}`, "bad value for"},
		{`{"line1":{"value":"vram","as":"bar"},"as":"graph"}`, "'g' slot as: goes inside line1"},
		{`{"line2":"util"}`, "bad value for"},
	}
	for _, tc := range tests {
		_, err := parseLayout([]byte(g(tc.slots)))
		var le *layoutError
		if !errors.As(err, &le) || le.Line < 1 || !strings.HasPrefix(le.Msg, tc.want) {
			t.Errorf("%s: %v, want %q", tc.slots, err, tc.want)
		} else if len(le.Error()) > 88 {
			t.Errorf("%s: too long", tc.slots)
		}
	}
	_, err := parseLayout([]byte(oneCard(`{"id":"p","type":"pool","at":[1,1],"size":"1x1","slots":{"gauge":"temp"}}`)))
	if err == nil || !strings.Contains(err.Error(), "'p' takes no slots") {
		t.Errorf("pool slots: %v", err)
	}
}

func TestLayoutDocSlots(t *testing.T) {
	served := func(card string) string {
		l, err := parseLayout([]byte(oneCard(card)))
		if err != nil {
			t.Fatal(err)
		}
		var d struct {
			Tabs []struct{ Cards []json.RawMessage }
		}
		if err := json.Unmarshal(encodeLayoutDoc(l), &d); err != nil {
			t.Fatal(err)
		}
		return string(d.Tabs[0].Cards[0])
	}
	if got := served(`{"id":"g","type":"gpu","at":[1,1],"size":"2x3"}`); !strings.Contains(got, `"slots":{"gauge":"temp","line1":"vram","as":"bar","line2":["util","power"]}`) {
		t.Errorf("defaults: %s", got)
	}
	if got := served(`{"id":"g","type":"gpu","at":[1,1],"size":"2x3","slots":{"line1":{"value":"vram","as":"graph"}}}`); !strings.Contains(got, `"slots":{"gauge":"temp","line1":"vram","as":"graph","line2":["util","power"]}`) {
		t.Errorf("graph: %s", got)
	}
	if got := served(`{"id":"g","type":"gpu","at":[1,1],"size":"2x3","slots":{"line2":[]}}`); !strings.Contains(got, `"line2":[]}`) {
		t.Errorf("empty line2: %s", got)
	}
	want := `{"id":"n","type":"number","at":[1,1],"size":[1,1],"tap":"none"}`
	if got := served(`{"id":"n","type":"number","at":[1,1],"size":"1x1","value":"cpu","unit":"%","warn":1}`); got != want {
		t.Errorf("general card served as %s", got)
	}
}

// servedShape checks what the firmware reads: the served document's cards, not what parseLayout accepts.
func servedShape(t *testing.T, name string, b []byte) {
	t.Helper()
	var d struct {
		Tabs []struct {
			Cards []map[string]json.RawMessage
		}
	}
	if err := json.Unmarshal(b, &d); err != nil {
		t.Errorf("%s: %v", name, err)
		return
	}
	for _, tab := range d.Tabs {
		for _, c := range tab.Cards {
			var id string
			var typ string
			var at, size []int
			var tap string
			json.Unmarshal(c["id"], &id)
			json.Unmarshal(c["type"], &typ)
			if json.Unmarshal(c["at"], &at) != nil || len(at) != 2 || json.Unmarshal(c["size"], &size) != nil || len(size) != 2 ||
				json.Unmarshal(c["tap"], &tap) != nil || id == "" || typ == "" || tap == "" {
				t.Errorf("%s: card %v lacks id/type/at/size/tap", name, c)
			}
			s, has := c["slots"]
			if (typ == "gpu") != has {
				t.Errorf("%s: %s: slots present = %v", name, id, has)
			}
			if _, v := c["value"]; v {
				t.Errorf("%s: %s: served with a value", name, id)
			}
			if !has {
				continue
			}
			var sl struct {
				Gauge, Line1, As string
				Line2            []string
			}
			var raw map[string]json.RawMessage
			if json.Unmarshal(s, &sl) != nil || json.Unmarshal(s, &raw) != nil || sl.Gauge == "" || sl.Line1 == "" || sl.As == "" ||
				sl.Line2 == nil || len(raw) != 4 || raw["line2"][0] != '[' {
				t.Errorf("%s: %s: slots %s", name, id, s)
			}
		}
	}
}

// three tabs, the second named Planka; tour is the block's JSON (or "" for none), on line 2
func tourLayout(tour string) string {
	head := "{\n"
	if tour != "" {
		head += `"tour": ` + tour + ",\n"
	}
	return head + `"tabs": [{"name":"Homelab","cards":[]},{"name":"Planka","cards":[]},{"name":"Weather","cards":[]}]}`
}

func servedTour(t *testing.T, src string) string {
	t.Helper()
	l, err := parseLayout([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(layoutDoc(l).Tour)
	return string(b)
}

func TestTourDefaults(t *testing.T) {
	want := `{"on":true,"idle_s":300,"clock_s":60,"tab_s":20,"clock":true,"skip":[]}`
	if got := servedTour(t, tourLayout("")); got != want {
		t.Errorf("no block: %s", got)
	}
	if got := servedTour(t, tourLayout("{}")); got != want {
		t.Errorf("empty block: %s", got)
	}
}

func TestTourParsed(t *testing.T) {
	got := servedTour(t, tourLayout(`{"idle_min":2,"tab_s":30,"skip":["Planka"]}`))
	if want := `{"on":true,"idle_s":120,"clock_s":60,"tab_s":30,"clock":true,"skip":[1]}`; got != want {
		t.Errorf("got %s", got)
	}
}

func TestTourErrors(t *testing.T) {
	tests := []struct{ tour, want string }{
		{`{"idle_min":0}`, "tour idle_min 0: 1 to 120"},
		{`{"idle_min":121}`, "tour idle_min 121: 1 to 120"},
		{`{"clock_s":4}`, "tour clock_s 4: 5 to 600"},
		{`{"tab_s":601}`, "tour tab_s 601: 5 to 600"},
		{`{"skip":["Nope"]}`, "tour skips no tab 'Nope'"},
		{`{"skip":["Planka","Planka"]}`, "tour skips 'Planka' twice"},
		{`{"clock":false,"skip":["Homelab","Planka","Weather"]}`, "tour has nothing to show"},
	}
	for _, tc := range tests {
		_, err := parseLayout([]byte(tourLayout(tc.tour)))
		var le *layoutError
		if !errors.As(err, &le) || le.Line != 2 || le.Msg != tc.want {
			t.Errorf("%s: %v", tc.tour, err)
		}
	}
	// off, or the clock on, is fine
	for _, ok := range []string{`{"on":false,"clock":false,"skip":["Homelab","Planka","Weather"]}`, `{"skip":["Homelab","Planka","Weather"]}`} {
		if _, err := parseLayout([]byte(tourLayout(ok))); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
}

func TestTourRoundTrip(t *testing.T) {
	for _, tour := range []string{"", `{"on":false,"idle_min":2,"clock_s":30,"tab_s":15,"clock":false,"skip":["Planka"]}`} {
		l, err := parseLayout([]byte(tourLayout(tour)))
		if err != nil {
			t.Fatal(err)
		}
		got, err := parseLayout(handLayout(l))
		if err != nil || !reflect.DeepEqual(got, l) {
			t.Errorf("tour %q: %v\n%s", tour, err, handLayout(l))
		}
	}
}

// hugeLayout is the most the limits allow: 6 tabs of 24 number 1x1 cards with 23-byte ids and 27-byte titles.
func hugeLayout() []byte {
	var tabs []string
	for ti := range maxTabs {
		var cs []string
		for i := range maxCardsPerTab {
			cs = append(cs, fmt.Sprintf(`{"id":"%023d","type":"number","at":[%d,%d],"size":"1x1","title":"%027d","value":"cpu"}`,
				ti*maxCardsPerTab+i, i%4+1, i/4+1, i))
		}
		tabs = append(tabs, fmt.Sprintf(`{"name":"t%d","cards":[%s]}`, ti, strings.Join(cs, ",")))
	}
	return []byte(`{"tabs":[` + strings.Join(tabs, ",") + `]}`)
}

// panelFit is a layout whose served document is exactly n bytes (23-byte ids, titles grown a byte at a time).
func panelFit(t *testing.T, n int) []byte {
	l := Layout{}
	for ti := range maxTabs {
		l.Tabs = append(l.Tabs, Tab{Name: fmt.Sprintf("t%d", ti), Cards: []Card{}})
	}
	for k := 0; len(encodeLayoutDoc(l)) <= n; k++ {
		i, tab := k%maxCardsPerTab, &l.Tabs[k/maxCardsPerTab]
		tab.Cards = append(tab.Cards, Card{ID: fmt.Sprintf("%023d", k), Type: "number", At: [2]int{i%4 + 1, i/4 + 1}, Size: "1x1",
			Title: "a", Value: json.RawMessage(`"cpu"`)})
		if len(encodeLayoutDoc(l)) > n {
			tab.Cards = tab.Cards[:len(tab.Cards)-1]
			break
		}
	}
	gap := n - len(encodeLayoutDoc(l))
	for ti := range l.Tabs {
		for j := range l.Tabs[ti].Cards {
			c := &l.Tabs[ti].Cards[j]
			k := min(gap, maxTitle-len(c.Title))
			c.Title, gap = c.Title+strings.Repeat("a", k), gap-k
		}
	}
	if got := len(encodeLayoutDoc(l)); got != n {
		t.Fatalf("panelFit(%d) made %d bytes", n, got)
	}
	return handLayout(l)
}

// A layout the panel couldn't read is refused: the firmware keeps a document only while it is shorter than its
// buffer (one byte stays free for the terminator).
func TestLayoutTooBigForPanel(t *testing.T) {
	l, err := parseLayout(hugeLayout())
	var le *layoutError
	want := fmt.Sprintf("too big for the panel (%d bytes, at most %d): use fewer cards", len(encodeLayoutDoc(l)), maxLayoutDoc-1)
	if !errors.As(err, &le) || le.Line != 1 || le.Msg != want {
		t.Errorf("huge: %v, want %q", err, want)
	}
	if _, err := parseLayout(panelFit(t, maxLayoutDoc-1)); err != nil {
		t.Errorf("%d bytes: %v", maxLayoutDoc-1, err)
	}
	if _, err := parseLayout(panelFit(t, maxLayoutDoc)); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("(%d bytes, at most %d)", maxLayoutDoc, maxLayoutDoc-1)) {
		t.Errorf("%d bytes: %v", maxLayoutDoc, err)
	}
}

// The largest layouts in use fit: the example and every demo layout, the editor's too.
func TestRealLayoutsFitThePanel(t *testing.T) {
	ex, err := os.ReadFile("layout.example.json")
	if err != nil {
		t.Fatal(err)
	}
	ls := map[string][]byte{"layout.example.json": ex, "editor demo": handLayout(editorDemoLayout(found{GPUs: []string{"workstation", "server"},
		Pools: []string{"tank", "backup", "boot-pool"}, TrueNAS: true}))}
	for n, l := range demoLayouts() {
		ls[n] = handLayout(l)
	}
	for n, b := range ls {
		l, err := parseLayout(b)
		if err != nil {
			t.Errorf("%s: %v", n, err)
		}
		t.Logf("%s: %d bytes", n, len(encodeLayoutDoc(l)))
	}
}

// Go reads "Tour" as the tour block, so its errors point at it.
func TestTourLineAnyCase(t *testing.T) {
	_, err := parseLayout([]byte("{\"tabs\":[{\"name\":\"A\",\"cards\":[]}],\n\n\"Tour\":{\"idle_min\":0}}"))
	var le *layoutError
	if !errors.As(err, &le) || le.Line != 3 || !strings.Contains(le.Msg, "idle_min 0") {
		t.Errorf("%v, want line 3", err)
	}
}
