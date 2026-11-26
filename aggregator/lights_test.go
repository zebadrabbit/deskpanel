package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeLight is one entity of the fake Home Assistant. It keeps its brightness and colour while off, and reports them
// only while on, as Home Assistant does.
type fakeLight struct {
	state  string // on, off, unavailable
	bri    int
	modes  []string
	mode   string
	kelvin int
	hs     []float64
	rgb    []int
}

func (l *fakeLight) json(entity string) []byte {
	a := map[string]any{"friendly_name": "x"}
	if strings.HasPrefix(entity, "light.") {
		a["supported_color_modes"], a["brightness"], a["color_mode"] = l.modes, nil, nil
		if l.state == "on" {
			a["color_mode"] = l.mode
			if l.bri > 0 {
				a["brightness"] = l.bri
			}
			if l.kelvin > 0 {
				a["color_temp_kelvin"] = l.kelvin
			}
			if l.hs != nil {
				a["hs_color"], a["rgb_color"] = l.hs, l.rgb
			}
		}
	}
	b, _ := json.Marshal(map[string]any{"entity_id": entity, "state": l.state, "attributes": a})
	return b
}

// fakeHA is a Home Assistant that knows /api/states/<entity> and the light and switch services, for token "tok".
type fakeHA struct {
	url      string
	mu       sync.Mutex
	l        map[string]*fakeLight
	posts    []string       // "light/turn_on <body>" of every service call, in order
	gets     int            // state reads
	getCode  int            // not 0: every state read answers it
	stallAt  int            // not 0: state reads from this one (counting gets) on never answer
	postCode map[string]int // entity ("": any) -> the status its service calls answer
}

func newFakeHA(t *testing.T) *fakeHA {
	f := &fakeHA{postCode: map[string]int{}, l: map[string]*fakeLight{
		"light.desk":  {state: "on", bri: 38, modes: []string{"color_temp", "hs"}, mode: "color_temp", kelvin: 2700, hs: []float64{28.4, 65.9}, rgb: []int{255, 169, 87}},
		"light.shelf": {state: "off", bri: 153, modes: []string{"brightness"}, mode: "brightness"},
		"light.plain": {state: "on", modes: []string{"onoff"}, mode: "onoff"},
		"switch.fan":  {state: "off"},
	}}
	f.url = jsrv(t, f.serve)
	return f
}

func (f *fakeHA) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer tok" {
		w.WriteHeader(401)
		return
	}
	if e, ok := strings.CutPrefix(r.URL.Path, "/api/states/"); ok && r.Method == "GET" {
		f.gets++
		if f.stallAt != 0 && f.gets >= f.stallAt {
			f.mu.Unlock()
			<-r.Context().Done() // until the client gives up
			f.mu.Lock()
			return
		}
		switch l := f.l[e]; {
		case f.getCode != 0:
			w.WriteHeader(f.getCode)
		case l == nil:
			w.WriteHeader(404)
		default:
			w.Write(l.json(e))
		}
		return
	}
	svc, ok := strings.CutPrefix(r.URL.Path, "/api/services/")
	if !ok || r.Method != "POST" || r.Header.Get("Content-Type") != "application/json" {
		w.WriteHeader(400)
		return
	}
	body, _ := io.ReadAll(r.Body)
	f.posts = append(f.posts, svc+" "+string(body))
	var in struct {
		Entity string    `json:"entity_id"`
		BriPct int       `json:"brightness_pct"`
		Bri    int       `json:"brightness"`
		Kelvin int       `json:"color_temp_kelvin"`
		HS     []float64 `json:"hs_color"`
	}
	json.Unmarshal(body, &in)
	if c := cmp.Or(f.postCode[in.Entity], f.postCode[""]); c != 0 {
		w.WriteHeader(c)
		return
	}
	l := f.l[in.Entity]
	if l == nil || path.Dir(svc)+"." != in.Entity[:strings.IndexByte(in.Entity, '.')+1] {
		w.WriteHeader(400)
		return
	}
	switch path.Base(svc) {
	case "toggle":
		l.state = map[string]string{"on": "off", "off": "on"}[l.state]
	case "turn_off":
		l.state = "off"
	case "turn_on":
		l.state = "on"
		if in.BriPct > 0 {
			l.bri = int(math.Round(float64(in.BriPct) * 2.55))
		}
		if in.Bri > 0 {
			l.bri = in.Bri
		}
		if in.Kelvin > 0 {
			l.mode, l.kelvin, l.rgb = "color_temp", in.Kelvin, []int{255, 169, 87}
		}
		if in.HS != nil {
			l.mode, l.hs, l.rgb = "hs", in.HS, []int{0, 0, 255}
		}
	default:
		w.WriteHeader(400)
		return
	}
	w.Write([]byte("[]"))
}

func (f *fakeHA) set(do func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	do()
}

// sent is the service calls since the last look.
func (f *fakeHA) sent() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.posts
	f.posts = nil
	return out
}

func (f *fakeHA) reads() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gets
}

// ltEnv is the lights against a Home Assistant (ha, nil when env names none), with the routes on mux for "ptok".
type ltEnv struct {
	t   *testing.T
	ha  *fakeHA
	src *jsonSrc
	hl  *haLights
	lt  *lights
	lay Layout
	mux *http.ServeMux
}

