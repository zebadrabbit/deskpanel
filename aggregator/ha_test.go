package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func haKey(e, a string) string { return jsonSpec{HA: e, Attr: a}.key() }

func haVal(v string) string { return `"value":{"ha":"` + v + `"}` }

func TestParseValueHA(t *testing.T) {
	l := mustLayout(t, `{"name":"t","cards":[{"id":"x","at":[1,1],"type":"number","size":"1x1",`+haVal("sensor.living_room_temperature")+`}]}`)
	got := l.Tabs[0].Cards[0].specs()[0].JSON
	if got == nil || *got != (jsonSpec{HA: "sensor.living_room_temperature", Every: 15}) {
		t.Fatalf("got %+v", got)
	}
	if k := got.key(); k != "ha:sensor.living_room_temperature\x00" {
		t.Errorf("key %q", k)
	}
	l = mustLayout(t, `{"name":"t","cards":[{"id":"x","at":[1,1],"type":"number","size":"1x1","value":{"ha":"light.kitchen","attr":"brightness","every":60}}]}`)
	got = l.Tabs[0].Cards[0].specs()[0].JSON
	if got == nil || *got != (jsonSpec{HA: "light.kitchen", Attr: "brightness", Every: 60}) {
		t.Fatalf("got %+v", got)
	}
	card := func(id string, col, every int, attr string) string {
		return fmt.Sprintf(`{"id":"%s","at":[%d,1],"type":"number","size":"1x1","value":{"ha":"sensor.x","every":%d%s}}`, id, col, every, attr)
	}
	l = mustLayout(t, `{"name":"t","cards":[`+card("a", 1, 60, "")+`,`+card("b", 2, 20, "")+`]}`)
	if _, _, js := queriesFor(l); len(js) != 1 || js[0].Every != 20 {
		t.Errorf("shared: %+v", js)
	}
	l = mustLayout(t, `{"name":"t","cards":[`+card("a", 1, 60, "")+`,`+card("b", 2, 20, "")+`,`+card("c", 3, 30, `,"attr":"a"`)+`]}`)
	if _, _, js := queriesFor(l); len(js) != 2 {
		t.Errorf("attr apart: %+v", js)
	}
}

func TestParseValueHAErrors(t *testing.T) {
	card := func(j string) string {
		return `{"tabs":[{"name":"A","cards":[` + "\n" + `{"id":"x","type":"number","at":[1,1],"size":"1x1","value":` + j + `}]}]}`
	}
	ent := "'x' value: ha needs an entity id"
	tests := []struct{ j, want string }{
		{`{"ha":""}`, ent},
		{`{"ha":"Sensor.X"}`, ent},
		{`{"ha":"sensor.x/../../api/config"}`, ent},
		{`{"ha":"sensor.x?a=b"}`, ent},
		{`{"ha":"sensor"}`, ent},
		{`{"ha":"` + strings.Repeat("a", 256) + `.x"}`, ent},
		{`{"ha":"sensor.x","every":5}`, "'x' value: ha every"},
		{`{"ha":"sensor.x","attr":"` + strings.Repeat("a", 65) + `"}`, "'x' value: ha attr over 64"},
		{`{"ha":"sensor.x","url":"http://other.lan"}`, "'x' value: ha takes"},
		{`{"ha":"sensor.x","json":{}}`, "'x' value: ha takes"},
	}
	for _, tc := range tests {
		_, err := parseLayout([]byte(card(tc.j)))
		var le *layoutError
		if !errors.As(err, &le) {
			t.Fatalf("%s: want *layoutError, got %v", tc.j, err)
		}
		if le.Line != 2 || !strings.HasPrefix(le.Msg, tc.want) || len(le.Error()) > 88 {
			t.Errorf("%.30s: line %d %q (%d bytes)", tc.j, le.Line, le.Msg, len(le.Error()))
		}
	}
}

func TestHAValue(t *testing.T) {
	for _, tc := range []struct {
		in   any
		want promVal
	}{
		{"21.5", promVal{V: 21.5}}, {" 7 ", promVal{V: 7}}, {"on", promVal{S: "on"}},
		{"NaN", promVal{S: "NaN"}}, {"1e400", promVal{S: "1e400"}},
		{true, promVal{V: 1}}, {false, promVal{}}, {180.0, promVal{V: 180}},
	} {
		if v, ok := haValue(tc.in); !ok || v != tc.want {
			t.Errorf("%#v: %+v %v", tc.in, v, ok)
		}
	}
	for _, in := range []any{nil, "", map[string]any{}, []any{}} {
		if _, ok := haValue(in); ok {
			t.Errorf("%#v is a value", in)
		}
	}
}

