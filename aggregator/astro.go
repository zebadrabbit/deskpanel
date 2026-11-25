package main

// Low-precision astronomy for the astronomical clock: sun and moon position,
// moon phase, rise/set. Good to about a degree for the moon, a few hundredths
// for the sun; plenty for a wall clock. Angles are degrees at every API;
// azimuth runs from north through east (0..360), altitude -90..90.

import (
	"fmt"
	"math"
	"time"
)

const (
	d2r = math.Pi / 180
	r2d = 180 / math.Pi
	// obliquity of the ecliptic (fixed; drifts 0.013 deg per century)
	obliq = 23.439
)

func norm360(x float64) float64 {
	x = math.Mod(x, 360)
	if x < 0 {
		x += 360
	}
	return x
}

// daysJ2000 is days since 2000-01-01 12:00 UTC (UT ~ TT, fine at this precision).
func daysJ2000(t time.Time) float64 {
	return float64(t.UnixMilli())/86400000 + 2440587.5 - 2451545.0
}

func sunLon(n float64) float64 {
	l := 280.460 + 0.9856474*n
	g := (357.528 + 0.9856003*n) * d2r
	return norm360(l + 1.915*math.Sin(g) + 0.020*math.Sin(2*g))
}

// eclToEq converts ecliptic longitude/latitude to right ascension/declination.
func eclToEq(lon, lat float64) (ra, dec float64) {
	l, b, e := lon*d2r, lat*d2r, obliq*d2r
	ra = math.Atan2(math.Sin(l)*math.Cos(e)-math.Tan(b)*math.Sin(e), math.Cos(l))
	dec = math.Asin(math.Sin(b)*math.Cos(e) + math.Cos(b)*math.Sin(e)*math.Sin(l))
	return norm360(ra * r2d), dec * r2d
}

func sunPos(t time.Time) (ra, dec float64) {
	return eclToEq(sunLon(daysJ2000(t)), 0)
}

// moonPos returns the moon's geocentric RA, Dec and ecliptic longitude.
func moonPos(t time.Time) (ra, dec, eclLon float64) {
	n := daysJ2000(t)
	lp := 218.316 + 13.176396*n
	mp := (134.963 + 13.064993*n) * d2r // moon anomaly
	f := (93.272 + 13.229350*n) * d2r   // argument of latitude
	d := (297.850 + 12.190749*n) * d2r  // mean elongation
	m := (357.529 + 0.985600*n) * d2r   // sun anomaly
	lon := lp + 6.289*math.Sin(mp) + 1.274*math.Sin(2*d-mp) + 0.658*math.Sin(2*d) +
		0.214*math.Sin(2*mp) - 0.186*math.Sin(m) - 0.114*math.Sin(2*f)
	lat := 5.128*math.Sin(f) + 0.280*math.Sin(mp+f) + 0.277*math.Sin(mp-f)
	eclLon = norm360(lon)
	ra, dec = eclToEq(eclLon, lat)
	return
}

// lst is the local sidereal time in degrees at longitude lonDeg (E+).
func lst(t time.Time, lonDeg float64) float64 {
	return norm360(280.46061837 + 360.98564736629*daysJ2000(t) + lonDeg)
}

// altAz converts RA/Dec to altitude/azimuth for an observer (lat N+, lon E+).
func altAz(ra, dec, lat, lon float64, t time.Time) (alt, az float64) {
	ha, de, la := (lst(t, lon)-ra)*d2r, dec*d2r, lat*d2r
	sa := math.Sin(de)*math.Sin(la) + math.Cos(de)*math.Cos(la)*math.Cos(ha)
	alt = math.Asin(math.Max(-1, math.Min(1, sa))) * r2d
	az = math.Atan2(-math.Sin(ha)*math.Cos(de), math.Cos(la)*math.Sin(de)-math.Sin(la)*math.Cos(de)*math.Cos(ha)) * r2d
	return alt, norm360(az)
}

func moonPhase(t time.Time) (illum float64, waxing bool, name string) {
	_, _, ml := moonPos(t)
	elong := norm360(ml - sunLon(daysJ2000(t)))
	illum = (1 - math.Cos(elong*d2r)) / 2
	waxing = elong < 180
	switch {
	case illum < 0.03:
		name = "New moon"
	case illum > 0.97:
		name = "Full moon"
	case math.Abs(illum-0.5) <= 0.05 && waxing:
		name = "First quarter"
	case math.Abs(illum-0.5) <= 0.05:
		name = "Last quarter"
	case illum < 0.5 && waxing:
		name = "Waxing crescent"
	case illum < 0.5:
		name = "Waning crescent"
	case waxing:
		name = "Waxing gibbous"
	default:
		name = "Waning gibbous"
	}
	return
}

// riseSet scans the local calendar day of `day` in loc at 1-minute steps and
// reports the first upward and first downward crossing of h0. Missing events
// (polar day/night, a moonrise that slips past midnight) come back not-ok.
func riseSet(alt func(time.Time) float64, day time.Time, loc *time.Location, h0 float64) (rise, set time.Time, okRise, okSet bool) {
	y, mo, d := day.In(loc).Date()
	start := time.Date(y, mo, d, 0, 0, 0, 0, loc)
	end := time.Date(y, mo, d+1, 0, 0, 0, 0, loc) // DST days run 23 or 25 hours
	prev := alt(start)
	for t := start.Add(time.Minute); !t.After(end) && !(okRise && okSet); t = t.Add(time.Minute) {
		a := alt(t)
		if !okRise && prev < h0 && a >= h0 {
			rise, okRise = t, true
		}
		if !okSet && prev >= h0 && a < h0 {
			set, okSet = t, true
		}
		prev = a
	}
	return
}

func clock12(t time.Time, ok bool) string {
	if !ok {
		return "—"
	}
	return t.Format("3:04 PM")
}

func dayLength(rise, set time.Time) string {
	d := set.Sub(rise)
	if d < 0 {
		d += 24 * time.Hour
	}
	m := int(d.Minutes() + 0.5)
	return fmt.Sprintf("%d h %d min", m/60, m%60)
}