// ltCards: a colour light, one that only dims, a switch, an on/off light, a card that is no light, and a profile.
const ltCards = `{"id":"desk","type":"light","at":[1,1],"size":"2x1","title":"Desk","value":{"ha":"light.desk"}},
{"id":"shelf","type":"light","at":[3,1],"size":"1x1","title":"Shelf lamp","value":{"ha":"light.shelf"}},
{"id":"fan","type":"light","at":[4,1],"size":"1x1","value":{"ha":"switch.fan"}},
{"id":"plain","type":"light","at":[1,2],"size":"1x1","value":{"ha":"light.plain"}},
{"id":"cpu","type":"number","at":[2,2],"size":"1x1","value":"cpu"},
{"id":"morning","type":"profile","at":[3,2],"size":"1x1","title":"Morning"}`

func newLT(t *testing.T, cards string) *ltEnv {
	ha := newFakeHA(t)
	e := newLTEnv(t, map[string]string{"HA_URL": ha.url, "HA_TOKEN": "tok"}, cards)
	e.ha = ha
	return e
}

func newLTEnv(t *testing.T, env map[string]string, cards string) *ltEnv {
	e := &ltEnv{t: t, src: newJSONSrc(func(k string) string { return env[k] }), mux: http.NewServeMux()}
	e.hl = newHALights(e.src)
	e.lt = &lights{ctl: e.hl, prof: newProfiles(""), layout: func() Layout { return e.lay }}
	addLightRoutes(e.mux, "ptok", e.lt)
	e.setLayout(cards)
	return e
}

// setLayout serves a one-tab layout of cards and reads its lights once, as serve() does on a layout change.
func (e *ltEnv) setLayout(cards string) {
	e.t.Helper()
	e.lay = mustLayout(e.t, `{"name":"L","cards":[`+cards+`]}`)
	e.hl.want(lightEntities(e.lay))
	e.hl.pass(context.Background())
}

func (e *ltEnv) post(path string) (int, string) { return e.postCtx(context.Background(), path) }

func (e *ltEnv) postCtx(ctx context.Context, path string) (int, string) {
	r := httptest.NewRequestWithContext(ctx, "POST", path, strings.NewReader(`{"entity_id":"light.other","entity":"light.other"}`))
	r.Header.Set("Authorization", "Bearer ptok")
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, r)
	return w.Code, strings.TrimSpace(w.Body.String())
}

// want posts path and checks the status and, for an error, its text.
func (e *ltEnv) want(path string, code int, msg string) {
	e.t.Helper()
	if c, b := e.post(path); c != code || b != msg {
		e.t.Errorf("%s: %d %q, want %d %q", path, c, b, code, msg)
	}
}

// sent checks the service calls since the last look.
func (e *ltEnv) sent(want ...string) {
	e.t.Helper()
	if got := e.ha.sent(); !slices.Equal(got, want) {
		e.t.Errorf("sent\n %s\nwant\n %s", strings.Join(got, "\n "), strings.Join(want, "\n "))
	}
}

func cardEntries(t *testing.T, l Layout, lt *lights) map[string]string {
	t.Helper()
	var d struct {
		Cards map[string]json.RawMessage `json:"cards"`
	}
	if err := json.Unmarshal(cardsDocWith(l, newMap(), nil, lt), &d); err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for id, c := range d.Cards {
		out[id] = string(c)
	}
	return out
}

// card checks card id's entry in the cards document as served now (no read in between).
func (e *ltEnv) card(id, want string) {
	e.t.Helper()
	if got := cardEntries(e.t, e.lay, e.lt)[id]; got != want {
		e.t.Errorf("%s: %s, want %s", id, got, want)
	}
}

// idCard is a valid light or profile card but for its id.
func idCard(id, typ string) string {
	val := ""
	if typ == "light" {
		val = `,"value":{"ha":"light.desk"}`
	}
	return oneCard(fmt.Sprintf(`{"id":%q,"type":%q,"at":[1,1],"size":"1x1","title":"T"%s}`, id, typ, val))
}

