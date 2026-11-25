package main

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

const homeLat, homeLon = 39.7456, -97.0892

func TestTileMath(t *testing.T) {
	v7 := newView(homeLat, homeLon, 7)
	// tile containing home at z=7
	hx, hy := v7.px(homeLat, homeLon)
	tx := int(math.Floor((v7.x0 + hx) / 256))
	ty := int(math.Floor((v7.y0 + hy) / 256))
	if tx != 29 || ty != 48 {
		t.Fatalf("tile = (%d,%d), want (29,48)", tx, ty)
	}
	v := newView(homeLat, homeLon, 8)
	x, y := v.px(homeLat, homeLon)
	if math.Abs(x-400) > 0.5 || math.Abs(y-184) > 0.5 {
		t.Fatalf("px = (%v,%v), want ~(400,184)", x, y)
	}
}

func TestViewTiles(t *testing.T) {
	v := newView(homeLat, homeLon, 8)
	covered := make([]bool, frameW*frameH)
	for _, tl := range v.tiles() {
		ox, oy := v.tileOrigin(tl[0], tl[1])
		if ox+256 <= 0 || oy+256 <= 0 || ox >= frameW || oy >= frameH {
			t.Fatalf("tile %v lies outside the frame (origin %d,%d)", tl, ox, oy)
		}
		for y := oy; y < oy+256; y++ {
			for x := ox; x < ox+256; x++ {
				if x >= 0 && x < frameW && y >= 0 && y < frameH {
					covered[y*frameW+x] = true
				}
			}
		}
	}
	for i, c := range covered {
		if !c {
			t.Fatalf("pixel (%d,%d) not covered", i%frameW, i/frameW)
		}
	}
}

func square(lat, lon, d float64) [][2]float64 {
	return [][2]float64{{lon - d, lat - d}, {lon + d, lat - d}, {lon + d, lat + d}, {lon - d, lat + d}}
}

func inFrame(p [][2]int16) bool {
	for _, q := range p {
		if q[0] < 0 || q[0] > frameW-1 || q[1] < 0 || q[1] > frameH-1 {
			return false
		}
	}
	return true
}

func TestPolyInView(t *testing.T) {
	v := newView(homeLat, homeLon, 8)

	small := square(homeLat, homeLon, 0.1)
	if p := polyInView(v, small); len(p) != 4 || !inFrame(p) {
		t.Fatalf("small square = %v", p)
	}
	if !pointInRing(homeLon, homeLat, small) {
		t.Fatal("home not in small square")
	}
	if pointInRing(homeLon+1, homeLat, small) {
		t.Fatal("outside point reported inside")
	}

	if p := polyInView(v, square(homeLat+20, homeLon, 0.1)); p != nil {
		t.Fatalf("far square = %v, want nil", p)
	}

	p := polyInView(v, square(homeLat, homeLon, 10))
	if len(p) != 4 {
		t.Fatalf("big square = %v, want 4 corners", p)
	}
	for _, c := range [][2]int16{{0, 0}, {frameW - 1, 0}, {frameW - 1, frameH - 1}, {0, frameH - 1}} {
		ok := false
		for _, q := range p {
			if abs16(q[0]-c[0]) <= 1 && abs16(q[1]-c[1]) <= 1 {
				ok = true
			}
		}
		if !ok {
			t.Fatalf("corner %v missing from %v", c, p)
		}
	}

	// 40-point circle straddling the left frame edge
	lonEdge := homeLon - 400*360/float64(256<<8)
	var circle [][2]float64
	for i := 0; i < 40; i++ {
		a := 2 * math.Pi * float64(i) / 40
		circle = append(circle, [2]float64{lonEdge + 0.5*math.Cos(a), homeLat + 0.3*math.Sin(a)})
	}
	p = polyInView(v, circle)
	if len(p) < 3 || len(p) > 16 || !inFrame(p) {
		t.Fatalf("circle = %d points %v", len(p), p)
	}
}

func abs16(a int16) int16 {
	if a < 0 {
		return -a
	}
	return a
}

func TestFrameEncoding(t *testing.T) {
	if rgb565(255, 0, 0) != 0xF800 || rgb565(0, 255, 0) != 0x07E0 || rgb565(0, 0, 255) != 0x001F {
		t.Fatal("rgb565 primaries wrong")
	}
	img := image.NewRGBA(image.Rect(0, 0, frameW, frameH))
	for y := 0; y < frameH; y++ {
		for x := 0; x < frameW; x++ {
			if x < frameW/2 {
				img.Set(x, y, color.RGBA{255, 0, 0, 255})
			} else {
				img.Set(x, y, color.RGBA{0, 0, 255, 255})
			}
		}
	}
	z, err := encodeFrame(img)
	if err != nil {
		t.Fatal(err)
	}
	px, err := decodeFrame(z)
	if err != nil {
		t.Fatal(err)
	}
	if len(px) != 294400 || px[0] != 0xF800 || px[799] != 0x001F {
		t.Fatalf("len=%d px0=%#x px799=%#x", len(px), px[0], px[799])
	}
	r, _ := zlib.NewReader(bytes.NewReader(z))
	raw, _ := io.ReadAll(r)
	if raw[0] != 0x00 || raw[1] != 0xF8 {
		t.Fatalf("raw starts % x, want 00 f8", raw[:2])
	}
	if _, err := encodeFrame(image.NewRGBA(image.Rect(0, 0, 10, 10))); err == nil {
		t.Fatal("wrong size accepted")
	}
}

// radarServer serves n0q_0.json (settable valid) and solid tiles, counting requests by path prefix.
type radarServer struct {
	*httptest.Server
	mu     sync.Mutex
	valid  time.Time
	counts map[string]int
	bump   bool // move valid +300 s on the next radar tile request
}

func newRadarServer() *radarServer {
	s := &radarServer{valid: time.Date(2026, 11, 18, 7, 10, 0, 0, time.UTC), counts: map[string]int{}}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		p := r.URL.Path
		switch {
		case strings.HasSuffix(p, "n0q_0.json"):
			fmt.Fprintf(w, `{"meta":{"valid":%q}}`, s.valid.Format(time.RFC3339))
		case strings.Contains(p, "nexrad-n0q-900913"):
			layer := strings.Split(p, "/")[4]
			s.counts[layer]++
			if s.bump {
				s.bump = false
				s.valid = s.valid.Add(300 * time.Second)
			}
			age := 0 // minutes behind newest
			fmt.Sscanf(layer, "nexrad-n0q-900913-m%dm", &age)
			solidTile(w, color.RGBA{scanRed(s.valid.Unix() - int64(age*60)), 255, 0, 255})
		case strings.HasSuffix(p, ".png"): // OSM-style /{z}/{x}/{y}.png
			s.counts["base"]++
			solidTile(w, testBase)
		default:
			http.NotFound(w, r)
		}
	}))
	return s
}

