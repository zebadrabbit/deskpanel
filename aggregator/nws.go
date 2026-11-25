package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// NWS alerts collector: active alerts at home (badge, takeover) and the
// tornado / severe thunderstorm warnings in view (outlines on the radar map).

const (
	nwsHomeAlerts = 4
	nwsPolys      = 8
	nwsTextMax    = 3072
	nwsKeyTTL     = 48 * time.Hour
	nwsPolysAge   = 10 * time.Minute // in-view outlines outlive failed fetches this long at most
	nwsInViewWait = 5 * time.Second  // the in-view fetch (the states in view, nationwide when none) must not hold up the home alerts
)

type nwsAlert struct {
	ID, Event, Headline, Desc, Instr, MsgType string
	Sent, Expires, Ends                       time.Time
	Refs                                      []string
	Rings                                     [][][2]float64 // outer rings, GeoJSON [lon, lat], not closed
	Detection, Damage                         string
}

// parseAlerts reads an NWS GeoJSON alert collection. A feature it can't read is skipped (and logged), so
// one bad timestamp can't hide every alert; a collection with nothing readable is an error.
func parseAlerts(body []byte) ([]nwsAlert, error) {
	var fc struct {
		Features []json.RawMessage `json:"features"`
	}
	if err := json.Unmarshal(body, &fc); err != nil {
		return nil, err
	}
	var out []nwsAlert
	var bad error
	for _, raw := range fc.Features {
		var f struct {
			Geometry *struct {
				Type        string          `json:"type"`
				Coordinates json.RawMessage `json:"coordinates"`
			} `json:"geometry"`
			Properties struct {
				ID          string `json:"id"`
				Event       string `json:"event"`
				Headline    string `json:"headline"`
				Description string `json:"description"`
				Instruction string `json:"instruction"`
				MessageType string `json:"messageType"`
				Sent        time.Time
				Expires     time.Time
				Ends        time.Time
				References  []struct {
					Identifier string    `json:"identifier"`
					Sent       time.Time `json:"sent"`
				} `json:"references"`
				Parameters struct {
					Detection []string `json:"tornadoDetection"`
					Damage    []string `json:"tornadoDamageThreat"`
				} `json:"parameters"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(raw, &f); err != nil {
			log.Printf("nws: skipping an alert: %v", err)
			bad = err
			// an unreadable takeover warning must not vanish quietly: fail the collection instead.
			// A readable event name decides alone (a statement that merely mentions a Tornado Warning must
			// not block the real one); only a missing or broken event falls back to matching the raw bytes.
			var ev struct {
				Properties struct {
					Event json.RawMessage `json:"event"`
				} `json:"properties"`
			}
			var name string
			if json.Unmarshal(raw, &ev) == nil && json.Unmarshal(ev.Properties.Event, &name) == nil && name != "" { // null or "" counts as missing
				if isTakeover(name) {
					return nil, fmt.Errorf("nws: unreadable %s: %w", name, err)
				}
				continue
			}
			for _, e := range takeoverEvents {
				if bytes.Contains(raw, []byte(e)) {
					return nil, fmt.Errorf("nws: unreadable %s: %w", e, err)
				}
			}
			continue
		}
		p := f.Properties
		a := nwsAlert{ID: p.ID, Event: p.Event, Headline: p.Headline, Desc: p.Description, Instr: p.Instruction,
			MsgType: p.MessageType, Sent: p.Sent, Expires: p.Expires, Ends: p.Ends}
		refs := p.References
		sort.SliceStable(refs, func(i, j int) bool { return refs[i].Sent.Before(refs[j].Sent) }) // oldest first
		for _, r := range refs {
			a.Refs = append(a.Refs, r.Identifier)
		}
		if len(p.Parameters.Detection) > 0 {
			a.Detection = p.Parameters.Detection[0]
		}
		if len(p.Parameters.Damage) > 0 {
			a.Damage = p.Parameters.Damage[0]
		}
		if g := f.Geometry; g != nil {
			var polys [][][][2]float64
			switch g.Type {
			case "Polygon":
				var poly [][][2]float64
				if json.Unmarshal(g.Coordinates, &poly) == nil {
					polys = [][][][2]float64{poly}
				}
			case "MultiPolygon":
				json.Unmarshal(g.Coordinates, &polys)
			}
			for _, poly := range polys {
				if len(poly) == 0 {
					continue
				}
				ring := poly[0]
				// real rings repeat the first point at the end; don't spend a slot on it
				if n := len(ring); n > 1 && ring[0] == ring[n-1] {
					ring = ring[:n-1]
				}
				a.Rings = append(a.Rings, ring)
			}
		}
		out = append(out, a)
	}
	if len(out) == 0 && bad != nil {
		return nil, bad
	}
	return out, nil
}

// alertClass is the event's last word: warning, watch, or advisory for the rest.
func alertClass(event string) string {
	w := strings.Fields(event)
	if len(w) > 0 {
		switch w[len(w)-1] {
		case "Warning":
			return "warning"
		case "Watch":
			return "watch"
		}
	}
	return "advisory"
}

// takeoverEvents are the warnings that take over the panel; the one list isTakeover and parseAlerts share.
var takeoverEvents = []string{"Tornado Warning", "Extreme Wind Warning"}

func isTakeover(event string) bool { return slices.Contains(takeoverEvents, event) }

// escalation: 1 = tornado observed, 2 = considerable damage threat, 3 = catastrophic.
func escalation(a nwsAlert) int {
	lvl := 0
	if a.Detection == "OBSERVED" {
		lvl = 1
	}
	switch a.Damage {
	case "CONSIDERABLE":
		lvl = max(lvl, 2)
	case "CATASTROPHIC":
		lvl = 3
	}
	return lvl
}

type wxAlert struct {
	N     int    `json:"n"`
	Key   string `json:"key"`
	Event string `json:"event"`
	Cls   string `json:"cls"`
	Until int64  `json:"until"`
	Take  bool   `json:"take,omitempty"`
	Lvl   int    `json:"lvl,omitempty"`
}

// wxPoly is a warning outline in frame pixels; Kind is "t" (tornado) or "s" (severe).
type wxPoly struct {
	Kind string     `json:"k"`
	Home bool       `json:"h,omitempty"`
	Pts  [][2]int16 `json:"p"`
}

type wxAlerts struct {
	List  []wxAlert `json:"list"`
	Polys []wxPoly  `json:"polys"`
}

type alertText struct {
	Key      string `json:"key"` // the list entry's chain key: how the panel knows a text still belongs to its chip
	Event    string `json:"event"`
	Headline string `json:"headline"`
	Sent     int64  `json:"sent"`
	Expires  int64  `json:"expires"`
	Desc     string `json:"desc"`
	Instr    string `json:"instr"`
}

type chainKey struct {
	key  string
	seen time.Time
}

type nws struct {
	hc        *http.Client
	base, ua  string
	lat, lon  float64
	v         view
	area      string // comma-joined state codes the map touches; "" = ask nationwide
	mu        sync.Mutex
	keys      map[string]chainKey // message id -> chain key
	texts     []alertText         // aligned with the last List
	lastPolys []wxPoly            // from the last good in-view fetch

	polysAt    time.Time     // when that was
	inViewWait time.Duration // nwsInViewWait; tests shorten it
}

func newNWS(hc *http.Client, base, ua string, lat, lon float64, v view) *nws {
	return &nws{hc: hc, base: base, ua: ua, lat: lat, lon: lon, v: v, area: areasIn(v, stateBoxes), keys: map[string]chainKey{}, lastPolys: []wxPoly{},
		inViewWait: nwsInViewWait}
}

func (n *nws) Section() string         { return "weather" }
func (n *nws) Key() string             { return "alerts" }
func (n *nws) Interval() time.Duration { return time.Minute }

func (n *nws) fetch(ctx context.Context, query string) ([]nwsAlert, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, n.base+"/alerts/active?"+query, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", n.ua)
	req.Header.Set("Accept", "application/geo+json")
	resp, err := n.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("nws alerts: %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	return parseAlerts(body)
}

// chain returns the key shared by every message of one warning's updates.
// Caller holds n.mu.
func (n *nws) chain(a nwsAlert, now time.Time) string {
	key := ""
	for _, r := range a.Refs {
		if k, ok := n.keys[r]; ok {
			key = k.key
			break
		}
	}
	if key == "" {
		// the chain's identity is its first message: the oldest reference, so the
		// key survives a restart; only a message with no references is its own root
		root := a.ID
		if len(a.Refs) > 0 {
			root = a.Refs[0]
		}
		sum := sha256.Sum256([]byte(root))
		key = hex.EncodeToString(sum[:])[:8]
	}
	n.keys[a.ID] = chainKey{key, now}
	return key
}

// alertRank orders the list: takeover, warning, watch, advisory.
func alertRank(event string) int {
	if isTakeover(event) {
		return 0
	}
	switch alertClass(event) {
	case "warning":
		return 1
	case "watch":
		return 2
	}
	return 3
}

// cut shortens s to at most n bytes, marking the cut with "..." and never splitting a UTF-8 sequence;
// unlike trunc it keeps line breaks. n < 4 leaves nothing.
func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n < 4 {
		return ""
	}
	i := n - 3
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return strings.TrimRight(s[:i], " \n") + "..."
}

var blankLine = regexp.MustCompile(`\n\s*\n`)

// paras normalises whitespace: NWS hard-wraps its text at ~69 columns, so each paragraph becomes one
// line, and paragraphs are kept apart by a blank line.
func paras(s string) string {
	var ps []string
	for _, p := range blankLine.Split(s, -1) {
		if p = strings.Join(strings.Fields(p), " "); p != "" {
			ps = append(ps, p)
		}
	}
	return strings.Join(ps, "\n\n")
}

// home builds the alert list and the matching full texts.
func (n *nws) home(as []nwsAlert) ([]wxAlert, []alertText) {
	now := time.Now()
	n.mu.Lock()
	defer n.mu.Unlock()
	for id, k := range n.keys {
		if now.Sub(k.seen) > nwsKeyTTL {
			delete(n.keys, id)
		}
	}
	// oldest first so an update finds its original's key
	// on a tie the Cancel goes first, so the non-Cancel (still active) is the newest and wins
	sort.SliceStable(as, func(i, j int) bool {
		if !as[i].Sent.Equal(as[j].Sent) {
			return as[i].Sent.Before(as[j].Sent)
		}
		return as[i].MsgType == "Cancel" && as[j].MsgType != "Cancel"
	})
	newest := map[string]nwsAlert{}
	for _, a := range as {
		newest[n.chain(a, now)] = a // later Sent wins
	}
	for k, a := range newest {
		if a.MsgType == "Cancel" { // the chain is over (/alerts/active drops the messages it replaced)
			delete(newest, k)
		}
	}
	type item struct {
		key string
		a   nwsAlert
	}
	var items []item
	for k, a := range newest {
		items = append(items, item{k, a})
	}
	sort.Slice(items, func(i, j int) bool {
		a, b := items[i].a, items[j].a
		if ra, rb := alertRank(a.Event), alertRank(b.Event); ra != rb {
			return ra < rb
		}
		if !a.Sent.Equal(b.Sent) {
			return a.Sent.After(b.Sent)
		}
		return items[i].key < items[j].key
	})
	if len(items) > nwsHomeAlerts {
		items = items[:nwsHomeAlerts]
	}
	list := make([]wxAlert, len(items))
	texts := make([]alertText, len(items))
	for i, it := range items {
		a := it.a
		until := a.Ends
		if until.IsZero() {
			until = a.Expires
		}
		list[i] = wxAlert{N: i, Key: it.key, Event: a.Event, Cls: alertClass(a.Event), Until: until.Unix(),
			Take: isTakeover(a.Event), Lvl: escalation(a)}
		texts[i] = alertText{Key: it.key, Event: cut(paras(a.Event), 100), Headline: cut(paras(a.Headline), 300), Sent: a.Sent.Unix(), Expires: until.Unix(),
			Desc: paras(a.Desc), Instr: paras(a.Instr)}
		t := &texts[i]
		rest := nwsTextMax - len(t.Event) - len(t.Headline)
		if len(t.Desc)+len(t.Instr) > rest {
			// the description (where and what) keeps at least half the budget, even against a huge instruction
			t.Desc = cut(t.Desc, max(rest-len(t.Instr), min(len(t.Desc), rest/2)))
			t.Instr = cut(t.Instr, rest-len(t.Desc))
		}
	}
	return list, texts
}

// polys keeps the in-view warnings that have something to draw.
func (n *nws) polys(as []nwsAlert) []wxPoly {
	type cand struct {
		p    wxPoly
		dist float64
	}
	var cs []cand
	for _, a := range as {
		kind := map[string]string{"Tornado Warning": "t", "Severe Thunderstorm Warning": "s"}[a.Event]
		if kind == "" || a.MsgType == "Cancel" {
			continue
		}
		for _, ring := range a.Rings {
			pts := polyInView(n.v, ring)
			if pts == nil {
				continue
			}
			var sx, sy float64
			for _, p := range pts {
				sx += float64(p[0])
				sy += float64(p[1])
			}
			c := float64(len(pts))
			cs = append(cs, cand{wxPoly{Kind: kind, Home: pointInRing(n.lon, n.lat, ring), Pts: pts},
				math.Hypot(sx/c-frameW/2, sy/c-frameH/2)})
		}
	}
	sort.SliceStable(cs, func(i, j int) bool {
		a, b := cs[i], cs[j]
		if a.p.Home != b.p.Home {
			return a.p.Home
		}
		if a.p.Kind != b.p.Kind {
			return a.p.Kind == "t"
		}
		return a.dist < b.dist
	})
	if len(cs) > nwsPolys {
		cs = cs[:nwsPolys]
	}
	out := make([]wxPoly, len(cs))
	for i, c := range cs {
		out[i] = c.p
	}
	return out
}

func (n *nws) Collect(ctx context.Context) (any, error) {
	// status=actual: test and exercise messages must never take over
	home, err := n.fetch(ctx, fmt.Sprintf("point=%.4f,%.4f&status=actual", n.lat, n.lon))
	if err != nil {
		return nil, err
	}
	list, texts := n.home(home)
	ictx, cancel := context.WithTimeout(ctx, n.inViewWait)
	defer cancel()
	q := "status=actual&event=Tornado%20Warning,Severe%20Thunderstorm%20Warning"
	if n.area != "" {
		q += "&area=" + n.area
	}
	inView, err := n.fetch(ictx, q)
	n.mu.Lock()
	defer n.mu.Unlock()
	n.texts = texts
	if err != nil {
		// outlines are optional: keep the last ones a while rather than lose the home alerts
		log.Printf("nws: warnings in view: %v", err)
		if time.Since(n.polysAt) > nwsPolysAge {
			n.lastPolys = []wxPoly{}
		}
	} else {
		n.lastPolys, n.polysAt = n.polys(inView), time.Now()
	}
	return wxAlerts{List: list, Polys: n.lastPolys}, nil
}

// text returns the full text of List[i] from the last Collect.
func (n *nws) text(i int) (alertText, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if i < 0 || i >= len(n.texts) {
		return alertText{}, false
	}
	return n.texts[i], true
}
