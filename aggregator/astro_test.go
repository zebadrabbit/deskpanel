package main

import (
	"math"
	"testing"
	"time"
)

const (
	grLat = 51.4769
	grLon = -0.0005
)

func sunAlt(lat, lon float64) func(time.Time) float64 {
	return func(t time.Time) float64 {
		ra, dec := sunPos(t)
		a, _ := altAz(ra, dec, lat, lon, t)
		return a
	}
}

func moonAlt(lat, lon float64) func(time.Time) float64 {
	return func(t time.Time) float64 {
		ra, dec, _ := moonPos(t)
		a, _ := altAz(ra, dec, lat, lon, t)
		return a
	}
}

func near(t time.Time, h, m, tol int) bool {
	want := time.Date(t.Year(), t.Month(), t.Day(), h, m, 0, 0, time.UTC)
	return math.Abs(t.Sub(want).Minutes()) <= float64(tol)
}

func TestSunEquinoxGreenwich(t *testing.T) {
	day := time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC)
	r, s, okR, okS := riseSet(sunAlt(grLat, grLon), day, time.UTC, -0.833)
	if !okR || !okS || !near(r, 6, 4, 4) || !near(s, 18, 13, 4) {
		t.Fatalf("rise %v (%v) set %v (%v)", r, okR, s, okS)
	}
	if got := clock12(time.Date(2026, 3, 20, 6, 59, 0, 0, time.UTC), true); got != "6:59 AM" {
		t.Errorf("clock12 = %q", got)
	}
}

func TestSunNoonAltitude(t *testing.T) {
	f := sunAlt(grLat, grLon)
	start := time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC)
	best := -90.0
	for i := 0; i < 1440; i++ {
		best = math.Max(best, f(start.Add(time.Duration(i)*time.Minute)))
	}
	if math.Abs(best-38.5) > 0.8 {
		t.Fatalf("noon altitude %.2f", best)
	}
}

func TestPolarisAltitude(t *testing.T) {
	for _, h := range []int{0, 5, 11, 17, 23} {
		a, _ := altAz(37.95, 89.26, grLat, grLon, time.Date(2026, 3, 20, h, 17, 0, 0, time.UTC))
		if math.Abs(a-51.5) > 1 {
			t.Errorf("hour %d: Polaris alt %.2f", h, a)
		}
	}
}

func TestMoonPhaseAnchors(t *testing.T) {
	full := time.Date(2026, 3, 3, 11, 33, 0, 0, time.UTC)
	newm := time.Date(2026, 3, 19, 1, 23, 0, 0, time.UTC)
	if i, _, n := moonPhase(full); i <= 0.98 || n != "Full moon" {
		t.Errorf("full: illum %.3f %s", i, n)
	}
	if i, _, n := moonPhase(newm); i >= 0.02 || n != "New moon" {
		t.Errorf("new: illum %.3f %s", i, n)
	}
	// ponytail: the brief says "waxing is true between the two", but full (Mar 3)
	// to new (Mar 19) is the waning half. Pinned as the astronomy says.
	if _, w, n := moonPhase(full.AddDate(0, 0, 4)); w || n != "Waning gibbous" {
		t.Errorf("after full: waxing=%v %s", w, n)
	}
	if _, w, n := moonPhase(newm.AddDate(0, 0, 4)); !w || n != "Waxing crescent" {
		t.Errorf("after new: waxing=%v %s", w, n)
	}
	if _, w, n := moonPhase(newm.AddDate(0, 0, 7)); !w || n != "First quarter" {
		t.Errorf("first quarter: waxing=%v %s", w, n)
	}
}

func TestPolarNoSet(t *testing.T) {
	day := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	r, s, okR, okS := riseSet(sunAlt(78.2, 15.6), day, time.UTC, -0.833)
	if okR || okS {
		t.Fatalf("expected no rise/set, got %v %v", r, s)
	}
	if clock12(r, okR) != "—" {
		t.Errorf("clock12 not-ok = %q", clock12(r, okR))
	}
}

func TestMoonriseMissingDay(t *testing.T) {
	f := moonAlt(grLat, grLon)
	missing := 0
	for d := 1; d <= 31; d++ {
		day := time.Date(2026, 3, d, 12, 0, 0, 0, time.UTC)
		r, s, okR, okS := riseSet(f, day, time.UTC, 0.125)
		if !okR || !okS {
			missing++
		}
		_ = clock12(r, okR)
		_ = clock12(s, okS)
	}
	if missing == 0 {
		t.Fatal("no day in March 2026 lacked a moonrise or moonset")
	}
}

func TestDayLength(t *testing.T) {
	r := time.Date(2026, 1, 1, 6, 59, 0, 0, time.UTC)
	if got := dayLength(r, r.Add(10*time.Hour+7*time.Minute)); got != "10 h 7 min" {
		t.Errorf("dayLength = %q", got)
	}
}

func TestRiseSetDST(t *testing.T) {
	cases := []struct {
		zone     string
		lat, lon float64
		y, m, d  int
		hours    int
		loH, hiH int // plausible local sunset hour range
	}{
		{"Europe/London", grLat, grLon, 2026, 3, 29, 23, 19, 20},
		{"Europe/London", grLat, grLon, 2026, 10, 25, 25, 16, 17},
		{"America/Chicago", 41.88, -87.63, 2026, 3, 8, 23, 17, 19},
		{"America/Chicago", 41.88, -87.63, 2026, 11, 1, 25, 16, 17},
	}
	for _, c := range cases {
		loc, err := time.LoadLocation(c.zone)
		if err != nil {
			t.Skipf("no tzdata: %v", err)
		}
		day := time.Date(c.y, time.Month(c.m), c.d, 12, 0, 0, 0, loc)
		calls := 0 // a never-crossing function makes the scan cover the whole day
		riseSet(func(time.Time) float64 { calls++; return -50 }, day, loc, 0)
		if want := c.hours*60 + 1; calls != want { // samples = minutes + 1
			t.Errorf("%s %d-%d: %d samples, want %d", c.zone, c.m, c.d, calls, want)
		}
		_, s, _, okS := riseSet(sunAlt(c.lat, c.lon), day, loc, -0.833)
		ls := s.In(loc)
		if !okS || ls.Day() != c.d || ls.Hour() < c.loH || ls.Hour() > c.hiH {
			t.Errorf("%s %d-%d: sunset %v ok=%v", c.zone, c.m, c.d, ls, okS)
		}
	}
}

func TestDayLengthWrap(t *testing.T) {
	r := time.Date(2026, 1, 1, 22, 0, 0, 0, time.UTC)
	if got := dayLength(r, r.Add(-9*time.Hour-30*time.Minute)); got != "14 h 30 min" {
		t.Errorf("dayLength = %q", got)
	}
}

func TestSunriseSydney(t *testing.T) {
	loc, err := time.LoadLocation("Australia/Sydney")
	if err != nil {
		t.Skip(err)
	}
	r, _, ok, _ := riseSet(sunAlt(-33.87, 151.21), time.Date(2026, 6, 21, 12, 0, 0, 0, loc), loc, -0.833)
	want := time.Date(2026, 6, 21, 7, 0, 0, 0, loc)
	if !ok || math.Abs(r.Sub(want).Minutes()) > 10 {
		t.Errorf("Sydney sunrise %v ok=%v", r, ok)
	}
}