func (s *radarServer) count(k string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.counts[k]
}

func (s *radarServer) radarCounts() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := map[string]int{}
	for k, n := range s.counts {
		if k != "base" {
			m[k] = n
		}
	}
	return m
}

// testBase is the (light) colour of every fake OSM tile.
var testBase = color.RGBA{0xf2, 0xef, 0xe9, 255}

func TestDark(t *testing.T) {
	if c := dark(color.RGBA{255, 255, 255, 255}); c.R > 20 || c.G > 20 || c.B > 30 {
		t.Fatalf("white -> %v, want near-black", c)
	}
	if c := dark(color.RGBA{0, 0, 0, 255}); c.R < 120 || c.R > 160 {
		t.Fatalf("black -> %v, want light grey", c)
	}
	if c := dark(color.RGBA{0xaa, 0xd3, 0xdf, 255}); c.B <= c.R || c.B > 120 { // OSM water
		t.Fatalf("water -> %v, want dark blue", c)
	}
}

func solidTile(w http.ResponseWriter, c color.RGBA) {
	img := image.NewRGBA(image.Rect(0, 0, 256, 256))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = c.R, c.G, c.B, c.A
	}
	png.Encode(w, img)
}

func testRadar(s *radarServer, cacheDir string) *radar {
	return newRadar(newView(homeLat, homeLon, 8), tileClient{s.Client(), "test"}, s.URL, s.URL, cacheDir)
}

func TestRadarCollect(t *testing.T) {
	s := newRadarServer()
	defer s.Close()
	r := testRadar(s, "")
	got, err := r.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	times := got.([]int64)
	valid := s.valid.Unix()
	if len(times) != 5 {
		t.Fatalf("times = %v", times)
	}
	for i, ts := range times {
		if want := valid - 1200 + int64(i)*300; ts != want {
			t.Fatalf("times[%d] = %d, want %d", i, ts, want)
		}
	}
	z, ok := r.frame(valid)
	if !ok {
		t.Fatal("no frame for valid")
	}
	px, err := decodeFrame(z)
	if err != nil || len(px) != 294400 {
		t.Fatalf("decode: %v len=%d", err, len(px))
	}
	// 85 % of (scanRed, 255, 0) over the darkened base
	d := dark(testBase)
	mix := func(s, b uint8) uint8 { return uint8(math.Round(float64(s)*0.85 + float64(b)*0.15)) }
	if want := rgb565(mix(scanRed(valid), d.R), mix(255, d.G), mix(0, d.B)); px[0] != want {
		t.Fatalf("px0 = %#x, want %#x", px[0], want)
	}
	if _, ok := r.frame(12345); ok {
		t.Fatal("unexpected frame")
	}
}

func TestRadarAdvance(t *testing.T) {
	s := newRadarServer()
	defer s.Close()
	r := testRadar(s, "")
	first, _ := r.Collect(context.Background())
	s.mu.Lock()
	s.valid = s.valid.Add(300 * time.Second)
	s.counts = map[string]int{}
	s.mu.Unlock()
	got, err := r.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	times := got.([]int64)
	if len(times) != 5 || times[4] != s.valid.Unix() || times[0] != first.([]int64)[1] {
		t.Fatalf("times = %v, first = %v", times, first)
	}
	if _, ok := r.frame(first.([]int64)[0]); ok {
		t.Fatal("oldest frame not pruned")
	}
	rc := s.radarCounts()
	if len(rc) != 1 || rc["nexrad-n0q-900913"] == 0 {
		t.Fatalf("radar requests = %v, want only the newest layer", rc)
	}
}

func TestRadarBaseCached(t *testing.T) {
	s := newRadarServer()
	defer s.Close()
	dir := t.TempDir()
	if _, err := testRadar(s, dir).Collect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s.count("base") == 0 {
		t.Fatal("first run fetched no base tiles")
	}
	s.mu.Lock()
	s.counts = map[string]int{}
	s.mu.Unlock()
	if _, err := testRadar(s, dir).Collect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := s.count("base"); n != 0 {
		t.Fatalf("second run made %d base requests", n)
	}
}

// scanRed identifies the imagery time of a radar tile (24 per 5 minutes).
func scanRed(imagery int64) uint8 { return uint8((imagery-radarT0)/300) * 24 }

var radarT0 = time.Date(2026, 11, 18, 7, 10, 0, 0, time.UTC).Unix() - 1200

