package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const edTok = "editor-token-123"

const edLayout = `{"tabs":[{"name":"Home","cards":[
 {"id":"gpu-1","type":"gpu","at":[1,1],"size":"2x3","src":"workstation"},
 {"id":"pool-1","type":"pool","at":[3,1],"size":"1x2","src":"tank"}]}]}`

type edEnv struct {
	t     *testing.T
	srv   *httptest.Server
	path  string
	e     *editor
	waits []time.Duration
}

func newEd(t *testing.T, token string) *edEnv {
	st, path, _ := storeFor(t)
	st.refresh(found{})
	env := &edEnv{t: t, path: path}
	env.e = newEditor(st)
	env.e.wait = func(_ context.Context, d time.Duration) { env.waits = append(env.waits, d) }
	mux := http.NewServeMux()
	addLayoutRoute(mux, "panel-token-abc", st)
	addEditorRoutes(mux, token, env.e)
	env.srv = httptest.NewServer(mux)
	t.Cleanup(env.srv.Close)
	return env
}

func (e *edEnv) do(method, path, bearer string, hdr map[string]string, body string) (*http.Response, string) {
	req, _ := http.NewRequest(method, e.srv.URL+path, strings.NewReader(body))
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res, string(b)
}

func (e *edEnv) get(path string) (*http.Response, string) { return e.do("GET", path, edTok, nil, "") }
func (e *edEnv) put(etag, body string) (*http.Response, string) {
	return e.do("PUT", "/api/v1/editor/layout", edTok, map[string]string{"If-Match": etag}, body)
}

func TestEditorOffWithoutToken(t *testing.T) {
	for _, tok := range []string{"", "elevenbytes"} {
		e := newEd(t, tok)
		for _, p := range []string{"/editor/", "/editor", "/api/v1/editor/ping"} {
			if res, _ := e.do("GET", p, tok, nil, ""); res.StatusCode != 404 {
				t.Errorf("token %q %s: %d, want 404", tok, p, res.StatusCode)
			}
		}
	}
}

func TestEditorAuth(t *testing.T) {
	e := newEd(t, edTok)
	for _, b := range []string{"", "wrong-token-1234", "panel-token-abc"} {
		if res, _ := e.do("GET", "/api/v1/editor/ping", b, nil, ""); res.StatusCode != 401 {
			t.Errorf("bearer %q: %d, want 401", b, res.StatusCode)
		}
	}
	if len(e.waits) != 3 || e.waits[0] != time.Second {
		t.Errorf("waits = %v, want three of 1s", e.waits)
	}
	e.waits = nil
	if res, _ := e.get("/api/v1/editor/ping"); res.StatusCode != 204 {
		t.Errorf("right token: %d, want 204", res.StatusCode)
	}
	if len(e.waits) != 0 {
		t.Errorf("right token waited: %v", e.waits)
	}
	if res, _ := e.do("GET", "/api/v1/layout", edTok, nil, ""); res.StatusCode != 401 {
		t.Errorf("editor token on /api/v1/layout: %d, want 401", res.StatusCode)
	}
	if res, _ := e.do("GET", "/api/v1/layout", "panel-token-abc", nil, ""); res.StatusCode != 200 {
		t.Errorf("panel token on /api/v1/layout: %d, want 200", res.StatusCode)
	}
}

func TestEditorLoadDefault(t *testing.T) {
	e := newEd(t, edTok)
	res, body := e.get("/api/v1/editor/layout")
	var got struct {
		Layout   Layout `json:"layout"`
		FromFile bool   `json:"from_file"`
		Error    string `json:"error"`
	}
	if res.StatusCode != 200 || json.Unmarshal([]byte(body), &got) != nil {
		t.Fatalf("%d %s", res.StatusCode, body)
	}
	if got.FromFile || res.Header.Get("ETag") != `"none"` || got.Error != "" || len(got.Layout.Tabs) == 0 {
		t.Errorf("from_file %v etag %q error %q tabs %d", got.FromFile, res.Header.Get("ETag"), got.Error, len(got.Layout.Tabs))
	}
}

// The editor serves the layout.json source form, not the panel document.
func TestEditorLoadSourceForm(t *testing.T) {
	e := newEd(t, edTok)
	src := `{"tour":{"idle_min":2},"tabs":[{"name":"Home","cards":[{"id":"gpu-1","type":"gpu","at":[1,1],"size":"2x3","src":"workstation"}]}]}`
	os.WriteFile(e.path, []byte(src), 0o644)
	_, body := e.get("/api/v1/editor/layout")
	var got struct{ Layout map[string]any }
	json.Unmarshal([]byte(body), &got)
	tour, _ := got.Layout["tour"].(map[string]any)
	if len(tour) != 1 || tour["idle_min"] != float64(2) {
		t.Errorf("tour = %v, want only idle_min 2", tour)
	}
	card := got.Layout["tabs"].([]any)[0].(map[string]any)["cards"].([]any)[0].(map[string]any)
	if card["size"] != "2x3" {
		t.Errorf("size = %v, want \"2x3\"", card["size"])
	}
	if _, bad := card["parsed"]; bad {
		t.Error("parsed leaked")
	}
	// and it parses back
	var raw struct{ Layout json.RawMessage }
	json.Unmarshal([]byte(body), &raw)
	if _, err := parseLayout(raw.Layout); err != nil {
		t.Errorf("served layout does not parse: %v", err)
	}
}

func TestEditorLoadBadFile(t *testing.T) {
	e := newEd(t, edTok)
	os.WriteFile(e.path, []byte(`{"tabs": [oops`), 0o644)
	res, body := e.get("/api/v1/editor/layout")
	var got struct {
		FromFile bool   `json:"from_file"`
		Error    string `json:"error"`
	}
	json.Unmarshal([]byte(body), &got)
	if res.StatusCode != 200 || !got.FromFile || !strings.Contains(got.Error, "layout.json line") || res.Header.Get("ETag") == `"none"` {
		t.Errorf("%d %s etag %q", res.StatusCode, body, res.Header.Get("ETag"))
	}
}

