package main

// The astronomical clock's sky picture: the sky above the location as a
// 378 × 378 disc (zenith at the centre, horizon at radius 189, azimuthal
// equidistant, north up, east left as you look up), tinted by daylight, with
// the Milky Way, constellation figures, stars, moon and sun. Everything
// outside the disc is the dial colour #040814, so the panel needs no mask.

import (
	"cmp"
	_ "embed"
	"encoding/json"
	"fmt"
	"image"
	"math"
	"slices"
	"strings"
	"time"
)

type star struct{ RA, Dec, Mag float64 }

// figure is one constellation's stick figure as RA/Dec polylines (degrees).
type figure struct {
	Name  string
	Lines [][][2]float64
}

var (
	//go:embed skydata/stars.csv
	starsCSV string
	//go:embed skydata/figures.json
	figuresJSON []byte

	stars   []star // magnitude ≤ 4.5, brightest first
	figures []figure
)

func init() {
	for _, ln := range strings.Split(strings.TrimSpace(starsCSV), "\n")[1:] {
		var s star
		if _, err := fmt.Sscanf(ln, "%g,%g,%g", &s.RA, &s.Dec, &s.Mag); err != nil {
			panic("skydata/stars.csv: " + ln + ": " + err.Error())
		}
		stars = append(stars, s)
	}
	if err := json.Unmarshal(figuresJSON, &figures); err != nil {
		panic("skydata/figures.json: " + err.Error())
	}
}

// overhead lists the constellations with a figure vertex more than 40° up, highest first.
func overhead(lat, lon float64, t time.Time) []string {
	type hit struct {
		name string
		alt  float64
	}
	var hits []hit
	for _, f := range figures {
		top := -90.0
		for _, pl := range f.Lines {
			for _, p := range pl {
				a, _ := altAz(p[0], p[1], lat, lon, t)
				top = max(top, a)
			}
		}
		if top > 40 {
			hits = append(hits, hit{f.Name, top})
		}
	}
	slices.SortStableFunc(hits, func(a, b hit) int { return cmp.Compare(b.alt, a.alt) })
	names := make([]string, len(hits))
	for i, h := range hits {
		names[i] = h.name
	}
	return names
}

const (
	skyN = 378
	skyR = 189.0
)

// skyXY projects altitude/azimuth (degrees) onto the picture; pixel (i, j)
// covers [i, i+1) × [j, j+1). Below-horizon points land outside radius 189.
func skyXY(alt, az float64) (x, y float64) {
	r := skyR * (90 - alt) / 90
	return skyR - r*math.Sin(az*d2r), skyR - r*math.Cos(az*d2r)
}

// galactic converts RA/Dec (J2000, degrees) to galactic longitude/latitude.
func galactic(ra, dec float64) (l, b float64) {
	const raP, decP, lNCP = 192.85948 * d2r, 27.12825 * d2r, 122.93192
	sd, cd := math.Sincos(dec * d2r)
	sp, cp := math.Sincos(decP)
	sh, ch := math.Sincos(ra*d2r - raP)
	b = math.Asin(math.Max(-1, math.Min(1, sd*sp+cd*cp*ch))) * r2d
	l = norm360(lNCP - math.Atan2(cd*sh, sd*cp-cd*sp*ch)*r2d)
	return l, b
}

type rgbf = [3]float64

func rgb(h uint32) rgbf { return rgbf{float64(h >> 16), float64(h >> 8 & 0xff), float64(h & 0xff)} }

func mix(a, b rgbf, f float64) rgbf {
	for k := range a {
		a[k] += (b[k] - a[k]) * f
	}
	return a
}

func clamp01(v float64) float64 { return math.Max(0, math.Min(1, v)) }

// canvas is the picture in float RGB (0..255) while the layers go down.
type canvas []rgbf

func (c canvas) blend(x, y int, col rgbf, a float64) {
	if x >= 0 && y >= 0 && x < skyN && y < skyN && a > 0 {
		c[y*skyN+x] = mix(c[y*skyN+x], col, math.Min(a, 1))
	}
}

