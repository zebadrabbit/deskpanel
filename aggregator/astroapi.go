package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// astroDoc is GET /api/v1/astro; the field names and caps are the panel's contract.
type astroDoc struct {
	Sky      int64    `json:"sky"`
	Rise     string   `json:"rise"`
	Set      string   `json:"set"`
	Daylight string   `json:"daylight"`
	Moonrise string   `json:"moonrise"`
	Moonset  string   `json:"moonset"`
	Phase    string   `json:"phase"`
	Illum    float64  `json:"illum"`
	Waxing   bool     `json:"waxing"`
	Day      [2]int   `json:"day"`
	Label    string   `json:"label"`
	Overhead []string `json:"overhead"`
	Stale    bool     `json:"stale"`
}

// astro keeps the current document and picture; renders happen in run, never in a handler.
type astro struct {
	lat, lon float64
	loc      *time.Location
	clock    func() time.Time
	render   func(lat, lon float64, t time.Time) ([]byte, error) // a test hook; default renders and encodes the sky

	mu    sync.Mutex
	skyT  int64
	sky   []byte
	stale bool
	doc   []byte
}

func newAstro(lat, lon float64, loc *time.Location, clock func() time.Time) *astro {
	a := &astro{lat: lat, lon: lon, loc: loc, clock: clock}
	a.render = func(lat, lon float64, t time.Time) ([]byte, error) { return encodeRGB565(renderSky(lat, lon, t)) }
	a.refreshDoc()
	return a
}

func (a *astro) compute(now time.Time) astroDoc {
	sunAlt := func(t time.Time) float64 { ra, dec := sunPos(t); al, _ := altAz(ra, dec, a.lat, a.lon, t); return al }
	moonAlt := func(t time.Time) float64 {
		ra, dec, _ := moonPos(t)
		al, _ := altAz(ra, dec, a.lat, a.lon, t)
		return al
	}
	rise, set, okR, okS := riseSet(sunAlt, now, a.loc, -0.833)
	mr, ms, okMr, okMs := riseSet(moonAlt, now, a.loc, 0.125)
	d := astroDoc{Rise: clock12(rise, okR), Set: clock12(set, okS), Moonrise: clock12(mr, okMr), Moonset: clock12(ms, okMs),
		Daylight: "—", Day: [2]int{-1, -1}}
	if okR && okS {
		d.Daylight = dayLength(rise, set)
		minute := func(t time.Time) int { t = t.In(a.loc); return t.Hour()*60 + t.Minute() }
		d.Day = [2]int{minute(rise), minute(set)}
	}
	illum, waxing, name := moonPhase(now)
	d.Illum, d.Waxing, d.Phase = math.Round(illum*1000)/1000, waxing, name
	at := now
	d.Label = "OVERHEAD NOW"
	if ln := now.In(a.loc); sunAlt(now) >= -0.833 && ln.Hour() < 21 { // sun up and before 9 PM
		y, m, dd := ln.Date()
		at, d.Label = time.Date(y, m, dd, 21, 0, 0, 0, a.loc), "TONIGHT AT 9 PM"
	}
	d.Overhead = overhead(a.lat, a.lon, at)
	if len(d.Overhead) > 12 {
		d.Overhead = d.Overhead[:12]
	}
	for i, n := range d.Overhead {
		d.Overhead[i] = trunc(n, 23)
	}
	return d
}

// refreshDoc recomputes the document (cheap) with the current picture's time.
func (a *astro) refreshDoc() {
	d := a.compute(a.clock())
	a.mu.Lock()
	defer a.mu.Unlock()
	d.Sky, d.Stale = a.skyT, a.stale
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(d)
	a.doc = buf.Bytes()
}

// renderPicture draws and encodes the sky for now; a failure keeps the old picture and marks the document stale.
func (a *astro) renderPicture() {
	now := a.clock()
	b, err := a.render(a.lat, a.lon, now)
	a.mu.Lock()
	if err != nil {
		a.stale = true
		log.Printf("astro: %v", err)
	} else {
		a.stale, a.sky, a.skyT = false, b, now.Unix()
	}
	a.mu.Unlock()
	a.refreshDoc()
}

func (a *astro) run(ctx context.Context) {
	go func() {
		for {
			a.renderPicture()
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Minute):
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Minute):
		}
		a.refreshDoc()
	}
}

func addAstroRoutes(mux *http.ServeMux, token string, a *astro) {
	mux.HandleFunc("GET /api/v1/astro", auth(token, func(w http.ResponseWriter, r *http.Request) {
		if a == nil {
			http.NotFound(w, r)
			return
		}
		a.mu.Lock()
		doc := a.doc
		a.mu.Unlock()
		serveETag(w, r, doc)
	}))
	mux.HandleFunc("GET /api/v1/astro/sky/{t}", auth(token, func(w http.ResponseWriter, r *http.Request) {
		if a == nil {
			http.NotFound(w, r)
			return
		}
		t, err := strconv.ParseInt(r.PathValue("t"), 10, 64)
		a.mu.Lock()
		sky, cur := a.sky, a.skyT
		a.mu.Unlock()
		if err != nil || sky == nil || t != cur {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		serveETag(w, r, sky)
	}))
}

// astroFromEnv is nil without WEATHER_LAT (the panel then shows a plain dark sky).
func astroFromEnv() *astro {
	if os.Getenv("WEATHER_LAT") == "" {
		return nil
	}
	return newAstro(mustFloat("WEATHER_LAT"), mustFloat("WEATHER_LON"), time.Local, time.Now)
}

// writeDemoAstro writes the simulator's fixtures for Greenwich: night at now, day 16 h later.
func writeDemoAstro(dir string, now time.Time) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for name, t := range map[string]time.Time{"night": now, "day": now.Add(16 * time.Hour)} {
		t := t
		a := newAstro(51.4769, -0.0005, time.UTC, func() time.Time { return t })
		a.renderPicture()
		if a.sky == nil {
			return os.ErrInvalid
		}
		if err := os.WriteFile(filepath.Join(dir, "astro-"+name+".json"), a.doc, 0o644); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, "sky-"+name+".z"), a.sky, 0o644); err != nil {
			return err
		}
	}
	return nil
}