func TestEditorSaveRoundTrip(t *testing.T) {
	e := newEd(t, edTok)
	res, body := e.put(`"none"`, edLayout)
	if res.StatusCode != 200 {
		t.Fatalf("create: %d %s", res.StatusCode, body)
	}
	first, err := os.ReadFile(e.path)
	if err != nil {
		t.Fatal(err)
	}
	var ok struct{ ETag string }
	json.Unmarshal([]byte(body), &ok)
	res, body = e.get("/api/v1/editor/layout")
	var got struct {
		Layout   json.RawMessage
		FromFile bool `json:"from_file"`
	}
	json.Unmarshal([]byte(body), &got)
	if !got.FromFile || res.Header.Get("ETag") != ok.ETag || ok.ETag == `"none"` || !strings.HasPrefix(ok.ETag, `"`) {
		t.Errorf("etag %q vs %q, from_file %v", res.Header.Get("ETag"), ok.ETag, got.FromFile)
	}
	l1, err1 := parseLayout(got.Layout)
	l2, err2 := parseLayout([]byte(edLayout))
	if err1 != nil || err2 != nil || string(handLayout(l1)) != string(handLayout(l2)) {
		t.Errorf("loaded layout differs: %v %v\n%s", err1, err2, got.Layout)
	}
	if _, err := os.Stat(e.path + ".bak"); err == nil {
		t.Error("first save made a .bak with nothing to back up")
	}
	// second save, with the new etag
	second := strings.Replace(edLayout, `"tank"`, `"backup"`, 1)
	if res, body = e.put(ok.ETag, second); res.StatusCode != 200 {
		t.Fatalf("second: %d %s", res.StatusCode, body)
	}
	bak, err := os.ReadFile(e.path + ".bak")
	if err != nil || !bytes.Equal(bak, first) {
		t.Errorf("bak differs from the first file: %v", err)
	}
	now, _ := os.ReadFile(e.path)
	if !strings.Contains(string(now), `"backup"`) {
		t.Errorf("file not rewritten:\n%s", now)
	}
	if _, err := os.Stat(e.path + ".tmp"); err == nil {
		t.Error(".tmp left")
	}
}

func TestEditorSaveStale(t *testing.T) {
	e := newEd(t, edTok)
	e.put(`"none"`, edLayout)
	res, _ := e.get("/api/v1/editor/layout")
	etag := res.Header.Get("ETag")
	hand := []byte(strings.Replace(edLayout, `"tank"`, `"hand"`, 1))
	os.WriteFile(e.path, hand, 0o644)
	res, body := e.put(etag, edLayout)
	if res.StatusCode != 412 || !strings.Contains(body, "layout.json changed on disk; reload") {
		t.Errorf("%d %s", res.StatusCode, body)
	}
	if now, _ := os.ReadFile(e.path); !bytes.Equal(now, hand) {
		t.Error("file changed")
	}
	if res, _ = e.put("", edLayout); res.StatusCode != 412 {
		t.Errorf("no If-Match: %d, want 412", res.StatusCode)
	}
	if _, err := os.Stat(e.path + ".bak"); err == nil {
		t.Error("stale save wrote a .bak")
	}
}

func TestEditorSaveInvalid(t *testing.T) {
	e := newEd(t, edTok)
	e.put(`"none"`, edLayout)
	before, _ := os.ReadFile(e.path)
	res, _ := e.get("/api/v1/editor/layout")
	// the bad card is the second one
	bad := `{"tabs":[{"name":"Home","cards":[
 {"id":"gpu-1","type":"gpu","at":[1,1],"size":"2x3","src":"workstation"},
 {"id":"gpu-2","type":"gpu","at":[3,1],"size":"3x3","src":"server"}]}]}`
	res, body := e.put(res.Header.Get("ETag"), bad)
	var got struct {
		Message   string
		Tab, Card int
	}
	json.Unmarshal([]byte(body), &got)
	if res.StatusCode != 422 || !strings.Contains(got.Message, "size 3x3 not allowed for gpu") || got.Tab != 0 || got.Card != 1 {
		t.Errorf("%d %s", res.StatusCode, body)
	}
	if strings.Contains(got.Message, "layout.json line") {
		t.Errorf("prefix not stripped: %q", got.Message)
	}
	if now, _ := os.ReadFile(e.path); !bytes.Equal(now, before) {
		t.Error("file changed")
	}
}

// A layout too big for the panel is refused as a whole: no tab or card to point at.
func TestEditorSaveTooBigForPanel(t *testing.T) {
	e := newEd(t, edTok)
	res, body := e.put(`"none"`, string(hugeLayout()))
	var got struct {
		Message   string
		Tab, Card int
	}
	json.Unmarshal([]byte(body), &got)
	if res.StatusCode != 422 || !strings.HasPrefix(got.Message, "too big for the panel (") || !strings.HasSuffix(got.Message, "): use fewer cards") ||
		got.Tab != -1 || got.Card != -1 {
		t.Errorf("%d %s", res.StatusCode, body)
	}
	if _, err := os.Stat(e.path); err == nil {
		t.Error("written")
	}
}

// Just started with a bad layout.json: nothing is served yet, so there is no layout to edit.
func TestEditorLoadNotReadyWithBadFile(t *testing.T) {
	st, path, _ := storeFor(t)
	writeLayout(t, path, `{"tabs": [oops`)
	st.refresh(found{Waiting: true}) // the collectors haven't run: no default either
	if st.doc() != nil {
		t.Fatal("something is served")
	}
	mux := http.NewServeMux()
	addEditorRoutes(mux, edTok, newEditor(st))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	req, _ := http.NewRequest("GET", srv.URL+"/api/v1/editor/layout", nil)
	req.Header.Set("Authorization", "Bearer "+edTok)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 503 || !strings.Contains(string(b), `"layout not ready yet"`) {
		t.Errorf("%d %s", res.StatusCode, b)
	}
}