func TestLightValidation(t *testing.T) {
	c := func(typ, size, rest string) string {
		return oneCard(fmt.Sprintf(`{"id":"x","type":%q,"at":[1,1],"size":%q%s}`, typ, size, rest))
	}
	val := `,"value":{"ha":"light.desk"}`
	takes := `'x' light takes {"ha": "light.x"} or a switch`
	for _, tc := range []struct{ name, src, want string }{
		{"no value", c("light", "1x1", ""), takes},
		{"a sensor", c("light", "1x1", `,"value":{"ha":"sensor.desk"}`), takes},
		{"attr", c("light", "1x1", `,"value":{"ha":"light.desk","attr":"brightness"}`), takes},
		{"every", c("light", "1x1", `,"value":{"ha":"light.desk","every":10}`), takes},
		{"a shortcut", c("light", "1x1", `,"value":"cpu"`), takes},
		{"prometheus", c("light", "1x1", `,"value":{"prometheus":"up"}`), takes},
		{"not an entity id", c("light", "1x1", `,"value":{"ha":"light.desk/../x"}`), takes},
		{"upper case", c("light", "1x1", `,"value":{"ha":"Light.Desk"}`), takes},
		{"no object id", c("light", "1x1", `,"value":{"ha":"light"}`), takes},
		{"unit", c("light", "1x1", val+`,"unit":"%"`), "'x' takes no unit, thresholds or min/max"},
		{"warn", c("light", "1x1", val+`,"warn":1`), "'x' takes no unit, thresholds or min/max"},
		{"max", c("light", "1x1", val+`,"max":255`), "'x' takes no unit, thresholds or min/max"},
		{"src", c("light", "1x1", val+`,"src":"a"`), "'x' takes no src"},
		{"slots", c("light", "1x1", val+`,"slots":{"gauge":"temp"}`), "'x' takes no slots"},
		{"tap", c("light", "1x1", val+`,"tap":"notifications"`), "'x' a light card takes no tap (it has its own)"},
		{"size", c("light", "2x3", val), "'x' size 2x3 not allowed for light"},
		{"profile title", c("profile", "1x1", ""), "'x' profile needs a title"},
		{"profile value", c("profile", "1x1", `,"title":"Morning"`+val), "'x' takes no value"},
		{"profile src", c("profile", "1x1", `,"title":"Morning","src":"a"`), "'x' takes no src"},
		{"profile unit", c("profile", "1x1", `,"title":"Morning","unit":"%"`), "'x' takes no unit, thresholds or min/max"},
		{"profile slots", c("profile", "1x1", `,"title":"Morning","slots":{}`), "'x' takes no slots"},
		{"profile tap", c("profile", "1x1", `,"title":"Morning","tap":"notifications"`), "'x' a profile card takes no tap (it has its own)"},
		{"profile size", c("profile", "2x2", `,"title":"Morning"`), "'x' size 2x2 not allowed for profile"},
		{"id space", idCard("my desk", "light"), "id 'my desk': letters, digits, - _ . only"},
		{"id slash", idCard("a/b", "light"), "id 'a/b': letters, digits, - _ . only"},
		{"id percent", idCard("50%", "profile"), "id '50%': letters, digits, - _ . only"},
		{"id question mark", idCard("on?", "profile"), "id 'on?': letters, digits, - _ . only"},
		{"id dots", idCard("..", "light"), "id '..': letters, digits, - _ . only"},
		{"id longest", idCard(strings.Repeat("é", 11), "light"), "id '" + strings.Repeat("é", 11) + "': letters, digits, - _ . only"},
	} {
		_, err := parseLayout([]byte(tc.src))
		var le *layoutError
		if !errors.As(err, &le) || le.Line != 2 || le.Msg != tc.want || len(le.Error()) > maxErrBytes {
			t.Errorf("%s: %v, want %q", tc.name, err, tc.want)
		}
	}
	for _, ok := range []string{
		c("light", "1x1", val), c("light", "1x2", val), c("light", "2x1", val), c("light", "2x2", val+`,"title":"Desk"`),
		c("light", "1x1", `,"value":{"ha":"switch.fan"},"tap":"none"`),
		c("profile", "1x1", `,"title":"Morning"`), c("profile", "2x1", `,"title":"Late night","tap":"none"`),
		idCard("desk-1", "light"), idCard("a.b_c", "light"), idCard("desk-1", "profile"), idCard("A.b_c", "profile"),
		oneCard(`{"id":"my gpu/1?","type":"gpu","at":[1,1],"size":"1x1"}`), // other types keep their rule
	} {
		if _, err := parseLayout([]byte(ok)); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	// the longest id still gets the whole message into a panel note
	_, err := parseLayout([]byte(oneCard(`{"id":"` + strings.Repeat("i", maxIDLen) + `","type":"light","at":[1,1],"size":"1x1"}`)))
	if err == nil || !strings.HasSuffix(err.Error(), `light takes {"ha": "light.x"} or a switch`) {
		t.Errorf("long id: %v", err)
	}
	// the served document carries nothing new: the entity stays on the server
	l := mustLayout(t, `{"name":"L","cards":[`+ltCards+`]}`)
	if doc := string(encodeLayoutDoc(l)); strings.Contains(doc, "light.desk") || strings.Contains(doc, "value") ||
		!strings.Contains(doc, `{"id":"desk","type":"light","at":[1,1],"size":[2,1],"title":"Desk","tap":"none"}`) ||
		!strings.Contains(doc, `{"id":"morning","type":"profile","at":[3,2],"size":[1,1],"title":"Morning","tap":"none"}`) {
		t.Errorf("served: %s", doc)
	}
}

// lightTabs is n light cards (ids of 23 bytes, titles of 27), 24 to a tab.
func lightTabs(n int) string {
	var tabs []string
	for i := 0; i < n; i += 24 {
		var cs []string
		for j := i; j < min(i+24, n); j++ {
			cs = append(cs, fmt.Sprintf(`{"id":"%023d","type":"light","at":[%d,%d],"size":"1x1","title":%q,"value":{"ha":"light.l%d"}}`,
				j, j%4+1, j%24/4+1, strings.Repeat("t", maxTitle), j))
		}
		tabs = append(tabs, fmt.Sprintf(`{"name":"L%d","cards":[%s]}`, i, strings.Join(cs, ",")))
	}
	return strings.Join(tabs, ",")
}

func TestLightCountLimit(t *testing.T) {
	if _, err := parseLayout([]byte(`{"tabs":[` + lightTabs(24) + `]}`)); err != nil {
		t.Errorf("24 lights: %v", err)
	}
	_, err := parseLayout([]byte(`{"tabs":[` + lightTabs(25) + `]}`))
	var le *layoutError
	if !errors.As(err, &le) || le.Msg != "'000000000000000…' at most 24 light cards" || len(le.Error()) > maxErrBytes {
		t.Errorf("25 lights: %v", err)
	}
}

// fixedLights answers every entity with one state.
type fixedLights struct{ s lightState }

func (f fixedLights) want([]string)                                        {}
func (f fixedLights) state(string) lightState                              { return f.s }
func (f fixedLights) read(context.Context, string) lightState              { return f.s }
func (f fixedLights) call(context.Context, string, string, lightCmd) error { return nil }

// 24 lights at their longest (id and title), beside four tabs of profiles and stale bars with the longest ids, fit
// the panel's layout and cards buffers. (A sixth full tab doesn't fit the layout buffer with ids this long, lights or
// not: 144 cards fit only untitled.)
func TestLightsFitThePanel(t *testing.T) {
	pr := newMap()
	tabs := []string{lightTabs(24)}
	for ti := 1; ti < 5; ti++ {
		var cs []string
		for i := 0; i < 24; i++ {
			typ, rest := "bar", fmt.Sprintf(`"value":{"prometheus":"q%d"},"unit":"abcdefgh"`, i)
			if ti == 1 && i < 4 { // a row of profile buttons
				typ, rest = "profile", `"title":"Late night"`
			}
			cs = append(cs, fmt.Sprintf(`{"id":"%023d","type":%q,"at":[%d,%d],"size":"1x1",%s}`, ti*24+i, typ, i%4+1, i/4+1, rest))
			pr.vals[fmt.Sprintf("q%d", i)] = promVal{V: 123456.789, St: stStale}
		}
		tabs = append(tabs, fmt.Sprintf(`{"name":"t%d","cards":[%s]}`, ti, strings.Join(cs, ",")))
	}
	l := mustLayout(t, strings.Join(tabs, ","))
	if n := len(encodeLayoutDoc(l)); n >= maxLayoutDoc {
		t.Errorf("layout: %d bytes", n)
	}
	worst := lightState{St: stStale, On: true, Bri: 255, RGB: "ffffff", Caps: capDim | capTemp | capColor}
	lt := &lights{ctl: fixedLights{worst}, prof: newProfiles("")}
	d := cardsDocWith(l, pr, nil, lt)
	if len(d) >= 16384 || strings.Count(string(d), `{"st":"stale","v":"100%","v2":"ffffff","f":1,"th":7}`) != 24 ||
		strings.Count(string(d), `{"sub":"not saved"}`) != 4 {
		t.Errorf("cards: %d bytes", len(d))
	}
}

func TestLightCardEntries(t *testing.T) {
	e := newLT(t, ltCards)
	e.card("desk", `{"v":"15%","v2":"ffa957","f":0.149,"th":7}`) // on with brightness and a colour
	e.card("shelf", `{"v":"off","f":0,"th":1}`)                  // off: no brightness, no colour, still dims
	e.card("plain", `{"v":"on","f":1,"th":0}`)                   // on without brightness
	e.card("fan", `{"v":"off","f":0,"th":0}`)                    // a switch
	e.card("morning", `{"sub":"not saved"}`)
	if got := cardEntries(t, e.lay, nil); got["desk"] != "" || got["morning"] != "" || got["cpu"] == "" {
		t.Errorf("without lights: %v", got)
	}
	e.ha.set(func() {
		e.ha.l["light.shelf"].state, e.ha.l["switch.fan"].state = "on", "on"
		e.ha.l["light.plain"].state = "unavailable"
		delete(e.ha.l, "light.desk")
	})
	e.hl.pass(context.Background())
	e.card("shelf", `{"v":"60%","f":0.6,"th":1}`)
	e.card("fan", `{"v":"on","f":1,"th":0}`)
	e.card("plain", `{"st":"none","sub":"unavailable"}`)
	e.card("desk", `{"st":"none","sub":"no entity"}`)

	e.ha.set(func() { e.ha.getCode = 500 }) // a failed read keeps the last good state, stale; one that had none says why
	e.hl.pass(context.Background())
	e.card("shelf", `{"st":"stale","v":"60%","f":0.6,"th":1}`)
	e.card("plain", `{"st":"none","sub":"http 500"}`)
	e.ha.set(func() { e.ha.getCode = 0 })

	// a light dropped from the layout is no longer read; a new one is read at once
	n := e.ha.reads()
	e.setLayout(`{"id":"only","type":"light","at":[1,1],"size":"1x1","value":{"ha":"light.shelf"}}`)
	if got := e.ha.reads() - n; got != 1 {
		t.Errorf("%d reads for one light", got)
	}
	e.card("only", `{"v":"60%","f":0.6,"th":1}`)

	// the smallest brightness is 1 %, not 0 %
	st, why, err := parseLight("light.a", []byte(`{"state":"on","attributes":{"supported_color_modes":["xy"],"color_mode":"xy","brightness":1,"rgb_color":[255,0,300],"hs_color":[300,100]}}`))
	if b, _ := json.Marshal(lightCard(st)); why != "" || err != nil || string(b) != `{"v":"1%","v2":"ff00ff","f":0.004,"th":5}` || st.Mode != "xy" || st.HS != [2]float64{300, 100} {
		t.Errorf("%s %q %v %+v", b, why, err, st)
	}
	if _, why, _ := parseLight("light.a", []byte(`{"state":"unknown"}`)); why != "unknown" {
		t.Errorf("unknown: %q", why)
	}
	if _, _, err := parseLight("light.a", []byte(`<html>`)); err == nil {
		t.Error("bad json read as a light")
	}
}

func TestLightRoutes(t *testing.T) {
	e := newLT(t, ltCards)
	r := httptest.NewRequest("POST", "/api/v1/lights/desk/toggle", nil)
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Errorf("no token: %d", w.Code)
	}
	// only a light card of the layout: not an unknown id, not another card type, never an entity id
	for _, id := range []string{"nope", "cpu", "morning", "light.shelf", "switch.fan"} {
		for _, a := range []string{"toggle", "on", "off", "bri/50", "color/soft"} {
			e.want("/api/v1/lights/"+id+"/"+a, 404, "no light card '"+id+"'")
		}
	}
	e.want("/api/v1/lights/"+strings.Repeat("x", 99)+"/on", 404, "no light card '"+strings.Repeat("x", 23)+"'")
	e.sent()

	// the entity comes from the layout: the request's body and query name another one (post sends such a body)
	e.want("/api/v1/lights/desk/toggle?entity_id=light.other", 204, "")
	e.sent(`light/toggle {"entity_id":"light.desk"}`)
	e.card("desk", `{"v":"off","f":0,"th":7}`) // read again at once: no poll in between
	e.want("/api/v1/lights/desk/on", 204, "")
	e.want("/api/v1/lights/desk/off", 204, "")
	e.sent(`light/turn_on {"entity_id":"light.desk"}`, `light/turn_off {"entity_id":"light.desk"}`)

	// brightness: turns an off light on, 0 turns it off
	e.want("/api/v1/lights/desk/bri/40", 204, "")
	e.sent(`light/turn_on {"entity_id":"light.desk","brightness_pct":40}`)
	e.card("desk", `{"v":"40%","v2":"ffa957","f":0.4,"th":7}`)
	e.want("/api/v1/lights/desk/bri/0", 204, "")
	e.sent(`light/turn_off {"entity_id":"light.desk"}`)
	e.card("desk", `{"v":"off","f":0,"th":7}`)
	e.want("/api/v1/lights/shelf/bri/100", 204, "")
	e.sent(`light/turn_on {"entity_id":"light.shelf","brightness_pct":100}`)
	e.card("shelf", `{"v":"100%","f":1,"th":1}`)
	for _, bad := range []string{"101", "-1", "x", "1.5", "+5", "999999999999999999999"} {
		e.want("/api/v1/lights/desk/bri/"+bad, 400, "brightness is 0 to 100")
	}

	// colours: each one's data, an off light turned on
	e.want("/api/v1/lights/desk/color/blue", 204, "")
	e.sent(`light/turn_on {"entity_id":"light.desk","hs_color":[240,100]}`)
	e.card("desk", `{"v":"40%","v2":"0000ff","f":0.4,"th":7}`)
	for name, data := range map[string]string{
		"soft": `"color_temp_kelvin":2700`, "neutral": `"color_temp_kelvin":4000`, "cool": `"color_temp_kelvin":6000`,
		"red": `"hs_color":[0,100]`, "amber": `"hs_color":[35,100]`, "green": `"hs_color":[120,100]`,
		"blue": `"hs_color":[240,100]`, "magenta": `"hs_color":[300,100]`,
	} {
		e.want("/api/v1/lights/desk/color/"+name, 204, "")
		e.sent(`light/turn_on {"entity_id":"light.desk",` + data + `}`)
	}
	if len(lightColors) != 8 {
		t.Errorf("%d colours", len(lightColors))
	}
	e.want("/api/v1/lights/desk/color/pink", 404, "no colour 'pink'")
	e.want("/api/v1/lights/desk/color/Blue", 404, "no colour 'Blue'")

	// what a light can't do: 409 naming the title, or the id without one; nothing is sent
	e.want("/api/v1/lights/shelf/color/blue", 409, "Shelf lamp can't do that")
	e.want("/api/v1/lights/shelf/color/soft", 409, "Shelf lamp can't do that")
	e.want("/api/v1/lights/plain/bri/50", 409, "plain can't do that")
	e.want("/api/v1/lights/plain/bri/0", 409, "plain can't do that")
	e.want("/api/v1/lights/plain/color/red", 409, "plain can't do that")
	e.want("/api/v1/lights/fan/bri/50", 409, "fan can't do that")
	e.want("/api/v1/lights/fan/color/soft", 409, "fan can't do that")
	e.sent()

	// a switch uses the switch services
	e.want("/api/v1/lights/fan/toggle", 204, "")
	e.card("fan", `{"v":"on","f":1,"th":0}`)
	e.want("/api/v1/lights/fan/off", 204, "")
	e.want("/api/v1/lights/fan/on", 204, "")
	e.sent(`switch/toggle {"entity_id":"switch.fan"}`, `switch/turn_off {"entity_id":"switch.fan"}`, `switch/turn_on {"entity_id":"switch.fan"}`)
	e.want("/api/v1/lights/plain/toggle", 204, "")
	e.card("plain", `{"v":"off","f":0,"th":0}`)
	e.sent(`light/toggle {"entity_id":"light.plain"}`)

	// Home Assistant refusing, the entity gone, and no answer at all
	e.ha.set(func() { e.ha.postCode["light.desk"] = 500 })
	e.want("/api/v1/lights/desk/toggle", 502, "http 500")
	e.ha.set(func() { e.ha.postCode["light.desk"] = 404 })
	e.want("/api/v1/lights/desk/toggle", 502, "no entity")
	e.ha.set(func() { delete(e.ha.postCode, "light.desk"); e.ha.l["light.desk"].state = "unavailable" })
	e.hl.pass(context.Background())
	e.want("/api/v1/lights/desk/bri/10", 502, "unavailable")

	down := newLTEnv(t, map[string]string{"HA_URL": "http://127.0.0.1:1", "HA_TOKEN": "tok"}, ltCards)
	down.want("/api/v1/lights/desk/toggle", 502, "unreachable")
	down.want("/api/v1/lights/desk/bri/10", 502, "unreachable")
	down.card("desk", `{"st":"none","sub":"unreachable"}`)
}