// TestRadarScanMovesMidBuild: every returned time's frame must show imagery of
// exactly that time, even when a new scan lands during the first pass.
func TestRadarScanMovesMidBuild(t *testing.T) {
	s := newRadarServer()
	defer s.Close()
	s.bump = true
	r := testRadar(s, "")
	got, err := r.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	times := got.([]int64)
	if len(times) != 5 || times[4] != s.valid.Unix() || s.valid.Unix() != radarT0+1200+300 {
		t.Fatalf("times = %v, valid = %d", times, s.valid.Unix())
	}
	for _, ts := range times {
		z, ok := r.frame(ts)
		if !ok {
			t.Fatalf("no frame for %d", ts)
		}
		px, _ := decodeFrame(z)
		// 85 % of the red channel (+15 % of the darkened base), in 5-bit units
		want := int(float64(scanRed(ts))*0.85+float64(dark(testBase).R)*0.15) >> 3
		if got := int(px[0] >> 11); got < want-1 || got > want+1 {
			t.Fatalf("frame %d red5 = %d, want ~%d", ts, got, want)
		}
	}
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseAlerts(t *testing.T) {
	as, err := parseAlerts(fixture(t, "nws-home.json"))
	if err != nil || len(as) != 5 {
		t.Fatalf("got %d alerts, err %v", len(as), err)
	}
	tor, watch := as[0], as[2]
	if tor.Event != "Tornado Warning" || tor.MsgType != "Alert" || tor.Detection != "RADAR INDICATED" ||
		tor.Instr == "" || tor.Desc == "" || tor.Headline == "" || !strings.HasPrefix(tor.ID, "urn:oid:") {
		t.Fatalf("tornado warning = %+v", tor)
	}
	if got := tor.Expires.Format("3:04 PM"); got != "2:45 PM" {
		t.Fatalf("expires = %s", got)
	}
	if got := tor.Sent.Format("3:04 PM"); got != "2:02 PM" {
		t.Fatalf("sent = %s", got)
	}
	// closed GeoJSON ring: the repeated last point is dropped
	if len(tor.Rings) != 1 || len(tor.Rings[0]) != 4 {
		t.Fatalf("rings = %v", tor.Rings)
	}
	if watch.Event != "Tornado Watch" || watch.Rings != nil || watch.Ends.IsZero() || !tor.Ends.IsZero() {
		t.Fatalf("watch = %+v", watch)
	}
	if _, err := parseAlerts([]byte("{")); err == nil {
		t.Fatal("bad JSON should error")
	}
}

func TestParseAlertsMultiPolygon(t *testing.T) {
	body := `{"features":[{"geometry":{"type":"MultiPolygon","coordinates":[
		[[[-97,39],[-97,40],[-96,40],[-97,39]],[[-96.9,39.1],[-96.9,39.2],[-96.8,39.2],[-96.9,39.1]]],
		[[[-95,39],[-95,40],[-94,40],[-95,39]]]]},
		"properties":{"id":"x","event":"Tornado Warning","sent":"2026-11-18T14:02:00-06:00"}}]}`
	as, err := parseAlerts([]byte(body))
	if err != nil || len(as) != 1 || len(as[0].Rings) != 2 || len(as[0].Rings[0]) != 3 {
		t.Fatalf("rings = %v, err %v", as, err)
	}
}

func TestAlertRules(t *testing.T) {
	for ev, want := range map[string]string{
		"Tornado Warning": "warning", "Tornado Watch": "watch",
		"Special Weather Statement": "advisory", "Wind Advisory": "advisory", "": "advisory",
	} {
		if got := alertClass(ev); got != want {
			t.Errorf("alertClass(%q) = %q, want %q", ev, got, want)
		}
	}
	if !isTakeover("Tornado Warning") || !isTakeover("Extreme Wind Warning") ||
		isTakeover("Severe Thunderstorm Warning") || isTakeover("Tornado Watch") {
		t.Error("isTakeover wrong")
	}
	for _, c := range []struct {
		det, dmg string
		want     int
	}{{"", "", 0}, {"RADAR INDICATED", "", 0}, {"OBSERVED", "", 1}, {"OBSERVED", "CONSIDERABLE", 2}, {"RADAR INDICATED", "CATASTROPHIC", 3}, {"OBSERVED", "BASE", 1}} {
		if got := escalation(nwsAlert{Detection: c.det, Damage: c.dmg}); got != c.want {
			t.Errorf("escalation(%s,%s) = %d, want %d", c.det, c.dmg, got, c.want)
		}
	}
}

// nwsServer serves the home and in-view queries; bodies can be swapped.
type nwsServer struct {
	*httptest.Server
	mu                    sync.Mutex
	home, inv             []byte
	invFail               bool
	invDelay              time.Duration // the in-view answer takes this long (or until the client gives up)
	uas, points, statuses []string
	areas                 []string // the area parameter of each in-view query
}

func newNWSServer(t *testing.T) *nwsServer {
	s := &nwsServer{home: fixture(t, "nws-home.json"), inv: fixture(t, "nws-inview.json")}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("event") != "" {
			s.mu.Lock()
			d := s.invDelay
			s.mu.Unlock()
			select {
			case <-time.After(d):
			case <-r.Context().Done():
			}
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		s.uas = append(s.uas, r.Header.Get("User-Agent"))
		s.statuses = append(s.statuses, r.URL.Query().Get("status"))
		if r.URL.Query().Get("event") != "" {
			a := r.URL.Query().Get("area")
			if a == "" && r.URL.Query().Has("area") {
				a = "EMPTY" // area= with nothing after it is the bug Review Focus 2 guards
			}
			s.areas = append(s.areas, a)
		}
		if r.URL.Path != "/alerts/active" || r.Header.Get("Accept") != "application/geo+json" {
			http.Error(w, "bad request", 400)
			return
		}
		q := r.URL.Query()
		switch {
		case q.Get("point") != "":
			s.points = append(s.points, q.Get("point"))
			w.Write(s.home)
		case s.invFail:
			http.Error(w, "down", 503)
		case q.Get("event") == "Tornado Warning,Severe Thunderstorm Warning":
			w.Write(s.inv)
		default:
			http.Error(w, "bad query", 400)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func testNWS(s *nwsServer) *nws {
	return newNWS(s.Client(), s.URL, "deskpanel", homeLat, homeLon, newView(homeLat, homeLon, 8))
}

func collectAlerts(t *testing.T, n *nws) wxAlerts {
	t.Helper()
	v, err := n.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return v.(wxAlerts)
}

func TestNWSCollect(t *testing.T) {
	n := testNWS(newNWSServer(t))
	if n.Section() != "weather" || n.Key() != "alerts" || n.Interval() != time.Minute {
		t.Fatal("collector identity wrong")
	}
	a := collectAlerts(t, n)
	if len(a.List) != 4 {
		t.Fatalf("list = %+v", a.List)
	}
	want := []string{"Tornado Warning", "Severe Thunderstorm Warning", "Tornado Watch", "Wind Advisory"}
	for i, w := range want {
		if a.List[i].Event != w || a.List[i].N != i {
			t.Errorf("List[%d] = %+v, want %s", i, a.List[i], w)
		}
	}
	if x := a.List[0]; !x.Take || x.Cls != "warning" || len(x.Key) != 8 || x.Until != time.Date(2026, 11, 18, 20, 45, 0, 0, time.UTC).Unix() {
		t.Errorf("List[0] = %+v", x)
	}
	if a.List[2].Take || a.List[2].Cls != "watch" || a.List[2].Until != time.Date(2026, 11, 19, 1, 0, 0, 0, time.UTC).Unix() {
		t.Errorf("watch = %+v", a.List[2])
	}
	if len(a.Polys) != 2 {
		t.Fatalf("polys = %+v", a.Polys)
	}
	if p := a.Polys[0]; !p.Home || p.Kind != "t" || len(p.Pts) != 4 {
		t.Errorf("Polys[0] = %+v", p)
	}
	if p := a.Polys[1]; p.Home || p.Kind != "s" {
		t.Errorf("Polys[1] = %+v", p)
	}
	tx, ok := n.text(0)
	if !ok || tx.Event != "Tornado Warning" || tx.Desc == "" || tx.Instr == "" || tx.Expires == 0 || tx.Sent == 0 {
		t.Fatalf("text(0) = %+v", tx)
	}
	if _, ok := n.text(9); ok {
		t.Fatal("text(9) should be false")
	}
	if _, ok := n.text(-1); ok {
		t.Fatal("text(-1) should be false")
	}
	b, _ := json.Marshal(tx)
	if !strings.Contains(string(b), `"instr"`) || !strings.Contains(string(b), `"headline"`) {
		t.Fatalf("json = %s", b)
	}
}

func TestNWSTextCap(t *testing.T) {
	s := newNWSServer(t)
	long := strings.Repeat("word ", 2000)
	s.home = []byte(fmt.Sprintf(`{"features":[{"geometry":null,"properties":{"id":"a","event":"Tornado Warning","headline":"H","description":%q,"instruction":%q,"sent":"2026-11-18T14:02:00-06:00","expires":"2026-11-18T14:45:00-06:00"}}]}`, long, "Take cover."))
	n := testNWS(s)
	collectAlerts(t, n)
	tx, ok := n.text(0)
	if !ok || len(tx.Event)+len(tx.Headline)+len(tx.Desc)+len(tx.Instr) > 3072 {
		t.Fatalf("text too long: %d", len(tx.Event)+len(tx.Headline)+len(tx.Desc)+len(tx.Instr))
	}
	if tx.Instr != "Take cover." || !strings.HasSuffix(tx.Desc, "...") {
		t.Fatalf("desc should be cut before instr: %q / %q", tx.Desc[len(tx.Desc)-10:], tx.Instr)
	}
}

func TestNWSChainKeys(t *testing.T) {
	s := newNWSServer(t)
	n := testNWS(s)
	first := collectAlerts(t, n).List[0]
	if first.Lvl != 0 {
		t.Fatalf("lvl = %d", first.Lvl)
	}
	s.mu.Lock()
	s.home = []byte(`{"features":[{"geometry":null,"properties":{"id":"urn:oid:update.2","event":"Tornado Warning","messageType":"Update",
		"references":[{"identifier":"urn:oid:2.49.0.1.840.0.test-tor.001.1"}],
		"sent":"2026-11-18T14:20:00-06:00","expires":"2026-11-18T15:00:00-06:00",
		"parameters":{"tornadoDetection":["OBSERVED"]}}}]}`)
	s.mu.Unlock()
	a := collectAlerts(t, n)
	if len(a.List) != 1 || a.List[0].Key != first.Key || a.List[0].Lvl != 1 {
		t.Fatalf("after update = %+v (first key %s)", a.List, first.Key)
	}
}

func TestNWSOneEntryPerChain(t *testing.T) {
	s := newNWSServer(t)
	s.home = []byte(`{"features":[
		{"properties":{"id":"a","event":"Tornado Warning","sent":"2026-11-18T14:00:00-06:00","expires":"2026-11-18T14:45:00-06:00"}},
		{"properties":{"id":"b","event":"Tornado Warning","references":[{"identifier":"a"}],"sent":"2026-11-18T14:10:00-06:00","expires":"2026-11-18T15:00:00-06:00"}}]}`)
	a := collectAlerts(t, testNWS(s))
	if len(a.List) != 1 || a.List[0].Until != time.Date(2026, 11, 18, 21, 0, 0, 0, time.UTC).Unix() {
		t.Fatalf("list = %+v", a.List)
	}
}

func TestNWSFreshCollectorMarksTake(t *testing.T) {
	s := newNWSServer(t)
	collectAlerts(t, testNWS(s))
	if a := collectAlerts(t, testNWS(s)); !a.List[0].Take {
		t.Fatal("fresh collector must mark Take")
	}
}

func TestNWSRequests(t *testing.T) {
	s := newNWSServer(t)
	collectAlerts(t, testNWS(s))
	s.mu.Lock()
	if len(s.areas) != 1 || s.areas[0] == "" || s.areas[0] != areasIn(newView(homeLat, homeLon, 8), stateBoxes) {
		t.Fatalf("areas = %v", s.areas)
	}
	if len(s.points) != 1 || s.points[0] != "39.7456,-97.0892" {
		t.Fatalf("points = %v", s.points)
	}
	if len(s.uas) != 2 || s.uas[0] != "deskpanel" || s.uas[1] != "deskpanel" {
		t.Fatalf("uas = %v", s.uas)
	}
	// test and exercise messages must never take over
	if len(s.statuses) != 2 || s.statuses[0] != "actual" || s.statuses[1] != "actual" {
		t.Fatalf("status = %v, want actual on both", s.statuses)
	}
	// Review Focus 2: a map that touches no state asks nationwide, never area= empty
	s.mu.Unlock()
	n := testNWS(s)
	n.area = ""
	a := collectAlerts(t, n)
	s.mu.Lock()
	if len(s.areas) != 2 || s.areas[1] != "" || len(a.Polys) == 0 {
		t.Fatalf("areas = %v, polys = %d", s.areas, len(a.Polys))
	}
	s.mu.Unlock()
}

// A Cancel ends its chain: the warning leaves the list, its outline the map, and the watcher says "ended".
func TestNWSCancelEndsChain(t *testing.T) {
	s := newNWSServer(t)
	n := testNWS(s)
	sl := &slot{c: n, ok: true, at: time.Now()}
	var got []string
	w := newWatcher([]*slot{sl}, func(src, msg, lvl, act string) { got = append(got, src+"|"+lvl+"|"+msg) })
	sl.data = collectAlerts(t, n)
	if l := sl.data.(wxAlerts).List; len(l) == 0 || !l[0].Take {
		t.Fatalf("before: %+v", l)
	}
	w.tick() // baseline: the tornado warning is up
	orig := `{"properties":{"id":"urn:oid:2.49.0.1.840.0.test-tor.001.1","event":"Tornado Warning","messageType":"Alert",
		"sent":"2026-11-18T14:02:00-06:00","expires":"2026-11-18T14:45:00-06:00"}}`
	cancel := `{"geometry":{"type":"Polygon","coordinates":[[[-97.19,39.64],[-96.99,39.64],[-96.99,39.84],[-97.19,39.84],[-97.19,39.64]]]},
		"properties":{"id":"urn:oid:cancel.1","event":"Tornado Warning","messageType":"Cancel",
		"references":[{"identifier":"urn:oid:2.49.0.1.840.0.test-tor.001.1","sent":"2026-11-18T14:02:00-06:00"}],
		"sent":"2026-11-18T14:20:00-06:00","expires":"2026-11-18T14:30:00-06:00"}}`
	s.mu.Lock()
	s.home = []byte(`{"features":[` + orig + `,` + cancel + `]}`)
	s.inv = []byte(`{"features":[` + cancel + `]}`)
	s.mu.Unlock()
	a := collectAlerts(t, n)
	if len(a.List) != 0 || len(a.Polys) != 0 {
		t.Fatalf("after cancel = %+v", a)
	}
	sl.data = a
	w.tick()
	if strings.Join(got, ";") != "NWS|info|Tornado Warning ended" {
		t.Fatalf("notes = %q", got)
	}
}

func TestParseAlertsSkipsBadFeature(t *testing.T) {
	as, err := parseAlerts([]byte(`{"features":[
		{"properties":{"id":"bad","event":"Flood Advisory","sent":"yesterday"}},
		{"properties":{"id":"good","event":"Tornado Watch","sent":"2026-11-18T14:02:00-06:00"}}]}`))
	if err != nil || len(as) != 1 || as[0].ID != "good" {
		t.Fatalf("got %+v, %v", as, err)
	}
	// nothing readable at all is an error, so a feed that changed format gets noticed
	if _, err := parseAlerts([]byte(`{"features":[{"properties":{"id":"bad","sent":"yesterday"}}]}`)); err == nil {
		t.Fatal("all-bad collection accepted")
	}
}

// NWS hard-wraps its text at ~69 columns: the lines are joined, the paragraphs kept.
func TestNWSTextParagraphs(t *testing.T) {
	s := newNWSServer(t)
	desc := "At 2:02 PM CST, a severe thunderstorm capable of producing a tornado\nwas located over the example point,   moving east at 30 mph.\r\n\r\nHAZARD...Tornado.\n \n\n\nSOURCE...Radar indicated\nrotation.\n"
	s.home = []byte(fmt.Sprintf(`{"features":[{"properties":{"id":"a","event":"Tornado Warning","headline":"H","description":%q,"instruction":%q,"sent":"2026-11-18T14:02:00-06:00","expires":"2026-11-18T14:45:00-06:00"}}]}`,
		desc, "TAKE COVER NOW! Move to a basement or an interior\nroom on the lowest floor."))
	n := testNWS(s)
	collectAlerts(t, n)
	tx, _ := n.text(0)
	if want := "At 2:02 PM CST, a severe thunderstorm capable of producing a tornado was located over the example point, moving east at 30 mph.\n\nHAZARD...Tornado.\n\nSOURCE...Radar indicated rotation."; tx.Desc != want {
		t.Errorf("desc = %q", tx.Desc)
	}
	if want := "TAKE COVER NOW! Move to a basement or an interior room on the lowest floor."; tx.Instr != want {
		t.Errorf("instr = %q", tx.Instr)
	}
}

// The nationwide in-view list has its own short timeout: it never holds up the home alerts.
func TestNWSSlowInViewDoesNotHoldHome(t *testing.T) {
	s := newNWSServer(t)
	n := testNWS(s)
	first := collectAlerts(t, n)
	s.mu.Lock()
	s.invDelay = 5 * time.Second
	s.mu.Unlock()
	n.inViewWait = 100 * time.Millisecond
	start := time.Now()
	a := collectAlerts(t, n)
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("collect took %v", took)
	}
	if len(a.List) != 4 || !a.List[0].Take || fmt.Sprint(a.Polys) != fmt.Sprint(first.Polys) {
		t.Fatalf("got %+v", a)
	}
}

func TestNWSHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	n := newNWS(srv.Client(), srv.URL, "deskpanel", homeLat, homeLon, newView(homeLat, homeLon, 8))
	if _, err := n.Collect(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}

func TestNWSInViewFailureKeepsHome(t *testing.T) {
	s := newNWSServer(t)
	n := testNWS(s)
	first := collectAlerts(t, n)
	if len(first.Polys) != 2 {
		t.Fatalf("polys = %d", len(first.Polys))
	}
	s.mu.Lock()
	s.invFail = true
	s.mu.Unlock()
	a := collectAlerts(t, n) // no error
	if len(a.List) != 4 || !a.List[0].Take {
		t.Fatalf("home alerts lost: %+v", a.List)
	}
	if fmt.Sprint(a.Polys) != fmt.Sprint(first.Polys) {
		t.Fatalf("polys = %v, want previous %v", a.Polys, first.Polys)
	}
	// the previous outlines last 10 minutes at most
	n.polysAt = n.polysAt.Add(-11 * time.Minute)
	if a := collectAlerts(t, n); a.Polys == nil || len(a.Polys) != 0 || len(a.List) != 4 {
		t.Fatalf("after 11 min = %+v", a)
	}
	// no previous outlines: empty, not an error
	s2 := newNWSServer(t)
	s2.invFail = true
	if a := collectAlerts(t, testNWS(s2)); len(a.List) != 4 || len(a.Polys) != 0 {
		t.Fatalf("fresh = %+v", a)
	}
}

func TestNWSHomeFailureErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("point") != "" {
			http.Error(w, "down", 503)
			return
		}
		w.Write([]byte(`{"features":[]}`))
	}))
	defer srv.Close()
	n := newNWS(srv.Client(), srv.URL, "deskpanel", homeLat, homeLon, newView(homeLat, homeLon, 8))
	if _, err := n.Collect(context.Background()); err == nil {
		t.Fatal("home failure must error")
	}
}