const (
	haTemp = `{"entity_id":"sensor.living_room_temperature","state":"21.5","attributes":{"unit_of_measurement":"°C"}}`
	haDoor = `{"entity_id":"binary_sensor.front_door","state":"off","attributes":{}}`
	haKit  = `{"entity_id":"light.kitchen","state":"on","attributes":{"brightness":180,"friendly_name":"Kitchen","unit_of_measurement":"lx"}}`
)

func TestHAFetch(t *testing.T) {
	var hits atomic.Int32
	var ua atomic.Value
	var slow atomic.Bool
	states := map[string]string{"sensor.living_room_temperature": haTemp, "binary_sensor.front_door": haDoor, "light.kitchen": haKit,
		"light.off": `{"entity_id":"light.off","state":"off","attributes":{"brightness":null}}`}
	var other atomic.Int32
	var otherAuth atomic.Value
	otherSrv := jsrv(t, func(w http.ResponseWriter, r *http.Request) {
		other.Add(1)
		otherAuth.Store(r.Header.Get("Authorization"))
		fmt.Fprint(w, haTemp)
	})
	base := jsrv(t, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		ua.Store(r.Header.Get("User-Agent"))
		if r.Header.Get("Authorization") != "Bearer t0k" {
			w.WriteHeader(401)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/api/states/")
		if id == "sensor.moved" {
			http.Redirect(w, r, otherSrv+"/api/states/"+id, http.StatusFound)
			return
		}
		if slow.Load() {
			w.WriteHeader(500)
			return
		}
		if b, ok := states[id]; ok {
			fmt.Fprint(w, b)
			return
		}
		w.WriteHeader(404)
		fmt.Fprint(w, `{"message":"Entity not found."}`)
	})
	env := map[string]string{"HA_URL": base + "/", "HA_TOKEN": "t0k"}
	s, c := newJ(env)
	sp := func(e, a string) jsonSpec { return jsonSpec{HA: e, Attr: a, Every: 10} }
	specs := []jsonSpec{sp("sensor.living_room_temperature", ""), sp("binary_sensor.front_door", ""),
		sp("light.kitchen", "brightness"), sp("light.kitchen", "friendly_name"), sp("light.kitchen", "color"),
		sp("sensor.missing", ""), sp("sensor.moved", ""), sp("light.off", "brightness")}
	s.want(specs)
	s.one() // the token is unproven: one request goes, the rest wait for the next cycle
	s.one()
	want := func(e, a string, w promVal) {
		t.Helper()
		if v := s.value(haKey(e, a)); v != w {
			t.Errorf("%s/%s: %+v want %+v", e, a, v, w)
		}
	}
	want("sensor.living_room_temperature", "", promVal{V: 21.5, U: "°C", St: stOK})
	want("binary_sensor.front_door", "", promVal{S: "off", St: stOK})
	want("light.kitchen", "brightness", promVal{V: 180, St: stOK})
	want("light.kitchen", "friendly_name", promVal{S: "Kitchen", St: stOK})
	want("light.kitchen", "color", promVal{St: stNone, Why: "no attr color"})
	want("light.off", "brightness", promVal{St: stNone, Why: "off"}) // present but null: the state; absent (color): no attr
	want("sensor.missing", "", promVal{St: stNone, Why: "no entity"})
	want("sensor.moved", "", promVal{St: stNone, Why: "unreachable"})
	if ua.Load() != "deskpanel" {
		t.Errorf("user agent %v", ua.Load())
	}
	if other.Load() != 0 || otherAuth.Load() != nil {
		t.Errorf("the redirect target was reached (%d hits, auth %v)", other.Load(), otherAuth.Load())
	}

	// stale: a good value, then the server fails
	slow.Store(true)
	c.add(11 * time.Second)
	s.one()
	if v := s.value(haKey("sensor.living_room_temperature", "")); v.St != stStale || v.V != 21.5 || v.U != "°C" {
		t.Errorf("stale: %+v", v)
	}
	slow.Store(false)

	// latch: a wrong token is tried once, and not again until it changes
	s2, c2 := newJ(map[string]string{"HA_URL": base, "HA_TOKEN": "wrong"})
	k := haKey("sensor.living_room_temperature", "")
	many := []jsonSpec{sp("sensor.living_room_temperature", "")}
	for i := 0; i < 7; i++ {
		many = append(many, sp("light.kitchen", fmt.Sprint("a", i))) // 8 keys: more than the pool of 4
	}
	s2.want(many)
	h0 := hits.Load()
	s2.one()
	s2.one() // the keys that skipped the first round see the latch
	if v := s2.value(k); v.St != stNone || v.Why != "bad token" || hits.Load() != h0+1 {
		t.Fatalf("1st: %+v hits %d", v, hits.Load()-h0)
	}
	c2.add(16 * time.Second)
	s2.one()
	if v := s2.value(k); v.Why != "bad token" || hits.Load() != h0+1 {
		t.Fatalf("2nd: %+v hits %d", v, hits.Load()-h0)
	}
	env2 := map[string]string{"HA_URL": base, "HA_TOKEN": "t0k"}
	s2.getenv = func(k string) string { return env2[k] }
	c2.add(16 * time.Second)
	s2.one()
	s2.one()
	if v := s2.value(k); v.St != stOK || v.V != 21.5 {
		t.Fatalf("3rd: %+v", v)
	}

	// unset: nothing is sent
	h0 = hits.Load()
	for _, tc := range []struct {
		env map[string]string
		why string
	}{
		{map[string]string{"HA_URL": "", "HA_TOKEN": "t0k"}, "set HA_URL"},
		{map[string]string{"HA_URL": "ftp://ha.lan", "HA_TOKEN": "t0k"}, "set HA_URL"},
		{map[string]string{"HA_URL": base, "HA_TOKEN": ""}, "set HA_TOKEN"},
	} {
		s3, _ := newJ(tc.env)
		s3.want([]jsonSpec{sp("sensor.living_room_temperature", "")})
		s3.one()
		if v := s3.value(k); v.St != stNone || v.Why != tc.why {
			t.Errorf("%v: %+v", tc.env, v)
		}
	}
	if hits.Load() != h0 {
		t.Errorf("unset config made %d requests", hits.Load()-h0)
	}
}

