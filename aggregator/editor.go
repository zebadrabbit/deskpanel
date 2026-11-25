package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

//go:embed editor
var editorFiles embed.FS

const (
	editorMinToken = 12
	editorMaxBody  = 256 << 10
)

// editor serves the screen editor: its page and the endpoints that load and save layout.json.
type editor struct {
	path   string
	store  *layoutStore
	wait   func(context.Context, time.Duration) // the pause after a wrong token; tests make it instant
	guess  chan struct{}                        // holds the one wrong guess waiting (a mutex a request can give up on)
	rename func(from, to string) error
	chown  func(path string, uid, gid int) error
	write  func(path string, data []byte) error // writes and fsyncs data to path; tests make it fail

	mu sync.Mutex // one save at a time

	// What sources, test and preview read; serve() fills them in.
	slots        []*slot         // the collectors: the GPUs and pools to pick from
	prom         *prom           // nil without PROMETHEUS_URL
	json         *jsonSrc        // the JSON and Home Assistant client (nil in demo mode)
	demo         *demoProm       // demo mode: answers Test, and the Prometheus and HA lists are made up
	environ      func() []string // os.Environ: the CARD_* names
	panel, cards func() []byte   // the /api/v1/panel and /api/v1/cards documents
	now          func() time.Time
	budget       time.Duration // how long sources and Test wait for Prometheus and Home Assistant

	srcMu   sync.Mutex // one sources build at a time; guards the cache
	srcAt   time.Time
	srcBody []byte
	testMu  sync.Mutex // one Test at a time
	testAt  time.Time  // when the last Test started
}

func newEditor(s *layoutStore) *editor {
	return &editor{path: s.path, store: s, wait: sleepCtx, guess: make(chan struct{}, 1), rename: os.Rename, chown: os.Chown, write: writeSynced, environ: os.Environ, now: time.Now, budget: 4 * time.Second}
}

func writeSynced(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// auth is main.go's auth() for the editor: constant-time compare, and a wrong token waits 1 s before the 401, one
// wrong guess at a time (parallel guesses get one a second in all). A guess given up stops waiting or queueing; the
// right token never waits.
func (e *editor) auth(token string, h http.HandlerFunc) http.HandlerFunc {
	want := []byte("Bearer " + token)
	return func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), want) != 1 {
			select {
			case e.guess <- struct{}{}:
				e.wait(r.Context(), time.Second)
				<-e.guess
			case <-r.Context().Done():
			}
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h(w, r)
	}
}

