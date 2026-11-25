package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// haEntityRe is Home Assistant's entity id, domain.object_id: nothing else can reach the URL the token goes to.
var haEntityRe = regexp.MustCompile(`^[a-z0-9_]+\.[a-z0-9_]+$`)

// haListing is what the editor offers from Home Assistant: its entities.
type haListing struct {
	Configured bool       `json:"configured"`
	Needs      []string   `json:"needs"` // the settings to add to .env, by name: HA_URL, HA_TOKEN
	Error      string     `json:"error"`
	Truncated  bool       `json:"truncated"` // the list was cut to haMaxEntities
	Entities   []haEntity `json:"entities"`
}

type haEntity struct {
	ID    string   `json:"id"`
	Name  string   `json:"name"`  // attributes.friendly_name
	State string   `json:"state"` // as Home Assistant sends it: text
	Unit  string   `json:"unit"`  // attributes.unit_of_measurement
	Attrs []string `json:"attrs"` // the attributes that hold a number or text, sorted
}

// parseHASpec decodes a value {"ha": entity, "attr": name, "every": s} (unknown keys refused).
func parseHASpec(raw json.RawMessage) (*jsonSpec, error) {
	var in struct {
		HA    string `json:"ha"`
		Attr  string `json:"attr"`
		Every *int   `json:"every"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&in) != nil {
		return nil, errors.New("ha takes ha, attr, every")
	}
	if len(in.HA) > 255 || !haEntityRe.MatchString(in.HA) {
		return nil, errors.New("ha needs an entity id like sensor.x")
	}
	if len(in.Attr) > 64 {
		return nil, errors.New("ha attr over 64 bytes")
	}
	every := 15
	if in.Every != nil {
		every = *in.Every
	}
	if every < 10 || every > 3600 {
		return nil, errors.New("ha every must be 10 to 3600 seconds")
	}
	return &jsonSpec{HA: in.HA, Attr: in.Attr, Every: every}, nil
}

func (s *jsonSrc) haState(tok string) (why string, proven bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.haBad == tok {
		return s.haWhy, false
	}
	return "", s.haOK == tok
}

// haGate: while HA hasn't accepted the token, one request at a time (a wrong token costs one failed login, not one
// per pool slot). Taken before a pool slot so a stuck HA can't hold the JSON cards' slots; false = someone else is
// probing, skip this round (the next cycle retries). The caller calls release when done.
func (s *jsonSrc) haGate() (release func(), ok bool) {
	tok := strings.TrimSpace(s.getenv("HA_TOKEN"))
	if why, proven := s.haState(tok); why != "" || proven {
		return func() {}, true
	}
	if !s.haProbe.TryLock() {
		return nil, false
	}
	if why, proven := s.haState(tok); why != "" || proven {
		s.haProbe.Unlock()
		return func() {}, true
	}
	return s.haProbe.Unlock, true
}

// haConf is HA_URL (without a trailing /) and HA_TOKEN, and the settings still to add to .env (unset, or a URL
// that isn't http(s)), URL first.
func (s *jsonSrc) haConf() (base, tok string, needs []string) {
	base = strings.TrimRight(strings.TrimSpace(s.getenv("HA_URL")), "/")
	tok = strings.TrimSpace(s.getenv("HA_TOKEN"))
	needs = []string{}
	if u, err := url.Parse(base); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		needs = append(needs, "HA_URL")
	}
	if tok == "" {
		needs = append(needs, "HA_TOKEN")
	}
	return base, tok, needs
}

// haSaw records Home Assistant's answer to tok: a 401 or 403 latches it (never sent again), anything else proves it.
func (s *jsonSrc) haSaw(tok string, code int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch code {
	case http.StatusUnauthorized:
		s.haBad, s.haWhy = tok, "bad token"
	case http.StatusForbidden:
		s.haBad, s.haWhy = tok, "forbidden" // what HA answers once it has banned the address
	default:
		s.haOK = tok
	}
}

func (s *jsonSrc) haFetch(ctx context.Context, sp jsonSpec) (promVal, string, error) {
	base, tok, needs := s.haConf()
	if len(needs) > 0 {
		return promVal{}, "set " + needs[0], nil
	}
	if !haEntityRe.MatchString(sp.HA) { // where the token is sent, like the CARD_ re-check
		return promVal{}, "bad entity", nil
	}
	// latched: this token was refused, never send it again.
	if why, _ := s.haState(tok); why != "" {
		return promVal{}, why, nil
	}
	body, code, err := s.do(ctx, &s.haHC, base+"/api/states/"+sp.HA, "Authorization", "Bearer "+tok, jsonMaxBody)
	if err == nil {
		s.haSaw(tok, code)
	}
	switch {
	case err != nil:
		return promVal{}, "", err // unreachable, or stale after a good value (refresh)
	case code == http.StatusUnauthorized:
		return promVal{}, "bad token", nil
	case code == http.StatusForbidden:
		return promVal{}, "forbidden", nil
	case code == http.StatusNotFound:
		return promVal{}, "no entity", nil
	case code/100 != 2:
		return promVal{}, "", errors.New("http " + strconv.Itoa(code))
	case len(body) > jsonMaxBody:
		return promVal{}, "reply too big", nil
	}
	var st struct {
		State      string         `json:"state"`
		Attributes map[string]any `json:"attributes"`
	}
	if json.Unmarshal(body, &st) != nil {
		return promVal{}, "", errors.New("bad json")
	}
	x, why := any(st.State), "no data"
	if sp.Attr != "" {
		a, has := st.Attributes[sp.Attr]
		x, why = a, cutBytes("no attr "+sp.Attr, maxSub)
		if has && a == nil && strings.TrimSpace(st.State) != "" { // present but null (a light's brightness while off): say what the entity is doing
			why = cutBytes(strings.TrimSpace(st.State), maxSub)
		}
	}
	v, ok := haValue(x)
	if !ok {
		return promVal{}, why, nil
	}
	v.St = stOK
	if u, _ := st.Attributes["unit_of_measurement"].(string); sp.Attr == "" {
		v.U = cutBytes(u, maxUnitLen) // the state's unit; an attribute isn't measured in it
	}
	return v, "", nil
}

const (
	haListMax     = 4 << 20 // /api/states of a big house is a few MB
	haMaxEntities = 2000
	haMaxAttrs    = 40
	haMaxText     = 64 // an entity's name and state in the list
	haMaxName     = 32 // its unit, and an attribute's name (a longer one is left out: cut, it would name nothing)
	// haBusy: a card's fetch is proving the token right now (one request at a time until HA accepts it). It passes:
	// the editor doesn't cache an answer that says it.
	haBusy = "busy, try again"
)

// haList is the editor's entity list: GET /api/states with the cards' client (no redirects), behind the same probe gate
// and 401/403 latch, so a refused token is never sent again. Asked only when the editor asks.
func (s *jsonSrc) haList(ctx context.Context) haListing {
	out := haListing{Needs: []string{}, Entities: []haEntity{}}
	if s == nil {
		return out
	}
	base, tok, needs := s.haConf()
	if out.Needs = needs; len(needs) > 0 { // not configured: the page says which .env lines to add
		return out
	}
	out.Configured = true
	release, ok := s.haGate()
	if !ok {
		out.Error = haBusy
		return out
	}
	defer release()
	if why, _ := s.haState(tok); why != "" {
		out.Error = why
		return out
	}
	body, code, err := s.do(ctx, &s.haHC, base+"/api/states", "Authorization", "Bearer "+tok, haListMax)
	if err != nil {
		out.Error = "unreachable" // the error carries the URL
		return out
	}
	s.haSaw(tok, code)
	switch {
	case code == http.StatusUnauthorized:
		out.Error = "bad token"
	case code == http.StatusForbidden:
		out.Error = "forbidden"
	case code/100 != 2:
		out.Error = "http " + strconv.Itoa(code)
	case len(body) > haListMax:
		out.Error = "reply too big"
	default:
		if out.Entities, out.Truncated, err = haStates(body); err != nil {
			out.Entities, out.Error = []haEntity{}, err.Error()
		}
	}
	return out
}

// haStates reads a /api/states reply: the entities a card can name (the haEntityRe ids), sorted by id. One it can't
// read is left out, not the list: a state that is a number or true/false is its text, anything else ""; attributes
// that aren't an object are none. Over haMaxEntities, sensors are kept first, then binary sensors, then the rest by
// id (truncated says so). Each has at most haMaxAttrs attributes holding a number or text, name and unit apart.
func haStates(body []byte) (list []haEntity, truncated bool, err error) {
	var in []json.RawMessage
	if json.Unmarshal(body, &in) != nil {
		return nil, false, errors.New("bad json")
	}
	out := []haEntity{}
	for _, raw := range in {
		var x struct {
			ID    string          `json:"entity_id"`
			State any             `json:"state"`
			Attrs json.RawMessage `json:"attributes"`
		}
		if json.Unmarshal(raw, &x) != nil || len(x.ID) > 255 || !haEntityRe.MatchString(x.ID) {
			continue
		}
		var attrs map[string]any
		json.Unmarshal(x.Attrs, &attrs) // not an object: no attributes
		e := haEntity{ID: x.ID, Attrs: []string{}}
		switch st := x.State.(type) {
		case string:
			e.State = st
		case float64:
			e.State = strconv.FormatFloat(st, 'f', -1, 64)
		case bool:
			e.State = strconv.FormatBool(st)
		}
		e.State = cutBytes(e.State, haMaxText)
		name, _ := attrs["friendly_name"].(string)
		unit, _ := attrs["unit_of_measurement"].(string)
		e.Name, e.Unit = cutBytes(name, haMaxText), cutBytes(unit, haMaxName)
		for k, v := range attrs {
			switch v.(type) {
			case float64, string:
				if k != "friendly_name" && k != "unit_of_measurement" && len(k) <= haMaxName {
					e.Attrs = append(e.Attrs, k)
				}
			}
		}
		sort.Strings(e.Attrs)
		e.Attrs = e.Attrs[:min(len(e.Attrs), haMaxAttrs)]
		out = append(out, e)
	}
	rank := func(e haEntity) int {
		switch domain, _, _ := strings.Cut(e.ID, "."); domain {
		case "sensor":
			return 0
		case "binary_sensor":
			return 1
		}
		return 2
	}
	out, truncated = capRanked(out, haMaxEntities, rank, func(e haEntity) string { return e.ID })
	return out, truncated, nil
}

// haValue: a number, or text that reads as a finite one ("21.5": HA's states are text), is a number; true/false are
// 1/0; other non-empty text stays text. Null, lists, objects and "" are nothing (false).
func haValue(x any) (promVal, bool) {
	switch t := x.(type) {
	case float64:
		if !math.IsNaN(t) && !math.IsInf(t, 0) {
			return promVal{V: t}, true
		}
	case bool:
		if t {
			return promVal{V: 1}, true
		}
		return promVal{}, true
	case string:
		t = strings.TrimSpace(t)
		if t == "" {
			return promVal{}, false
		}
		if f, err := strconv.ParseFloat(t, 64); err == nil && !math.IsNaN(f) && !math.IsInf(f, 0) {
			return promVal{V: f}, true
		}
		return promVal{S: t}, true
	}
	return promVal{}, false
}