// A 401 to a service call latches the token exactly as a read's does: nothing is sent to Home Assistant again,
// by the lights or by the cards.
func TestLightTokenLatch(t *testing.T) {
	e := newLT(t, ltCards)
	e.ha.set(func() { e.ha.postCode[""] = 401 })
	e.want("/api/v1/lights/desk/toggle", 502, "bad token")
	e.sent(`light/toggle {"entity_id":"light.desk"}`)
	reads := e.ha.reads()
	e.hl.pass(context.Background())
	e.want("/api/v1/lights/desk/toggle", 502, "bad token")
	e.want("/api/v1/lights/desk/bri/10", 502, "bad token")
	e.want("/api/v1/profiles/morning/save", 502, "bad token")
	if _, why, err := e.src.haFetch(context.Background(), jsonSpec{HA: "sensor.x"}); why != "bad token" || err != nil {
		t.Errorf("a card's fetch after the latch: %q %v", why, err)
	}
	e.sent()
	if n := e.ha.reads(); n != reads {
		t.Errorf("%d reads after the token was refused", n-reads)
	}
	e.card("desk", `{"st":"none","sub":"bad token"}`)

	// and the other way: a read's 403 stops the service calls
	e = newLT(t, ltCards)
	e.ha.set(func() { e.ha.getCode = 403 })
	e.hl.pass(context.Background())
	e.want("/api/v1/lights/desk/toggle", 502, "forbidden")
	e.sent()
}