func addEditorRoutes(mux *http.ServeMux, token string, e *editor) {
	if len(token) < editorMinToken {
		log.Printf("editor off: EDITOR_TOKEN must be at least %d bytes", editorMinToken)
		return
	}
	for _, n := range []string{"PANEL_TOKEN", "NOTIFY_TOKEN"} {
		if v, _ := lookupEnv(n); v == token {
			log.Printf("editor off: EDITOR_TOKEN must differ from %s", n)
			return
		}
	}
	sub, _ := fs.Sub(editorFiles, "editor")
	files := http.StripPrefix("/editor/", http.FileServerFS(sub))
	mux.HandleFunc("GET /editor", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/editor/", http.StatusMovedPermanently)
	})
	mux.HandleFunc("GET /editor/", func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Cache-Control", "no-cache")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; frame-ancestors 'none'")
		files.ServeHTTP(w, r)
	})
	mux.HandleFunc("GET /api/v1/editor/ping", e.auth(token, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	mux.HandleFunc("GET /api/v1/editor/layout", e.auth(token, e.load))
	mux.HandleFunc("PUT /api/v1/editor/layout", e.auth(token, e.save))
	mux.HandleFunc("GET /api/v1/editor/sources", e.auth(token, e.sources))
	mux.HandleFunc("POST /api/v1/editor/test", e.auth(token, e.testValue))
	mux.HandleFunc("GET /api/v1/editor/preview", e.auth(token, e.preview))
}

const (
	sourcesFor  = 30 * time.Second
	testMaxBody = 16 << 10
)

// sourcesDoc is GET /api/v1/editor/sources: the tables parseLayout reads (the page keeps no copy of a rule), the
// GPUs and pools the collectors report, the Prometheus and Home Assistant lists, and the CARD_* names. editor-demo
// builds the demo's with it too.
func sourcesDoc(slots []*slot, pl promListing, hl haListing, cardEnv []string) map[string]any {
	types := map[string]any{}
	for t, sizes := range cardSizes {
		src := typeSrc[t]
		if src == "" {
			src = "none"
		}
		types[t] = map[string]any{"sizes": sizes, "src": src, "general": contains(generalTypes, t),
			"tap": !contains(ownTapTypes, t), "default_tap": defaultTap(t)}
	}
	scs := []map[string]any{}
	for _, n := range slices.Sorted(maps.Keys(shortcuts)) {
		sc, inst := shortcuts[n], "optional"
		if sc.instance {
			inst = "required"
		}
		scs = append(scs, map[string]any{"name": n, "types": strings.Fields(sc.types), "instance": inst, "arg": sc.arg})
	}
	gpus, per := []map[string]string{}, map[string]int{}
	g, _ := gpuData(slots)
	for _, st := range g {
		if st.Host != "" {
			gpus = append(gpus, map[string]string{"src": gpuKey(per, st.Host), "name": st.Name})
		}
	}
	sort.Slice(gpus, func(i, j int) bool { return gpus[i]["src"] < gpus[j]["src"] })
	pools := []map[string]string{}
	for _, p := range slices.Sorted(slices.Values(foundSources(slots).Pools)) {
		pools = append(pools, map[string]string{"name": p})
	}
	return map[string]any{
		"limits": map[string]int{"tabs": maxTabs, "cards": maxCardsPerTab, "tab_name": maxTabName, "id": maxIDLen,
			"title": maxTitle, "src": maxSrcLen, "unit": maxUnitLen, "prom": maxPromLen},
		"types":     types,
		"shortcuts": scs,
		"slots": map[string]any{"gauge": gaugeSlots, "line1": line1Slots, "as": asSlots, "line2": line2Slots,
			"no_bar": noBarSlots, "default": gpuSlots{Gauge: slotDefault.Gauge, // in layout.json's form
				Line1: &slotLine{Value: slotDefault.Line1, As: slotDefault.As}, Line2: slotDefault.Line2}},
		"tour":       tourDefault,
		"taps":       tapNames,
		"units":      formatUnits,
		"dots":       dotsSrcs,
		"gpus":       gpus,
		"pools":      pools,
		"prometheus": pl,
		"ha":         hl,
		"card_env":   cardEnv,
	}
}

// cardEnvNames are the CARD_* variables a JSON source can send (set and allowed), by name only, sorted.
func cardEnvNames(env []string) []string {
	out := []string{}
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "CARD_") && headerEnvProblem(k) == "" && !contains(out, k) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// sources answers from a 30 s cache; Prometheus and Home Assistant are asked only here, at the same time, within budget.
// An answer that says HA is busy for the moment isn't kept: the next call asks again.
func (e *editor) sources(w http.ResponseWriter, r *http.Request) {
	e.srcMu.Lock()
	defer e.srcMu.Unlock()
	body := e.srcBody
	if body == nil || e.now().Sub(e.srcAt) >= sourcesFor {
		var pl promListing
		var hl haListing
		if e.demo != nil {
			pl, hl = demoPromListing(), demoHAListing(e.demo)
		} else {
			ctx, cancel := context.WithTimeout(context.Background(), e.budget) // not the request's: the answer is cached
			var wg sync.WaitGroup
			wg.Go(func() { pl = e.prom.labels(ctx) })
			wg.Go(func() { hl = e.json.haList(ctx) })
			wg.Wait()
			cancel()
		}
		body, _ = json.Marshal(sourcesDoc(e.slots, pl, hl, cardEnvNames(e.environ())))
		e.srcBody, e.srcAt = body, e.now()
		if hl.Error == haBusy {
			e.srcBody = nil
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(body)
}

// testValue is POST /api/v1/editor/test: a general card's type, value, unit, min, max, warn, bad and below as in
// layout.json, checked as parseLayout checks them, its value asked of its source once, now (nothing joins the
// polling), and answered with the card as /api/v1/cards would serve it, or {"error"}. The body is read first; then
// one test at a time, at most one a second.
func (e *editor) testValue(w http.ResponseWriter, r *http.Request) {
	fail := func(msg string) { editorJSON(w, 200, map[string]string{"error": msg}) }
	var c Card // only the general card fields are read
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, testMaxBody)).Decode(&c) != nil {
		fail("bad JSON")
		return
	}
	busy := func(msg string) { editorJSON(w, http.StatusTooManyRequests, map[string]string{"error": msg}) }
	if !e.testMu.TryLock() {
		busy("one test at a time")
		return
	}
	defer e.testMu.Unlock()
	now := e.now()
	if !e.testAt.IsZero() && now.Sub(e.testAt) < time.Second {
		busy("one test a second")
		return
	}
	e.testAt = now
	specs, err := parseValue(c.Value, c.Type)
	if err != nil {
		fail(err.Error())
		return
	}
	lr := &liveReader{prom: e.prom, json: e.json, specs: map[string]jsonSpec{}, got: map[string]promVal{}}
	for _, sp := range specs {
		if sp.JSON != nil {
			if msg := headerEnvProblem(sp.JSON.HeaderEnv); msg != "" { // the CARD_* rule, as parseLayout has it
				fail(msg)
				return
			}
			lr.specs[sp.JSON.key()] = *sp.JSON
		}
	}
	if msg := c.generalProblem(specs); msg != "" {
		fail(msg)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), e.budget)
	defer cancel()
	lr.ctx = ctx
	var pr promReader = lr
	if e.demo != nil {
		pr = readers{prom: e.demo, json: e.demo}
	}
	c.parsed = specs
	o := generalCard(c, pr)
	if o.St == stNone {
		fail(o.Sub)
		return
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false) // as encodeCards writes it
	enc.Encode(o)
	w.Header().Set("Content-Type", "application/json")
	w.Write(b.Bytes())
}

