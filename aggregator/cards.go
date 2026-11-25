package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Caps from the /api/v1/cards contract (the panel's buffers).
const (
	maxV   = 15
	maxSub = 23
	// histKeep is how many 60 s samples a GPU keeps (one hour).
	histKeep = 60
)

// cardOut is one card in /api/v1/cards. Empty fields are omitted; st is omitted when ok.
type cardOut struct {
	St  state    `json:"st,omitempty"`
	V   string   `json:"v,omitempty"`
	V2  string   `json:"v2,omitempty"`
	F   *float64 `json:"f,omitempty"`
	Sub string   `json:"sub,omitempty"`
	G   []string `json:"g,omitempty"`
	Th  *int     `json:"th,omitempty"`
}

// gpuHistory keeps the last histKeep samples per GPU, keyed like the gpu card's src, and reads the GPUs as
// last collected for the fan slot (live).
type gpuHistory struct {
	mu   sync.Mutex
	m    map[string][]gpuSample
	now  func() time.Time // time.Now when nil; tests pin it
	live func() []gpuStat // the GPUs as last collected; nil reads as none
}

type gpuSample struct {
	at                           time.Time
	temp, util, vram, power, fan float64
	fanOK                        bool // an unknown fan (N/A, an old agent) isn't drawn
}

// gpuKey names a GPU like the gpu card's src: host, host#2, ... in report order, counting down ones too,
// as the default layout does. per counts each host's GPUs so far.
func gpuKey(per map[string]int, host string) string {
	if per[host]++; per[host] > 1 {
		return host + "#" + strconv.Itoa(per[host])
	}
	return host
}

func (h *gpuHistory) clock() time.Time {
	if h.now != nil {
		return h.now()
	}
	return time.Now()
}

// record adds one sample for every GPU that is up, keyed by gpuKey.
func (h *gpuHistory) record(stats []gpuStat) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.m == nil {
		h.m = map[string][]gpuSample{}
	}
	per, at := map[string]int{}, h.clock()
	for _, g := range stats {
		if g.Host == "" {
			continue
		}
		key := gpuKey(per, g.Host)
		if !g.Up {
			continue
		}
		vram := 0.0
		if g.VRAMTotal > 0 {
			vram = 100 * float64(g.VRAM) / float64(g.VRAMTotal)
		}
		s := append(h.m[key], gpuSample{at, float64(g.Temp), float64(g.Util), vram, float64(g.Power), float64(g.Fan), g.FanOK})
		if len(s) > histKeep {
			s = s[len(s)-histKeep:]
		}
		h.m[key] = s
	}
}

// series is one metric (temp, util, vram, power, fan) of a GPU over the last hour, oldest first: a GPU
// back after hours down doesn't join its old samples to the new.
func (h *gpuHistory) series(key, metric string) []float64 {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []float64
	since := h.clock().Add(-time.Hour)
	for _, s := range h.m[key] {
		if !s.at.After(since) {
			continue
		}
		switch metric {
		case "temp":
			out = append(out, s.temp)
		case "util":
			out = append(out, s.util)
		case "vram":
			out = append(out, s.vram)
		case "power":
			out = append(out, s.power)
		case "fan":
			if s.fanOK {
				out = append(out, s.fan)
			}
		}
	}
	return out
}

// fan is the fan speed (%) of the GPU named key, as last collected; false when unknown: no reading
// (passive cooling), an old agent, or the GPU down or not there. 0 (zero-RPM) is a reading.
func (h *gpuHistory) fan(key string) (int, bool) {
	if h == nil || h.live == nil {
		return 0, false
	}
	per := map[string]int{}
	for _, g := range h.live() {
		if g.Host != "" && gpuKey(per, g.Host) == key {
			return g.Fan, g.Up && g.FanOK
		}
	}
	return 0, false
}

// gpuData is the gpu slot's data, and whether its last run succeeded.
func gpuData(slots []*slot) ([]gpuStat, bool) {
	for _, s := range slots {
		if s.c.Section()+"."+s.c.Key() != "homelab.gpu" {
			continue
		}
		s.mu.Lock()
		d, ok := s.data, s.ok
		s.mu.Unlock()
		g, isGPU := d.([]gpuStat)
		return g, ok && isGPU
	}
	return nil, false
}

