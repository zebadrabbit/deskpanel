package main

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Lights: the `light` and `profile` cards. A light card names one Home Assistant light or switch; the panel can turn
// it on and off, dim it and pick one of lightColors, by the card's id only: the entity always comes from the layout,
// never from a request. A profile card saves what every light card's entity is doing and sets it again later.

const (
	maxLights  = 24 // light cards in a layout
	lightEvery = 5 * time.Second
	lightPool  = 4 // state reads at once

	// What a light can do (lightState.Caps, a light card's "th").
	capDim   = 1
	capTemp  = 2
	capColor = 4
)

// lightDomains are the Home Assistant domains a light card takes (and its picker in the editor lists).
var lightDomains = []string{"light", "switch"}

// lightColor is one of the fixed colours: its name in the route, the capability it needs, a swatch to draw, and what
// Home Assistant is sent (a colour temperature or hue/saturation).
type lightColor struct {
	Name   string `json:"name"`
	Needs  int    `json:"needs"`
	Swatch string `json:"swatch"` // rrggbb
	kelvin int
	hs     [2]float64
}

var lightColors = []lightColor{
	{"soft", capTemp, "ffa957", 2700, [2]float64{}},
	{"neutral", capTemp, "ffd1a3", 4000, [2]float64{}},
	{"cool", capTemp, "e6f0ff", 6000, [2]float64{}},
	{"red", capColor, "ff0000", 0, [2]float64{0, 100}},
	{"amber", capColor, "ff9500", 0, [2]float64{35, 100}},
	{"green", capColor, "00ff00", 0, [2]float64{120, 100}},
	{"blue", capColor, "0000ff", 0, [2]float64{240, 100}},
	{"magenta", capColor, "ff00ff", 0, [2]float64{300, 100}},
}

// lightID: e is a light or switch entity id, the only thing that can reach a light request's URL or body.
func lightID(e string) bool {
	domain, _, _ := strings.Cut(e, ".")
	return len(e) <= 255 && haEntityRe.MatchString(e) && contains(lightDomains, domain)
}

// lightEntity is a light card's entity: its value must be exactly {"ha": "<a light. or switch. entity>"}.
func (c Card) lightEntity() (string, bool) {
	var in struct {
		HA string `json:"ha"`
	}
	dec := json.NewDecoder(bytes.NewReader(c.Value))
	dec.DisallowUnknownFields()
	if dec.Decode(&in) != nil {
		return "", false
	}
	return in.HA, lightID(in.HA)
}

// lightState is one entity as last read.
type lightState struct {
	St     state  // ok | stale | none
	Why    string // when none: set HA_URL, set HA_TOKEN, bad token, forbidden, no entity, unreachable, unavailable, unknown, no data (not read yet)
	On     bool
	Bri    int    // 0..255, 0 when the light has no brightness
	RGB    string // "rrggbb" from rgb_color when on, else ""
	Caps   int    // capDim | capTemp | capColor (from supported_color_modes)
	Mode   string // color_mode
	HS     [2]float64
	Kelvin int
}

// parseLight reads a GET /api/states/<entity> reply; why is Home Assistant's own word when the entity is neither on
// nor off (unavailable, unknown).
func parseLight(entity string, body []byte) (st lightState, why string, err error) {
	var in struct {
		State string `json:"state"`
		A     struct {
			Bri    *float64  `json:"brightness"`
			RGB    []float64 `json:"rgb_color"`
			Modes  []string  `json:"supported_color_modes"`
			Mode   string    `json:"color_mode"`
			HS     []float64 `json:"hs_color"`
			Kelvin *float64  `json:"color_temp_kelvin"`
		} `json:"attributes"`
	}
	if json.Unmarshal(body, &in) != nil {
		return st, "", errors.New("bad json")
	}
	switch in.State {
	case "on":
		st.On = true
	case "off":
	default:
		return st, cmp.Or(cutBytes(strings.TrimSpace(in.State), maxSub), "unknown"), nil
	}
	st.St = stOK
	if strings.HasPrefix(entity, "switch.") {
		return st, "", nil
	}
	for _, m := range in.A.Modes {
		switch m {
		case "onoff", "unknown":
		case "color_temp":
			st.Caps |= capDim | capTemp
		case "hs", "xy", "rgb", "rgbw", "rgbww":
			st.Caps |= capDim | capColor
		default: // brightness, white
			st.Caps |= capDim
		}
	}
	if !st.On {
		return st, "", nil
	}
	byteOf := func(x float64) int { return int(math.Round(min(max(x, 0), 255))) }
	if in.A.Bri != nil && st.Caps&capDim != 0 {
		st.Bri = byteOf(*in.A.Bri)
	}
	if c := in.A.RGB; len(c) == 3 {
		st.RGB = fmt.Sprintf("%02x%02x%02x", byteOf(c[0]), byteOf(c[1]), byteOf(c[2]))
	}
	if len(in.A.HS) == 2 {
		st.HS = [2]float64{in.A.HS[0], in.A.HS[1]}
	}
	if in.A.Kelvin != nil {
		st.Kelvin = int(math.Round(*in.A.Kelvin))
	}
	st.Mode = in.A.Mode
	return st, "", nil
}

