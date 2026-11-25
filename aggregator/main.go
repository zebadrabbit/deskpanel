// Command deskpanel is the aggregator behind the desk touch panel.
//
//	deskpanel serve          run every collector, serve /api/v1/panel to the device
//	deskpanel agent [addr]   on a GPU host: serve nvidia-smi as JSON (default :9835)
//	deskpanel editor-demo    print the screen editor demo's data (editor/demo-data.json)
//
// serve is configured entirely through environment variables; see .env.example.
package main

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	_ "time/tzdata" // the alpine image has no zoneinfo; alert times use TZ
	"unicode/utf8"
)

func main() {
	mode := "serve"
	if len(os.Args) > 1 {
		mode = os.Args[1]
	}
	switch mode {
	case "serve":
		serve()
	case "agent":
		addr := ":9835"
		if len(os.Args) > 2 {
			addr = os.Args[2]
		}
		agent(addr)
	case "demo-json":
		os.Stdout.Write(demoJSON())
	case "demo-weather":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: deskpanel demo-weather <dir>")
			os.Exit(2)
		}
		if err := writeDemoWeather(os.Args[2], demoNow()); err != nil {
			log.Fatal(err)
		}
	case "demo-layout":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: deskpanel demo-layout <dir>")
			os.Exit(2)
		}
		if err := writeDemoLayouts(os.Args[2]); err != nil {
			log.Fatal(err)
		}
	case "demo-cards":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: deskpanel demo-cards <dir>")
			os.Exit(2)
		}
		if err := writeDemoCards(os.Args[2], demoNow()); err != nil {
			log.Fatal(err)
		}
	case "demo-astro":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: deskpanel demo-astro <dir>")
			os.Exit(2)
		}
		if err := writeDemoAstro(os.Args[2], demoNow()); err != nil {
			log.Fatal(err)
		}
	case "layout-default":
		if err := layoutDefault(); err != nil {
			log.Fatal(err)
		}
	case "editor-demo":
		b, err := editorDemo()
		if err != nil {
			log.Fatal(err)
		}
		os.Stdout.Write(b)
	default:
		fmt.Fprintln(os.Stderr, "usage: deskpanel serve | deskpanel agent [addr] | deskpanel demo-json | deskpanel demo-weather <dir> | deskpanel demo-layout <dir> | deskpanel demo-cards <dir> | deskpanel demo-astro <dir> | deskpanel layout-default | deskpanel editor-demo")
		os.Exit(2)
	}
}

// Collector is one data source. To add a section (ADS-B, weather, Meshtastic...), write a
// Collector and register it in serve(). The device ignores sections and keys it doesn't know,
// so new ones never break the protocol.
type Collector interface {
	Section() string // top-level key in /api/v1/panel, e.g. "homelab"
	Key() string     // key inside that section, e.g. "nas"
	Interval() time.Duration
	Collect(ctx context.Context) (any, error)
}

// slot runs one collector on its own interval and keeps its last good result. A failed run
// marks the section stale but keeps serving the old data; it never blocks a response.
type slot struct {
	c    Collector
	kick chan struct{} // send to refresh now instead of waiting out the interval
	mu   sync.Mutex
	data any
	at   time.Time // last success
	ok   bool      // last run succeeded
	ran  bool      // finished at least one run (success or error)
}

func (s *slot) run() {
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		data, err := s.c.Collect(ctx)
		cancel()
		s.mu.Lock()
		s.ok, s.ran = err == nil, true
		if err == nil {
			s.data, s.at = data, time.Now()
		}
		s.mu.Unlock()
		if err != nil {
			log.Printf("%s.%s: %v", s.c.Section(), s.c.Key(), err)
		}
		select {
		case <-time.After(s.c.Interval()):
		case <-s.kick:
		}
	}
}

