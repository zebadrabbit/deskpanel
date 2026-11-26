package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	jsonMaxBody = 256 << 10
	jsonPool    = 4
)

// jsonSpec is one JSON URL source: URL, a dotted Path into the reply, how often
// (seconds) to fetch, and the optional env var holding one whole header line.
type jsonSpec struct {
	URL, Path string
	Every     int
	HeaderEnv string
	HA, Attr  string // a Home Assistant entity and optional attribute (URL, Path, HeaderEnv then empty)
}

func (j jsonSpec) key() string {
	if j.HA != "" {
		return "ha:" + j.HA + "\x00" + j.Attr
	}
	return "json:" + j.URL + "\x00" + j.Path + "\x00" + j.HeaderEnv
}

type jsonPt struct {
	t time.Time
	v float64
}

// jsonSrc polls the wanted specs, each on its own schedule, at most jsonPool
// fetches at once. The mutex only guards the maps; HTTP never happens under it.
type jsonSrc struct {
	getenv  func(string) string
	hc      http.Client
	now     func() time.Time
	timeout time.Duration
	tick    time.Duration

	sem chan struct{}
	wg  sync.WaitGroup

	mu       sync.Mutex
	specs    map[string]jsonSpec
	vals     map[string]promVal
	hist     map[string][]jsonPt
	last     map[string]time.Time
	inflight map[string]bool
	haBad    string // the HA_TOKEN that got a 401/403: never sent again (Home Assistant can ban an address after failed logins)
	haWhy    string // what to show for haBad: "bad token" or "forbidden"
	haOK     string // the HA_TOKEN Home Assistant last accepted
	haHC     http.Client
	haProbe  sync.Mutex // one request at a time while the token is unproven
}

func newJSONSrc(getenv func(string) string) *jsonSrc {
	return &jsonSrc{
		hc:     http.Client{CheckRedirect: noCrossRedirect},
		haHC:   http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, // HA's API never redirects
		getenv: getenv, now: time.Now, timeout: 5 * time.Second, tick: 500 * time.Millisecond,
		sem:   make(chan struct{}, jsonPool),
		specs: map[string]jsonSpec{}, vals: map[string]promVal{}, hist: map[string][]jsonPt{},
		last: map[string]time.Time{}, inflight: map[string]bool{},
	}
}

func (s *jsonSrc) setClock(f func() time.Time) {
	s.mu.Lock()
	s.now = f
	s.mu.Unlock()
}

func (s *jsonSrc) want(specs []jsonSpec) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.specs = map[string]jsonSpec{}
	for _, sp := range specs {
		s.specs[sp.key()] = sp
	}
	for k := range s.vals {
		if _, ok := s.specs[k]; !ok {
			delete(s.vals, k)
			delete(s.hist, k)
			delete(s.last, k)
		}
	}
}

func (s *jsonSrc) value(key string) promVal {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.vals[key]; ok {
		return v
	}
	return promVal{St: stNone, Why: "no data"}
}

func (s *jsonSrc) series(key string) promSeries {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.vals[key]
	if !ok || len(s.hist[key]) == 0 {
		return promSeries{St: stNone, Why: "no data"}
	}
	var p []float64
	for _, h := range s.hist[key] {
		p = append(p, h.v)
	}
	return promSeries{P: p, St: v.St, Why: v.Why}
}