// lightCard is a light card's entry in /api/v1/cards: v "off", "on" (no brightness) or "NN%", f the brightness 0..1,
// v2 the colour "rrggbb" when it has one, th what the light can do (Caps); none carries the reason in sub.
func lightCard(s lightState) cardOut {
	if s.St == stNone {
		return noneCard(s.Why)
	}
	o := cardOut{V: "off", F: frac(0), Th: &s.Caps}
	if s.On {
		o.V, o.F, o.V2 = "on", frac(1), s.RGB
		if s.Bri > 0 {
			o.V = fmt.Sprintf("%d%%", max(1, int(math.Round(float64(s.Bri)*100/255))))
			o.F = frac(float64(s.Bri) / 255)
		}
	}
	if s.St == stStale {
		o.St = stStale
	}
	return o
}

// lightCmd is what a service call sets besides on/off; the zero value sets nothing.
type lightCmd struct {
	BriPct int         `json:"brightness_pct,omitempty"`    // 1..100, from the panel's slider
	Bri    int         `json:"brightness,omitempty"`        // 1..255, from a profile
	Kelvin int         `json:"color_temp_kelvin,omitempty"` //
	HS     *[2]float64 `json:"hs_color,omitempty"`
}

// lightBackend is where the lights are: Home Assistant (haLights), or the demo's three (demoLights).
type lightBackend interface {
	want(entities []string)                                               // the layout's light entities
	state(entity string) lightState                                       // as last read; never waits
	read(ctx context.Context, entity string) lightState                   // read now
	call(ctx context.Context, entity, service string, cmd lightCmd) error // service: turn_on, turn_off, toggle
}

// whyErr is a reason an entity can't be driven as the route's answer: 503 for a missing setting, else 502.
func whyErr(why string) error {
	if strings.HasPrefix(why, "set ") {
		return httpErr{http.StatusServiceUnavailable, why}
	}
	return httpErr{http.StatusBadGateway, why}
}

// haLights reads the wanted entities from Home Assistant every lightEvery and sends the service calls, all through
// src's HA plumbing (haDo): its client, 5 s timeout, probe gate and one-try token latch.
type haLights struct {
	src  *jsonSrc
	kick chan struct{} // the wanted entities changed: read now

	mu  sync.Mutex
	st  map[string]lightState // the wanted entities
	gen int                   // bumped by every service call: a read that began before one is not recorded
}

func newHALights(src *jsonSrc) *haLights {
	return &haLights{src: src, kick: make(chan struct{}, 1), st: map[string]lightState{}}
}

var lightUnread = lightState{St: stNone, Why: "no data"}

func (h *haLights) want(entities []string) {
	h.mu.Lock()
	st := map[string]lightState{}
	for _, e := range entities {
		st[e] = lightUnread
		if old, ok := h.st[e]; ok {
			st[e] = old
		}
	}
	h.st = st
	h.mu.Unlock()
	select {
	case h.kick <- struct{}{}:
	default:
	}
}

func (h *haLights) state(e string) lightState {
	h.mu.Lock()
	defer h.mu.Unlock()
	if s, ok := h.st[e]; ok {
		return s
	}
	return lightUnread
}