func TestNWSChainKeyAcrossRestart(t *testing.T) {
	s := newNWSServer(t)
	orig := collectAlerts(t, testNWS(s)).List[0]
	// restarted collector sees only an Update; its oldest reference is the original
	s.mu.Lock()
	s.home = []byte(`{"features":[{"properties":{"id":"urn:oid:update.3","event":"Tornado Warning","messageType":"Update",
		"references":[{"identifier":"urn:oid:update.2","sent":"2026-11-18T14:20:00-06:00"},
		              {"identifier":"urn:oid:2.49.0.1.840.0.test-tor.001.1","sent":"2026-11-18T14:02:00-06:00"}],
		"sent":"2026-11-18T14:30:00-06:00","expires":"2026-11-18T15:00:00-06:00"}}]}`)
	s.mu.Unlock()
	if got := collectAlerts(t, testNWS(s)).List[0].Key; got != orig.Key {
		t.Fatalf("key after restart = %s, want %s", got, orig.Key)
	}
}

func TestConditions(t *testing.T) {
	body, err := os.ReadFile("testdata/openmeteo.json")
	if err != nil {
		t.Fatal(err)
	}
	var q string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q = r.URL.RawQuery
		w.Write(body)
	}))
	defer srv.Close()
	c := newConditions(srv.Client(), srv.URL, homeLat, homeLon)
	got, err := c.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := wxNow{T: 48, RH: 58, Code: 3, Day: false, Text: "Overcast", Hi: 64, Lo: 46, Wind: "S 21 mph"}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if !strings.Contains(q, "temperature_unit=fahrenheit") {
		t.Fatalf("query = %s", q)
	}
	if wmoText(2) != "Partly cloudy" || wmoText(1234) != "" {
		t.Fatal("wmoText")
	}
	if windStr(14, 186) != "S 14 mph" || windStr(0.4, 0) != "Calm" {
		t.Fatal("windStr")
	}
}

func TestConditionsHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "x", 500) }))
	defer srv.Close()
	if _, err := newConditions(srv.Client(), srv.URL, homeLat, homeLon).Collect(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}

func TestCheckWeather(t *testing.T) { // Review Focus 4: today's configs and the boundaries pass
	for _, c := range []struct {
		lat, lon float64
		z        int
		ok       bool
	}{
		{homeLat, homeLon, 8, true}, {85, 180, 2, true}, {-85, -180, 19, true},
		{85.1, 0, 8, false}, {math.NaN(), 0, 8, false}, {0, 180.5, 8, false}, {0, math.Inf(-1), 8, false},
		{0, 0, 1, false}, {0, 0, 20, false},
	} {
		if err := checkWeather(c.lat, c.lon, c.z); (err == nil) != c.ok {
			t.Errorf("checkWeather(%v, %v, %d) = %v", c.lat, c.lon, c.z, err)
		}
	}
}

func TestConditionsNoCurrent(t *testing.T) {
	for _, body := range []string{
		`{"daily":{"temperature_2m_max":[64],"temperature_2m_min":[46]}}`,
		`{"current":null,"daily":{"temperature_2m_max":[64],"temperature_2m_min":[46]}}`,
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
		got, err := newConditions(srv.Client(), srv.URL, homeLat, homeLon).Collect(context.Background())
		srv.Close()
		if err == nil {
			t.Errorf("%s: got %+v, want an error", body, got)
		}
	}
}

