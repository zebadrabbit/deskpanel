package main

import (
	"math"
	"strings"
)

// Web-Mercator maths for the weather map: an frameW x frameH window centred
// on home at a fixed zoom.

const frameW, frameH = 800, 368

// view is the global pixel position (world = 256*2^z) of the frame's top-left.
type view struct {
	z      int
	x0, y0 float64
}

func world(z int) float64 { return 256 * math.Pow(2, float64(z)) }

func mercator(lat, lon float64, z int) (x, y float64) {
	w := world(z)
	s := math.Sin(lat * math.Pi / 180)
	return (lon + 180) / 360 * w, (0.5 - math.Log((1+s)/(1-s))/(4*math.Pi)) * w
}

// newView centres the frame on lat/lon.
func newView(lat, lon float64, z int) view {
	x, y := mercator(lat, lon, z)
	return view{z, x - frameW/2, y - frameH/2}
}

// px converts lat/lon to frame pixels (may fall outside the frame).
func (v view) px(lat, lon float64) (x, y float64) {
	x, y = mercator(lat, lon, v.z)
	return x - v.x0, y - v.y0
}

// tiles lists every tile (x, y) overlapping the frame.
// ponytail: no antimeridian wrap, y clamped to the world; fine for a home panel.
func (v view) tiles() [][2]int {
	n := 1 << v.z
	var out [][2]int
	for ty := int(math.Floor(v.y0 / 256)); ty <= int(math.Floor((v.y0+frameH-1)/256)); ty++ {
		for tx := int(math.Floor(v.x0 / 256)); tx <= int(math.Floor((v.x0+frameW-1)/256)); tx++ {
			if tx >= 0 && tx < n && ty >= 0 && ty < n {
				out = append(out, [2]int{tx, ty})
			}
		}
	}
	return out
}

// tileOrigin is where tile (tx, ty)'s top-left lands in frame pixels.
func (v view) tileOrigin(tx, ty int) (x, y int) {
	return int(math.Round(float64(tx*256) - v.x0)), int(math.Round(float64(ty*256) - v.y0))
}

// polyInView projects a GeoJSON [lon, lat] ring, clips it to the frame
// (Sutherland-Hodgman) and thins it to at most 16 points. nil if nothing is left.
func polyInView(v view, ring [][2]float64) [][2]int16 {
	pts := make([][2]float64, len(ring))
	for i, p := range ring {
		pts[i][0], pts[i][1] = v.px(p[1], p[0])
	}
	// each edge: inside test and intersection with that edge's line
	type edge struct {
		in  func(p [2]float64) bool
		cut func(a, b [2]float64) [2]float64
	}
	atX := func(x float64) func(a, b [2]float64) [2]float64 {
		return func(a, b [2]float64) [2]float64 {
			return [2]float64{x, a[1] + (b[1]-a[1])*(x-a[0])/(b[0]-a[0])}
		}
	}
	atY := func(y float64) func(a, b [2]float64) [2]float64 {
		return func(a, b [2]float64) [2]float64 {
			return [2]float64{a[0] + (b[0]-a[0])*(y-a[1])/(b[1]-a[1]), y}
		}
	}
	edges := []edge{
		{func(p [2]float64) bool { return p[0] >= 0 }, atX(0)},
		{func(p [2]float64) bool { return p[0] <= frameW-1 }, atX(frameW - 1)},
		{func(p [2]float64) bool { return p[1] >= 0 }, atY(0)},
		{func(p [2]float64) bool { return p[1] <= frameH-1 }, atY(frameH - 1)},
	}
	for _, e := range edges {
		in := pts
		pts = nil
		for i, cur := range in {
			prev := in[(i+len(in)-1)%len(in)]
			if e.in(cur) {
				if !e.in(prev) {
					pts = append(pts, e.cut(prev, cur))
				}
				pts = append(pts, cur)
			} else if e.in(prev) {
				pts = append(pts, e.cut(prev, cur))
			}
		}
		if len(pts) == 0 {
			return nil
		}
	}
	// ponytail: plain decimation, may cut corners on very wiggly polygons
	for len(pts) > 16 {
		var half [][2]float64
		for i := 0; i < len(pts); i += 2 {
			half = append(half, pts[i])
		}
		pts = half
	}
	if len(pts) < 3 {
		return nil
	}
	out := make([][2]int16, len(pts))
	for i, p := range pts {
		out[i] = [2]int16{int16(math.Round(p[0])), int16(math.Round(p[1]))}
	}
	return out
}

