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
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// promVal is one instant query's result. St is stOK, stStale (V is the last
// good value) or stNone (V unset; Why says why, per S6).
type promVal struct {
	V   float64
	S   string // text value of a JSON source (V is then 0)
	U   string // a unit the source reports (Home Assistant's unit_of_measurement); the card's own unit wins
	St  state
	Why string
}

// promSeries is a range query's result: P oldest first, at most 60 points.
type promSeries struct {
	P   []float64
	St  state
	Why string
}

type promReader interface {
	value(q string) promVal
	series(q string) promSeries
}

const (
	promMaxBody = 4 << 20
	promPoints  = 60
	promStep    = 60
)

// prom fetches the wanted queries one after another and caches each result.
// The mutex only guards the maps; HTTP never happens with it held.
type prom struct {
	base string
	hc   http.Client
	now  func() time.Time

	instEvery, rangeEvery, timeout, tick time.Duration

	mu    sync.Mutex
	wantI []string
	wantR []string
	vals  map[string]promVal
	sers  map[string]promSeries
	lastI map[string]time.Time
	lastR map[string]time.Time
}

func newProm(baseURL string) *prom {
	return &prom{
		base: baseURL, now: time.Now,
		instEvery: 30 * time.Second, rangeEvery: 60 * time.Second,
		timeout: 5 * time.Second, tick: time.Second,
		vals: map[string]promVal{}, sers: map[string]promSeries{},
		lastI: map[string]time.Time{}, lastR: map[string]time.Time{},
	}
}

func (p *prom) setClock(f func() time.Time) {
	p.mu.Lock()
	p.now = f
	p.mu.Unlock()
}