type stubFrames map[int64][]byte

func (s stubFrames) frame(t int64) ([]byte, bool) { b, ok := s[t]; return b, ok }

type stubTexts []alertText

func (s stubTexts) text(i int) (alertText, bool) {
	if i < 0 || i >= len(s) {
		return alertText{}, false
	}
	return s[i], true
}

func wxSlotsFixture(ok bool) []*slot {
	var al wxAlerts
	for i := range 4 {
		al.List = append(al.List, wxAlert{N: i, Key: "k", Event: strings.Repeat("e", 39), Cls: "s", Until: 1800000000, Take: true, Lvl: 3})
	}
	pts := make([][2]int16, 16)
	for i := range pts {
		pts[i] = [2]int16{799, 367}
	}
	for range 8 {
		al.Polys = append(al.Polys, wxPoly{Kind: "t", Home: true, Pts: pts})
	}
	var out []*slot
	for _, c := range []fixed{
		{"weather", "now", wxNow{T: -10, RH: 100, Code: 99, Day: true, Text: strings.Repeat("n", 23), Hi: 100, Lo: -10, Wind: "NNW 30G45"}},
		{"weather", "frames", []int64{1800000000, 1800000300, 1800000600, 1800000900, 1800001200}},
		{"weather", "alerts", al},
	} {
		out = append(out, &slot{c: c, data: c.data, at: time.Unix(1800000000, 0), ok: ok})
	}
	return out
}

func TestWeatherDocBudget(t *testing.T) {
	body := weatherDoc(wxSlotsFixture(true))
	if len(body) > 4096 {
		t.Errorf("weather doc is %d bytes, budget 4096", len(body))
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"updated_at", "stale", "now", "frames", "alerts"} {
		if doc[k] == nil {
			t.Errorf("missing key %q", k)
		}
	}
	t.Logf("worst-case weather doc: %d bytes", len(body))
}

func TestWeatherDocStaleKeepsData(t *testing.T) {
	var doc struct {
		Stale  bool
		Frames []int64
		Now    *wxNow
	}
	if err := json.Unmarshal(weatherDoc(wxSlotsFixture(false)), &doc); err != nil {
		t.Fatal(err)
	}
	if !doc.Stale || len(doc.Frames) != 5 || doc.Now == nil {
		t.Errorf("stale doc: %+v", doc)
	}
}

func TestWeatherRoutes(t *testing.T) {
	mux := http.NewServeMux()
	addWeatherRoutes(mux, "tok", wxSlotsFixture(true), stubFrames{7: []byte("PIX")}, stubTexts{{Event: "Tornado Warning"}})
	get := func(path, token string, hdr ...string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		if len(hdr) == 2 {
			r.Header.Set(hdr[0], hdr[1])
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	if w := get("/api/v1/weather", "bad"); w.Code != 401 {
		t.Errorf("bad token: %d", w.Code)
	}
	w := get("/api/v1/weather", "tok")
	etag := w.Header().Get("ETag")
	if w.Code != 200 || etag == "" {
		t.Fatalf("weather: %d etag %q", w.Code, etag)
	}
	if w := get("/api/v1/weather", "tok", "If-None-Match", etag); w.Code != 304 {
		t.Errorf("etag: %d", w.Code)
	}
	if w := get("/api/v1/weather/frame/abc", "tok"); w.Code != 400 {
		t.Errorf("frame/abc: %d", w.Code)
	}
	if w := get("/api/v1/weather/frame/1", "tok"); w.Code != 404 {
		t.Errorf("frame/1: %d", w.Code)
	}
	if w := get("/api/v1/weather/frame/7", "tok"); w.Code != 200 || w.Body.String() != "PIX" ||
		w.Header().Get("Content-Type") != "application/octet-stream" || w.Header().Get("Cache-Control") != "max-age=3600" {
		t.Errorf("frame/7: %d %q %v", w.Code, w.Body, w.Header())
	}
	if w := get("/api/v1/weather/alert/7", "tok"); w.Code != 404 {
		t.Errorf("alert/7: %d", w.Code)
	}
	if w := get("/api/v1/weather/alert/0", "tok"); w.Code != 200 || !strings.Contains(w.Body.String(), `"event":"Tornado Warning"`) {
		t.Errorf("alert/0: %d %s", w.Code, w.Body)
	}
	bare := httptest.NewRecorder()
	http.NewServeMux().ServeHTTP(bare, httptest.NewRequest("GET", "/api/v1/weather", nil))
	if bare.Code != 404 {
		t.Errorf("without routes: %d", bare.Code)
	}
}

func TestDemoWeather(t *testing.T) {
	now := time.Unix(1800000123, 0)
	d := newDemoWeather(now, false)
	slots := func(d *demoWeather) []*slot {
		var out []*slot
		for _, c := range d.collectors() {
			data, _ := c.Collect(context.Background())
			out = append(out, &slot{c: c, data: data, at: now, ok: true})
		}
		return out
	}
	body := weatherDoc(slots(d))
	if len(body) > 4096 {
		t.Errorf("demo doc is %d bytes", len(body))
	}
	var doc struct {
		Frames []int64
		Alerts wxAlerts
	}
	if err := json.Unmarshal(body, &doc); err != nil || len(doc.Frames) != 5 {
		t.Fatalf("doc: %v %s", err, body)
	}
	if last := doc.Frames[4]; last > now.Unix() || last%300 != 0 || now.Unix()-last >= 300 {
		t.Errorf("newest frame %d vs now %d", last, now.Unix())
	}
	for _, ts := range doc.Frames {
		b, ok := d.frame(ts)
		if !ok {
			t.Fatalf("no frame %d", ts)
		}
		if px, err := decodeFrame(b); err != nil || len(px) != 294400 {
			t.Errorf("frame %d: %d px, %v", ts, len(px), err)
		}
	}
	if len(doc.Alerts.List) != 2 {
		t.Errorf("alerts: %+v", doc.Alerts)
	}
	if _, ok := d.text(1); !ok {
		t.Error("no alert text 1")
	}
	if _, ok := d.text(2); ok {
		t.Error("alert text 2 should not exist")
	}

	w := newDemoWeather(now, true)
	if err := json.Unmarshal(weatherDoc(slots(w)), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Alerts.List) == 0 || !doc.Alerts.List[0].Take || len(doc.Alerts.Polys) < 2 || !doc.Alerts.Polys[0].Home {
		t.Errorf("warning doc: %+v", doc.Alerts)
	}

	dir := t.TempDir()
	if err := writeDemoWeather(dir, now); err != nil {
		t.Fatal(err)
	}
	ents, _ := os.ReadDir(dir)
	n := 0
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), "frame-") && strings.HasSuffix(e.Name(), ".z") {
			n++
		}
	}
	if n != 5 {
		t.Errorf("%d frame files", n)
	}
	for _, f := range []string{"weather.json", "warning.json", "alert-0.json", "warning-alert-0.json"} {
		if _, err := os.Stat(dir + "/" + f); err != nil {
			t.Error(err)
		}
	}
}