func (h *haLights) run(ctx context.Context) {
	t := time.NewTicker(lightEvery)
	defer t.Stop()
	for {
		h.pass(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-h.kick:
		}
	}
}

// pass reads every wanted entity once. The first goes alone: while Home Assistant hasn't accepted the token only one
// request may be out, and that one proves it (or latches it) for the rest.
// ponytail: with HA down and the token unproven, the entities that lose the probe gate keep "no data" until their
// turn comes; give them the pass's failure if that ever confuses.
func (h *haLights) pass(ctx context.Context) {
	h.mu.Lock()
	ents := make([]string, 0, len(h.st))
	for e := range h.st {
		ents = append(ents, e)
	}
	h.mu.Unlock()
	if len(ents) == 0 {
		return
	}
	h.read(ctx, ents[0])
	sem := make(chan struct{}, lightPool)
	var wg sync.WaitGroup
	for _, e := range ents[1:] {
		sem <- struct{}{}
		wg.Go(func() {
			defer func() { <-sem }()
			h.read(ctx, e)
		})
	}
	wg.Wait()
}

// read asks Home Assistant for e now and records the answer (if e is still wanted): a failure after a good state
// keeps it, stale. When nothing was asked (another request is proving the token) the last state is the answer.
func (h *haLights) read(ctx context.Context, e string) lightState {
	h.mu.Lock()
	gen := h.gen
	h.mu.Unlock()
	st, why, err := lightState{}, "bad entity", error(nil)
	if lightID(e) { // again, where the token is sent
		var body []byte
		if body, why, err = h.src.haDo(ctx, "GET", "/api/states/"+e, nil); why == "" && err == nil {
			if len(body) > jsonMaxBody {
				why = "reply too big"
			} else {
				st, why, err = parseLight(e, body)
			}
		}
	}
	if why == haBusy || ctx.Err() != nil {
		return h.state(e)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	old, wanted := h.st[e]
	switch {
	case why != "":
		st = lightState{St: stNone, Why: why}
	case err != nil && (old.St == stOK || old.St == stStale):
		st = old
		st.St, st.Why = stStale, errWhy(err)
	case err != nil:
		st = lightState{St: stNone, Why: errWhy(err)}
	}
	if wanted && gen == h.gen { // not one that was on its way while a service call changed the light
		h.st[e] = st
	}
	return st
}

func (h *haLights) call(ctx context.Context, e, service string, cmd lightCmd) error {
	if !lightID(e) {
		return httpErr{http.StatusBadGateway, "bad entity"}
	}
	domain, _, _ := strings.Cut(e, ".")
	if domain == "switch" { // a switch is on or off, nothing else
		cmd = lightCmd{}
	}
	send, _ := json.Marshal(struct {
		Entity string `json:"entity_id"`
		lightCmd
	}{e, cmd})
	_, why, err := h.src.haDo(ctx, "POST", "/api/services/"+domain+"/"+service, send)
	switch {
	case why != "":
		return whyErr(why)
	case err != nil:
		return httpErr{http.StatusBadGateway, errWhy(err)}
	}
	h.mu.Lock()
	h.gen++
	h.mu.Unlock()
	return nil
}

// savedLight is one entity in a saved profile.
type savedLight struct {
	On     bool       `json:"on"`
	Bri    int        `json:"bri"`  // 0..255, 0: none to set
	Mode   string     `json:"mode"` // Home Assistant's color_mode when saved: says whether kelvin or hs is set
	HS     [2]float64 `json:"hs"`
	Kelvin int        `json:"kelvin"`
}

// profiles is the saved profiles: profile card id -> entity -> what to set. Kept in file (tmp + rename on every
// save, like the notes); with no file, in memory only.
// ponytail: a profile whose card is deleted from the layout stays in the file (a few hundred bytes); prune on save
// if that ever matters.
type profiles struct {
	mu   sync.Mutex
	file string
	m    map[string]map[string]savedLight
}

// profilesPath is PROFILES_FILE, else profiles.json next to NOTES_FILE, else "" (memory only).
func profilesPath(getenv func(string) string) string {
	if f := getenv("PROFILES_FILE"); f != "" {
		return f
	}
	if n := getenv("NOTES_FILE"); n != "" {
		return filepath.Join(filepath.Dir(n), "profiles.json")
	}
	return ""
}

func newProfiles(file string) *profiles {
	p := &profiles{file: file, m: map[string]map[string]savedLight{}}
	if file == "" {
		return p
	}
	b, err := os.ReadFile(file)
	if err == nil {
		err = json.Unmarshal(b, &p.m)
	}
	switch {
	case err == nil || errors.Is(err, os.ErrNotExist):
	case b != nil: // read but not understood: keep it for its owner, the next save won't overwrite it
		p.m = nil
		if rerr := os.Rename(file, file+".bad"); rerr != nil {
			log.Printf("profiles: can't read %s: %v; can't set it aside either: %v (starting empty)", file, err, rerr)
		} else {
			log.Printf("profiles: can't read %s: %v (kept as %s.bad, starting empty)", file, err, file)
		}
	default:
		log.Printf("profiles: can't load %s: %v (starting empty)", file, err)
	}
	if p.m == nil { // the file said null, or half of it parsed
		p.m = map[string]map[string]savedLight{}
	}
	return p
}

func (p *profiles) has(id string) bool {
	_, ok := p.get(id)
	return ok
}

func (p *profiles) get(id string) (map[string]savedLight, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	m, ok := p.m[id]
	return m, ok
}

// put keeps m as profile id and writes the file; an error means it is in memory only until the next good write.
func (p *profiles) put(id string, m map[string]savedLight) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.m[id] = m
	if p.file == "" {
		return nil
	}
	b, _ := json.Marshal(p.m)
	tmp := p.file + ".tmp"
	err := os.WriteFile(tmp, b, 0o600)
	if err == nil {
		err = os.Rename(tmp, p.file)
	}
	return err
}