// line draws an antialiased 1 px line (Xiaolin Wu) between picture points.
func (c canvas) line(x0, y0, x1, y1 float64, col rgbf, a float64) {
	x0, y0, x1, y1 = x0-0.5, y0-0.5, x1-0.5, y1-0.5 // pixel centres on integers
	steep := math.Abs(y1-y0) > math.Abs(x1-x0)
	if steep {
		x0, y0, x1, y1 = y0, x0, y1, x1
	}
	if x0 > x1 {
		x0, y0, x1, y1 = x1, y1, x0, y0
	}
	g := 0.0
	if x1 > x0 {
		g = (y1 - y0) / (x1 - x0)
	}
	frac := func(v float64) float64 { return v - math.Floor(v) }
	xs, xe := math.Round(x0), math.Round(x1)
	for x := xs; x <= xe; x++ {
		w := 1.0
		if x == xs {
			w = 1 - frac(x0+0.5)
		}
		if x == xe {
			w = math.Min(w, frac(x1+0.5))
		}
		y := y0 + g*(x-x0)
		fy := math.Floor(y)
		for i, v := range [2]float64{1 - (y - fy), y - fy} {
			px, py := int(x), int(fy)+i
			if steep {
				px, py = py, px
			}
			c.blend(px, py, col, a*v*w)
		}
	}
}

// disc paints a filled circle with an antialiased edge; shade gives each
// pixel's colour and alpha from its offset (dx, dy) and distance d.
func (c canvas) disc(cx, cy, r float64, shade func(dx, dy, d float64) (rgbf, float64)) {
	for y := int(cy - r - 1); y <= int(cy+r+1); y++ {
		for x := int(cx - r - 1); x <= int(cx+r+1); x++ {
			dx, dy := float64(x)+0.5-cx, float64(y)+0.5-cy
			if d := math.Hypot(dx, dy); d < r+0.5 {
				col, a := shade(dx, dy, d)
				c.blend(x, y, col, a)
			}
		}
	}
}

var (
	dialBG   = rgb(0x040814)
	nightMid = rgb(0x0d2350)
	nightRim = rgb(0x050b1e)
	dayMid   = rgb(0x2b5a95)
	dayRim   = rgb(0x122a52)
	milkyCol = rgb(0x9fb6ff)
	lineCol  = rgb(0xc9a75e)
	starCol  = rgb(0xfff8e1)
	moonDark = rgb(0x2a3550)
	moonLit  = rgb(0xe9eef6)
	moonEdge = rgb(0xc9d3e3)
	sunGlow  = rgb(0xffd27a)
	sunDisc  = rgb(0xffe6a8)
)

const (
	moonR     = 9.0
	sunR      = 9.0
	sunGlowR  = 26.0
	lineAlpha = 0.45
)