func dedup(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func (p *prom) want(instant, ranged []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.wantI, p.wantR = dedup(instant), dedup(ranged)
	keep := func(l []string) map[string]bool {
		m := map[string]bool{}
		for _, s := range l {
			m[s] = true
		}
		return m
	}
	ki, kr := keep(p.wantI), keep(p.wantR)
	for q := range p.vals {
		if !ki[q] {
			delete(p.vals, q)
			delete(p.lastI, q)
		}
	}
	for q := range p.sers {
		if !kr[q] {
			delete(p.sers, q)
			delete(p.lastR, q)
		}
	}
}

func (p *prom) value(q string) promVal {
	p.mu.Lock()
	defer p.mu.Unlock()
	if v, ok := p.vals[q]; ok {
		return v
	}
	return promVal{St: stNone, Why: "no data"}
}

func (p *prom) series(q string) promSeries {
	p.mu.Lock()
	defer p.mu.Unlock()
	if s, ok := p.sers[q]; ok {
		s.P = append([]float64(nil), s.P...)
		return s
	}
	return promSeries{St: stNone, Why: "no data"}
}

func (p *prom) run(ctx context.Context) {
	t := time.NewTicker(p.tick)
	defer t.Stop()
	for {
		p.cycle(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// cycle fetches every wanted query that has never been fetched, then the due
// ones. ponytail: sequential, so against a half-down Prometheus a cycle can
// take due-queries x 5 s; a small worker pool if it ever matters.
func (p *prom) cycle(ctx context.Context) {
	type job struct {
		q     string
		rng   bool
		fresh bool
	}
	var jobs []job
	p.mu.Lock()
	add := func(wanted []string, last map[string]time.Time, every time.Duration, rng bool) {
		for _, q := range wanted {
			l, ok := last[q]
			if !ok || time.Since(l) >= every {
				jobs = append(jobs, job{q, rng, !ok})
			}
		}
	}
	add(p.wantI, p.lastI, p.instEvery, false)
	add(p.wantR, p.lastR, p.rangeEvery, true)
	p.mu.Unlock()
	sort.SliceStable(jobs, func(a, b int) bool { return jobs[a].fresh && !jobs[b].fresh })
	for _, j := range jobs {
		if ctx.Err() != nil {
			return
		}
		if j.rng {
			p.refreshSeries(ctx, j.q)
		} else {
			p.refreshValue(ctx, j.q)
		}
	}
}

func (p *prom) refreshValue(ctx context.Context, q string) {
	v, why, err := p.fetchValue(ctx, q)
	if ctx.Err() != nil { // cancelled run records nothing
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !contains(p.wantI, q) { // dropped while in flight
		return
	}
	old := p.vals[q]
	p.lastI[q] = time.Now()
	switch {
	case errors.Is(err, errBadQuery):
		p.vals[q] = promVal{St: stNone, Why: errBadQuery.Error()}
	case err != nil && (old.St == stOK || old.St == stStale):
		p.vals[q] = promVal{V: old.V, St: stStale, Why: clipBytes(err.Error(), 23)}
	case err != nil:
		p.vals[q] = promVal{St: stNone, Why: "unreachable"}
	case why != "":
		p.vals[q] = promVal{St: stNone, Why: why}
	default:
		p.vals[q] = promVal{V: v, St: stOK}
	}
}

func (p *prom) refreshSeries(ctx context.Context, q string) {
	pts, why, err := p.fetchSeries(ctx, q)
	if ctx.Err() != nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !contains(p.wantR, q) {
		return
	}
	old := p.sers[q]
	p.lastR[q] = time.Now()
	switch {
	case errors.Is(err, errBadQuery):
		p.sers[q] = promSeries{St: stNone, Why: errBadQuery.Error()}
	case err != nil && (old.St == stOK || old.St == stStale):
		p.sers[q] = promSeries{P: old.P, St: stStale, Why: clipBytes(err.Error(), 23)}
	case err != nil:
		p.sers[q] = promSeries{St: stNone, Why: "unreachable"}
	case why != "":
		p.sers[q] = promSeries{St: stNone, Why: why}
	default:
		p.sers[q] = promSeries{P: pts, St: stOK}
	}
}

// promListing is what the editor offers from Prometheus: its metric names, instances and mounts.
type promListing struct {
	Configured bool     `json:"configured"`
	Needs      []string `json:"needs"` // the settings to add to .env, by name: PROMETHEUS_URL
	Error      string   `json:"error"`
	Truncated  bool     `json:"truncated"` // a list was cut to promMaxLabels
	Instances  []string `json:"instances"`
	Mounts     []string `json:"mounts"`
	Metrics    []string `json:"metrics"`
}

const promMaxLabels = 5000

// labels lists the metric names, instances and mounts Prometheus knows (the editor's pickers), all three asked at
// once; a nil prom (no PROMETHEUS_URL) is not configured. Lists are sorted, at most promMaxLabels long: over it, the
// metrics a card most likely wants (up, node_*) are kept first.
func (p *prom) labels(ctx context.Context) promListing {
	out := promListing{Needs: []string{}, Instances: []string{}, Mounts: []string{}, Metrics: []string{}}
	if p == nil {
		out.Needs = []string{"PROMETHEUS_URL"}
		return out
	}
	out.Configured = true
	nodeFirst := func(m string) int {
		if m == "up" || strings.HasPrefix(m, "node_") {
			return 0
		}
		return 1
	}
	same := func(string) int { return 0 }
	var mu sync.Mutex
	var wg sync.WaitGroup
	for name, list := range map[string]*[]string{"__name__": &out.Metrics, "instance": &out.Instances, "mountpoint": &out.Mounts} {
		rank := same
		if name == "__name__" {
			rank = nodeFirst
		}
		wg.Go(func() {
			var vals []string
			data, err := p.get(ctx, "/api/v1/label/"+name+"/values", nil)
			if err == nil && json.Unmarshal(data, &vals) != nil {
				err = errors.New("bad json")
			}
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				out.Error = errWhy(err)
				return
			}
			vals, cut := capRanked(vals, promMaxLabels, rank, func(s string) string { return s })
			*list = append(*list, vals...)
			out.Truncated = out.Truncated || cut
		})
	}
	wg.Wait()
	return out
}

// capRanked keeps at most n of list, the lowest ranks first (ties by id), and returns them sorted by id; cut says
// some were left out.
func capRanked[T any](list []T, n int, rank func(T) int, id func(T) string) (kept []T, cut bool) {
	byID := func(a, b T) int { return strings.Compare(id(a), id(b)) }
	if len(list) > n {
		slices.SortFunc(list, func(a, b T) int { return cmp.Or(cmp.Compare(rank(a), rank(b)), byID(a, b)) })
		list, cut = list[:n], true
	}
	slices.SortFunc(list, byID)
	return list, cut
}

// errWhy is an upstream error short and safe enough to show: a failed request is "unreachable" (its error names
// the URL, which can hold credentials), an HTTP status only its code (never the reply's text), anything else its first
// 23 bytes ("bad json", "bad query").
func errWhy(err error) string {
	var ue *url.Error
	var he httpErr
	switch {
	case errors.As(err, &ue):
		return "unreachable"
	case errors.As(err, &he):
		return "http " + strconv.Itoa(he.code)
	}
	return cutBytes(err.Error(), maxSub)
}

// errBadQuery is Prometheus rejecting the query itself (HTTP 400/422): the layout needs fixing, so it
// is none ("bad query"), never stale or "unreachable".
var errBadQuery = errors.New("bad query")

func clipBytes(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// get runs one API call and returns the "data" object of a success reply.
func (p *prom) get(ctx context.Context, path string, args url.Values) (json.RawMessage, error) {
	u, err := url.Parse(p.base)
	if err != nil || u.Host == "" {
		return nil, errors.New("bad PROMETHEUS_URL")
	}
	u.Path = strings.TrimRight(u.Path, "/") + path
	u.RawQuery = args.Encode()
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, promMaxBody+1))
	if err != nil {
		return nil, err
	}
	if len(body) > promMaxBody {
		return nil, errors.New("response too large")
	}
	if resp.StatusCode == 400 || resp.StatusCode == 422 { // a typo (bad_data) or a query that can't run
		return nil, errBadQuery
	}
	var env struct {
		Status string          `json:"status"`
		Data   json.RawMessage `json:"data"`
		Error  string          `json:"error"`
	}
	if json.Unmarshal(body, &env) != nil && resp.StatusCode == 200 {
		return nil, errors.New("bad json")
	}
	if resp.StatusCode != 200 || env.Status != "success" { // the text is Prometheus's: errWhy shows only the code
		return nil, httpErr{resp.StatusCode, fmt.Sprintf("prometheus %d %s", resp.StatusCode, env.Error)}
	}
	return env.Data, nil
}

// sample parses a [t,"v"] pair; ok is false for NaN/Inf or a bad pair.
func sample(raw json.RawMessage) (t, v float64, ok bool) {
	var pair []json.RawMessage
	var s string
	if json.Unmarshal(raw, &pair) != nil || len(pair) != 2 ||
		json.Unmarshal(pair[0], &t) != nil || json.Unmarshal(pair[1], &s) != nil {
		return 0, 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return t, 0, false
	}
	return t, v, true
}

func seriesCount(n int) string { return fmt.Sprintf("returns %d series", n) }

// fetchValue returns (value, "", nil), or why it can't be shown, or an error.
func (p *prom) fetchValue(ctx context.Context, q string) (float64, string, error) {
	data, err := p.get(ctx, "/api/v1/query", url.Values{"query": {q}})
	if err != nil {
		return 0, "", err
	}
	var d struct {
		Type   string          `json:"resultType"`
		Result json.RawMessage `json:"result"`
	}
	if json.Unmarshal(data, &d) != nil {
		return 0, "", errors.New("bad json")
	}
	var one json.RawMessage
	switch d.Type {
	case "scalar":
		one = d.Result
	case "vector":
		var vec []struct {
			Value json.RawMessage `json:"value"`
		}
		if json.Unmarshal(d.Result, &vec) != nil {
			return 0, "", errors.New("bad json")
		}
		if len(vec) > 1 {
			return 0, seriesCount(len(vec)), nil
		}
		if len(vec) == 0 {
			return 0, "no data", nil
		}
		one = vec[0].Value
	default:
		return 0, "no data", nil
	}
	if _, v, ok := sample(one); ok {
		return v, "", nil
	}
	return 0, "no data", nil
}

// fetchSeries returns the newest 60 points of the last hour, gaps between
// returned points forward-filled (nothing invented before the first or after
// the last sample).
func (p *prom) fetchSeries(ctx context.Context, q string) ([]float64, string, error) {
	p.mu.Lock()
	now := p.now()
	p.mu.Unlock()
	end := now.Unix() / promStep * promStep
	data, err := p.get(ctx, "/api/v1/query_range", url.Values{
		"query": {q}, "step": {strconv.Itoa(promStep)},
		"start": {strconv.FormatInt(end-3600, 10)}, "end": {strconv.FormatInt(end, 10)},
	})
	if err != nil {
		return nil, "", err
	}
	var d struct {
		Result []struct {
			Values []json.RawMessage `json:"values"`
		} `json:"result"`
	}
	if json.Unmarshal(data, &d) != nil {
		return nil, "", errors.New("bad json")
	}
	if len(d.Result) > 1 {
		return nil, seriesCount(len(d.Result)), nil
	}
	var out []float64
	var lastT float64
	for _, raw := range func() []json.RawMessage {
		if len(d.Result) == 0 {
			return nil
		}
		return d.Result[0].Values
	}() {
		t, v, ok := sample(raw)
		if !ok {
			continue
		}
		if len(out) > 0 {
			for n := int(math.Round((t-lastT)/promStep)) - 1; n > 0; n-- {
				out = append(out, out[len(out)-1])
			}
		}
		out = append(out, v)
		lastT = t
	}
	if len(out) == 0 {
		return nil, "no data", nil
	}
	if len(out) > promPoints {
		out = out[len(out)-promPoints:]
	}
	return out, "", nil
}