func TestLightsWithoutHA(t *testing.T) {
	e := newLTEnv(t, nil, ltCards)
	e.card("desk", `{"st":"none","sub":"set HA_URL"}`)
	for _, p := range []string{"lights/desk/toggle", "lights/desk/on", "lights/desk/off", "lights/desk/bri/10", "lights/desk/bri/0",
		"lights/desk/color/soft", "profiles/morning/save"} {
		e.want("/api/v1/"+p, 503, "set HA_URL")
	}
	e.want("/api/v1/profiles/morning/apply", 409, "not saved")
	e.want("/api/v1/lights/nope/toggle", 404, "no light card 'nope'")
	e = newLTEnv(t, map[string]string{"HA_URL": "http://ha.lan:8123"}, ltCards)
	e.want("/api/v1/lights/desk/toggle", 503, "set HA_TOKEN")
	e.want("/api/v1/lights/desk/bri/10", 503, "set HA_TOKEN")
	e.card("fan", `{"st":"none","sub":"set HA_TOKEN"}`)
}

func TestProfiles(t *testing.T) {
	e := newLT(t, ltCards)
	e.want("/api/v1/profiles/morning/apply", 409, "not saved")
	for _, id := range []string{"nope", "desk", "cpu", "light.desk"} {
		e.want("/api/v1/profiles/"+id+"/apply", 404, "no profile card '"+id+"'")
		e.want("/api/v1/profiles/"+id+"/save", 404, "no profile card '"+id+"'")
	}
	e.sent()

	// save reads every light fresh (these changes were never polled), then apply sends exactly what was saved
	e.ha.set(func() {
		e.ha.l["light.shelf"].state, e.ha.l["switch.fan"].state, e.ha.l["light.plain"].state = "on", "on", "off"
	})
	e.want("/api/v1/profiles/morning/save", 204, "")
	e.card("morning", `{}`)
	for _, p := range []string{"desk/off", "shelf/bri/10", "fan/off", "plain/on"} {
		e.want("/api/v1/lights/"+p, 204, "")
	}
	e.ha.sent()
	e.want("/api/v1/profiles/morning/apply", 204, "")
	e.sent(`light/turn_on {"entity_id":"light.desk","brightness":38,"color_temp_kelvin":2700}`,
		`light/turn_on {"entity_id":"light.shelf","brightness":153}`,
		`switch/turn_on {"entity_id":"switch.fan"}`,
		`light/turn_off {"entity_id":"light.plain"}`)
	e.card("desk", `{"v":"15%","v2":"ffa957","f":0.149,"th":7}`) // read again at once
	e.card("shelf", `{"v":"60%","f":0.6,"th":1}`)
	e.card("fan", `{"v":"on","f":1,"th":0}`)
	e.card("plain", `{"v":"off","f":0,"th":0}`)

	// a colour is saved as hue and saturation
	e.want("/api/v1/lights/desk/color/blue", 204, "")
	e.want("/api/v1/profiles/morning/save", 204, "")
	e.want("/api/v1/lights/desk/color/soft", 204, "")
	e.ha.sent()
	e.want("/api/v1/profiles/morning/apply", 204, "")
	if got := e.ha.sent(); len(got) != 4 || got[0] != `light/turn_on {"entity_id":"light.desk","brightness":38,"hs_color":[240,100]}` {
		t.Errorf("hs profile: %v", got)
	}

	// one light failing: 502, the rest still set
	e.ha.set(func() { e.ha.postCode["light.desk"] = 500 })
	e.want("/api/v1/profiles/morning/apply", 502, "http 500")
	if got := e.ha.sent(); len(got) != 4 {
		t.Errorf("after a failure: %v", got)
	}
	e.ha.set(func() { delete(e.ha.postCode, "light.desk") })

	// an entity dropped from the layout is skipped, whatever the profile holds
	all := e.lay
	e.setLayout(strings.Join(slices.DeleteFunc(strings.Split(ltCards, "\n"), func(c string) bool { return strings.Contains(c, "light.shelf") }), "\n"))
	e.want("/api/v1/profiles/morning/apply", 204, "")
	e.sent(`light/turn_on {"entity_id":"light.desk","brightness":38,"hs_color":[240,100]}`,
		`switch/turn_on {"entity_id":"switch.fan"}`, `light/turn_off {"entity_id":"light.plain"}`)

	// an entity that can't be read is left out of a save
	e.ha.set(func() { e.ha.l["light.plain"].state = "unavailable" })
	e.want("/api/v1/profiles/morning/save", 204, "")
	e.want("/api/v1/profiles/morning/apply", 204, "")
	e.sent(`light/turn_on {"entity_id":"light.desk","brightness":38,"hs_color":[240,100]}`, `switch/turn_on {"entity_id":"switch.fan"}`)
	// and when none can, the save fails and the profile stays as it was
	e.ha.set(func() { e.ha.getCode = 500 })
	e.want("/api/v1/profiles/morning/save", 502, "http 500")
	e.ha.set(func() { e.ha.getCode = 0 })
	e.want("/api/v1/profiles/morning/apply", 204, "")
	if got := e.ha.sent(); len(got) != 2 {
		t.Errorf("after a failed save: %v", got)
	}

	e.setLayout(`{"id":"cpu","type":"number","at":[2,2],"size":"1x1","value":"cpu"},{"id":"morning","type":"profile","at":[3,2],"size":"1x1","title":"Morning"}`)
	e.want("/api/v1/profiles/morning/save", 409, "no light cards")
	e.want("/api/v1/profiles/morning/apply", 204, "") // saved, with nothing left to set
	e.sent()
	e.lay = all
}