// Wrong guesses wait one at a time (one a second in all), one given up stops queueing, and the right token never waits.
func TestEditorGuessesOneAtATime(t *testing.T) {
	e := newEd(t, edTok)
	var active, most, calls atomic.Int32
	release := make(chan struct{})
	e.e.wait = func(ctx context.Context, d time.Duration) {
		calls.Add(1)
		n := active.Add(1)
		for m := most.Load(); n > m && !most.CompareAndSwap(m, n); m = most.Load() {
		}
		select {
		case <-release:
		case <-ctx.Done():
		}
		active.Add(-1)
	}
	guess := func(ctx context.Context) (int, error) {
		req, _ := http.NewRequestWithContext(ctx, "GET", e.srv.URL+"/api/v1/editor/ping", nil)
		req.Header.Set("Authorization", "Bearer wrong-token-1234")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			return 0, err
		}
		res.Body.Close()
		return res.StatusCode, nil
	}
	codes := make(chan int, 4)
	for range 4 {
		go func() { c, _ := guess(context.Background()); codes <- c }()
	}
	time.Sleep(100 * time.Millisecond)
	if calls.Load() != 1 {
		t.Errorf("%d guesses waiting at once", calls.Load())
	}
	start := time.Now()
	if res, _ := e.get("/api/v1/editor/ping"); res.StatusCode != 204 || time.Since(start) > 500*time.Millisecond {
		t.Errorf("right token: %d after %v", res.StatusCode, time.Since(start))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	if _, err := guess(ctx); err == nil {
		t.Error("a guess given up still got an answer")
	}
	cancel()
	time.Sleep(100 * time.Millisecond) // its handler has gone
	for range 4 {
		release <- struct{}{}
		if c := <-codes; c != 401 {
			t.Errorf("guess: %d", c)
		}
	}
	if most.Load() != 1 || calls.Load() != 4 {
		t.Errorf("at most %d waiting at once, %d waits (the given-up guess must not wait)", most.Load(), calls.Load())
	}
}

// A write that fails half-way leaves the old file, no .tmp, and names the cause.
func TestEditorSaveReadOnly(t *testing.T) {
	e := newEd(t, edTok)
	e.put(`"none"`, edLayout)
	before, _ := os.ReadFile(e.path)
	res, _ := e.get("/api/v1/editor/layout")
	e.e.write = func(path string, data []byte) error {
		os.WriteFile(path, data[:5], 0o644) // half-way
		return &os.PathError{Op: "open", Path: path, Err: errors.New("read-only file system")}
	}
	res, body := e.put(res.Header.Get("ETag"), strings.Replace(edLayout, `"tank"`, `"backup"`, 1))
	if res.StatusCode != 500 || !strings.Contains(body, "Mount config without :ro") || !strings.Contains(body, "read-only file system") || !strings.Contains(body, e.path) {
		t.Errorf("%d %s", res.StatusCode, body)
	}
	if now, _ := os.ReadFile(e.path); !bytes.Equal(now, before) {
		t.Error("old file not intact")
	}
	if _, err := os.Stat(e.path + ".tmp"); err == nil {
		t.Error(".tmp left")
	}
}

// With chmod working (not root), the real writer fails the same way.
func TestEditorSaveReadOnlyDir(t *testing.T) {
	e := newEd(t, edTok)
	e.put(`"none"`, edLayout)
	before, _ := os.ReadFile(e.path)
	res, _ := e.get("/api/v1/editor/layout")
	dir := filepath.Dir(e.path)
	os.Chmod(dir, 0o555)
	defer os.Chmod(dir, 0o755)
	if f, err := os.Create(filepath.Join(dir, "probe")); err == nil {
		f.Close()
		t.Skip("chmod has no effect (root)")
	}
	res, body := e.put(res.Header.Get("ETag"), strings.Replace(edLayout, `"tank"`, `"backup"`, 1))
	if res.StatusCode != 500 || !strings.Contains(body, "without :ro") {
		t.Errorf("%d %s", res.StatusCode, body)
	}
	if now, _ := os.ReadFile(e.path); !bytes.Equal(now, before) {
		t.Error("old file not intact")
	}
}

func TestEditorBodyCap(t *testing.T) {
	e := newEd(t, edTok)
	res, _ := e.put(`"none"`, strings.Repeat(" ", 300<<10))
	if res.StatusCode != 413 {
		t.Errorf("%d, want 413", res.StatusCode)
	}
}

func TestEditorStaticHeaders(t *testing.T) {
	e := newEd(t, edTok)
	res, _ := e.do("GET", "/editor/", "", nil, "")
	if res.StatusCode != 200 {
		t.Fatalf("%d", res.StatusCode)
	}
	h := res.Header
	if h.Get("Cache-Control") != "no-cache" || h.Get("X-Content-Type-Options") != "nosniff" ||
		h.Get("Content-Security-Policy") != "default-src 'self'; img-src 'self' data:; frame-ancestors 'none'" {
		t.Errorf("headers %v", h)
	}
	nr := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	r2, _ := nr.Get(e.srv.URL + "/editor")
	if r2.StatusCode/100 != 3 || r2.Header.Get("Location") != "/editor/" {
		t.Errorf("/editor: %d %q", r2.StatusCode, r2.Header.Get("Location"))
	}
}

// srcEd is an editor over the demo collectors, with no Prometheus, no Home Assistant and no CARD_* variables until a
// test sets them.
func srcEd(t *testing.T) *edEnv {
	e := newEd(t, edTok)
	e.e.slots = demoSlots(time.Unix(editorDemoNow, 0))
	e.e.json = newJSONSrc(func(string) string { return "" })
	e.e.environ = func() []string { return nil }
	return e
}

type srcDoc struct {
	Limits map[string]int `json:"limits"`
	Types  map[string]struct {
		Sizes      []string `json:"sizes"`
		Src        string   `json:"src"`
		General    bool     `json:"general"`
		Tap        bool     `json:"tap"`
		DefaultTap string   `json:"default_tap"`
	} `json:"types"`
	Shortcuts []struct {
		Name     string   `json:"name"`
		Types    []string `json:"types"`
		Instance string   `json:"instance"`
		Arg      string   `json:"arg"`
	} `json:"shortcuts"`
	Slots struct {
		Gauge   []string `json:"gauge"`
		Line1   []string `json:"line1"`
		As      []string `json:"as"`
		Line2   []string `json:"line2"`
		NoBar   []string `json:"no_bar"`
		Default gpuSlots `json:"default"`
	} `json:"slots"`
	Tour       Tour                         `json:"tour"`
	Taps       []string                     `json:"taps"`
	Units      []string                     `json:"units"`
	Dots       []string                     `json:"dots"`
	GPUs       []struct{ Src, Name string } `json:"gpus"`
	Pools      []struct{ Name string }      `json:"pools"`
	Prometheus promListing                  `json:"prometheus"`
	HA         haListing                    `json:"ha"`
	CardEnv    []string                     `json:"card_env"`
}

func (e *edEnv) sources() (srcDoc, string) {
	e.t.Helper()
	res, body := e.get("/api/v1/editor/sources")
	var d srcDoc
	if res.StatusCode != 200 || json.Unmarshal([]byte(body), &d) != nil {
		e.t.Fatalf("sources: %d %s", res.StatusCode, body)
	}
	return d, body
}