// lights is the light and profile cards' server side: their /api/v1/cards entries and the panel's action routes.
type lights struct {
	ctl    lightBackend
	prof   *profiles
	layout func() Layout // the layout being served: the only place a card id's entity comes from
}

// lightEntities are the entities of l's light cards, in layout order, each once.
func lightEntities(l Layout) []string {
	var out []string
	for _, t := range l.Tabs {
		for _, c := range t.Cards {
			if c.Type != "light" {
				continue
			}
			if e, ok := c.lightEntity(); ok && !contains(out, e) {
				out = append(out, e)
			}
		}
	}
	return out
}

// card finds the card id of type typ ("light" or "profile") in the layout being served.
func (lt *lights) card(id, typ string) (Card, error) {
	for _, t := range lt.layout().Tabs {
		for _, c := range t.Cards {
			if c.ID == id && c.Type == typ {
				return c, nil
			}
		}
	}
	return Card{}, httpErr{http.StatusNotFound, fmt.Sprintf("no %s card '%s'", typ, cutBytes(id, maxIDLen))}
}

// act is one press on a light card: action toggle, on, off, bri (arg 0..100) or color (arg a lightColors name).
func (lt *lights) act(ctx context.Context, id, action, arg string) error {
	c, err := lt.card(id, "light")
	if err != nil {
		return err
	}
	e, _ := c.lightEntity()
	service, cmd, need := "turn_on", lightCmd{}, 0
	switch action {
	case "toggle":
		service = "toggle"
	case "off":
		service = "turn_off"
	case "bri":
		n, err := strconv.Atoi(arg)
		if err != nil || !isDigits(arg) || n > 100 {
			return httpErr{http.StatusBadRequest, "brightness is 0 to 100"}
		}
		if need, cmd.BriPct = capDim, n; n == 0 {
			service = "turn_off"
		}
	case "color":
		i := 0
		for i < len(lightColors) && lightColors[i].Name != arg {
			i++
		}
		if i == len(lightColors) {
			return httpErr{http.StatusNotFound, fmt.Sprintf("no colour '%s'", short(arg))}
		}
		col := lightColors[i]
		if need = col.Needs; col.kelvin != 0 {
			cmd.Kelvin = col.kelvin
		} else {
			cmd.HS = &col.hs
		}
	}
	if need != 0 {
		st := lt.ctl.state(e)
		if st.St == stNone { // not read yet, or HA_URL was unset: ask (a latched token sends nothing)
			st = lt.ctl.read(ctx, e)
		}
		if st.St == stNone {
			return whyErr(st.Why)
		}
		if st.Caps&need == 0 {
			return httpErr{http.StatusConflict, cmp.Or(c.Title, c.ID) + " can't do that"}
		}
	}
	if err := lt.ctl.call(ctx, e, service, cmd); err != nil {
		return err
	}
	lt.ctl.read(ctx, e) // so the panel's next /cards poll already shows it
	return nil
}