// pointInRing is an even-odd ray test; ring points are [lon, lat].
func pointInRing(lon, lat float64, ring [][2]float64) bool {
	in := false
	for i, j := 0, len(ring)-1; i < len(ring); j, i = i, i+1 {
		a, b := ring[i], ring[j]
		if (a[1] > lat) != (b[1] > lat) && lon < (b[0]-a[0])*(lat-a[1])/(b[1]-a[1])+a[0] {
			in = !in
		}
	}
	return in
}

// areaBox is a state's bounding box in degrees.
type areaBox struct {
	code       string
	s, w, n, e float64
}

// areasIn returns the codes of the boxes overlapping the frame, comma-joined in table order, or "".
func areasIn(v view, boxes []areaBox) string {
	var out []string
	for _, b := range boxes {
		x1, y1 := v.px(b.n, b.w) // Mercator is monotonic: north-west is the top-left corner
		x2, y2 := v.px(b.s, b.e)
		if x1 < frameW && x2 > 0 && y1 < frameH && y2 > 0 {
			out = append(out, b.code)
		}
	}
	return strings.Join(out, ",")
}

// ponytail: whole-degree boxes rounded outward, so a superset of the states the map touches; exact shapes if that ever matters.
// Only the 48 contiguous states + DC: Alaska/Hawaii/Puerto Rico outlines drop when the frame also touches a box (the takeover is unaffected).
var stateBoxes = []areaBox{
	{"AL", 30, -89, 36, -84}, {"AZ", 31, -115, 38, -109}, {"AR", 32, -95, 37, -89}, {"CA", 32, -125, 43, -114},
	{"CO", 36, -110, 42, -102}, {"CT", 40, -74, 43, -71}, {"DE", 38, -76, 40, -75}, {"DC", 38, -78, 39, -76},
	{"FL", 24, -88, 32, -80}, {"GA", 30, -86, 36, -80}, {"ID", 41, -118, 50, -111}, {"IL", 36, -92, 43, -87},
	{"IN", 37, -89, 42, -84}, {"IA", 40, -97, 44, -90}, {"KS", 36, -103, 41, -94}, {"KY", 36, -90, 40, -81},
	{"LA", 28, -95, 34, -88}, {"ME", 43, -72, 48, -66}, {"MD", 37, -80, 40, -75}, {"MA", 41, -74, 43, -69},
	{"MI", 41, -91, 49, -82}, {"MN", 43, -98, 50, -89}, {"MS", 30, -92, 36, -88}, {"MO", 35, -96, 41, -89},
	{"MT", 44, -117, 50, -104}, {"NE", 39, -105, 44, -95}, {"NV", 34, -121, 43, -114}, {"NH", 42, -73, 46, -70},
	{"NJ", 38, -76, 42, -73}, {"NM", 31, -110, 38, -102}, {"NY", 40, -80, 46, -71}, {"NC", 33, -85, 37, -75},
	{"ND", 45, -105, 50, -96}, {"OH", 38, -85, 43, -80}, {"OK", 33, -104, 38, -94}, {"OR", 41, -125, 47, -116},
	{"PA", 39, -81, 43, -74}, {"RI", 41, -72, 43, -71}, {"SC", 32, -84, 36, -78}, {"SD", 42, -105, 46, -96},
	{"TN", 34, -91, 37, -81}, {"TX", 25, -107, 37, -93}, {"UT", 36, -115, 43, -109}, {"VT", 42, -74, 46, -71},
	{"VA", 36, -84, 40, -75}, {"WA", 45, -125, 50, -116}, {"WV", 37, -83, 41, -77}, {"WI", 42, -93, 48, -86},
	{"WY", 40, -112, 46, -104},
}