// A Home Assistant that never answers (token unproven) must not hold the JSON cards' pool slots.
func TestHAStuckDoesNotDelayJSON(t *testing.T) {
	release := make(chan struct{})
	ha := jsrv(t, func(w http.ResponseWriter, r *http.Request) { <-release })
	t.Cleanup(func() { close(release) }) // runs before the server's Close
	js := jsrv(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"c":4}`) })
	s, _ := newJ(map[string]string{"HA_URL": ha, "HA_TOKEN": "t0k"})
	s.timeout = 3 * time.Second
	specs := []jsonSpec{{URL: js, Path: "c", Every: 10}}
	for i := 0; i < 8; i++ {
		specs = append(specs, jsonSpec{HA: "light.kitchen", Attr: fmt.Sprint("a", i), Every: 10})
	}
	s.want(specs)
	s.cycle(context.Background())
	k := specs[0].key()
	for end := time.Now().Add(time.Second); s.value(k).St != stOK; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("JSON card waited behind HA: %+v", s.value(k))
		}
	}
}

func TestCardsDocHA(t *testing.T) {
	m := newMap()
	k := haKey("sensor.x", "")
	set := func(v promVal) {
		if v.St == "" {
			v.St = stOK
		}
		m.vals[k] = v
	}
	card := func(typ, size, extra string) string {
		return one(t, `"type":"`+typ+`","size":"`+size+`",`+haVal("sensor.x")+extra, readers{json: m}, nil)
	}
	set(promVal{V: 1234, U: "W"})
	eq(t, card("number", "1x1", ""), `{"v":"1234 W"}`)
	eq(t, card("number", "1x1", `,"unit":"watts"`), `{"v":"1234 watts"}`)
	set(promVal{V: 48, U: "%"})
	eq(t, card("gauge", "1x2", ""), `{"v":"48 %","f":0.48}`)
	set(promVal{V: 50, U: "%"})
	m.sers[k] = promSeries{P: []float64{50}, St: stOK}
	eq(t, card("graph", "2x2", ""), `{"v":"50 %","g":["g"]}`)
	m.vals[haKey("light.kitchen", "brightness")] = promVal{V: 180, St: stOK}
	eq(t, one(t, `"type":"bar","size":"1x1","value":{"ha":"light.kitchen","attr":"brightness"},"min":0,"max":255`, readers{json: m}, nil), `{"v":"180","f":0.706}`)
	for _, w := range []string{"on", "home", "open", "playing"} {
		set(promVal{S: w})
		eq(t, card("status", "1x1", ""), `{"v":"up"}`)
	}
	for _, w := range []string{"off", "unavailable"} {
		set(promVal{S: w})
		eq(t, card("status", "1x1", ""), `{"st":"bad","v":"`+w+`"}`)
	}
	set(promVal{S: "on"})
	eq(t, card("gauge", "1x2", ""), `{"st":"none","sub":"not a number"}`)
	set(promVal{St: stNone, Why: "no attr brightness"})
	eq(t, card("number", "1x1", ""), `{"st":"none","sub":"no attr brightness"}`)
}

func TestHAForbiddenRedirectEntity(t *testing.T) {
	var hits atomic.Int32
	base := jsrv(t, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch {
		case r.Header.Get("Authorization") == "Bearer banned":
			w.WriteHeader(403)
		case strings.HasSuffix(r.URL.Path, "sensor.same"): // even a same-host redirect is refused
			http.Redirect(w, r, "/api/states/sensor.x", http.StatusFound)
		default:
			fmt.Fprint(w, haTemp)
		}
	})
	sp := func(e string) jsonSpec { return jsonSpec{HA: e, Every: 10} }
	// 403 latches like 401
	s, c := newJ(map[string]string{"HA_URL": base, "HA_TOKEN": "banned"})
	s.want([]jsonSpec{sp("sensor.x")})
	s.one()
	c.add(16 * time.Second)
	s.one()
	if v := s.value(haKey("sensor.x", "")); v.Why != "forbidden" || hits.Load() != 1 {
		t.Errorf("403: %+v hits %d", v, hits.Load())
	}
	// same-host redirect
	hits.Store(0)
	s, _ = newJ(map[string]string{"HA_URL": base, "HA_TOKEN": "t0k"})
	s.want([]jsonSpec{sp("sensor.same")})
	s.one()
	if v := s.value(haKey("sensor.same", "")); v.Why != "unreachable" || hits.Load() != 1 {
		t.Errorf("redirect: %+v hits %d", v, hits.Load())
	}
	// an entity that never matched the pattern is not put in a URL
	hits.Store(0)
	if _, why, _ := s.haFetch(context.Background(), jsonSpec{HA: "x/../../api/config"}); why != "bad entity" || hits.Load() != 0 {
		t.Errorf("entity: %q hits %d", why, hits.Load())
	}
}

func TestHAHistorySurvivesUnavailable(t *testing.T) {
	state := "21.5"
	base := jsrv(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"state":%q,"attributes":{}}`, state)
	})
	s, c := newJ(map[string]string{"HA_URL": base, "HA_TOKEN": "t0k"})
	s.want([]jsonSpec{{HA: "sensor.x", Every: 10}})
	for _, st := range []string{"21.5", "22.5", "unavailable", "23.5"} {
		state = st
		c.add(61 * time.Second)
		s.one()
	}
	if p := s.series(haKey("sensor.x", "")).P; len(p) != 3 || p[0] != 21.5 {
		t.Errorf("points %v", p)
	}
}

func TestParseValuePromRefusesSourceKeys(t *testing.T) {
	for _, q := range []string{"ha:power:sum", "json:x"} {
		_, err := parseLayout([]byte(`{"tabs":[{"name":"A","cards":[` + "\n" + `{"id":"x","type":"number","at":[1,1],"size":"1x1","value":{"prometheus":"` + q + `"}}]}]}`))
		var le *layoutError
		if !errors.As(err, &le) || le.Line != 2 || !strings.Contains(le.Msg, "can't start with ha: or json:") || len(le.Error()) > 88 {
			t.Errorf("%s: %v", q, err)
		}
	}
}

func TestCardsDocHAUnavailable(t *testing.T) {
	m := newMap()
	m.vals[haKey("sensor.x", "")] = promVal{S: "unavailable", St: stOK}
	eq(t, one(t, `"type":"gauge","size":"1x2",`+haVal("sensor.x"), readers{json: m}, nil), `{"st":"none","sub":"unavailable"}`)
}
