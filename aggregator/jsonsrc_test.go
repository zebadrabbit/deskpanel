package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// one drives a single cycle and waits for its fetches.
func (s *jsonSrc) one() { s.cycle(context.Background()); s.wg.Wait() }

func jsrv(t *testing.T, h http.HandlerFunc) string {
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	return s.URL
}

func newJ(env map[string]string) (*jsonSrc, *fakeClock) {
	s := newJSONSrc(func(k string) string { return env[k] })
	c := &fakeClock{t: time.Unix(1_000_000, 0)}
	s.setClock(c.now)
	return s, c
}

func TestWalk(t *testing.T) {
	var doc any = map[string]any{"a": map[string]any{"b": []any{map[string]any{"c": 7.0}}}}
	if v, ok := walk(doc, "a.b.0.c"); !ok || v != 7.0 {
		t.Fatal(v, ok)
	}
	for _, p := range []string{"a.x", "a.b.1.c", "a.b.-1", "a.b.x", "a.b.0.c.d", ""} {
		if _, ok := walk(doc, p); ok {
			t.Errorf("%q resolved", p)
		}
	}
}

func TestJSONValue(t *testing.T) {
	var mu sync.Mutex
	body, status := `{"c":12.5}`, 200
	url := jsrv(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		b, s := body, status
		mu.Unlock()
		w.WriteHeader(s)
		fmt.Fprint(w, b)
	})
	set := func(b string, s int) { mu.Lock(); body, status = b, s; mu.Unlock() }
	s, c := newJ(nil)
	sp := jsonSpec{URL: url, Path: "c", Every: 10}
	s.want([]jsonSpec{sp})
	k := sp.key()
	step := func(b string, st int) promVal {
		set(b, st)
		c.add(11 * time.Second)
		s.one()
		return s.value(k)
	}
	if v := step(`{"c":12.5}`, 200); v.St != stOK || v.V != 12.5 {
		t.Fatal(v)
	}
	if v := step(`{"c":true}`, 200); v.V != 1 || v.St != stOK {
		t.Fatal(v)
	}
	if v := step(`{"c":"running"}`, 200); v.S != "running" || v.V != 0 || v.St != stOK {
		t.Fatal(v)
	}
	if v := step(`{"d":1}`, 200); v.St != stNone || v.Why != "no c" {
		t.Fatal(v)
	}
	step(`{"c":3}`, 200)
	if v := step(`oops`, 500); v.St != stStale || v.V != 3 {
		t.Fatal(v)
	}
	if v := step(`<html>hi</html>`, 200); v.St != stStale || v.V != 3 {
		t.Fatal(v)
	}
	// never-good source
	s2, c2 := newJ(nil)
	s2.want([]jsonSpec{sp})
	for _, b := range []struct {
		b string
		s int
	}{{"x", 500}, {"<html>", 200}} {
		set(b.b, b.s)
		c2.add(11 * time.Second)
		s2.one()
		if v := s2.value(k); v.St != stNone || v.Why != "unreachable" {
			t.Fatal(v)
		}
	}
	set(`{"c":"`+strings.Repeat("x", 300<<10)+`"}`, 200)
	c2.add(11 * time.Second)
	s2.one()
	if v := s2.value(k); v.St != stNone || v.Why != "reply too big" {
		t.Fatal(v)
	}
}

func TestJSONHeader(t *testing.T) {
	var got atomic.Value
	var hits atomic.Int32
	url := jsrv(t, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		got.Store(r.Header.Get("X-Api-Key") + "|" + r.Header.Get("User-Agent"))
		fmt.Fprint(w, `{"c":1}`)
	})
	s, _ := newJ(map[string]string{"CARD_X": "X-Api-Key:  k1 ", "CARD_Y": "nocolon"})
	good := jsonSpec{URL: url, Path: "c", Every: 10, HeaderEnv: "CARD_X"}
	s.want([]jsonSpec{good})
	s.one()
	if g := got.Load(); g != "k1|deskpanel" {
		t.Fatal(g)
	}
	bad := jsonSpec{URL: url, Path: "c", Every: 10, HeaderEnv: "CARD_Y"}
	s.want([]jsonSpec{bad})
	n := hits.Load()
	s.one()
	if v := s.value(bad.key()); v.St != stNone || v.Why != "bad header" || hits.Load() != n {
		t.Fatal(v, hits.Load(), n)
	}
}