// recordGPU adds a sample from the gpu slot, only if its last run succeeded (a failing
// collector keeps its old data, which must not be recorded again as new).
func recordGPU(h *gpuHistory, slots []*slot) {
	if g, ok := gpuData(slots); ok {
		h.record(g)
	}
}

// queriesFor lists the queries the layout needs: instant for every value (and its
// second query), ranged for every graph line.
func queriesFor(l Layout) (instant, ranged []string, jsons []jsonSpec) {
	at := map[string]int{} // a JSON source's index in jsons
	for _, t := range l.Tabs {
		for _, c := range t.Cards {
			for _, sp := range c.specs() {
				if sp.JSON != nil { // the JSON source keeps its own history
					if i, ok := at[sp.JSON.key()]; ok { // shared: polled as often as its keenest card wants (C1)
						jsons[i].Every = min(jsons[i].Every, sp.JSON.Every)
					} else {
						at[sp.JSON.key()] = len(jsons)
						jsons = append(jsons, *sp.JSON)
					}
					continue
				}
				q := sp.queries()
				instant = append(instant, q.Value)
				if q.Second != "" {
					instant = append(instant, q.Second)
				}
				if c.Type == "graph" {
					ranged = append(ranged, q.Graph)
					if sp.Shortcut == "net" {
						ranged = append(ranged, q.Second)
					}
				}
			}
		}
	}
	return dedup(instant), dedup(ranged), jsons
}

// srcKey says a query key belongs to the JSON source (a URL or a Home Assistant entity).
func srcKey(k string) bool { return strings.HasPrefix(k, "json:") || strings.HasPrefix(k, "ha:") }

// unitOf is the card's unit, else the one the source reported.
func unitOf(c Card, l look) string {
	if c.Unit != "" {
		return c.Unit
	}
	return l.u
}

// readers routes a query key to its source: JSON and Home Assistant keys to the JSON source,
// the rest to Prometheus (nil without PROMETHEUS_URL).
type readers struct{ prom, json promReader }

func (r readers) value(k string) promVal {
	if srcKey(k) {
		return r.json.value(k)
	}
	if r.prom == nil {
		return promVal{St: stNone, Why: "set PROMETHEUS_URL"}
	}
	return r.prom.value(k)
}

func (r readers) series(k string) promSeries {
	if srcKey(k) {
		return r.json.series(k)
	}
	if r.prom == nil {
		return promSeries{St: stNone, Why: "set PROMETHEUS_URL"}
	}
	return r.prom.series(k)
}

func rank(s state) int {
	switch s {
	case stNone:
		return 2
	case stStale:
		return 1
	}
	return 0
}

// look is one spec's cached numbers and the worst state of its instant queries.
type look struct {
	sp        promQueries
	short     string
	v, second float64
	st        state
	why       string
	s         string // a JSON source's text value
	u         string // the unit the source reported
}

func fetch(sp valueSpec, pr promReader) look {
	q := sp.queries()
	l := look{sp: q, short: sp.Shortcut, st: stOK}
	add := func(v promVal) {
		if rank(v.St) > rank(l.st) {
			l.st, l.why = v.St, v.Why
			// a shortcut with no @instance can be fixed by naming one; raw PromQL, or one already
			// naming an instance, keeps the plain count. "add @instance (N)" fits 23 bytes for any N.
			if n, ok := strings.CutPrefix(v.Why, "returns "); ok && sp.Shortcut != "" && sp.Instance == "" {
				if n, ok = strings.CutSuffix(n, " series"); ok {
					l.why = "add @instance (" + n + ")"
				}
			}
		}
	}
	v := pr.value(q.Value)
	l.v, l.s, l.u = v.V, v.S, v.U
	add(v)
	if q.Second != "" {
		s := pr.value(q.Second)
		l.second = s.V
		add(s)
	}
	return l
}

// frac is a gauge/bar fraction: clamped to 0..1, 3 decimals.
func frac(x float64) *float64 {
	if math.IsNaN(x) || x < 0 {
		x = 0
	}
	x = math.Min(x, 1)
	x = math.Round(x*1000) / 1000
	return &x
}