// A save the panel gave up on (or that ran out of time) part-way stores nothing: the saved profile stays whole.
func TestProfileSaveAllOrNothing(t *testing.T) {
	e := newLT(t, ltCards)
	e.want("/api/v1/profiles/morning/save", 204, "")
	before, _ := e.lt.prof.get("morning")
	e.ha.set(func() {
		e.ha.l["light.desk"].state, e.ha.l["switch.fan"].state = "off", "on" // what a part of a new save would hold
		e.ha.stallAt = e.ha.gets + 2                                         // the first read answers, the second never does
	})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for e.ha.reads() < e.ha.stallAt { // the second read is out
			time.Sleep(time.Millisecond)
		}
		cancel()
	}()
	if c, b := e.postCtx(ctx, "/api/v1/profiles/morning/save"); c != 502 || b != "timed out" {
		t.Errorf("cancelled save: %d %q", c, b)
	}
	if after, _ := e.lt.prof.get("morning"); len(before) != 4 || !reflect.DeepEqual(after, before) {
		t.Errorf("profile changed: %v, was %v", after, before)
	}
}

// The profiles survive a restart through their file, and live in memory without one.
func TestProfilesFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "profiles.json")
	e := newLT(t, ltCards)
	e.lt.prof = newProfiles(file) // no file yet
	e.card("morning", `{"sub":"not saved"}`)
	e.want("/api/v1/profiles/morning/save", 204, "")
	fi, err := os.Stat(file)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("profiles file: %v %v", fi, err)
	}
	if _, err := os.Stat(file + ".tmp"); err == nil {
		t.Error("the temp file was left behind")
	}
	e.want("/api/v1/lights/desk/off", 204, "")
	e.ha.sent()

	e.lt.prof = newProfiles(file) // a restart
	e.card("morning", `{}`)
	e.want("/api/v1/profiles/morning/apply", 204, "")
	if got := e.ha.sent(); len(got) != 4 || got[0] != `light/turn_on {"entity_id":"light.desk","brightness":38,"color_temp_kelvin":2700}` {
		t.Errorf("after a restart: %v", got)
	}

	// a broken file starts empty and is kept as .bad (replacing an older one), not overwritten by the next save
	os.WriteFile(file+".bad", []byte("older"), 0o600)
	os.WriteFile(file, []byte(`{"morning": {"light.desk": {"on": tr`), 0o600)
	p := newProfiles(file)
	if _, err := os.Stat(file); p.has("morning") || len(p.m) != 0 || err == nil {
		t.Errorf("broken file: %v, still there: %v", p.m, err == nil)
	}
	if err := p.put("x", nil); err != nil {
		t.Error(err)
	}
	if bad, _ := os.ReadFile(file + ".bad"); string(bad) != `{"morning": {"light.desk": {"on": tr` {
		t.Errorf(".bad holds %q", bad)
	}
	if again := newProfiles(file); !again.has("x") {
		t.Error("the save after a broken file was lost")
	}
	// a folder that can't be written: the save says so
	e.lt.prof = newProfiles(filepath.Join(t.TempDir(), "missing", "profiles.json"))
	e.want("/api/v1/profiles/morning/save", 500, "can't write the profiles file")

	// no file: memory only
	env := map[string]string{}
	get := func(k string) string { return env[k] }
	if p := profilesPath(get); p != "" {
		t.Errorf("no settings: %q", p)
	}
	env["NOTES_FILE"] = "/data/notes.json"
	if p := profilesPath(get); filepath.ToSlash(p) != "/data/profiles.json" {
		t.Errorf("next to NOTES_FILE: %q", p)
	}
	env["PROFILES_FILE"] = "/config/p.json"
	if p := profilesPath(get); p != "/config/p.json" {
		t.Errorf("PROFILES_FILE: %q", p)
	}
	dir := t.TempDir()
	t.Chdir(dir)
	e.lt.prof = newProfiles("")
	e.want("/api/v1/profiles/morning/save", 204, "")
	e.want("/api/v1/profiles/morning/apply", 204, "")
	if es, _ := os.ReadDir(dir); len(es) != 0 {
		t.Errorf("memory-only profiles wrote %v", es)
	}
}