// save stores what every light card's entity is doing now as profile card id. An entity that can't be read is left
// out; if none can, nothing is stored (a saved profile isn't wiped while Home Assistant is away). All or nothing: when
// ctx ends on the way (the panel gave up, the budget ran out) nothing is stored either, never a part.
func (lt *lights) save(ctx context.Context, id string) error {
	c, err := lt.card(id, "profile")
	if err != nil {
		return err
	}
	ents := lightEntities(lt.layout())
	if len(ents) == 0 {
		return httpErr{http.StatusConflict, "no light cards"}
	}
	saved, why := map[string]savedLight{}, ""
	for _, e := range ents {
		st := lt.ctl.read(ctx, e)
		if ctx.Err() != nil {
			return httpErr{http.StatusBadGateway, "timed out"}
		}
		if st.St != stOK {
			why = cmp.Or(why, st.Why, "unreachable")
			continue
		}
		saved[e] = savedLight{st.On, st.Bri, st.Mode, st.HS, st.Kelvin}
	}
	if len(saved) == 0 {
		return whyErr(why)
	}
	if err := lt.prof.put(c.ID, saved); err != nil {
		log.Printf("profiles: can't save %s: %v", lt.prof.file, err)
		return httpErr{http.StatusInternalServerError, "can't write the profiles file"}
	}
	return nil
}

// apply sets every entity saved in profile card id that is still a light card's, one after another: all are tried,
// the first failure is the answer.
func (lt *lights) apply(ctx context.Context, id string) error {
	c, err := lt.card(id, "profile")
	if err != nil {
		return err
	}
	saved, ok := lt.prof.get(c.ID)
	if !ok {
		return httpErr{http.StatusConflict, "not saved"}
	}
	var first error
	var done []string
	for _, e := range lightEntities(lt.layout()) {
		s, ok := saved[e]
		if !ok {
			continue
		}
		service, cmd := "turn_off", lightCmd{}
		if s.On {
			service, cmd.Bri = "turn_on", s.Bri
			switch s.Mode {
			case "color_temp":
				cmd.Kelvin = s.Kelvin
			// ponytail: only hue and saturation are saved, so an rgbw/rgbww light comes back in its colour without its
			// white channel, and one saved in "white" mode with its brightness only. Save rgbw_color/rgbww_color too
			// if someone's lights need it.
			case "hs", "xy", "rgb", "rgbw", "rgbww":
				cmd.HS = &s.HS
			}
		}
		if err := lt.ctl.call(ctx, e, service, cmd); err != nil {
			log.Printf("profile %s: %s: %v", c.ID, e, err)
			first = cmp.Or(first, err)
			continue
		}
		done = append(done, e)
	}
	for _, e := range done {
		lt.ctl.read(ctx, e)
	}
	return first
}

// addLightRoutes: the panel's presses (PANEL_TOKEN, no body). 204 done; 404 no such light or profile card in the
// layout, or no such colour; 409 the light can't do that, or the profile isn't saved; 502 Home Assistant refused or
// didn't answer; 503 HA_URL or HA_TOKEN unset.
func addLightRoutes(mux *http.ServeMux, token string, lt *lights) {
	handle := func(pattern string, budget time.Duration, f func(ctx context.Context, r *http.Request) error) {
		mux.HandleFunc("POST "+pattern, auth(token, func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), budget)
			defer cancel()
			if err := f(ctx, r); err != nil {
				code := http.StatusBadGateway
				var he httpErr
				if errors.As(err, &he) {
					code = he.code
				}
				log.Printf("%q: %v", r.URL.Path, err)
				http.Error(w, err.Error(), code)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		}))
	}
	// The panel's HTTP client gives up after 10 s: every budget ends before that, so it always gets the answer.
	// (Each Home Assistant request keeps its own 5 s.)
	const one, all = 8 * time.Second, 9 * time.Second
	for _, a := range []string{"toggle", "on", "off"} {
		handle("/api/v1/lights/{card}/"+a, one, func(ctx context.Context, r *http.Request) error {
			return lt.act(ctx, r.PathValue("card"), a, "")
		})
	}
	for _, a := range []string{"bri", "color"} {
		handle("/api/v1/lights/{card}/"+a+"/{arg}", one, func(ctx context.Context, r *http.Request) error {
			return lt.act(ctx, r.PathValue("card"), a, r.PathValue("arg"))
		})
	}
	handle("/api/v1/profiles/{card}/apply", all, func(ctx context.Context, r *http.Request) error {
		return lt.apply(ctx, r.PathValue("card"))
	})
	handle("/api/v1/profiles/{card}/save", all, func(ctx context.Context, r *http.Request) error {
		return lt.save(ctx, r.PathValue("card"))
	})
}