// shown is the main text, gauge fraction, sub-line and the number thresholds
// compare for one value (S8: ram/disk compare % used).
func shown(c Card, l look) (v string, f float64, sub string, thr float64) {
	switch l.short {
	case "ram", "disk":
		pct := 0.0
		if l.second > 0 {
			pct = 100 * l.v / l.second
		}
		if c.Type == "number" {
			return format(l.v, "bytes"), 0, "of " + format(l.second, "bytes"), pct
		}
		u, t := format(l.v, "bytes"), format(l.second, "bytes")
		if i := strings.LastIndexByte(u, ' '); i >= 0 && strings.HasSuffix(t, u[i:]) {
			u = u[:i] // "9.2 / 76 GB"
		}
		return format(pct, "%"), pct / 100, u + " / " + t, pct
	case "load":
		if l.second > 0 {
			f = l.v / l.second
		}
		return format(l.v, ""), f, fmt.Sprintf("of %d cores", int(math.Round(l.second))), l.v
	case "cpu", "temp":
		return format(l.v, l.sp.Unit), l.v / 100, "", l.v
	case "uptime":
		return format(l.v, "s"), 0, "", l.v
	case "net":
		return format(l.v, "bps"), 0, "", l.v
	}
	lo, hi := 0.0, 100.0 // prometheus values scale by the card's min/max
	if c.Min != nil {
		lo = *c.Min
	}
	if c.Max != nil {
		hi = *c.Max
	}
	if hi > lo {
		f = (l.v - lo) / (hi - lo)
	}
	return format(l.v, unitOf(c, l)), f, "", l.v
}

// autoHi is 1.1 x the largest of vals, or 1 when nothing is above zero.
func autoHi(vals ...float64) float64 {
	m := 0.0
	for _, v := range vals {
		m = math.Max(m, v)
	}
	if m <= 0 {
		return 1
	}
	return m * 1.1
}

// graphScale picks lo..hi for a graph card (ruling C3).
func graphScale(c Card, l look, lines [][]float64) (lo, hi float64) {
	var all []float64
	for _, p := range lines {
		all = append(all, p...)
	}
	if c.Warn != nil {
		all = append(all, *c.Warn)
	}
	switch {
	case c.Min != nil || c.Max != nil:
		if c.Min != nil {
			lo = *c.Min
		}
		if c.Max != nil {
			return lo, *c.Max
		}
		return lo, autoHi(all...)
	case l.sp.Hi > l.sp.Lo: // cpu, temp, ram, disk
		return l.sp.Lo, l.sp.Hi
	case l.short == "" && (unitOf(c, l) == "%" || unitOf(c, l) == "°C"):
		return 0, 100
	}
	return 0, autoHi(all...)
}

var upWords = []string{"ok", "up", "true", "healthy", "running", "online", "active", "on", "home", "open", "playing"}

func noneCard(why string) cardOut { return cardOut{St: stNone, Sub: cutBytes(why, maxSub)} }