// panel builds {section: {updated_at, stale, <key>: data, ...}}. updated_at is the oldest
// last-success time in the section (unix seconds, 0 until every collector has succeeded once);
// stale is true if any collector in the section failed its last run.
func panel(slots []*slot) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, s := range slots {
		sec := out[s.c.Section()]
		if sec == nil {
			sec = map[string]any{"stale": false}
			out[s.c.Section()] = sec
		}
		s.mu.Lock()
		sec[s.c.Key()] = s.data
		var t int64
		if !s.at.IsZero() {
			t = s.at.Unix()
		}
		if old, seen := sec["updated_at"].(int64); !seen || t < old {
			sec["updated_at"] = t
		}
		if !s.ok {
			sec["stale"] = true
		}
		s.mu.Unlock()
	}
	return out
}

func encodePanel(slots []*slot) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)     // "&" stays 1 byte, not 6; the device isn't a browser
	_ = enc.Encode(panel(slots)) // maps, slices, strings and numbers only: cannot fail
	return buf.Bytes()
}

func serve() {
	token := mustEnv("PANEL_TOKEN")
	var slots []*slot
	add := func(c Collector) *slot {
		s := &slot{c: c, kick: make(chan struct{}, 1)}
		slots = append(slots, s)
		return s
	}
	var tasks interface { // what POST .../cards/{id}/done talks to: Planka, or the demo boards
		markDone(ctx context.Context, cardID string) error
	}
	var tasksSlot *slot
	var wxSlots []*slot
	var wxFrames frameSource
	var wxTexts textSource
	demo := envOn("DEMO")
	if demo {
		log.Print("DEMO mode: fake data, no real sources")
		for _, c := range demoSources(time.Now()) {
			add(c)
		}
		boards := newDemoBoards(time.Now())
		tasks, tasksSlot = boards, add(boards)
		dw := newDemoWeather(time.Now(), envOn("DEMO_WARNING"))
		dw.clock = time.Now
		for _, c := range dw.collectors() {
			wxSlots = append(wxSlots, &slot{c: c, kick: make(chan struct{}, 1)})
		}
		wxFrames, wxTexts = dw, dw
	} else {
		if url := os.Getenv("TRUENAS_URL"); url != "" {
			add(newTrueNAS(url, getenv("TRUENAS_USER", "deskpanel"), mustEnv("TRUENAS_API_KEY"), mustEnv("TRUENAS_CERT_SHA256")))
		}
		if agents := os.Getenv("GPU_AGENTS"); agents != "" {
			add(newGPU(agents))
		}
		if sock := os.Getenv("TAILSCALE_SOCKET"); sock != "" {
			add(newTailscale(sock))
		}
		if spec := os.Getenv("SERVICES"); spec != "" {
			list := nameURLs("SERVICES", spec)
			add(&services{list: list[:min(len(list), maxServices)]})
		}
		if url := os.Getenv("PLANKA_URL"); url != "" {
			pk := &planka{url: strings.TrimRight(url, "/"), user: mustEnv("PLANKA_USER"), pass: mustEnv("PLANKA_PASSWORD")}
			tasks, tasksSlot = pk, add(pk)
		}
		var wxRadar *radar
		var wxNWS *nws
		wxSlots, wxRadar, wxNWS = weatherFromEnv()
		if wxRadar != nil {
			wxFrames, wxTexts = wxRadar, wxNWS
		}
	}
	notes := newNotifier(os.Getenv("NOTES_FILE"))
	notesSlot := add(notes)
	all := append(slices.Clone(slots), wxSlots...)
	for _, s := range all {
		go s.run()
	}
	kick := func(s *slot) { // refresh now so the next panel poll already shows the change
		select {
		case s.kick <- struct{}{}:
		default:
		}
	}
	postAct := func(src, msg, lvl, act string) note {
		nt := notes.add(src, msg, lvl, act)
		kick(notesSlot)
		log.Printf("note %d [%s] %s: %s", nt.ID, nt.Lvl, nt.Src, nt.Msg)
		return nt
	}
	postNote := func(src, msg, lvl string) note { return postAct(src, msg, lvl, "") }
	boots := &bootWatch{post: func(src, msg, lvl, act string) { postAct(src, msg, lvl, act) }}
	go newWatcher(all, func(src, msg, lvl, act string) { postAct(src, msg, lvl, act) }).run()
	if demo {
		go demoNotes(postNote)
	}

	layouts := newLayoutStore(getenv("LAYOUT_FILE", "/config/layout.json"), func(src, msg, lvl string) { postNote(src, msg, lvl) })
	var pr promReader // stays nil without PROMETHEUS_URL (not a nil *prom inside the interface)
	var pm *prom
	var dp *demoProm
	demoStart := time.Now()
	if demo {
		dp = &demoProm{start: demoStart, now: time.Now}
		pr = dp
	} else if u := os.Getenv("PROMETHEUS_URL"); u != "" {
		pm = newProm(u)
		pr = pm
		go pm.run(context.Background())
	}
	var rd promReader
	var js *jsonSrc
	if demo {
		rd = readers{prom: pr, json: pr} // demoProm answers JSON keys too
	} else {
		js = newJSONSrc(os.Getenv)
		rd = readers{prom: pr, json: js}
		go js.run(context.Background())
	}
	wantProm := func() {
		instant, ranged, jsons := queriesFor(layouts.layout())
		if pm != nil {
			pm.want(instant, ranged)
		}
		if js != nil {
			js.want(jsons)
		}
	}
	layouts.refresh(foundSources(all)) // layout.json at once; the default waits for the GPU/TrueNAS first runs
	wantProm()
	go func() {
		for range time.Tick(5 * time.Second) {
			if layouts.refresh(foundSources(all)) {
				wantProm()
			}
		}
	}()
	hist := &gpuHistory{}
	if demo {
		hist = demoHistory(demoStart)
		go func() {
			for {
				time.Sleep(time.Minute)
				hist.record(demoGPUs(time.Since(demoStart).Seconds()).([]gpuStat))
			}
		}()
	} else {
		go func() {
			for {
				recordGPU(hist, all)
				time.Sleep(time.Minute)
			}
		}()
	}
	hist.live = func() []gpuStat { g, _ := gpuData(all); return g } // the fan slot shows the GPUs as /panel does

	panelDoc := func() []byte { return encodePanel(slots) }
	cardsBody := func() []byte { return cardsDoc(layouts.layout(), rd, hist) }

	mux := http.NewServeMux()
	addLayoutRoute(mux, token, layouts)
	ed := newEditor(layouts)
	ed.slots, ed.prom, ed.json, ed.demo, ed.panel, ed.cards = all, pm, js, dp, panelDoc, cardsBody
	addEditorRoutes(mux, os.Getenv("EDITOR_TOKEN"), ed)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "ok\n")
	})
	mux.HandleFunc("GET /api/v1/panel", auth(token, func(w http.ResponseWriter, r *http.Request) {
		boots.seen(r.Header.Get("X-Panel-Boot"))
		serveETag(w, r, panelDoc())
	}))
	addCardsRoute(mux, token, cardsBody)
	mux.HandleFunc("POST /api/v1/planka/cards/{id}/done", auth(token, func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if tasks == nil {
			http.Error(w, "planka not configured", http.StatusNotFound)
			return
		}
		if !isDigits(id) { // Planka IDs are numeric; nothing else goes into its URL
			http.Error(w, "bad card id", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		if err := tasks.markDone(ctx, id); err != nil {
			code := http.StatusBadGateway
			var he httpErr
			if errors.As(err, &he) && (he.code == http.StatusNotFound || he.code == http.StatusConflict) {
				code = he.code
			}
			log.Printf("done %s: %v", id, err)
			http.Error(w, err.Error(), code)
			return
		}
		kick(tasksSlot)
		w.WriteHeader(http.StatusNoContent)
	}))

	// Notes for the panel: senders use NOTIFY_TOKEN (so they can't mark cards done); the panel
	// reports reads with its own token.
	if notifyToken := os.Getenv("NOTIFY_TOKEN"); notifyToken != "" {
		mux.HandleFunc("POST /api/v1/notify", auth(notifyToken, func(w http.ResponseWriter, r *http.Request) {
			var in struct{ Src, Msg, Lvl string }
			err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in)
			if in.Lvl == "" {
				in.Lvl = "info"
			}
			if err != nil || strings.TrimSpace(in.Msg) == "" || (in.Lvl != "info" && in.Lvl != "warn" && in.Lvl != "alert") {
				http.Error(w, `want JSON {"msg": "...", "src": "who", "lvl": "info|warn|alert"}`, http.StatusBadRequest)
				return
			}
			nt := postNote(in.Src, in.Msg, in.Lvl)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]int{"id": nt.ID})
		}))

		// ntfy-compatible publishing: point anything that speaks ntfy (Uptime Kuma, scripts, CI) at
		// http://<this host>:8088 with any topic and NOTIFY_TOKEN as its access token.
		ntfy := auth(notifyToken, func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
			topic, msg, lvl := ntfyNote(r.PathValue("topic"), r.Header, body)
			if msg == "" {
				http.Error(w, "empty message", http.StatusBadRequest)
				return
			}
			nt := postNote(topic, msg, lvl)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"id": fmt.Sprint(nt.ID), "time": nt.T, "event": "message",
				"topic": topic, "message": nt.Msg})
		})
		mux.HandleFunc("POST /{topic}", ntfy)
		mux.HandleFunc("PUT /{topic}", ntfy)
		mux.HandleFunc("POST /{$}", ntfy) // JSON publish

		// Alertmanager webhook receiver; Grafana's webhook contact point sends the same payload.
		mux.HandleFunc("POST /api/v1/alertmanager", auth(notifyToken, func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(io.LimitReader(r.Body, 256<<10))
			list, err := alertmanagerNotes(body)
			if err != nil {
				http.Error(w, "want an Alertmanager webhook payload: "+err.Error(), http.StatusBadRequest)
				return
			}
			for _, n := range list {
				postNote(n[0], n[1], n[2])
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]int{"posted": len(list)})
		}))
	} else {
		log.Print("NOTIFY_TOKEN unset: notification inputs (/api/v1/notify, ntfy, Alertmanager) disabled")
	}
	if wxFrames != nil {
		addWeatherRoutes(mux, token, wxSlots, wxFrames, wxTexts)
	}
	var ast *astro // nil: no location, both astro routes answer 404
	if demo {
		ast = newDemoAstro()
	} else {
		ast = astroFromEnv()
	}
	if ast != nil {
		go ast.run(context.Background())
	}
	addAstroRoutes(mux, token, ast)
	mux.HandleFunc("POST /api/v1/notify/{id}/read", auth(token, func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil || id <= 0 {
			http.Error(w, "bad note id", http.StatusBadRequest)
			return
		}
		notes.markRead(id)
		kick(notesSlot)
		w.WriteHeader(http.StatusNoContent)
	}))

	addr := getenv("LISTEN", ":8088")
	log.Printf("deskpanel serve on %s, %d collectors", addr, len(slots))
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	log.Fatal(srv.ListenAndServe())
}

func auth(token string, h http.HandlerFunc) http.HandlerFunc {
	want := []byte("Bearer " + token)
	return func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), want) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h(w, r)
	}
}

// httpErr carries an HTTP status from an upstream call or a precondition.
type httpErr struct {
	code int
	msg  string
}

func (e httpErr) Error() string { return e.msg }

// trunc cuts s to at most n bytes without splitting a UTF-8 sequence, marking the cut with "...".
// Byte limits (not rune limits) are what keep the panel payload under its size budget.
func trunc(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	cut := n - 3
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "..."
}

func isDigits(s string) bool {
	if s == "" || len(s) > 32 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// envOn reads a boolean flag: unset or empty is off, else strconv.ParseBool's spellings or yes/no/on/off in any case;
// anything else stops the aggregator.
func envOn(k string) bool {
	v := os.Getenv(k)
	if v == "" {
		return false
	}
	switch strings.ToLower(v) {
	case "yes", "on":
		return true
	case "no", "off":
		return false
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		log.Fatalf("%s=%q: use 1 or 0 (or yes/no)", k, v)
	}
	return b
}

func mustEnv(k string) string {
	v := os.Getenv(k)
	if v == "" {
		log.Fatalf("%s is required (see .env.example)", k)
	}
	return v
}