func TestDemoWeatherStaysFresh(t *testing.T) {
	start := time.Unix(1800000123, 0)
	d := newDemoWeather(start, true)
	d.clock = func() time.Time { return start.Add(20 * time.Minute) }
	now := d.clock().Unix()
	ts := d.times()
	newest := ts[len(ts)-1]
	if now-newest >= 900 {
		t.Errorf("newest frame is %d s old", now-newest)
	}
	list, _, _ := d.alertsAt(d.clock())
	if list[0].Event != "Tornado Warning" || list[0].Until <= now {
		t.Errorf("warning expired: %+v", list[0])
	}
	if a, _ := d.text(0); a.Expires <= now {
		t.Errorf("text expired: %+v", a)
	}
}

// A demo frame is a function of its time alone: the panel caches frames by time and fetches each new
// one once, as the newest, so an image picked by age would freeze the loop after five steps.
func TestDemoFramesFollowTime(t *testing.T) {
	start := time.Unix(1800000123, 0)
	d := newDemoWeather(start, false)
	ts := d.times()
	a, _ := d.frame(ts[3])
	b, ok := d.frame(ts[4])
	if !ok || bytes.Equal(a, b) {
		t.Fatal("consecutive frames are the same image")
	}
	d.clock = func() time.Time { return start.Add(10 * time.Minute) }
	d.imgs = map[int64][]byte{} // drawn again, not cached
	if c, ok := d.frame(ts[4]); !ok || !bytes.Equal(b, c) {
		t.Fatal("the same time gives another image at another now")
	}
	at := start
	d.clock = func() time.Time { return at }
	held := map[int64][]byte{}
	for range 30 { // a long run, the way the panel fetches
		at = at.Add(5 * time.Minute)
		ts = d.times()
		held[ts[4]], _ = d.frame(ts[4])
	}
	for i := 1; i < len(ts); i++ {
		if bytes.Equal(held[ts[i-1]], held[ts[i]]) {
			t.Fatalf("frames %d and %d are the same image", ts[i-1], ts[i])
		}
	}
	if len(d.imgs) > 8 {
		t.Errorf("%d frames cached", len(d.imgs))
	}
}

// A Cancel and an Update sent in the same second: the warning is still active, whatever order the API lists them in.
func TestNWSCancelTieKeepsActive(t *testing.T) {
	sent := time.Date(2026, 11, 18, 14, 20, 0, 0, time.UTC)
	orig := nwsAlert{ID: "o", Event: "Tornado Warning", MsgType: "Alert", Sent: sent.Add(-10 * time.Minute), Expires: sent.Add(time.Hour)}
	upd := nwsAlert{ID: "u", Event: "Tornado Warning", MsgType: "Update", Sent: sent, Expires: sent.Add(time.Hour), Refs: []string{"o"}}
	can := nwsAlert{ID: "c", Event: "Tornado Warning", MsgType: "Cancel", Sent: sent, Expires: sent.Add(time.Hour), Refs: []string{"o"}}
	for _, order := range [][]nwsAlert{{orig, upd, can}, {orig, can, upd}} {
		n := newNWS(nil, "", "", homeLat, homeLon, newView(homeLat, homeLon, 8))
		if l, _ := n.home(append([]nwsAlert(nil), order...)); len(l) != 1 || !l[0].Take {
			t.Fatalf("order %s%s%s: list = %+v", order[0].ID, order[1].ID, order[2].ID, l)
		}
	}
}

