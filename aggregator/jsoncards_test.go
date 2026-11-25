package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const jsURL = "http://app.lan/status"

func jsKey(path string) string { return jsonSpec{URL: jsURL, Path: path}.key() }

func jsVal(path string) string {
	return fmt.Sprintf(`"value":{"json":{"url":%q,"path":%q}}`, jsURL, path)
}

func fakeEnv(t *testing.T, env map[string]string) {
	old := lookupEnv
	lookupEnv = func(k string) (string, bool) { v, ok := env[k]; return v, ok }
	t.Cleanup(func() { lookupEnv = old })
}

func TestParseValueJSON(t *testing.T) {
	fakeEnv(t, map[string]string{"CARD_APP_AUTH": "Authorization: Bearer abc"})
	l := mustLayout(t, `{"name":"t","cards":[{"id":"x","at":[1,1],"type":"gauge","size":"1x2","min":0,"max":50,"value":{"json":{"url":"https://app.lan/s","path":"a.0.b","every":60,"header_env":"CARD_APP_AUTH"}}}]}`)
	got := l.Tabs[0].Cards[0].specs()[0].JSON
	want := jsonSpec{URL: "https://app.lan/s", Path: "a.0.b", Every: 60, HeaderEnv: "CARD_APP_AUTH"}
	if got == nil || *got != want {
		t.Fatalf("got %+v", got)
	}
	l = mustLayout(t, `{"name":"t","cards":[{"id":"x","at":[1,1],"type":"number","size":"1x1",`+jsVal("n")+`}]}`)
	if g := l.Tabs[0].Cards[0].specs()[0].JSON; g.Every != 30 {
		t.Errorf("default every %d", g.Every)
	}
	_, _, js := queriesFor(l)
	if len(js) != 1 || js[0].key() != jsKey("n") {
		t.Errorf("queriesFor json: %v", js)
	}
	// C1: cards sharing a source poll it as often as the keenest wants, whatever their order
	src := func(id string, col, every int) string {
		return fmt.Sprintf(`{"id":"%s","at":[%d,1],"type":"number","size":"1x1","value":{"json":{"url":"https://app.lan/s","path":"a","every":%d}}}`, id, col, every)
	}
	for _, order := range [][2]int{{60, 15}, {15, 60}} {
		l = mustLayout(t, `{"name":"t","cards":[`+src("a", 1, order[0])+`,`+src("b", 2, order[1])+`,`+src("c", 3, 30)+`]}`)
		if _, _, js = queriesFor(l); len(js) != 1 || js[0].Every != 15 {
			t.Errorf("every %v: %+v", order, js)
		}
	}
}

func TestParseValueJSONErrors(t *testing.T) {
	fakeEnv(t, map[string]string{"PANEL_TOKEN": "secret", "CARD_OK": "X-Key: 1"})
	card := func(j string) string {
		return `{"tabs":[{"name":"A","cards":[` + "\n" + `{"id":"x","type":"number","at":[1,1],"size":"1x1","value":{"json":` + j + `}}]}]}`
	}
	tests := []struct{ name, j, want string }{
		{"ftp", `{"url":"ftp://app.lan/s","path":"a"}`, "'x' value: json url must be http"},
		{"empty path", `{"url":"` + jsURL + `","path":""}`, "'x' value: json path"},
		{"every 5", `{"url":"` + jsURL + `","path":"a","every":5}`, "'x' value: json every"},
		{"every 4000", `{"url":"` + jsURL + `","path":"a","every":4000}`, "'x' value: json every"},
		{"panel token", `{"url":"` + jsURL + `","path":"a","header_env":"PANEL_TOKEN"}`, "'x' header_env must be CARD_"},
		{"lower case", `{"url":"` + jsURL + `","path":"a","header_env":"CARD_ok"}`, "'x' header_env must be CARD_"},
		{"unset", `{"url":"` + jsURL + `","path":"a","header_env":"CARD_X"}`, "'x' header_env unset: CARD_X"},
		{"unknown key", `{"url":"` + jsURL + `","path":"a","method":"POST"}`, "'x' value: json takes"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseLayout([]byte(card(tc.j)))
			var le *layoutError
			if !errors.As(err, &le) {
				t.Fatalf("want *layoutError, got %v", err)
			}
			if le.Line != 2 || !strings.HasPrefix(le.Msg, tc.want) || len(le.Error()) > 88 {
				t.Errorf("line %d %q (%d bytes)", le.Line, le.Msg, len(le.Error()))
			}
		})
	}
	// header_env CARD_OK is fine; 64 distinct sources are fine, 65 are not
	if _, err := parseLayout([]byte(card(`{"url":"` + jsURL + `","path":"a","header_env":"CARD_OK"}`))); err != nil {
		t.Fatal(err)
	}
	tabs := func(n int) string {
		var tb []string
		for i := 0; i < n; i++ {
			if i%22 == 0 {
				tb = append(tb, fmt.Sprintf(`{"name":"t%d","cards":[`, i/22))
			} else {
				tb[len(tb)-1] += ","
			}
			tb[len(tb)-1] += fmt.Sprintf(`{"id":"c%d","type":"number","at":[%d,%d],"size":"1x1",%s}`, i, i%4+1, i%22/4+1, jsVal(fmt.Sprint("p", i)))
		}
		for i := range tb {
			tb[i] += "]}"
		}
		return `{"tabs":[` + strings.Join(tb, ",") + `]}`
	}
	if _, err := parseLayout([]byte(tabs(64))); err != nil {
		t.Fatalf("64: %v", err)
	}
	_, err := parseLayout([]byte(tabs(65)))
	var le *layoutError
	if !errors.As(err, &le) || !strings.Contains(le.Msg, "over 64 JSON sources") || len(le.Error()) > 88 {
		t.Errorf("65: %v", err)
	}
}