// edLay is a one-tab layout of cards; edCard a valid card of typ at size (a general one with a value), plus extra.
func edLay(cards ...string) []byte {
	return []byte(`{"tabs":[{"name":"A","cards":[` + strings.Join(cards, ",") + `]}]}`)
}

func edCard(typ, size, extra string) string {
	c := fmt.Sprintf(`{"id":"x","type":%q,"at":[1,1],"size":%q`, typ, size)
	switch {
	case typ == "status":
		c += `,"value":{"prometheus":"up"}`
	case contains(generalTypes, typ):
		c += `,"value":"cpu"`
	}
	return c + extra + "}"
}

func parses(b []byte) bool { _, err := parseLayout(b); return err == nil }

// Every table the page reads is the one parseLayout enforces.
func TestSourcesTables(t *testing.T) {
	d, _ := srcEd(t).sources()
	if len(d.Types) != len(cardSizes) {
		t.Errorf("%d types, parseLayout knows %d", len(d.Types), len(cardSizes))
	}
	for typ, sizes := range cardSizes {
		tt := d.Types[typ]
		if !slices.Equal(tt.Sizes, sizes) || tt.General != contains(generalTypes, typ) || !slices.Contains([]string{"gpu", "pool", "dots", "none"}, tt.Src) {
			t.Errorf("%s: %+v", typ, tt)
		}
		for _, size := range tt.Sizes {
			if !parses(edLay(edCard(typ, size, ""))) {
				t.Errorf("%s %s refused", typ, size)
			}
		}
		size := tt.Sizes[0]
		if parses(edLay(edCard(typ, size, `,"tap":"notifications"`))) != tt.Tap {
			t.Errorf("%s: tap %v, parseLayout disagrees", typ, tt.Tap)
		}
		if tt.DefaultTap != defaultTap(typ) || !parses(edLay(edCard(typ, size, `,"tap":"`+tt.DefaultTap+`"`))) {
			t.Errorf("%s: default_tap %q", typ, tt.DefaultTap)
		}
		if tt.General && (tt.Src != "none" || parses(edLay(edCard(typ, size, `,"src":"a"`)))) {
			t.Errorf("%s: src %q", typ, tt.Src)
		}
	}
	for _, s := range d.Dots {
		if !parses(edLay(edCard("dots", "4x1", `,"src":"`+s+`"`))) {
			t.Errorf("dots src %q refused", s)
		}
	}
	if len(d.Dots) == 0 || parses(edLay(edCard("dots", "4x1", `,"src":"x"`))) {
		t.Errorf("dots %v", d.Dots)
	}
	for _, tap := range d.Taps {
		if !parses(edLay(edCard("gpu", "1x1", `,"tap":"`+tap+`"`))) {
			t.Errorf("tap %q refused", tap)
		}
	}
	if len(d.Taps) == 0 || parses(edLay(edCard("gpu", "1x1", `,"tap":"x"`))) {
		t.Errorf("taps %v", d.Taps)
	}

	sl := d.Slots
	slot := func(s string) bool { return parses(edLay(edCard("gpu", "2x3", `,"slots":`+s))) }
	if len(sl.Line2) == 0 || len(sl.As) == 0 {
		t.Fatalf("slots %+v", sl)
	}
	for _, v := range sl.Line2 { // line2 takes every value
		if slot(`{"gauge":"`+v+`"}`) != slices.Contains(sl.Gauge, v) || !slot(`{"line2":["`+v+`"]}`) {
			t.Errorf("slot %s: gauge or line2 disagrees", v)
		}
		for _, as := range sl.As {
			want := slices.Contains(sl.Line1, v) && !(slices.Contains(sl.NoBar, v) && as == "bar")
			if slot(`{"line1":{"value":"`+v+`","as":"`+as+`"}}`) != want {
				t.Errorf("line1 %s as %s: want %v", v, as, want)
			}
		}
	}
	if slot(`{"line1":{"value":"temp","as":"dial"}}`) {
		t.Error("as dial passes")
	}
	// a gpu card without slots and a layout without a tour block are served exactly the defaults sent
	l, err := parseLayout(edLay(edCard("gpu", "2x3", "")))
	if err != nil {
		t.Fatal(err)
	}
	var served struct {
		Tour map[string]any `json:"tour"`
		Tabs []struct{ Cards []struct{ Slots docSlots } }
	}
	json.Unmarshal(encodeLayoutDoc(l), &served)
	st := served.Tour
	df, got := sl.Default, served.Tabs[0].Cards[0].Slots
	if df.Line1 == nil || got.Gauge != df.Gauge || got.Line1 != df.Line1.Value || got.As != df.Line1.As || !slices.Equal(got.Line2, df.Line2) {
		t.Errorf("slots.default %+v, a gpu card without slots is served %+v", df, got)
	}
	tr := d.Tour
	if tr.On == nil || tr.IdleMin == nil || tr.ClockS == nil || tr.TabS == nil || tr.Clock == nil ||
		st["on"] != *tr.On || st["idle_s"] != float64(*tr.IdleMin*60) || st["clock_s"] != float64(*tr.ClockS) ||
		st["tab_s"] != float64(*tr.TabS) || st["clock"] != *tr.Clock {
		t.Errorf("tour defaults %+v, a layout without a tour block is served %v", tr, st)
	}

	// each limit is the most parseLayout takes
	a := func(n int) string { return strings.Repeat("a", n) }
	lims := map[string]func(n int) []byte{
		"tabs": func(n int) []byte {
			var ts []string
			for i := range n {
				ts = append(ts, fmt.Sprintf(`{"name":"t%d","cards":[]}`, i))
			}
			return []byte(`{"tabs":[` + strings.Join(ts, ",") + `]}`)
		},
		"cards": func(n int) []byte {
			var cs []string
			for i := range n {
				cs = append(cs, fmt.Sprintf(`{"id":"c%d","type":"gpu","at":[%d,%d],"size":"1x1"}`, i, i%4+1, i/4+1))
			}
			return edLay(cs...)
		},
		"tab_name": func(n int) []byte {
			return bytes.Replace(edLay(edCard("gpu", "1x1", "")), []byte(`"A"`), []byte(`"`+a(n)+`"`), 1)
		},
		"id":    func(n int) []byte { return edLay(strings.Replace(edCard("gpu", "1x1", ""), `"x"`, `"`+a(n)+`"`, 1)) },
		"title": func(n int) []byte { return edLay(edCard("gpu", "1x1", `,"title":"`+a(n)+`"`)) },
		"src":   func(n int) []byte { return edLay(edCard("gpu", "1x1", `,"src":"`+a(n)+`"`)) },
		"unit":  func(n int) []byte { return edLay(edCard("number", "1x1", `,"unit":"`+a(n)+`"`)) },
		"prom": func(n int) []byte {
			return edLay(`{"id":"x","type":"number","at":[1,1],"size":"1x1","value":{"prometheus":"` + a(n) + `"}}`)
		},
	}
	if len(d.Limits) != len(lims) {
		t.Errorf("limits %v", d.Limits)
	}
	for k, mk := range lims {
		n := d.Limits[k]
		if n == 0 || !parses(mk(n)) || parses(mk(n+1)) {
			t.Errorf("limit %s %d: parseLayout's is another", k, n)
		}
	}
	if len(d.Units) == 0 {
		t.Error("no units")
	}
	for _, u := range d.Units { // each is a unit format() knows, not one it just appends
		if format(12.345, u) == format(12.345, "")+" "+u {
			t.Errorf("unit %q", u)
		}
	}
}