// An unreadable takeover warning fails the collection: "no alerts" (or dropping it silently) is worse than
// keeping the stale takeover and letting the "Can't reach NWS" note fire.
func TestParseAlertsUnreadableTakeoverFails(t *testing.T) {
	_, err := parseAlerts([]byte(`{"features":[
		{"properties":{"id":"bad","event":"Tornado Warning","sent":"yesterday"}},
		{"properties":{"id":"good","event":"Tornado Watch","sent":"2026-11-18T14:02:00-06:00"}}]}`))
	if err == nil {
		t.Fatal("unreadable Tornado Warning was skipped silently")
	}
	_, err = parseAlerts([]byte(`{"features":[
		{"properties":{"id":"bad","event":"Extreme Wind Warning","sent":"yesterday"}}]}`))
	if err == nil {
		t.Fatal("unreadable Extreme Wind Warning was skipped silently")
	}
	// the event field itself is the broken part: still a takeover when the feature names one
	if _, err := parseAlerts([]byte(`{"features":[{"properties":{"id":"bad","event":7,"headline":"Tornado Warning issued"}},
		{"properties":{"id":"good","event":"Tornado Watch","sent":"2026-11-18T14:02:00-06:00"}}]}`)); err == nil {
		t.Fatal("a takeover with an unreadable event was skipped silently")
	}
	// a null event is "missing": the raw text decides
	if _, err := parseAlerts([]byte(`{"features":[{"properties":{"id":"bad","event":null,"sent":"yesterday","headline":"Tornado Warning issued"}},
		{"properties":{"id":"good","event":"Tornado Watch","sent":"2026-11-18T14:02:00-06:00"}}]}`)); err == nil {
		t.Fatal("a takeover with a null event was skipped silently")
	}
	// an unreadable non-takeover alert must not block a readable warning (Review Focus 1)
	// an unreadable non-takeover that merely mentions a Tornado Warning must not block the real one
	as, err := parseAlerts([]byte(`{"features":[{"properties":{"id":"bad","event":"Severe Thunderstorm Warning","description":"replaced by a Tornado Warning","sent":"yesterday"}},
		{"properties":{"id":"tor","event":"Tornado Warning","sent":"2026-11-18T14:02:00-06:00"}}]}`))
	if err != nil || len(as) != 1 || as[0].ID != "tor" {
		t.Fatalf("mention: got %+v, %v", as, err)
	}
	as, err = parseAlerts([]byte(`{"features":[{"properties":{"id":"bad","event":"Flood Advisory","sent":"yesterday"}},
		{"properties":{"id":"tor","event":"Tornado Warning","sent":"2026-11-18T14:02:00-06:00"}}]}`))
	if err != nil || len(as) != 1 || as[0].ID != "tor" {
		t.Fatalf("got %+v, %v", as, err)
	}
}

func TestAreasIn(t *testing.T) { // the frame reaches ~2.197° of longitude either side of home at z8
	boxes := []areaBox{
		{"AA", homeLat - 1, homeLon - 1, homeLat + 1, homeLon + 1},     // around home
		{"BB", homeLat + 10, homeLon, homeLat + 11, homeLon + 1},       // far north
		{"CC", homeLat - 1, homeLon + 2, homeLat + 1, homeLon + 3},     // straddles the right edge
		{"DD", homeLat - 1, homeLon + 2.5, homeLat + 1, homeLon + 3},   // just past it
		{"EE", homeLat - 1, homeLon - 3, homeLat + 1, homeLon - 2.5},   // just west of it
		{"FF", homeLat - 1, homeLon - 3, homeLat + 1, homeLon - 2},     // straddles the left edge
		{"GG", homeLat - 2.5, homeLon - 1, homeLat - 1.5, homeLon + 1}, // just south of it
		{"HH", homeLat - 1.5, homeLon - 1, homeLat - 0.5, homeLon + 1}, // straddles the bottom edge
	}
	v := newView(homeLat, homeLon, 8)
	if got := areasIn(v, boxes); got != "AA,CC,FF,HH" {
		t.Fatalf("areasIn = %q", got)
	}
	if got := areasIn(v, []areaBox{boxes[1], boxes[3], boxes[4], boxes[6]}); got != "" {
		t.Fatalf("nothing in view = %q", got)
	}
}

func TestStateBoxes(t *testing.T) { // 48 contiguous states + DC; whole degrees, inside CONUS
	if len(stateBoxes) != 49 {
		t.Fatalf("len = %d", len(stateBoxes))
	}
	seen := map[string]bool{}
	for _, b := range stateBoxes {
		if !regexp.MustCompile(`^[A-Z]{2}$`).MatchString(b.code) || seen[b.code] {
			t.Errorf("bad or duplicate code %q", b.code)
		}
		seen[b.code] = true
		if b.s >= b.n || b.w >= b.e || b.s < 24 || b.n > 50 || b.w < -125 || b.e > -66 {
			t.Errorf("%s box out of range: %+v", b.code, b)
		}
	}
}

func TestNWSKeyPruning(t *testing.T) { // pins shipped behaviour
	n := newNWS(nil, "", "", homeLat, homeLon, newView(homeLat, homeLon, 8))
	n.keys["old"] = chainKey{"k1", time.Now().Add(-nwsKeyTTL - time.Minute)}
	n.keys["new"] = chainKey{"k2", time.Now().Add(-nwsKeyTTL + time.Minute)}
	n.home(nil)
	if _, ok := n.keys["old"]; ok {
		t.Error("old key not pruned")
	}
	if _, ok := n.keys["new"]; !ok {
		t.Error("new key pruned")
	}
}

func TestNWSPolysCapAndOrder(t *testing.T) { // pins shipped behaviour
	n := newNWS(nil, "", "", homeLat, homeLon, newView(homeLat, homeLon, 8))
	ring := func(lon float64) [][][2]float64 { return [][][2]float64{square(homeLat, lon, 0.05)} }
	var as []nwsAlert
	for i := range 9 { // severe storms east of home, built farthest first
		as = append(as, nwsAlert{Event: "Severe Thunderstorm Warning", Rings: ring(homeLon + 1.5 - 0.15*float64(i))})
	}
	as = append(as, nwsAlert{Event: "Tornado Warning", Rings: ring(homeLon - 1.5)},
		nwsAlert{Event: "Severe Thunderstorm Warning", Rings: ring(homeLon)}) // covers home
	p := n.polys(as)
	if len(p) != 8 || p[0].Kind != "s" || !p[0].Home || p[1].Kind != "t" {
		t.Fatalf("polys = %+v", p)
	}
	for i := 2; i < 7; i++ {
		if p[i].Kind != "s" || p[i].Home || p[i].Pts[0][0] >= p[i+1].Pts[0][0] {
			t.Errorf("p[%d] out of order: %+v then %+v", i, p[i], p[i+1])
		}
	}
	if p[7].Kind != "s" || p[7].Home {
		t.Errorf("p[7] = %+v", p[7])
	}
}

func TestNWSTextCapLongInstr(t *testing.T) { // pins shipped behaviour: the instruction is cut too
	s := newNWSServer(t)
	long := strings.Repeat("word ", 2000)
	s.home = []byte(fmt.Sprintf(`{"features":[{"geometry":null,"properties":{"id":"a","event":"Tornado Warning","headline":"H","description":"A storm.","instruction":%q,"sent":"2026-11-18T14:02:00-06:00","expires":"2026-11-18T14:45:00-06:00"}}]}`, long))
	n := testNWS(s)
	collectAlerts(t, n)
	tx, ok := n.text(0)
	if ok && tx.Desc != "A storm." {
		t.Fatalf("description wiped by a long instruction: %q", tx.Desc)
	}
	if !ok || len(tx.Event)+len(tx.Headline)+len(tx.Desc)+len(tx.Instr) > 3072 || !strings.HasSuffix(tx.Instr, "...") {
		t.Fatalf("instr not cut: total %d", len(tx.Event)+len(tx.Headline)+len(tx.Desc)+len(tx.Instr))
	}
}
