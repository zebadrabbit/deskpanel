package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"
)

// The weather tab has its own document so /api/v1/panel stays under 8 KB. Weather collectors
// register their slots separately in serve() and are never passed to encodePanel.

// frameSource and textSource let demo data and real collectors share the routes.
type frameSource interface{ frame(t int64) ([]byte, bool) }
type textSource interface{ text(i int) (alertText, bool) }

// weatherDoc is panel(slots)["weather"] as JSON: updated_at, stale, now, frames, alerts.
func weatherDoc(slots []*slot) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(panel(slots)["weather"])
	return buf.Bytes()
}

func addWeatherRoutes(mux *http.ServeMux, token string, slots []*slot, frames frameSource, texts textSource) {
	mux.HandleFunc("GET /api/v1/weather", auth(token, func(w http.ResponseWriter, r *http.Request) {
		body := weatherDoc(slots)
		sum := sha256.Sum256(body)
		etag := `"` + hex.EncodeToString(sum[:8]) + `"`
		w.Header().Set("ETag", etag)
		w.Header().Set("Cache-Control", "no-cache")
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}))
	mux.HandleFunc("GET /api/v1/weather/frame/{t}", auth(token, func(w http.ResponseWriter, r *http.Request) {
		t := r.PathValue("t")
		if !isDigits(t) {
			http.Error(w, "bad frame time", http.StatusBadRequest)
			return
		}
		ts, err := strconv.ParseInt(t, 10, 64)
		b, ok := frames.frame(ts)
		if err != nil || !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Cache-Control", "max-age=3600")
		w.Write(b)
	}))
	mux.HandleFunc("GET /api/v1/weather/alert/{n}", auth(token, func(w http.ResponseWriter, r *http.Request) {
		n := r.PathValue("n")
		if len(n) != 1 || n[0] > '3' || n[0] < '0' {
			http.NotFound(w, r)
			return
		}
		a, ok := texts.text(int(n[0] - '0'))
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(false)
		enc.Encode(a)
	}))
}

// weatherFromEnv builds the weather collectors from WEATHER_* (see .env.example); nil when
// WEATHER_LAT is unset. The slots stay out of the panel document, which has its own budget.
func weatherFromEnv() (slots []*slot, r *radar, n *nws) {
	if os.Getenv("WEATHER_LAT") == "" {
		log.Print("WEATHER_LAT unset: weather disabled")
		return
	}
	lat := mustFloat("WEATHER_LAT")
	lon := mustFloat("WEATHER_LON")
	z, err := strconv.Atoi(getenv("WEATHER_ZOOM", "8"))
	if err != nil {
		log.Fatalf("WEATHER_ZOOM: %v", err)
	}
	if err := checkWeather(lat, lon, z); err != nil {
		log.Fatalf("weather: %v", err)
	}
	ua := getenv("WEATHER_UA", "deskpanel")
	hc := &http.Client{Timeout: 15 * time.Second}
	v := newView(lat, lon, z)
	r = newRadar(v, tileClient{hc, ua}, "https://mesonet.agron.iastate.edu", "https://tile.openstreetmap.org", os.Getenv("WEATHER_CACHE"))
	n = newNWS(hc, "https://api.weather.gov", ua, lat, lon, v)
	for _, c := range []Collector{newConditions(hc, "https://api.open-meteo.com", lat, lon), r, n} {
		slots = append(slots, &slot{c: c, kick: make(chan struct{}, 1)})
	}
	return
}

// checkWeather range-checks WEATHER_LAT/LON/ZOOM. Written as !(lo <= v && v <= hi) so NaN fails.
// lat: Web-Mercator is infinite at ±90. zoom 2 is the first whose world fills the frame; 19 is OSM's deepest.
func checkWeather(lat, lon float64, z int) error {
	if !(-85 <= lat && lat <= 85) {
		return fmt.Errorf("WEATHER_LAT %v is outside -85..85", lat)
	}
	if !(-180 <= lon && lon <= 180) {
		return fmt.Errorf("WEATHER_LON %v is outside -180..180", lon)
	}
	if !(2 <= z && z <= 19) {
		return fmt.Errorf("WEATHER_ZOOM %d is outside 2..19", z)
	}
	return nil
}

func mustFloat(k string) float64 {
	f, err := strconv.ParseFloat(mustEnv(k), 64)
	if err != nil {
		log.Fatalf("%s: %v", k, err)
	}
	return f
}