// liveReader asks each query's source once, now, for the Test button (cards read the collectors' caches instead).
type liveReader struct {
	ctx   context.Context
	prom  *prom // nil: no PROMETHEUS_URL
	json  *jsonSrc
	specs map[string]jsonSpec // the value's JSON and Home Assistant sources, by key
	got   map[string]promVal  // a JSON source is asked once for its value and its plot
}

// seen is a one-off fetch as a cached one reads: why or an error is none (the error short and safe), else ok.
func seen(v promVal, why string, err error) promVal {
	switch {
	case err != nil:
		return promVal{St: stNone, Why: errWhy(err)}
	case why != "":
		return promVal{St: stNone, Why: why}
	}
	v.St = stOK
	return v
}

func (l *liveReader) value(q string) promVal {
	if !srcKey(q) {
		if l.prom == nil {
			return promVal{St: stNone, Why: "set PROMETHEUS_URL"}
		}
		v, why, err := l.prom.fetchValue(l.ctx, q)
		return seen(promVal{V: v}, why, err)
	}
	if v, ok := l.got[q]; ok {
		return v
	}
	sp := l.specs[q]
	if sp.HA != "" {
		release, ok := l.json.haGate() // the probe gate a card's fetch takes
		if !ok {
			return promVal{St: stNone, Why: haBusy}
		}
		defer release()
	}
	v, why, err := l.json.fetch(l.ctx, sp)
	l.got[q] = seen(v, why, err)
	return l.got[q]
}

func (l *liveReader) series(q string) promSeries {
	if srcKey(q) {
		v := l.value(q)
		if v.St != stOK {
			return promSeries{St: v.St, Why: v.Why}
		}
		return promSeries{P: []float64{v.V}, St: stOK}
	}
	if l.prom == nil {
		return promSeries{St: stNone, Why: "set PROMETHEUS_URL"}
	}
	p, why, err := l.prom.fetchSeries(l.ctx, q)
	v := seen(promVal{}, why, err)
	return promSeries{P: p, St: v.St, Why: v.Why}
}

// preview is GET /api/v1/editor/preview: the panel and cards documents the panel is being served now, byte for byte.
func (e *editor) preview(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Write(slices.Concat([]byte(`{"panel":`), e.panel(), []byte(`,"cards":`), e.cards(), []byte("}\n")))
}

// etagOf is the quoted hex of the first 16 bytes of the SHA-256 of the file, or "none" for no file.
func etagOf(data []byte, exists bool) string {
	if !exists {
		return `"none"`
	}
	sum := sha256.Sum256(data)
	return `"` + hex.EncodeToString(sum[:16]) + `"`
}

func editorJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