// The demo's lights go through the same routes and end in the same cards entries as Home Assistant's.
func TestDemoLights(t *testing.T) {
	lt := newDemoLights()
	lay, err := parseLayout(handLayout(demoLayouts()["lights-demo"]))
	if err != nil {
		t.Fatal(err)
	}
	e := &ltEnv{t: t, lt: lt, lay: lay, mux: http.NewServeMux()}
	lt.layout = func() Layout { return e.lay }
	addLightRoutes(e.mux, "ptok", lt)
	for id, want := range map[string]string{
		"lt-desk": `{"v":"15%","v2":"ffa957","f":0.149,"th":7}`, "lt-shelf": `{"v":"60%","f":0.6,"th":1}`, "lt-fan": `{"v":"off","f":0,"th":0}`,
		"lt-morning": `{}`, "lt-all-off": `{}`, "lt-afternoon": `{"sub":"not saved"}`, "lt-evening": `{"sub":"not saved"}`, "lt-late": `{"sub":"not saved"}`,
	} {
		e.card(id, want)
	}
	e.want("/api/v1/lights/lt-fan/toggle", 204, "")
	e.card("lt-fan", `{"v":"on","f":1,"th":0}`)
	e.want("/api/v1/lights/lt-desk/bri/50", 204, "")
	e.want("/api/v1/lights/lt-desk/color/blue", 204, "")
	e.card("lt-desk", `{"v":"50%","v2":"0000ff","f":0.502,"th":7}`)
	e.want("/api/v1/lights/lt-shelf/off", 204, "")
	e.card("lt-shelf", `{"v":"off","f":0,"th":1}`)
	e.want("/api/v1/lights/lt-shelf/bri/20", 204, "") // turns it on
	e.card("lt-shelf", `{"v":"20%","f":0.2,"th":1}`)
	e.want("/api/v1/lights/lt-shelf/color/blue", 409, "Shelf can't do that")
	e.want("/api/v1/lights/lt-fan/bri/5", 409, "Fan can't do that")
	e.want("/api/v1/lights/lt-desk/color/pink", 404, "no colour 'pink'")
	e.want("/api/v1/lights/lt-morning/toggle", 404, "no light card 'lt-morning'")

	e.want("/api/v1/profiles/lt-evening/apply", 409, "not saved")
	e.want("/api/v1/profiles/lt-evening/save", 204, "")
	e.card("lt-evening", `{}`)
	e.want("/api/v1/profiles/lt-all-off/apply", 204, "")
	e.card("lt-desk", `{"v":"off","f":0,"th":7}`)
	e.card("lt-shelf", `{"v":"off","f":0,"th":1}`)
	e.card("lt-fan", `{"v":"off","f":0,"th":0}`)
	e.want("/api/v1/profiles/lt-evening/apply", 204, "")
	e.card("lt-desk", `{"v":"50%","v2":"0000ff","f":0.502,"th":7}`)
	e.card("lt-shelf", `{"v":"20%","f":0.2,"th":1}`)
	e.card("lt-fan", `{"v":"on","f":1,"th":0}`)
	e.want("/api/v1/profiles/lt-morning/apply", 204, "")
	e.card("lt-desk", `{"v":"15%","v2":"ffa957","f":0.149,"th":7}`)
	e.card("lt-shelf", `{"v":"60%","f":0.6,"th":1}`)

	// the simulator's files carry the tab and its entries, and the editor demo can list the three entities
	b, err := demoCardsDoc(demoNow())
	if err != nil || !strings.Contains(string(b), `"lt-desk":{"v":"15%","v2":"ffa957","f":0.149,"th":7}`) || !strings.Contains(string(b), `"lt-late":{"sub":"not saved"}`) {
		t.Errorf("demo-cards: %v", err)
	}
	dir := t.TempDir()
	if err := writeDemoLayouts(dir); err != nil {
		t.Fatal(err)
	}
	doc, _ := os.ReadFile(filepath.Join(dir, "lights-demo.json"))
	servedShape(t, "lights-demo", doc)
	for _, ent := range lightEntities(lay) {
		if _, ok := demoHA[ent]; !ok || lt.ctl.state(ent).St != stOK {
			t.Errorf("%s: not a demo entity", ent)
		}
	}
}