// renderSky draws the sky above (lat, lon) at t.
func renderSky(lat, lon float64, t time.Time) *image.RGBA {
	sra, sdec := sunPos(t)
	sunAlt, sunAz := altAz(sra, sdec, lat, lon, t)
	day := clamp01((sunAlt + 12) / 12) // 0 = night (sun below -12°), 1 = day (above 0°)
	mid, rim := mix(nightMid, dayMid, day), mix(nightRim, dayRim, day)
	starK, milkyK := 1-0.45*day, 1-0.70*day

	c := make(canvas, skyN*skyN)

	// Background gradient and Milky Way, per pixel inside the horizon. The
	// glow follows the galactic plane (|b| falloff), brighter toward l = 0.
	sidereal := lst(t, lon)
	sl, cl := math.Sincos(lat * d2r)
	for y := range skyN {
		for x := range skyN {
			dx, dy := float64(x)+0.5-skyR, float64(y)+0.5-skyR
			r := math.Hypot(dx, dy)
			if r > skyR {
				continue
			}
			sa, ca := math.Sincos((90 - 90*r/skyR) * d2r)
			sz, cz := math.Sincos(math.Atan2(-dx, -dy)) // azimuth, inverse of skyXY
			dec := math.Asin(sa*sl+ca*cl*cz) * r2d
			ha := math.Atan2(-sz*ca, cl*sa-sl*ca*cz) * r2d
			l, b := galactic(norm360(sidereal-ha), dec)
			along := 0.40 + 0.75*math.Pow(math.Max(0, math.Cos(l*d2r)), 2)
			across := 0.65*math.Exp(-0.5*(b/5)*(b/5)) + 0.35*math.Exp(-0.5*(b/12)*(b/12)) // core + halo
			c[y*skyN+x] = mix(mix(mid, rim, r/skyR), milkyCol, milkyK*0.30*along*across)
		}
	}

	// Constellation figures. A segment with one end below the horizon runs
	// out to the rim; the corner paint clips it.
	for _, f := range figures {
		for _, pl := range f.Lines {
			for i := 1; i < len(pl); i++ {
				a0, z0 := altAz(pl[i-1][0], pl[i-1][1], lat, lon, t)
				a1, z1 := altAz(pl[i][0], pl[i][1], lat, lon, t)
				if a0 > 0 || a1 > 0 {
					x0, y0 := skyXY(a0, z0)
					x1, y1 := skyXY(a1, z1)
					c.line(x0, y0, x1, y1, lineCol, lineAlpha*starK)
				}
			}
		}
	}

	// Stars, faintest first so bright ones sit on top. Radius by magnitude;
	// the faint end also fades so the field reads as depth, not noise.
	for i := len(stars) - 1; i >= 0; i-- {
		s := stars[i]
		alt, az := altAz(s.RA, s.Dec, lat, lon, t)
		if alt <= 0 {
			continue
		}
		x, y := skyXY(alt, az)
		r := math.Max(1.1, 3.4-0.8*s.Mag)
		a := starK * math.Max(0.45, math.Min(1, 1.6-0.25*s.Mag))
		glow := 0.25 * (r - 1.1) / 2.3 // bright stars get a faint halo out to 2r
		c.disc(x, y, 2*r, func(_, _, d float64) (rgbf, float64) {
			return starCol, a * math.Max(clamp01(r+0.5-d), glow*math.Exp(-(d/r)*(d/r)))
		})
	}

	// The moon, lit side by phase: waxing lit on the right (west), waning on the left.
	mra, mdec, _ := moonPos(t)
	if alt, az := altAz(mra, mdec, lat, lon, t); alt > 0 {
		illum, waxing, _ := moonPhase(t)
		k := 2*illum - 1 // terminator: lit where u ≥ -k·sqrt(1-v²)
		x, y := skyXY(alt, az)
		c.disc(x, y, moonR, func(dx, dy, d float64) (rgbf, float64) {
			u, v := dx/moonR, dy/moonR
			if !waxing {
				u = -u
			}
			lit := clamp01((u+k*math.Sqrt(math.Max(0, 1-v*v)))*moonR + 0.5)
			return mix(mix(moonDark, moonLit, lit), moonEdge, clamp01(0.9-math.Abs(d-moonR))), clamp01(moonR + 0.5 - d)
		})
	}

	// The sun: a soft warm glow to radius 26 under a radius-9 disc.
	if sunAlt > 0 {
		x, y := skyXY(sunAlt, sunAz)
		c.disc(x, y, sunGlowR, func(_, _, d float64) (rgbf, float64) {
			f := clamp01((d - 6) / (sunGlowR - 6))
			return sunGlow, 0.42 * (1 - f*f*(3-2*f)) // smoothstep falloff
		})
		c.disc(x, y, sunR, func(_, _, d float64) (rgbf, float64) { return sunDisc, clamp01(sunR + 0.5 - d) })
	}

	img := image.NewRGBA(image.Rect(0, 0, skyN, skyN))
	for i, p := range c {
		x, y := i%skyN, i/skyN
		if math.Hypot(float64(x)+0.5-skyR, float64(y)+0.5-skyR) > skyR {
			p = dialBG // corners last, exactly the dial colour
		}
		for k := range 3 {
			img.Pix[i*4+k] = uint8(math.Round(math.Max(0, math.Min(255, p[k]))))
		}
		img.Pix[i*4+3] = 0xff
	}
	return img
}