func TestSourcesShortcuts(t *testing.T) {
	d, _ := srcEd(t).sources()
	var names []string
	for _, sc := range d.Shortcuts {
		names = append(names, sc.Name)
		s := sc.Name
		switch sc.Arg {
		case "mount":
			s += ":/"
			if _, err := parseShortcut(sc.Name, sc.Types[0]); err == nil {
				t.Errorf("%s passes without its mount", sc.Name)
			}
		case "":
		default:
			t.Errorf("%s: arg %q", sc.Name, sc.Arg)
		}
		for _, typ := range generalTypes {
			if _, err := parseShortcut(s, typ); (err == nil) != slices.Contains(sc.Types, typ) {
				t.Errorf("%s on %s: %v, listed %v", s, typ, err, sc.Types)
			}
		}
		want := "optional"
		if slices.Contains([]string{"ram", "disk", "load", "uptime"}, sc.Name) {
			want = "required"
		}
		if sc.Instance != want {
			t.Errorf("%s: instance %q, want %q", sc.Name, sc.Instance, want)
		}
	}
	if len(names) != 7 || !slices.IsSorted(names) {
		t.Errorf("shortcuts %v", names)
	}
}

func TestSourcesUnconfigured(t *testing.T) {
	d, body := srcEd(t).sources()
	p, h := d.Prometheus, d.HA
	if p.Configured || h.Configured || p.Error != "" || h.Error != "" || len(p.Metrics)+len(p.Instances)+len(p.Mounts)+len(h.Entities) != 0 {
		t.Errorf("prometheus %+v ha %+v", p, h)
	}
	for _, empty := range []string{`"metrics":[]`, `"instances":[]`, `"mounts":[]`, `"entities":[]`, `"card_env":[]`} {
		if !strings.Contains(body, empty) {
			t.Errorf("no %s in %s", empty, body)
		}
	}
	if fmt.Sprint(d.GPUs) != "[{server RTX 3060} {workstation RTX 4070}]" || fmt.Sprint(d.Pools) != "[{backup} {boot-pool} {tank}]" {
		t.Errorf("gpus %v pools %v", d.GPUs, d.Pools)
	}
	if fmt.Sprint(p.Needs) != "[PROMETHEUS_URL]" || fmt.Sprint(h.Needs) != "[HA_URL HA_TOKEN]" || p.Truncated || h.Truncated {
		t.Errorf("needs %v %v", p.Needs, h.Needs)
	}
	// one of the two set: the other is named
	for env, want := range map[string]string{"HA_URL": "[HA_TOKEN]", "HA_TOKEN": "[HA_URL]"} {
		e := srcEd(t)
		e.e.json = newJSONSrc(func(k string) string { return map[string]string{env: "http://ha.lan"}[k] })
		if d, _ := e.sources(); d.HA.Configured || fmt.Sprint(d.HA.Needs) != want {
			t.Errorf("only %s set: %+v", env, d.HA)
		}
	}
}