func generalCard(c Card, pr promReader) cardOut {
	specs := c.specs()
	if len(specs) == 0 {
		return noneCard("no value")
	}
	if pr == nil {
		return noneCard("set PROMETHEUS_URL")
	}
	st, why := stOK, ""
	merge := func(s state, w string) {
		if rank(s) > rank(st) {
			st, why = s, w
		}
	}
	ls := make([]look, len(specs))
	for i, sp := range specs {
		ls[i] = fetch(sp, pr)
		merge(ls[i].st, ls[i].why)
	}
	if st != stNone && c.Type != "number" && c.Type != "status" {
		for _, l := range ls {
			if l.s != "" {
				if strings.HasPrefix(l.sp.Value, "ha:") && (l.s == "unavailable" || l.s == "unknown") {
					return noneCard(l.s) // Home Assistant's own word
				}
				return noneCard("not a number")
			}
		}
	}
	var lines [][]float64
	if c.Type == "graph" {
		qs := []string{}
		for _, l := range ls {
			qs = append(qs, l.sp.Graph)
		}
		if ls[0].short == "net" {
			qs = append(qs, ls[0].sp.Second)
		}
		badSeries := ""
		for _, q := range qs {
			s := pr.series(q)
			if s.St == stNone || len(s.P) == 0 {
				if badSeries == "" {
					badSeries = s.Why
					if badSeries == "" {
						badSeries = "no data"
					}
				}
				continue
			}
			if s.St == stStale { // a down source keeps its last plot, greyed
				merge(stStale, s.Why)
			}
			lines = append(lines, s.P[max(0, len(s.P)-promPoints):])
		}
		if st != stNone && badSeries != "" { // a missing series: show the value, stale, with an empty plot (C5a)
			lines = nil
			merge(stStale, badSeries)
		}
	}
	if st == stNone {
		return noneCard(why)
	}

	var o cardOut
	grade := stOK
	l0 := ls[0]
	switch c.Type {
	case "status":
		o.V = "up"
		switch {
		case l0.s != "": // J3: a string is up or it is its own bad word
			w := strings.TrimSpace(l0.s)
			if !contains(upWords, strings.ToLower(w)) {
				o.V, grade = w, stBad
			}
		case l0.v <= 0:
			o.V, grade = "down", stBad
		}
	case "number", "gauge", "bar":
		v, f, sub, thr := shown(c, l0)
		o.V, o.Sub = v, sub
		if l0.s != "" { // text on a number card; thresholds never see it
			o.V, o.Sub, thr = l0.s, "", math.NaN()
		}
		if c.Type != "number" {
			o.F = frac(f)
		}
		grade = threshold(thr, c.Warn, c.Bad, c.Below)
	case "graph":
		v, _, _, thr := shown(c, l0)
		o.V = v
		if len(ls) == 2 {
			o.V2, _, _, _ = shown(c, ls[1])
		} else if l0.short == "net" {
			o.V2 = format(l0.second, "bps")
		}
		lo, hi := graphScale(c, l0, lines)
		for _, p := range lines {
			o.G = append(o.G, encodePoints(p, lo, hi))
		}
		if c.Warn != nil {
			th := scaleIdx(*c.Warn, lo, hi)
			o.Th = &th
		}
		grade = threshold(thr, c.Warn, c.Bad, c.Below)
	}
	if st == stStale {
		o.St = stStale
	} else if grade != stOK {
		o.St = grade
	}
	o.V, o.V2, o.Sub = cutBytes(o.V, maxV), cutBytes(o.V2, maxV), cutBytes(o.Sub, maxSub)
	return o
}

// gpuCard carries what the card can't read from /panel: line 1's history graph, and (J7) the fan for a card
// that shows it in any slot: v "45%" and f 0.45, or v "--" and no f when unknown. The card formats the rest
// of its text from /panel. No graph and no fan: no entry.
func gpuCard(c Card, h *gpuHistory) (cardOut, bool) {
	_, line1, as, _ := c.slots()
	fan := c.usesFan()
	if as != "graph" && !fan {
		return cardOut{}, false
	}
	var o cardOut
	if fan {
		o.V = "--"
		if pct, ok := h.fan(c.Src); ok {
			o.V, o.F = fmt.Sprintf("%d%%", pct), frac(float64(pct)/100)
		}
	}
	if as != "graph" {
		return o, true
	}
	s := h.series(c.Src, line1)
	if len(s) == 0 {
		o.St = stNone
		return o, true
	}
	lo, hi := 0.0, 100.0 // fan too: a percentage
	if line1 == "power" {
		hi = autoHi(s...)
	}
	o.G = []string{encodePoints(s, lo, hi)}
	return o, true
}

func encodeCards(cards map[string]cardOut) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(struct {
		Cards map[string]cardOut `json:"cards"`
	}{cards})
	return bytes.TrimRight(buf.Bytes(), "\n")
}

// cardsDoc builds the /api/v1/cards document from cached values only. pr is nil
// when PROMETHEUS_URL is unset.
func cardsDoc(l Layout, pr promReader, h *gpuHistory) []byte {
	cards := map[string]cardOut{}
	for _, t := range l.Tabs {
		for _, c := range t.Cards {
			switch {
			case c.Type == "gpu":
				if o, ok := gpuCard(c, h); ok {
					cards[c.ID] = o
				}
			case contains(generalTypes, c.Type):
				cards[c.ID] = generalCard(c, pr)
			}
		}
	}
	return encodeCards(cards)
}

// serveETag answers 304 when the client already has body, else sends it.
func serveETag(w http.ResponseWriter, r *http.Request, body []byte) {
	sum := sha256.Sum256(body)
	etag := `"` + hex.EncodeToString(sum[:8]) + `"`
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "no-cache")
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "application/json")
	}
	w.Write(body)
}

func addCardsRoute(mux *http.ServeMux, token string, doc func() []byte) {
	mux.HandleFunc("GET /api/v1/cards", auth(token, func(w http.ResponseWriter, r *http.Request) {
		serveETag(w, r, doc())
	}))
}