func TestCardsDocJSON(t *testing.T) {
	m := newMap()
	set := func(path string, v promVal) {
		v.St = stOK
		m.vals[jsKey(path)] = v
	}
	set("n", promVal{V: 7.5})
	set("s", promVal{S: "a very long status string"})
	set("run", promVal{S: "Running"})
	set("deg", promVal{S: "degraded"})
	set("seven", promVal{V: 7})
	set("nine", promVal{S: "9"})
	r := readers{json: m}
	card := func(typ, path, extra string) string {
		return one(t, `"type":"`+typ+`","size":"1x1",`+jsVal(path)+extra, r, nil)
	}
	eq(t, card("number", "n", `,"unit":"ms"`), `{"v":"7.50 ms"}`)
	eq(t, card("number", "s", ""), `{"v":"a very long sta"}`)
	eq(t, card("number", "nine", `,"warn":5`), `{"v":"9"}`) // a string never trips thresholds
	eq(t, card("number", "n", `,"warn":5`), `{"st":"warn","v":"7.50"}`)
	eq(t, card("status", "run", ""), `{"v":"up"}`)
	eq(t, card("status", "deg", ""), `{"st":"bad","v":"degraded"}`)
	eq(t, card("status", "seven", ""), `{"v":"up"}`)
	for _, typ := range []string{"gauge", "bar", "graph"} {
		sz := map[string]string{"gauge": "1x2", "bar": "1x1", "graph": "2x2"}[typ]
		got := one(t, `"type":"`+typ+`","size":"`+sz+`",`+jsVal("run"), r, nil)
		eq(t, got, `{"st":"none","sub":"not a number"}`)
	}
	// min/max are allowed on a JSON gauge, and scale it
	eq(t, one(t, `"type":"gauge","size":"1x2",`+jsVal("n")+`,"min":0,"max":10`, r, nil), `{"v":"7.50","f":0.75}`)
	// a graph with a JSON series
	m.sers[jsKey("n")] = promSeries{P: ramp(5), St: stOK}
	got := one(t, `"type":"graph","size":"2x2",`+jsVal("n"), r, nil)
	if !strings.Contains(got, `"g":["`) || !strings.Contains(got, `"v":"7.50"`) {
		t.Errorf("graph: %s", got)
	}
}

func TestReadersRoute(t *testing.T) {
	m := newMap()
	m.vals[jsKey("n")] = promVal{V: 3, St: stOK}
	m.vals[haKey("sensor.x", "")] = promVal{V: 5, St: stOK}
	l := mustLayout(t, `{"name":"t","cards":[
		{"id":"p","at":[1,1],"type":"number","size":"1x1","value":"cpu"},
		{"id":"h","at":[3,1],"type":"number","size":"1x1",`+haVal("sensor.x")+`},
		{"id":"j","at":[2,1],"type":"number","size":"1x1",`+jsVal("n")+`}]}`)
	got := string(cardsDoc(l, readers{json: m}, nil))
	if !strings.Contains(got, `"p":{"st":"none","sub":"set PROMETHEUS_URL"}`) || !strings.Contains(got, `"j":{"v":"3.00"}`) || !strings.Contains(got, `"h":{"v":"5.00"}`) {
		t.Errorf("%s", got)
	}
}

func TestCardsRouteFastJSON(t *testing.T) {
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

	l := mustLayout(t, `{"name":"t","cards":[{"id":"x","at":[1,1],"type":"number","size":"1x1","value":{"json":{"url":"`+srv.URL+`","path":"a"}}}]}`)
	js := newJSONSrc(func(string) string { return "" })
	_, _, specs := queriesFor(l)
	js.want(specs)
	go js.run(ctx)
	time.Sleep(50 * time.Millisecond) // the fetch is now in flight, hung

	start := time.Now()
	got := string(cardsDoc(l, readers{json: js}, nil))
	if time.Since(start) > 200*time.Millisecond || !strings.Contains(got, `"st":"none"`) {
		t.Errorf("%v: %s", time.Since(start), got)
	}
}