// Prometheus hangs and Home Assistant refuses the token: the answer still comes, with an error on each.
func TestSourcesUpstreamDown(t *testing.T) {
	var promHits, haHits atomic.Int32
	release := make(chan struct{})
	prom := jsrv(t, func(w http.ResponseWriter, r *http.Request) {
		promHits.Add(1)
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	t.Cleanup(func() { close(release) }) // before the server's Close
	ha := jsrv(t, func(w http.ResponseWriter, r *http.Request) { haHits.Add(1); w.WriteHeader(401) })
	if b := newEditor(newLayoutStore("x", nil)).budget; b <= 0 || b >= 5*time.Second {
		t.Errorf("budget %v: the answer must come within 5 s", b)
	}
	e := srcEd(t)
	clk := &fakeClock{t: time.Unix(1_000_000, 0)}
	e.e.now = clk.now
	e.e.prom = newProm(prom)
	e.e.json = newJSONSrc(func(k string) string { return map[string]string{"HA_URL": ha, "HA_TOKEN": "tok"}[k] })
	e.e.budget = 300 * time.Millisecond
	start := time.Now()
	d, _ := e.sources()
	if time.Since(start) > 5*time.Second {
		t.Errorf("took %v", time.Since(start))
	}
	if p := d.Prometheus; !p.Configured || p.Error != "unreachable" || len(p.Metrics) != 0 {
		t.Errorf("prometheus %+v", p)
	}
	if h := d.HA; !h.Configured || h.Error != "bad token" || len(d.GPUs) != 2 {
		t.Errorf("ha %+v gpus %v", h, d.GPUs)
	}
	p0 := promHits.Load()
	e.sources() // cached
	if promHits.Load() != p0 || haHits.Load() != 1 {
		t.Errorf("within 30 s: prometheus asked %d more, ha %d times", promHits.Load()-p0, haHits.Load())
	}
	clk.add(31 * time.Second)
	d, _ = e.sources()
	if promHits.Load() == p0 || haHits.Load() != 1 || d.HA.Error != "bad token" {
		t.Errorf("after 30 s: prometheus %d more, ha asked %d times (latched: once), %+v", promHits.Load()-p0, haHits.Load(), d.HA)
	}
}

func TestSourcesLists(t *testing.T) {
	f := newFake(t, `{"status":"success","data":["node_load1","up","node_cpu_seconds_total"]}`)
	ha := jsrv(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/states" || r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(404)
			return
		}
		long := strings.Repeat("é", 40) // 80 bytes
		fmt.Fprint(w, `[`+haTemp+`,`+haKit+`,{"entity_id":"Bad/../id","state":"1","attributes":{}},`+
			`{"entity_id":"sensor.x","state":"1","attributes":{"list":[1],"obj":{},"on":true,"nul":null}},`+
			// odd ones: kept, coerced
			`{"entity_id":"sensor.num","state":21.5,"attributes":{"unit_of_measurement":"W"}},`+
			`{"entity_id":"sensor.null","state":null,"attributes":[1,2]},{"entity_id":"sensor.bare"},`+
			// odd ones: skipped
			`{"entity_id":42,"state":"x"},"text",null,`+
			`{"entity_id":"sensor.long","state":"`+long+`","attributes":{"friendly_name":"`+long+`","unit_of_measurement":"`+long+`","`+long+`":1,"ok":2}}]`)
	})
	e := srcEd(t)
	e.e.prom = newProm(f.srv.URL)
	e.e.json = newJSONSrc(func(k string) string { return map[string]string{"HA_URL": ha, "HA_TOKEN": "tok"}[k] })
	e.e.environ = func() []string { return []string{"CARD_B=x", "CARD_A=y", "CARD_EMPTY=", "PANEL_TOKEN=z", "card_low=1"} }
	lookupEnv = func(k string) (string, bool) {
		v, ok := map[string]string{"CARD_A": "y", "CARD_B": "x", "CARD_EMPTY": ""}[k]
		return v, ok
	}
	t.Cleanup(func() { lookupEnv = os.LookupEnv })
	d, _ := e.sources()
	if p := d.Prometheus; !p.Configured || p.Error != "" || p.Truncated || len(p.Needs) != 0 || p.Needs == nil ||
		fmt.Sprint(p.Metrics) != "[node_cpu_seconds_total node_load1 up]" || len(p.Mounts) != 3 {
		t.Errorf("prometheus %+v", p)
	}
	h := d.HA
	var ids []string
	ents := map[string]haEntity{}
	for _, en := range h.Entities {
		ids = append(ids, en.ID)
		ents[en.ID] = en
	}
	if !h.Configured || h.Error != "" || h.Truncated || len(h.Needs) != 0 || h.Needs == nil ||
		fmt.Sprint(ids) != "[light.kitchen sensor.bare sensor.living_room_temperature sensor.long sensor.null sensor.num sensor.x]" {
		t.Fatalf("ha %+v", h) // sorted by id; Bad/../id, 42, "text" and null are no entities
	}
	if kit := ents["light.kitchen"]; kit.Name != "Kitchen" || kit.Unit != "lx" || kit.State != "on" || fmt.Sprint(kit.Attrs) != "[brightness]" {
		t.Errorf("kitchen %+v: name and unit are no attrs", kit)
	}
	if temp := ents["sensor.living_room_temperature"]; temp.Unit != "°C" || temp.Name != "" || temp.State != "21.5" {
		t.Errorf("temp %+v", temp)
	}
	if x := ents["sensor.x"]; len(x.Attrs) != 0 {
		t.Errorf("only numbers and text are attributes: %+v", x)
	}
	if n, nul, bare := ents["sensor.num"], ents["sensor.null"], ents["sensor.bare"]; n.State != "21.5" || n.Unit != "W" ||
		nul.State != "" || nul.Attrs == nil || len(nul.Attrs) != 0 || bare.State != "" || bare.Attrs == nil {
		t.Errorf("odd entities %+v %+v %+v", n, nul, bare)
	}
	// name and state at most 64 bytes, unit 32, cut on a character; a longer attr name couldn't be picked: left out
	if lg := ents["sensor.long"]; lg.Name != strings.Repeat("é", 32) || lg.State != strings.Repeat("é", 32) ||
		lg.Unit != strings.Repeat("é", 16) || fmt.Sprint(lg.Attrs) != "[ok]" {
		t.Errorf("long %+v", lg)
	}
	if fmt.Sprint(d.CardEnv) != "[CARD_A CARD_B]" {
		t.Errorf("card_env %v", d.CardEnv)
	}
}

// With every secret set to a marker (the tokens and keys, the CARD_* header, the user in PROMETHEUS_URL and HA_URL),
// and upstreams that are down or echo the request back, no editor answer and no log line carries one.
func TestSourcesNoSecrets(t *testing.T) {
	const mark = "Mk7zQ"
	for _, k := range []string{"PANEL_TOKEN", "NOTIFY_TOKEN", "HA_TOKEN", "TRUENAS_API_KEY", "PLANKA_PASSWORD"} {
		t.Setenv(k, mark+"-"+k)
	}
	t.Setenv("CARD_APP_TOKEN", "X-Key: "+mark+"-card")
	var echoed atomic.Int32
	echo := jsrv(t, func(w http.ResponseWriter, r *http.Request) { // says back the user, the headers and the URL
		echoed.Add(1)
		u, _, _ := r.BasicAuth()
		said := strings.Join([]string{u, r.Header.Get("Authorization"), r.Header.Get("X-Key"), r.URL.String()}, " ")
		w.WriteHeader(500)
		json.NewEncoder(w).Encode(map[string]string{"status": "error", "error": said, "message": said})
	})
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	for _, host := range []string{"127.0.0.1:1", strings.TrimPrefix(echo, "http://")} { // down, then echoing
		promURL, haURL := "http://"+mark+"-promuser:pw@"+host, "http://"+mark+"-hauser@"+host
		t.Setenv("HA_URL", haURL)
		e := srcEd(t)
		clk := &fakeClock{t: time.Unix(1_000_000, 0)}
		e.e.now = clk.now
		e.e.prom = newProm(promURL)
		e.e.json = newJSONSrc(os.Getenv)
		e.e.environ = os.Environ
		e.e.panel = func() []byte { return encodePanel(e.e.slots) }
		e.e.cards = func() []byte { return []byte(`{"cards":{}}`) }
		d, body := e.sources()
		bodies := []string{body}
		if !d.HA.Configured || d.HA.Error == "" || !d.Prometheus.Configured || d.Prometheus.Error == "" || fmt.Sprint(d.CardEnv) != "[CARD_APP_TOKEN]" {
			t.Errorf("%s: ha %+v prometheus %+v card_env %v", host, d.HA, d.Prometheus, d.CardEnv)
		}
		for _, p := range []string{"/api/v1/editor/preview", "/api/v1/editor/layout"} {
			_, b := e.get(p)
			bodies = append(bodies, b)
		}
		for _, v := range []string{
			`{"json":{"url":"http://` + host + `/x","path":"a","header_env":"CARD_APP_TOKEN"}}`,
			`{"ha":"sensor.living_room_temperature"}`,
			`"cpu"`,
		} {
			clk.add(2 * time.Second)
			_, b := e.do("POST", "/api/v1/editor/test", edTok, nil, `{"type":"number","value":`+v+`}`)
			if !strings.Contains(b, `"error"`) {
				t.Errorf("%s %s: %s", host, v, b)
			}
			bodies = append(bodies, b)
		}
		for i, b := range append(bodies, logs.String()) {
			if strings.Contains(b, mark) {
				t.Errorf("%s: answer %d carries a secret: %s", host, i, b)
			}
		}
	}
	if echoed.Load() < 6 { // 3 label lists, the entity list, and the three tests
		t.Errorf("the echo was asked %d times", echoed.Load())
	}
}

