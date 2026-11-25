package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeProm answers every API call with the current body/status.
type fakeProm struct {
	mu     sync.Mutex
	body   string
	status int
	last   *http.Request
	srv    *httptest.Server
}

func newFake(t *testing.T, body string) *fakeProm {
	f := &fakeProm{body: body, status: 200}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.last = r
		b, s := f.body, f.status
		f.mu.Unlock()
		w.WriteHeader(s)
		fmt.Fprint(w, b)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeProm) set(body string, status int) {
	f.mu.Lock()
	f.body, f.status = body, status
	f.mu.Unlock()
}

func vec(vals ...string) string {
	var items []string
	for _, v := range vals {
		items = append(items, fmt.Sprintf(`{"metric":{},"value":[1,"%s"]}`, v))
	}
	return `{"status":"success","data":{"resultType":"vector","result":[` + strings.Join(items, ",") + `]}}`
}

func TestPromInstant(t *testing.T) {
	f := newFake(t, "")
	p := newProm(f.srv.URL + "/") // trailing slash is fine
	p.want([]string{"q"}, nil)
	ctx := context.Background()
	for _, c := range []struct {
		body, why string
		st        state
		v         float64
	}{
		{vec("39.76"), "", stOK, 39.76},
		{`{"status":"success","data":{"resultType":"scalar","result":[1,"2"]}}`, "", stOK, 2},
		{vec("1", "2"), "returns 2 series", stNone, 0},
		{vec(), "no data", stNone, 0},
		{vec("NaN"), "no data", stNone, 0},
		{vec("+Inf"), "no data", stNone, 0},
		{vec("-Inf"), "no data", stNone, 0},
	} {
		f.set(c.body, 200)
		p.refreshValue(ctx, "q")
		got := p.value("q")
		if got.St != c.st || got.Why != c.why || (c.st == stOK && got.V != c.v) {
			t.Errorf("%s: got %+v", c.body, got)
		}
	}
	f.set(vec("7"), 200)
	p.refreshValue(ctx, "q")
	for _, bad := range []struct {
		body   string
		status int
	}{{"boom", 500}, {`{"status":"error","error":"x"}`, 200}} {
		f.set(bad.body, bad.status)
		p.refreshValue(ctx, "q")
		if got := p.value("q"); got.St != stStale || got.V != 7 {
			t.Errorf("%v: got %+v", bad, got)
		}
	}
	// recovers
	f.set(vec("8"), 200)
	p.refreshValue(ctx, "q")
	if got := p.value("q"); got.St != stOK || got.V != 8 {
		t.Errorf("recover: %+v", got)
	}
	// a query Prometheus rejects (400 bad_data: a typo; 422: can't execute) is none, even after a good value
	for _, status := range []int{400, 422} {
		f.set(`{"status":"error","errorType":"bad_data","error":"parse error"}`, status)
		p.refreshValue(ctx, "q")
		if got := p.value("q"); got.St != stNone || got.Why != "bad query" {
			t.Errorf("HTTP %d: got %+v", status, got)
		}
	}
}

func TestPromInstantNeverGood(t *testing.T) {
	f := newFake(t, "x")
	f.set("x", 500)
	p := newProm(f.srv.URL)
	p.want([]string{"q"}, nil)
	p.refreshValue(context.Background(), "q")
	if got := p.value("q"); got.St != stNone || got.Why != "unreachable" {
		t.Errorf("got %+v", got)
	}
}

func TestPromRequestURL(t *testing.T) {
	f := newFake(t, vec("1"))
	p := newProm(f.srv.URL)
	p.want([]string{`a+b{x="y"}`}, []string{`a+b{x="y"}`})
	p.setClock(func() time.Time { return time.Unix(36000+17, 0) })
	p.refreshValue(context.Background(), `a+b{x="y"}`)
	q := f.last.URL.Query()
	if f.last.URL.Path != "/api/v1/query" || q.Get("query") != `a+b{x="y"}` || q.Has("time") {
		t.Errorf("instant: %v", f.last.URL)
	}
	if !strings.Contains(f.last.URL.RawQuery, "query=a%2Bb") {
		t.Errorf("not encoded: %s", f.last.URL.RawQuery)
	}
	p.refreshSeries(context.Background(), `a+b{x="y"}`)
	q = f.last.URL.Query()
	if f.last.URL.Path != "/api/v1/query_range" || q.Get("step") != "60" || q.Get("end") != "36000" || q.Get("start") != "32400" {
		t.Errorf("range: %v", f.last.URL)
	}
}

func matrix(ts []int, vals []string) string {
	var pts []string
	for i, t := range ts {
		pts = append(pts, fmt.Sprintf(`[%d,"%s"]`, t, vals[i]))
	}
	return `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{},"values":[` + strings.Join(pts, ",") + `]}]}}`
}

func steps(n int) (ts []int, vals []string) {
	for i := 0; i < n; i++ {
		ts = append(ts, 1000+60*i)
		vals = append(vals, fmt.Sprint(i))
	}
	return
}

