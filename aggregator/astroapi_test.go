package main

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"
)

const gLat, gLon = 51.4769, -0.0005

func gAstro(t time.Time) *astro {
	return newAstro(gLat, gLon, time.UTC, func() time.Time { return t })
}

func docAt(t *testing.T, tm time.Time) astroDoc {
	t.Helper()
	var d astroDoc
	if err := json.Unmarshal(gAstro(tm).doc, &d); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestAstroDoc(t *testing.T) {
	d := docAt(t, time.Date(2026, 11, 20, 21, 0, 0, 0, time.UTC))
	if d.Label != "OVERHEAD NOW" {
		t.Errorf("label %q", d.Label)
	}
	for _, s := range []string{d.Rise, d.Set, d.Moonrise, d.Moonset} {
		if _, err := time.Parse("3:04 PM", s); err != nil && s != "—" {
			t.Errorf("time %q", s)
		}
		if len(s) > 11 {
			t.Errorf("time %q too long", s)
		}
	}
	if d.Day[0] < 0 || d.Day[0] > 1439 || d.Day[1] < 0 || d.Day[1] > 1439 {
		t.Errorf("day %v", d.Day)
	}
	if len(d.Daylight) > 15 || len(d.Phase) > 23 || len(d.Label) > 23 || len(d.Overhead) > 12 || d.Illum < 0 || d.Illum > 1 {
		t.Errorf("caps: %+v", d)
	}
	for _, n := range d.Overhead {
		if len(n) > 23 {
			t.Errorf("name %q", n)
		}
	}
}

func TestAstroLabelSunUpAfter9PM(t *testing.T) {
	// 62 N in June: the sun is still up at 21:10 local, so it is OVERHEAD NOW, not TONIGHT AT 9 PM.
	tm := time.Date(2026, 6, 21, 21, 10, 0, 0, time.UTC)
	a := newAstro(62, 0, time.UTC, func() time.Time { return tm })
	var d astroDoc
	if err := json.Unmarshal(a.doc, &d); err != nil {
		t.Fatal(err)
	}
	if sunAlt(62, 0)(tm) < -0.833 {
		t.Fatal("test premise: sun should be up")
	}
	if d.Label != "OVERHEAD NOW" {
		t.Errorf("label %q", d.Label)
	}
}

func TestAstroDocDay(t *testing.T) {
	d := docAt(t, time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC))
	if d.Label != "TONIGHT AT 9 PM" {
		t.Fatalf("label %q", d.Label)
	}
	want := overhead(gLat, gLon, time.Date(2026, 11, 20, 21, 0, 0, 0, time.UTC))
	if len(want) > 12 {
		want = want[:12]
	}
	if !slices.Equal(d.Overhead, want) {
		t.Errorf("overhead %v, want %v", d.Overhead, want)
	}
}

func TestAstroRoutes(t *testing.T) {
	get := func(mux *http.ServeMux, path, inm string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer tok")
		if inm != "" {
			r.Header.Set("If-None-Match", inm)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	none := http.NewServeMux()
	addAstroRoutes(none, "tok", nil)
	if c := get(none, "/api/v1/astro", "").Code; c != 404 {
		t.Errorf("no location: %d", c)
	}
	if c := get(none, "/api/v1/astro/sky/1", "").Code; c != 404 {
		t.Errorf("no location sky: %d", c)
	}

	now := time.Date(2026, 11, 20, 21, 0, 0, 0, time.UTC)
	a := gAstro(now)
	a.renderPicture()
	mux := http.NewServeMux()
	addAstroRoutes(mux, "tok", a)
	w := get(mux, "/api/v1/astro", "")
	if w.Code != 200 || w.Header().Get("ETag") == "" || w.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("doc: %d %v", w.Code, w.Header())
	}
	if c := get(mux, "/api/v1/astro", w.Header().Get("ETag")).Code; c != 304 {
		t.Errorf("etag: %d", c)
	}
	var d astroDoc
	json.Unmarshal(w.Body.Bytes(), &d)
	if d.Sky != now.Unix() {
		t.Fatalf("sky %d", d.Sky)
	}
	w = get(mux, "/api/v1/astro/sky/"+strconv.FormatInt(d.Sky, 10), "")
	if w.Code != 200 || w.Header().Get("Content-Type") != "application/octet-stream" {
		t.Fatalf("sky: %d %v", w.Code, w.Header())
	}
	zr, err := zlib.NewReader(w.Body)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(zr)
	if len(raw) != 285768 {
		t.Errorf("inflated %d bytes", len(raw))
	}
	if c := get(mux, "/api/v1/astro/sky/"+strconv.FormatInt(d.Sky-1, 10), "").Code; c != 404 {
		t.Errorf("old sky: %d", c)
	}
}

func TestAstroRenderOffPath(t *testing.T) {
	a := gAstro(time.Date(2026, 11, 20, 21, 0, 0, 0, time.UTC))
	a.render = func(float64, float64, time.Time) ([]byte, error) { time.Sleep(2 * time.Second); return []byte{1}, nil }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.run(ctx)
	time.Sleep(50 * time.Millisecond) // the render is under way
	mux := http.NewServeMux()
	addAstroRoutes(mux, "tok", a)
	r := httptest.NewRequest("GET", "/api/v1/astro", nil)
	r.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	start := time.Now()
	mux.ServeHTTP(w, r)
	if w.Code != 200 || time.Since(start) > 100*time.Millisecond {
		t.Errorf("%d in %v", w.Code, time.Since(start))
	}
}

func TestDemoAstro(t *testing.T) {
	names := []string{"astro-night.json", "astro-day.json", "sky-night.z", "sky-day.z"}
	var runs [2][][]byte
	for i := range runs {
		dir := t.TempDir()
		if err := writeDemoAstro(dir, time.Unix(1795139700, 0)); err != nil {
			t.Fatal(err)
		}
		for _, n := range names {
			b, err := os.ReadFile(filepath.Join(dir, n))
			if err != nil || len(b) == 0 {
				t.Fatalf("%s: %v", n, err)
			}
			runs[i] = append(runs[i], b)
		}
	}
	for i, n := range names {
		if !bytes.Equal(runs[0][i], runs[1][i]) {
			t.Errorf("%s differs between runs", n)
		}
	}
}