func TestJSONEvery(t *testing.T) {
	var a, b atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/a", func(w http.ResponseWriter, r *http.Request) { a.Add(1); fmt.Fprint(w, `{"c":1}`) })
	mux.HandleFunc("/b", func(w http.ResponseWriter, r *http.Request) { b.Add(1); fmt.Fprint(w, `{"c":1}`) })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	s, c := newJ(nil)
	s.want([]jsonSpec{{URL: srv.URL + "/a", Path: "c", Every: 10}, {URL: srv.URL + "/b", Path: "c", Every: 30}})
	for i := 0; i < 25; i++ {
		s.one()
		c.add(time.Second)
	}
	if a.Load() != 3 || b.Load() != 1 {
		t.Fatal(a.Load(), b.Load())
	}
}

func TestJSONSlowIsolated(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	mux.HandleFunc("/fast", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"c":9}`) })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	s := newJSONSrc(func(string) string { return "" })
	s.tick, s.timeout = 10*time.Millisecond, 300*time.Millisecond
	slow := jsonSpec{URL: srv.URL + "/slow", Path: "c", Every: 10}
	fast := jsonSpec{URL: srv.URL + "/fast", Path: "c", Every: 10}
	s.want([]jsonSpec{slow, fast})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.run(ctx); close(done) }()
	start := time.Now()
	for s.value(fast.key()).St != stOK {
		if time.Since(start) > 1500*time.Millisecond {
			t.Fatal("fast value late")
		}
		time.Sleep(5 * time.Millisecond)
	}
	t0 := time.Now()
	s.value(slow.key())
	if time.Since(t0) > 50*time.Millisecond {
		t.Fatal("value blocked")
	}
	time.Sleep(450 * time.Millisecond) // the slow one times out: unreachable
	if v := s.value(slow.key()); v.St != stNone || v.Why != "unreachable" {
		t.Fatal(v)
	}
	cancel()
	<-done
}

func TestJSONCancelRecordsNothing(t *testing.T) {
	started := make(chan struct{}, 1)
	url := jsrv(t, func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-r.Context().Done()
	})
	s := newJSONSrc(func(string) string { return "" })
	sp := jsonSpec{URL: url, Path: "c", Every: 10}
	s.want([]jsonSpec{sp})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.run(ctx); close(done) }()
	<-started
	cancel()
	<-done
	if v := s.value(sp.key()); v.Why != "no data" {
		t.Fatal(v)
	}
}

func TestJSONSeries(t *testing.T) {
	url := jsrv(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"c":5}`) })
	sp := jsonSpec{URL: url, Path: "c", Every: 10}
	s, c := newJ(nil)
	s.want([]jsonSpec{sp})
	for i := 0; i < 130; i++ { // fetches 30 s apart over 65 minutes
		s.one()
		c.add(30 * time.Second)
	}
	p := s.series(sp.key())
	if p.St != stOK || len(p.P) != 60 {
		t.Fatal(p.St, len(p.P))
	}
	// 3 minutes of 30 s fetches: one sample a minute
	s2, c2 := newJ(nil)
	s2.want([]jsonSpec{sp})
	for i := 0; i < 6; i++ {
		s2.one()
		c2.add(30 * time.Second)
	}
	if n := len(s2.series(sp.key()).P); n != 3 {
		t.Fatal(n)
	}
	// older than an hour is dropped (a long gap between fetches)
	s2.want([]jsonSpec{sp})
	s2.one()
	c2.add(2 * time.Hour)
	s2.one()
	if n := len(s2.series(sp.key()).P); n != 1 {
		t.Fatal(n)
	}
	// strings record nothing
	surl := jsrv(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"c":"up"}`) })
	s3, _ := newJ(nil)
	ss := jsonSpec{URL: surl, Path: "c", Every: 10}
	s3.want([]jsonSpec{ss})
	s3.one()
	if p := s3.series(ss.key()); p.St != stNone || len(p.P) != 0 {
		t.Fatal(p)
	}
}

func TestJSONWantDrops(t *testing.T) {
	gate := make(chan struct{})
	url := jsrv(t, func(w http.ResponseWriter, r *http.Request) {
		<-gate
		fmt.Fprint(w, `{"c":1}`)
	})
	s, _ := newJ(nil)
	sp := jsonSpec{URL: url, Path: "c", Every: 10}
	s.want([]jsonSpec{sp})
	s.cycle(context.Background()) // in flight, blocked on the gate
	s.want(nil)
	close(gate)
	s.wg.Wait()
	if v := s.value(sp.key()); v.St != stNone || v.Why != "no data" {
		t.Fatal(v)
	}
	if len(s.series(sp.key()).P) != 0 {
		t.Fatal("late result stored")
	}
}

func TestJSONRedirect(t *testing.T) {
	var leaked atomic.Int32
	other := jsrv(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "" {
			leaked.Add(1)
		}
		fmt.Fprint(w, `{"c":1}`)
	})
	mux := http.NewServeMux()
	mux.HandleFunc("/out", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, other, 302) })
	mux.HandleFunc("/in", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/ok", 302) })
	mux.HandleFunc("/ok", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"c":2}`) })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	s, c := newJ(map[string]string{"CARD_X": "X-Api-Key: k1"})
	out := jsonSpec{URL: srv.URL + "/out", Path: "c", Every: 10, HeaderEnv: "CARD_X"}
	in := jsonSpec{URL: srv.URL + "/in", Path: "c", Every: 10, HeaderEnv: "CARD_X"}
	s.want([]jsonSpec{out, in})
	s.one()
	if v := s.value(out.key()); v.St != stNone || v.Why != "unreachable" || leaked.Load() != 0 {
		t.Fatal(v, leaked.Load())
	}
	if v := s.value(in.key()); v.St != stOK || v.V != 2 {
		t.Fatal(v)
	}
	_ = c
}