// Over a cap, the useful entries are kept: up and node_* metrics, sensor and then binary_sensor entities.
func TestSourcesTruncated(t *testing.T) {
	var names []string
	for i := range promMaxLabels + 5 {
		names = append(names, fmt.Sprintf("aaa_%05d", i))
	}
	b, _ := json.Marshal(map[string]any{"status": "success", "data": append(names, "zz_last", "up", "node_load1")})
	f := newFake(t, string(b))
	var ents []string
	for i := range haMaxEntities + 5 {
		ents = append(ents, fmt.Sprintf(`{"entity_id":"automation.a%05d","state":"on"}`, i))
	}
	ents = append(ents, `{"entity_id":"zone.home","state":"0"}`, `{"entity_id":"sensor.x","state":"1"}`, `{"entity_id":"binary_sensor.y","state":"off"}`)
	ha := jsrv(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "["+strings.Join(ents, ",")+"]") })
	e := srcEd(t)
	e.e.prom = newProm(f.srv.URL)
	e.e.json = newJSONSrc(func(k string) string { return map[string]string{"HA_URL": ha, "HA_TOKEN": "tok"}[k] })
	d, _ := e.sources()
	if m := d.Prometheus.Metrics; !d.Prometheus.Truncated || len(m) != promMaxLabels || !slices.IsSorted(m) ||
		!slices.Contains(m, "up") || !slices.Contains(m, "node_load1") || slices.Contains(m, "zz_last") {
		t.Errorf("metrics: truncated %v, %d, up %v", d.Prometheus.Truncated, len(m), slices.Contains(m, "up"))
	}
	var ids []string
	for _, en := range d.HA.Entities {
		ids = append(ids, en.ID)
	}
	if !d.HA.Truncated || len(ids) != haMaxEntities || !slices.IsSorted(ids) ||
		!slices.Contains(ids, "sensor.x") || !slices.Contains(ids, "binary_sensor.y") || slices.Contains(ids, "zone.home") {
		t.Errorf("entities: truncated %v, %d, %v", d.HA.Truncated, len(ids), ids[:3])
	}
}

// A refusal of the moment (a card is proving the HA token) isn't cached: the next call asks again.
func TestSourcesBusyNotCached(t *testing.T) {
	var hits atomic.Int32
	ha := jsrv(t, func(w http.ResponseWriter, r *http.Request) { hits.Add(1); fmt.Fprint(w, `[`+haTemp+`]`) })
	e := srcEd(t)
	e.e.json = newJSONSrc(func(k string) string { return map[string]string{"HA_URL": ha, "HA_TOKEN": "tok"}[k] })
	e.e.json.haProbe.Lock() // a card's fetch is proving the token
	d, _ := e.sources()
	e.e.json.haProbe.Unlock()
	if d.HA.Error != "busy, try again" || hits.Load() != 0 {
		t.Errorf("while a card probes: %+v, %d asks", d.HA, hits.Load())
	}
	if d, _ = e.sources(); d.HA.Error != "" || len(d.HA.Entities) != 1 || hits.Load() != 1 {
		t.Errorf("next call: %+v, %d asks", d.HA, hits.Load())
	}
}

// A slow upload doesn't hold the one-test slot: the body is read before the slot is taken.
func TestTestSlowUpload(t *testing.T) {
	f := newFake(t, vec("87"))
	e := newEd(t, edTok)
	e.e.prom = newProm(f.srv.URL)
	e.e.json = newJSONSrc(func(string) string { return "" })
	pr, pw := io.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		req, _ := http.NewRequest("POST", e.srv.URL+"/api/v1/editor/test", pr)
		req.Header.Set("Authorization", "Bearer "+edTok)
		if res, err := http.DefaultClient.Do(req); err == nil {
			res.Body.Close()
		}
	}()
	pw.Write([]byte(`{"type":"gauge",`))
	time.Sleep(100 * time.Millisecond) // the slow one is in the handler, reading
	if res, body := e.do("POST", "/api/v1/editor/test", edTok, nil, `{"type":"gauge","value":"cpu"}`); res.StatusCode != 200 || !strings.Contains(body, `"87 %"`) {
		t.Errorf("beside a slow upload: %d %s", res.StatusCode, body)
	}
	pw.Close()
	<-done
}