// demoLights stands in for Home Assistant's lights (DEMO=1, demo-cards, the editor demo): a colour light, one that
// only dims and a switch, changed in memory by the same routes. A light keeps its brightness and colour while off.
type demoLights struct {
	mu sync.Mutex
	m  map[string]lightState
}

// newDemoLights is the demo's lights with two of the lights-demo layout's profiles saved.
func newDemoLights() *lights {
	on := func(bri, caps int, mode string) lightState {
		return lightState{St: stOK, On: true, Bri: bri, Caps: caps, Mode: mode}
	}
	desk := on(38, capDim|capTemp|capColor, "color_temp") // 15 %, soft white
	desk.Kelvin, desk.RGB = 2700, demoSwatch(lightCmd{Kelvin: 2700})
	d := &demoLights{m: map[string]lightState{
		"light.desk":  desk,
		"light.shelf": on(153, capDim, "brightness"), // 60 %
		"switch.fan":  {St: stOK},
	}}
	return &lights{ctl: d, prof: &profiles{m: map[string]map[string]savedLight{
		"lt-morning": {
			"light.desk":  {On: true, Bri: 38, Mode: "color_temp", Kelvin: 2700},
			"light.shelf": {On: true, Bri: 153, Mode: "brightness"},
			"switch.fan":  {On: true},
		},
		"lt-all-off": {"light.desk": {}, "light.shelf": {}, "switch.fan": {}},
	}}}
}

// demoSwatch is the colour a demo light shows after cmd: the fixed colour's swatch (the panel sends no others).
func demoSwatch(cmd lightCmd) string {
	for _, c := range lightColors {
		if cmd.HS != nil && c.kelvin == 0 && c.hs == *cmd.HS || cmd.HS == nil && cmd.Kelvin != 0 && c.kelvin == cmd.Kelvin {
			return c.Swatch
		}
	}
	return "ffffff"
}

func (d *demoLights) want([]string) {}

func (d *demoLights) state(e string) lightState {
	d.mu.Lock()
	defer d.mu.Unlock()
	s, ok := d.m[e]
	if !ok {
		return lightState{St: stNone, Why: "no entity"}
	}
	if !s.On { // as Home Assistant reports a light that is off
		s.Bri, s.RGB = 0, ""
	}
	return s
}

func (d *demoLights) read(_ context.Context, e string) lightState { return d.state(e) }

func (d *demoLights) call(_ context.Context, e, service string, cmd lightCmd) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	s, ok := d.m[e]
	if !ok {
		return httpErr{http.StatusBadGateway, "no entity"}
	}
	switch service {
	case "toggle":
		s.On = !s.On
	case "turn_off":
		s.On = false
	default:
		s.On = true
		if s.Caps&capDim != 0 {
			if cmd.BriPct > 0 {
				s.Bri = (cmd.BriPct*255 + 50) / 100
			}
			if cmd.Bri > 0 {
				s.Bri = cmd.Bri
			}
		}
		switch {
		case cmd.Kelvin > 0 && s.Caps&capTemp != 0:
			s.Mode, s.Kelvin, s.RGB = "color_temp", cmd.Kelvin, demoSwatch(cmd)
		case cmd.HS != nil && s.Caps&capColor != 0:
			s.Mode, s.HS, s.RGB = "hs", *cmd.HS, demoSwatch(cmd)
		}
	}
	d.m[e] = s
	return nil
}