// current reads the file: its bytes and whether it exists.
func (e *editor) current() ([]byte, bool, error) {
	data, err := os.ReadFile(e.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	return data, err == nil, err
}

func (e *editor) load(w http.ResponseWriter, r *http.Request) {
	data, exists, err := e.current()
	if err != nil {
		editorJSON(w, 500, map[string]string{"message": "can't read " + e.path + ": " + err.Error()})
		return
	}
	if e.store.doc() == nil { // just started, nothing served yet (no default, and no good file: a bad one has no layout)
		editorJSON(w, http.StatusServiceUnavailable, map[string]string{"message": "layout not ready yet"})
		return
	}
	out := map[string]any{"from_file": exists}
	lay := e.store.layout() // the default being served, or the last good file
	if exists {
		l, perr := parseLayout(data)
		if perr != nil {
			out["error"] = perr.Error()
		} else {
			lay = l
		}
	}
	// ponytail: the source form is Layout marshalled as-is (size "WxH", no parsed field); the served panel document is never used here
	out["layout"] = lay
	w.Header().Set("ETag", etagOf(data, exists))
	editorJSON(w, 200, out)
}

func (e *editor) save(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, editorMaxBody))
	if err != nil {
		var mb *http.MaxBytesError
		code := 400
		if errors.As(err, &mb) {
			code = http.StatusRequestEntityTooLarge
		}
		editorJSON(w, code, map[string]string{"message": "can't read the body: " + err.Error()})
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	cur, exists, err := e.current()
	if err != nil {
		editorJSON(w, 500, map[string]string{"message": "can't read " + e.path + ": " + err.Error()})
		return
	}
	if r.Header.Get("If-Match") != etagOf(cur, exists) {
		editorJSON(w, http.StatusPreconditionFailed, map[string]string{"message": "layout.json changed on disk; reload"})
		return
	}
	bad := func(msg string, tab, card int) {
		editorJSON(w, http.StatusUnprocessableEntity, map[string]any{"message": msg, "tab": tab, "card": card})
	}
	var l Layout
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&l); err != nil {
		bad("bad JSON: "+err.Error(), -1, -1)
		return
	}
	out := handLayout(l)
	if _, err := parseLayout(out); err != nil {
		msg, tab, card := err.Error(), -1, -1
		var le *layoutError
		if errors.As(err, &le) {
			msg = le.Msg
			tabs, cards := objectLines(out)
			for i, ln := range tabs {
				if ln == le.Line {
					tab = i
				}
			}
			for i, row := range cards {
				for j, ln := range row {
					if ln == le.Line {
						tab, card = i, j
					}
				}
			}
		}
		bad(msg, tab, card)
		return
	}
	fail := func(err error) {
		os.Remove(e.path + ".tmp")
		os.Remove(e.path + ".bak.tmp")
		editorJSON(w, 500, map[string]string{"message": fmt.Sprintf("can't write %s: %v. Mount config without :ro", e.path, err)})
	}
	// The new files take the old file's mode and owner (the container runs as root; the owner hand-edits the file).
	var mode os.FileMode = 0o644
	var owner os.FileInfo
	if fi, err := os.Stat(e.path); err == nil {
		mode, owner = fi.Mode().Perm(), fi
	} else if fi, err := os.Stat(filepath.Dir(e.path)); err == nil {
		owner = fi
	}
	own := func(path string) {
		os.Chmod(path, mode)
		if owner != nil {
			if uid, gid, ok := fileOwner(owner); ok {
				e.chown(path, uid, gid) // ponytail: fails unless root; then the owner is whoever runs the server
			}
		}
	}
	if exists { // through its own temp file: a failed backup keeps the previous good .bak
		if err := e.write(e.path+".bak.tmp", cur); err != nil {
			fail(err)
			return
		}
		own(e.path + ".bak.tmp")
		if err := e.rename(e.path+".bak.tmp", e.path+".bak"); err != nil {
			fail(err)
			return
		}
	}
	if err := e.write(e.path+".tmp", out); err != nil {
		fail(err)
		return
	}
	own(e.path + ".tmp")
	if now, nowExists, err := e.current(); err != nil || etagOf(now, nowExists) != etagOf(cur, exists) {
		os.Remove(e.path + ".tmp") // a hand edit landed during the save
		editorJSON(w, http.StatusPreconditionFailed, map[string]string{"message": "layout.json changed on disk; reload"})
		return
	}
	if err := e.rename(e.path+".tmp", e.path); err != nil {
		fail(err)
		return
	}
	if d, err := os.Open(filepath.Dir(e.path)); err == nil {
		d.Sync() // ignored where a folder can't be synced
		d.Close()
	}
	editorJSON(w, 200, map[string]string{"etag": etagOf(out, true)})
}

// sleepCtx waits d, or until the request is gone.
func sleepCtx(ctx context.Context, d time.Duration) {
	select {
	case <-time.After(d):
	case <-ctx.Done():
	}
}