func TestJSONKinds(t *testing.T) {
	var mu sync.Mutex
	body := `{"c":4}`
	url := jsrv(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		fmt.Fprint(w, body)
	})
	s, c := newJ(nil)
	sp := jsonSpec{URL: url, Path: "c", Every: 10}
	s.want([]jsonSpec{sp})
	step := func(b string) {
		mu.Lock()
		body = b
		mu.Unlock()
		c.add(70 * time.Second)
		s.one()
	}
	step(`{"c":4}`)
	if n := len(s.series(sp.key()).P); n != 1 {
		t.Fatal(n)
	}
	step(`{"c":""}`) // empty string is no value, not the number 0
	if v := s.value(sp.key()); v.St != stNone || v.Why != "no c" {
		t.Fatal(v)
	}
	step(`{"c":"up"}`) // number -> string clears the plot
	if p := s.series(sp.key()); len(p.P) != 0 {
		t.Fatal(p)
	}
	step(`{"c":5}`)
	step(`{"c":6}`)
	if n := len(s.series(sp.key()).P); n != 2 {
		t.Fatal(n)
	}
}

func TestJSONBodyBoundary(t *testing.T) {
	var pad atomic.Int32
	url := jsrv(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"c":"`+strings.Repeat("x", jsonMaxBody-8+int(pad.Load()))+`"}`)
	})
	s, c := newJ(nil)
	sp := jsonSpec{URL: url, Path: "c", Every: 10}
	s.want([]jsonSpec{sp})
	s.one()
	if v := s.value(sp.key()); v.St != stOK || len(v.S) != jsonMaxBody-8 {
		t.Fatal(v.St, v.Why)
	}
	pad.Store(1)
	c.add(11 * time.Second)
	s.one()
	if v := s.value(sp.key()); v.St != stNone || v.Why != "reply too big" {
		t.Fatal(v.St, v.Why)
	}
}

func TestJSONHeaderEnvRule(t *testing.T) {
	var hits atomic.Int32
	url := jsrv(t, func(w http.ResponseWriter, r *http.Request) { hits.Add(1); fmt.Fprint(w, `{"c":1}`) })
	s, _ := newJ(map[string]string{"HOME": "X-Key: secret"})
	sp := jsonSpec{URL: url, Path: "c", Every: 10, HeaderEnv: "HOME"}
	s.want([]jsonSpec{sp})
	s.one()
	if v := s.value(sp.key()); v.Why != "bad header" || hits.Load() != 0 {
		t.Fatal(v, hits.Load())
	}
}

func TestJSONDroppedWhileQueued(t *testing.T) {
	var hits atomic.Int32
	url := jsrv(t, func(w http.ResponseWriter, r *http.Request) { hits.Add(1); fmt.Fprint(w, `{"c":1}`) })
	s, _ := newJ(nil)
	for i := 0; i < cap(s.sem); i++ {
		s.sem <- struct{}{}
	}
	s.want([]jsonSpec{{URL: url, Path: "c", Every: 10}})
	s.cycle(context.Background()) // waits for a slot
	s.want(nil)
	for i := 0; i < cap(s.sem); i++ {
		<-s.sem
	}
	s.wg.Wait()
	if hits.Load() != 0 {
		t.Fatal("fetched after being dropped")
	}
}

func TestJSONBOM(t *testing.T) {
	url := jsrv(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "\xef\xbb\xbf{\"c\":4}") })
	s, _ := newJ(nil)
	sp := jsonSpec{URL: url, Path: "c", Every: 10}
	s.want([]jsonSpec{sp})
	s.one()
	if v := s.value(sp.key()); v.St != stOK || v.V != 4 {
		t.Fatal(v)
	}
}
