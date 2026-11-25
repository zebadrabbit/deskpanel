package main

import (
	"bytes"
	"compress/zlib"
	"image"
	"image/color"
	"io"
	"math"
	"slices"
	"testing"
	"time"
)

var (
	skyNight = time.Date(2026, 11, 20, 21, 0, 0, 0, time.UTC)
	skyNoon  = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	skyMid   = time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
)

func TestSkyData(t *testing.T) {
	if len(stars) < 800 || len(figures) != 88 {
		t.Fatalf("%d stars, %d figures", len(stars), len(figures))
	}
	// Serpens' two halves are one figure, named for the whole.
	if !slices.ContainsFunc(figures, func(f figure) bool { return f.Name == "Serpens" }) {
		t.Fatal("no figure named Serpens")
	}
	for _, s := range stars {
		if s.Mag > 4.5 || s.RA < 0 || s.RA >= 360 || math.Abs(s.Dec) > 90 {
			t.Fatalf("bad star %+v", s)
		}
	}
	// The Milky Way hangs on this: galactic centre at (0, 0), north galactic pole at b = 90.
	if l, b := galactic(266.405, -28.936); math.Abs(b) > 0.1 || math.Min(l, 360-l) > 0.1 {
		t.Fatalf("galactic centre at l %.2f b %.2f", l, b)
	}
	if _, b := galactic(192.859, 27.128); b < 89.9 {
		t.Fatalf("pole at b %.2f", b)
	}
}

func TestRenderSize(t *testing.T) {
	if b := renderSky(grLat, grLon, skyNight).Bounds(); b != image.Rect(0, 0, 378, 378) {
		t.Fatal(b)
	}
}

func TestRenderDeterministic(t *testing.T) {
	start := time.Now()
	a := renderSky(grLat, grLon, skyNight)
	if d := time.Since(start); d > time.Second {
		t.Fatalf("render took %v", d)
	}
	if b := renderSky(grLat, grLon, skyNight); !bytes.Equal(a.Pix, b.Pix) {
		t.Fatal("two renders differ")
	}
}

func TestCornersExact(t *testing.T) {
	want := color.RGBA{0x04, 0x08, 0x14, 0xff}
	for _, tm := range []time.Time{skyNight, skyNoon} {
		img := renderSky(grLat, grLon, tm)
		if img.RGBAAt(0, 0) != want {
			t.Fatalf("corner %v", img.RGBAAt(0, 0))
		}
		for y := 0; y < 378; y++ {
			for x := 0; x < 378; x++ {
				if math.Hypot(float64(x)+0.5-189, float64(y)+0.5-189) > 190 && img.RGBAAt(x, y) != want {
					t.Fatalf("%v at (%d,%d)", img.RGBAAt(x, y), x, y)
				}
			}
		}
	}
}

func TestDayBluer(t *testing.T) {
	day := renderSky(grLat, grLon, skyNoon).RGBAAt(189, 189)
	night := renderSky(grLat, grLon, skyMid).RGBAAt(189, 189)
	if day.B <= night.B || day.G <= night.G {
		t.Fatalf("noon %v midnight %v", day, night)
	}
}

func lum(c color.RGBA) int { return int(c.R) + int(c.G) + int(c.B) }

func TestStarAppears(t *testing.T) {
	img := renderSky(grLat, grLon, skyNight)
	alt, az := altAz(310.358, 45.280, grLat, grLon, skyNight) // Deneb
	fx, fy := skyXY(alt, az)
	x, y := int(fx), int(fy)
	for _, d := range [][2]int{{6, 0}, {-6, 0}, {0, 6}, {0, -6}} {
		if lum(img.RGBAAt(x, y)) <= lum(img.RGBAAt(x+d[0], y+d[1])) {
			t.Fatalf("Deneb %v vs %v at %v", img.RGBAAt(x, y), img.RGBAAt(x+d[0], y+d[1]), d)
		}
	}
}

func TestSunDrawn(t *testing.T) {
	ra, dec := sunPos(skyNoon)
	alt, az := altAz(ra, dec, grLat, grLon, skyNoon)
	fx, fy := skyXY(alt, az)
	if c := renderSky(grLat, grLon, skyNoon).RGBAAt(int(fx), int(fy)); alt <= 0 || c.R <= c.B {
		t.Fatalf("noon sun alt %.1f pixel %v", alt, c)
	}
	// At midnight the sun is below the north horizon: the rim towards it stays cool.
	img := renderSky(grLat, grLon, skyMid)
	ra, dec = sunPos(skyMid)
	alt, az = altAz(ra, dec, grLat, grLon, skyMid)
	for r := 160.0; r <= 188; r++ {
		x, y := int(189-r*math.Sin(az*d2r)), int(189-r*math.Cos(az*d2r))
		if c := img.RGBAAt(x, y); alt >= 0 || c.R > c.B {
			t.Fatalf("midnight sun alt %.1f, warm %v at (%d,%d)", alt, c, x, y)
		}
	}
}

func TestOverheadNight(t *testing.T) {
	got := overhead(grLat, grLon, skyNight)
	if !slices.Contains(got, "Cassiopeia") {
		t.Fatalf("no Cassiopeia in %v", got)
	}
	top := func(name string) float64 {
		best := -90.0
		for _, f := range figures {
			if f.Name != name {
				continue
			}
			for _, pl := range f.Lines {
				for _, p := range pl {
					a, _ := altAz(p[0], p[1], grLat, grLon, skyNight)
					best = math.Max(best, a)
				}
			}
		}
		return best
	}
	for i, n := range got {
		if top(n) <= 40 || (i > 0 && top(n) > top(got[i-1])) {
			t.Fatalf("order or cut wrong at %d in %v", i, got)
		}
	}
}

func TestEncodeRGB565Size(t *testing.T) {
	z, err := encodeRGB565(renderSky(grLat, grLon, skyNight))
	if err != nil {
		t.Fatal(err)
	}
	r, err := zlib.NewReader(bytes.NewReader(z))
	if err != nil {
		t.Fatal(err)
	}
	if raw, _ := io.ReadAll(r); len(raw) != 285768 || raw[0] != 0x42 || raw[1] != 0x00 {
		t.Fatalf("raw %d bytes, starts % x (want 42 00 for #040814)", len(raw), raw[:2])
	}
	frame := image.NewRGBA(image.Rect(0, 0, frameW, frameH))
	frame.Pix[0] = 0xff
	a, err1 := encodeFrame(frame)
	b, err2 := encodeRGB565(frame)
	if err1 != nil || err2 != nil || !bytes.Equal(a, b) {
		t.Fatal("encodeFrame differs from encodeRGB565 on a frame")
	}
	if _, err := encodeFrame(image.NewRGBA(image.Rect(0, 0, 378, 378))); err == nil {
		t.Fatal("encodeFrame accepted 378x378")
	}
}

func BenchmarkRenderSky(b *testing.B) {
	for b.Loop() {
		renderSky(grLat, grLon, skyNight)
	}
}