func (s *jsonSrc) run(ctx context.Context) {
	t := time.NewTicker(s.tick)
	defer t.Stop()
	defer s.wg.Wait()
	for {
		s.cycle(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// cycle starts a fetch goroutine for every wanted key that is due and not
// already in flight (so a slow source is dropped from the schedule, not queued).
func (s *jsonSrc) cycle(ctx context.Context) {
	s.mu.Lock()
	now := s.now()
	var due []jsonSpec
	for k, sp := range s.specs {
		l, seen := s.last[k]
		if !s.inflight[k] && (!seen || now.Sub(l) >= time.Duration(sp.Every)*time.Second) {
			s.inflight[k] = true
			due = append(due, sp)
		}
	}
	s.mu.Unlock()
	for _, sp := range due {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.refresh(ctx, sp)
		}()
	}
}

func (s *jsonSrc) refresh(ctx context.Context, sp jsonSpec) {
	k := sp.key()
	defer func() {
		s.mu.Lock()
		delete(s.inflight, k)
		s.mu.Unlock()
	}()
	if sp.HA != "" {
		release, ok := s.haGate()
		if !ok {
			return // not recorded: the next cycle tries again
		}
		defer release()
	}
	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	case <-ctx.Done():
		return
	}
	s.mu.Lock()
	_, wanted := s.specs[k]
	s.mu.Unlock()
	if !wanted { // dropped while waiting for a slot: send nothing
		return
	}
	v, why, err := s.fetch(ctx, sp)
	if ctx.Err() != nil { // cancelled run records nothing
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.specs[k]; !ok { // dropped while in flight
		return
	}
	now := s.now()
	s.last[k] = now
	old := s.vals[k]
	switch {
	case why != "":
		s.vals[k] = promVal{St: stNone, Why: why}
	case err != nil && (old.St == stOK || old.St == stStale):
		old.St, old.Why = stStale, clipBytes(err.Error(), 23)
		s.vals[k] = old
	case err != nil:
		s.vals[k] = promVal{St: stNone, Why: "unreachable"}
	default:
		if (v.S != "" || old.S != "") && sp.HA == "" { // number <-> string: the old plot no longer fits (HA says "unavailable" while it restarts)
			delete(s.hist, k)
		}
		s.vals[k] = v
		if v.S == "" { // J5: one numeric sample a minute, last 60, last hour
			h := s.hist[k]
			if n := len(h); n == 0 || now.Sub(h[n-1].t) >= time.Minute {
				h = append(h, jsonPt{now, v.V})
			}
			for len(h) > 0 && (now.Sub(h[0].t) > time.Hour || len(h) > promPoints) {
				h = h[1:]
			}
			s.hist[k] = h
		}
	}
}

// fetch returns the value, or why it can't be shown, or an error (unreachable).
func (s *jsonSrc) fetch(ctx context.Context, sp jsonSpec) (promVal, string, error) {
	if sp.HA != "" {
		return s.haFetch(ctx, sp)
	}
	if sp.HeaderEnv != "" && !headerEnvRe.MatchString(sp.HeaderEnv) { // the CARD_ rule, where the secret is read
		return promVal{}, "bad header", nil
	}
	var hname, hval string
	if sp.HeaderEnv != "" {
		line := s.getenv(sp.HeaderEnv)
		i := strings.IndexByte(line, ':')
		if i < 0 {
			return promVal{}, "bad header", nil
		}
		hname, hval = strings.TrimSpace(line[:i]), strings.TrimSpace(line[i+1:])
		if hname == "" {
			return promVal{}, "bad header", nil
		}
	}
	body, code, err := s.get(ctx, sp.URL, hname, hval)
	if err != nil {
		return promVal{}, "", err
	}
	if code/100 != 2 {
		return promVal{}, "", errors.New("http " + strconv.Itoa(code))
	}
	if len(body) > jsonMaxBody {
		return promVal{}, "reply too big", nil
	}
	var doc any
	if json.Unmarshal(bytes.TrimPrefix(body, []byte("\xef\xbb\xbf")), &doc) != nil {
		return promVal{}, "", errors.New("bad json")
	}
	x, ok := walk(doc, sp.Path)
	if !ok {
		last := sp.Path[strings.LastIndexByte(sp.Path, '.')+1:]
		return promVal{}, clipBytes("no "+last, 23), nil
	}
	switch t := x.(type) {
	case float64:
		if !math.IsNaN(t) && !math.IsInf(t, 0) {
			return promVal{V: t, St: stOK}, "", nil
		}
	case bool:
		if t {
			return promVal{V: 1, St: stOK}, "", nil
		}
		return promVal{St: stOK}, "", nil
	case string:
		if t != "" { // an empty string would read as a number (S == "")
			return promVal{S: t, St: stOK}, "", nil
		}
	}
	last := sp.Path[strings.LastIndexByte(sp.Path, '.')+1:]
	return promVal{}, clipBytes("no "+last, 23), nil // null, object, array: nothing to show
}

// get fetches u within s.timeout with User-Agent deskpanel and, when hname isn't "", one header: the body (at most
// limit+1 bytes, so a caller can say "reply too big") and the status.
func (s *jsonSrc) get(ctx context.Context, u, hname, hval string) ([]byte, int, error) {
	return s.do(ctx, &s.hc, "GET", u, nil, hname, hval, jsonMaxBody)
}

// do sends one request within s.timeout; send, when not nil, is its JSON body (the lights' service calls).
func (s *jsonSrc) do(ctx context.Context, hc *http.Client, method, u string, send []byte, hname, hval string, limit int64) ([]byte, int, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	var rd io.Reader
	if send != nil {
		rd = bytes.NewReader(send)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", "deskpanel")
	if send != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if hname != "" {
		req.Header.Set(hname, hval)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, 0, err
	}
	return body, resp.StatusCode, nil
}

// walk follows a dotted path; a numeric part indexes an array.
func walk(doc any, path string) (any, bool) {
	if path == "" {
		return nil, false
	}
	for _, part := range strings.Split(path, ".") {
		switch d := doc.(type) {
		case map[string]any:
			v, ok := d[part]
			if !ok {
				return nil, false
			}
			doc = v
		case []any:
			i, err := strconv.Atoi(part)
			if err != nil || i < 0 || i >= len(d) {
				return nil, false
			}
			doc = d[i]
		default:
			return nil, false
		}
	}
	return doc, true
}

// noCrossRedirect keeps a CARD_* secret header from following a redirect to
// another host or down from https to http. Same-host redirects are fine.
func noCrossRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("too many redirects")
	}
	if req.URL.Host != via[0].URL.Host || (via[0].URL.Scheme == "https" && req.URL.Scheme != "https") {
		return errors.New("redirect refused")
	}
	return nil
}