func TestPromRange(t *testing.T) {
	f := newFake(t, "")
	p := newProm(f.srv.URL)
	p.want(nil, []string{"q"})
	ctx := context.Background()

	ts, vs := steps(61)
	f.set(matrix(ts, vs), 200)
	p.refreshSeries(ctx, "q")
	s := p.series("q")
	if s.St != stOK || len(s.P) != 60 || s.P[59] != 60 || s.P[0] != 1 {
		t.Errorf("61 points: %+v", s)
	}

	// samples at steps 0,1,5 -> steps 2..4 filled with 1
	f.set(matrix([]int{1000, 1060, 1300}, []string{"0", "1", "5"}), 200)
	p.refreshSeries(ctx, "q")
	if s := p.series("q"); fmt.Sprint(s.P) != "[0 1 1 1 1 5]" {
		t.Errorf("gap: %v", s.P)
	}

	ts, vs = steps(10)
	f.set(matrix(ts, vs), 200)
	p.refreshSeries(ctx, "q")
	if s := p.series("q"); len(s.P) != 10 {
		t.Errorf("10 points: %v", s.P)
	}

	// 5xx keeps the points, stale
	f.set("x", 500)
	p.refreshSeries(ctx, "q")
	if s := p.series("q"); s.St != stStale || len(s.P) != 10 {
		t.Errorf("stale: %+v", s)
	}
	// a rejected query drops the points: none, "bad query"
	f.set(`{"status":"error","errorType":"bad_data","error":"parse error"}`, 400)
	p.refreshSeries(ctx, "q")
	if s := p.series("q"); s.St != stNone || s.Why != "bad query" || len(s.P) != 0 {
		t.Errorf("bad query: %+v", s)
	}

	f.set(`{"status":"success","data":{"resultType":"matrix","result":[{"values":[]},{"values":[]}]}}`, 200)
	p.refreshSeries(ctx, "q")
	if s := p.series("q"); s.St != stNone || s.Why != "returns 2 series" {
		t.Errorf("two series: %+v", s)
	}
	f.set(`{"status":"success","data":{"resultType":"matrix","result":[]}}`, 200)
	p.refreshSeries(ctx, "q")
	if s := p.series("q"); s.St != stNone || s.Why != "no data" {
		t.Errorf("empty: %+v", s)
	}
}

func TestPromHugeBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"success","data":"`))
		w.Write([]byte(strings.Repeat("a", 10<<20)))
	}))
	defer srv.Close()
	p := newProm(srv.URL)
	if _, _, err := p.fetchValue(context.Background(), "q"); err == nil {
		t.Error("10 MB body accepted")
	}
}

func TestPromTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(6 * time.Second):
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	p := newProm(srv.URL)
	p.want([]string{"q"}, nil)
	done := make(chan struct{})
	start := time.Now()
	go func() { p.refreshValue(context.Background(), "q"); close(done) }()
	time.Sleep(100 * time.Millisecond)
	t0 := time.Now()
	if got := p.value("q"); got.St != stNone {
		t.Errorf("got %+v", got)
	}
	if time.Since(t0) > 50*time.Millisecond {
		t.Error("value() blocked")
	}
	<-done
	if d := time.Since(start); d > 5500*time.Millisecond {
		t.Errorf("took %v", d)
	}
	if got := p.value("q"); got.St != stNone || got.Why != "unreachable" {
		t.Errorf("after: %+v", got)
	}
}

func TestPromWant(t *testing.T) {
	f := newFake(t, vec("3"))
	p := newProm(f.srv.URL)
	p.tick = 10 * time.Millisecond
	p.want([]string{"old", "old"}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { p.run(ctx); close(done) }()
	waitFor := func(q string) {
		for i := 0; i < 100; i++ {
			if p.value(q).St == stOK {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("%s never fetched", q)
	}
	waitFor("old")
	p.want([]string{"new"}, nil)
	if p.value("old").St != stNone {
		t.Error("old not dropped")
	}
	waitFor("new")
	p.want(nil, nil)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("run did not stop")
	}
}

func TestPromFreshBeforeHanging(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("query") == "hang" {
			select {
			case <-time.After(5 * time.Second):
			case <-r.Context().Done():
			}
			return
		}
		fmt.Fprint(w, vec("1"))
	}))
	defer srv.Close()
	p := newProm(srv.URL)
	p.tick, p.timeout, p.instEvery = 10*time.Millisecond, 300*time.Millisecond, 0
	p.want([]string{"hang"}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.run(ctx)
	start := time.Now()
	time.Sleep(50 * time.Millisecond) // cycle 1 is blocked on "hang" until ~300 ms
	p.want([]string{"hang", "fast"}, nil)
	// new order: fast lands in cycle 2 at ~310 ms; old order: behind hang, ~600 ms
	for p.value("fast").St != stOK {
		if time.Since(start) > 450*time.Millisecond {
			t.Fatal("fast query starved behind hanging one")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestPromCancelRecordsNothing(t *testing.T) {
	f := newFake(t, vec("7"))
	p := newProm(f.srv.URL)
	p.want([]string{"q"}, []string{"q"})
	p.refreshValue(context.Background(), "q")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f.set("x", 500)
	p.refreshValue(ctx, "q")
	p.refreshSeries(ctx, "q")
	if got := p.value("q"); got.St != stOK || got.V != 7 {
		t.Errorf("value: %+v", got)
	}
	if s := p.series("q"); s.St != stNone || s.Why != "no data" {
		t.Errorf("series: %+v", s)
	}
}