// What the editor reads for the two types: the picker's domains, the colour table, and lights kept in a cut list.
func TestLightSources(t *testing.T) {
	_, body := srcEd(t).sources()
	var d struct {
		Types  map[string]map[string]any `json:"types"`
		Colors []lightColor              `json:"colors"`
	}
	if err := json.Unmarshal([]byte(body), &d); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(d.Types["light"]["entity"]) != "[light switch]" || d.Types["light"]["tap"] != false || d.Types["light"]["src"] != "none" {
		t.Errorf("light: %v", d.Types["light"])
	}
	if _, has := d.Types["profile"]["entity"]; has || d.Types["profile"]["tap"] != false || d.Types["profile"]["general"] != false {
		t.Errorf("profile: %v", d.Types["profile"])
	}
	if !strings.Contains(body, `"colors":[{"name":"soft","needs":2,"swatch":"ffa957"},`) || len(d.Colors) != len(lightColors) {
		t.Errorf("colors: %+v", d.Colors)
	}
	hex := regexp.MustCompile(`^[0-9a-f]{6}$`)
	for i, c := range d.Colors {
		if c.Name != lightColors[i].Name || !hex.MatchString(c.Swatch) || (c.Needs != capTemp && c.Needs != capColor) || len(c.Name) > 10 {
			t.Errorf("colour %+v", c) // "color/" + name fits the firmware's action[16]
		}
	}

	ents := []string{`{"entity_id":"zone.home","state":"0"}`, `{"entity_id":"light.desk","state":"on"}`, `{"entity_id":"switch.fan","state":"off"}`, `{"entity_id":"sensor.x","state":"1"}`}
	for i := range haMaxEntities {
		ents = append(ents, fmt.Sprintf(`{"entity_id":"binary_sensor.b%05d","state":"on"}`, i))
	}
	list, cut, err := haStates([]byte("[" + strings.Join(ents, ",") + "]"))
	has := func(id string) bool { return slices.ContainsFunc(list, func(e haEntity) bool { return e.ID == id }) }
	if err != nil || !cut || len(list) != haMaxEntities || !has("light.desk") || !has("switch.fan") || !has("sensor.x") || has("zone.home") || has(fmt.Sprintf("binary_sensor.b%05d", haMaxEntities-1)) {
		t.Errorf("cut list: %d, cut %v, %v", len(list), cut, err)
	}
}