func TestTestEndpoint(t *testing.T) {
	var instant, lastQ atomic.Value // the instant query's answer; the last query asked
	instant.Store(vec("87"))
	prom := jsrv(t, func(w http.ResponseWriter, r *http.Request) {
		lastQ.Store(r.URL.Query().Get("query"))
		if strings.HasSuffix(r.URL.Path, "/query_range") {
			fmt.Fprint(w, `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{},"values":[[1000,"10"],[1060,"20"],[1120,"30"]]}]}}`)
			return
		}
		fmt.Fprint(w, instant.Load())
	})
	var appHits atomic.Int32
	app := jsrv(t, func(w http.ResponseWriter, r *http.Request) { appHits.Add(1); fmt.Fprint(w, `{"a":1}`) })
	e := newEd(t, edTok)
	clk := &fakeClock{t: time.Unix(1_000_000, 0)}
	e.e.now = clk.now
	e.e.prom = newProm(prom)
	e.e.json = newJSONSrc(func(string) string { return "" })
	raw := func(body string) (int, string) {
		t.Helper()
		clk.add(time.Second)
		res, b := e.do("POST", "/api/v1/editor/test", edTok, nil, body)
		return res.StatusCode, strings.TrimSpace(b)
	}
	post := func(body string) (int, map[string]string) {
		t.Helper()
		code, b := raw(body)
		var got map[string]string
		json.Unmarshal([]byte(b), &got)
		return code, got
	}
	// the whole card, as /api/v1/cards serves it
	if code, b := raw(`{"type":"gauge","value":"cpu","unit":"%"}`); code != 200 || b != `{"v":"87 %","f":0.87}` {
		t.Errorf("cpu: %d %s", code, b)
	}
	if lastQ.Load() != (valueSpec{Shortcut: "cpu"}).queries().Value {
		t.Errorf("asked %v", lastQ.Load())
	}
	instant.Store(vec("150"))
	if code, b := raw(`{"type":"gauge","value":{"prometheus":"q"},"min":0,"max":600,"warn":100,"bad":200}`); code != 200 || b != `{"st":"warn","v":"150","f":0.25}` {
		t.Errorf("gauge 0..600: %d %s", code, b)
	}
	var g cardOut
	if code, b := raw(`{"type":"graph","value":{"prometheus":"q"},"warn":20}`); code != 200 || json.Unmarshal([]byte(b), &g) != nil ||
		g.V != "150" || len(g.G) != 1 || len(g.G[0]) != 3 || g.Th == nil || g.St != stWarn {
		t.Errorf("graph: %d %s", code, b)
	}
	instant.Store(vec("1", "2"))
	for _, tc := range []struct{ body, want string }{
		{`{"type":"number","value":"cpu","unit":"123456789"}`, "unit over 8 bytes"},
		{`{"type":"number","value":"cpu","warn":90,"bad":80}`, "warn must not be above bad"},
		{`{"type":"number","value":"cpu","warn":70,"bad":80,"below":true}`, "warn must not be below bad"},
		{`{"type":"bar","value":"cpu","min":0,"max":5}`, "min/max need a prometheus value"},
		{`{"type":"number","value":{"prometheus":"q"},"min":5,"max":5}`, "min must be below max"},
		{`{"type":"status","value":{"prometheus":"q"},"warn":1}`, "status takes no thresholds"},
		{`{"type":"number","value":{"prometheus":"up"}}`, "returns 2 series"},
		{`{"type":"number","value":{"json":{"url":"` + app + `/x","path":"a","header_env":"PANEL_TOKEN"}}}`, "header_env must be CARD_"},
		{`{"type":"number","value":"cpu@a:1","unit":"%"}`, "returns 2 series"},
		{`{"type":"number","value":"cpu"}`, "add @instance (2)"},
		{`{"type":"number","value":"nope"}`, "unknown shortcut 'nope'"},
		{`{"type":"number","value":{"ha":"sensor.x/../../api"}}`, "ha needs an entity id"},
		{`{"type":"gpu","value":"cpu"}`, "not a value card"},
		{`{"type":"number","value":{"ha":"sensor.x"}}`, "set HA_URL"},
	} {
		if code, got := post(tc.body); code != 200 || !strings.HasPrefix(got["error"], tc.want) {
			t.Errorf("%s: %d %v, want error %q", tc.body, code, got, tc.want)
		}
	}
	// the field messages are parseLayout's
	if _, err := parseLayout(edLay(edCard("number", "1x1", `,"warn":90,"bad":80`))); err == nil || !strings.HasSuffix(err.Error(), "warn must not be above bad") {
		t.Errorf("parseLayout says %v", err)
	}
	if appHits.Load() != 0 {
		t.Error("PANEL_TOKEN's source was asked")
	}
	instant.Store(vec("87"))
	raw(`{"type":"gauge","value":"cpu"}`)
	clk.add(-500 * time.Millisecond) // raw adds 1 s: the next is 0.5 s after
	if code, _ := raw(`{"type":"gauge","value":"cpu"}`); code != 429 {
		t.Errorf("second test inside 1 s: %d, want 429", code)
	}
}

// The documents go in byte for byte, as the panel routes serve them (no HTML escaping).
func TestPreview(t *testing.T) {
	e := newEd(t, edTok)
	panel, cards := `{"planka":{"boards":[{"n":"Fix & <b>"}]},"homelab":{"stale":false}}`+"\n", `{"cards":{"x":{"v":"1 & <2"}}}`
	e.e.panel = func() []byte { return []byte(panel) }
	e.e.cards = func() []byte { return []byte(cards) }
	res, body := e.get("/api/v1/editor/preview")
	var got struct{ Panel, Cards json.RawMessage }
	if res.StatusCode != 200 || json.Unmarshal([]byte(body), &got) != nil {
		t.Fatalf("%d %s", res.StatusCode, body)
	}
	if string(got.Panel) != strings.TrimSpace(panel) || string(got.Cards) != cards || strings.Contains(body, `\u00`) {
		t.Errorf("panel %s cards %s", got.Panel, got.Cards)
	}
}

// The public demo's data is editor-demo's output, made-up throughout, and the same on every run.
func TestDemoDataCurrent(t *testing.T) {
	want, err := editorDemo()
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := editorDemo(); !bytes.Equal(again, want) {
		t.Fatal("editor-demo differs between runs")
	}
	got, _ := os.ReadFile("editor/demo-data.json")
	if !bytes.Equal(got, want) {
		t.Error("editor/demo-data.json is stale: go run . editor-demo > editor/demo-data.json")
	}
	var d struct {
		Layout  json.RawMessage
		Sources srcDoc
		Cards   struct{ Cards map[string]cardOut }
	}
	if err := json.Unmarshal(want, &d); err != nil {
		t.Fatal(err)
	}
	l, err := parseLayout(d.Layout)
	if err != nil {
		t.Fatal(err)
	}
	for _, tab := range l.Tabs {
		for _, c := range tab.Cards {
			if c.Tap == defaultTap(c.Type) {
				t.Errorf("%s: default tap written out", c.ID)
			}
			if o, ok := d.Cards.Cards[c.ID]; hasEntry(c) && (!ok || o.St == stNone) {
				t.Errorf("%s: %+v", c.ID, o)
			}
		}
	}
	s := d.Sources
	if len(s.Prometheus.Metrics) < 30 || len(s.Prometheus.Instances) != 2 || len(s.HA.Entities) < 12 || len(s.GPUs) != 2 || fmt.Sprint(s.CardEnv) != "[CARD_APP_TOKEN]" {
		t.Errorf("sources %+v", s)
	}
}
