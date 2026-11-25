package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"time"
)

// Open-Meteo current conditions: the temperature, humidity, text and wind the
// panel overlays on the radar.

type wxNow struct {
	T    int    `json:"t"`
	RH   int    `json:"rh"`
	Code int    `json:"code"`
	Day  bool   `json:"day"`
	Text string `json:"text"`
	Hi   int    `json:"hi"`
	Lo   int    `json:"lo"`
	Wind string `json:"wind"`
}

type conditions struct {
	hc       *http.Client
	base     string
	lat, lon float64
}

func newConditions(hc *http.Client, base string, lat, lon float64) *conditions {
	return &conditions{hc: hc, base: base, lat: lat, lon: lon}
}

func (c *conditions) Section() string         { return "weather" }
func (c *conditions) Key() string             { return "now" }
func (c *conditions) Interval() time.Duration { return 10 * time.Minute }

var wmoTexts = map[int]string{
	0: "Clear", 1: "Mostly clear", 2: "Partly cloudy", 3: "Overcast", 45: "Fog", 48: "Fog",
	51: "Drizzle", 53: "Drizzle", 55: "Drizzle", 56: "Freezing drizzle", 57: "Freezing drizzle",
	61: "Light rain", 63: "Rain", 65: "Heavy rain", 66: "Freezing rain", 67: "Freezing rain",
	71: "Light snow", 73: "Snow", 75: "Heavy snow", 77: "Snow grains",
	80: "Light showers", 81: "Showers", 82: "Heavy showers", 85: "Snow showers", 86: "Snow showers",
	95: "Thunderstorm", 96: "Thunderstorm, hail", 99: "Thunderstorm, hail",
}

// wmoText is the short text for a WMO weather code, "" if unknown.
func wmoText(code int) string { return wmoTexts[code] }

var compass = [16]string{"N", "NNE", "NE", "ENE", "E", "ESE", "SE", "SSE", "S", "SSW", "SW", "WSW", "W", "WNW", "NW", "NNW"}

// windStr formats wind as "S 14 mph" (16-point compass); under 1 mph is "Calm".
func windStr(mph, deg float64) string {
	if mph < 1 {
		return "Calm"
	}
	i := int(math.Round(math.Mod(deg, 360)/22.5)) % 16
	if i < 0 {
		i += 16
	}
	return fmt.Sprintf("%s %d mph", compass[i], int(math.Round(mph)))
}

func (c *conditions) Collect(ctx context.Context) (any, error) {
	url := fmt.Sprintf("%s/v1/forecast?latitude=%.4f&longitude=%.4f&current=temperature_2m,relative_humidity_2m,weather_code,is_day,wind_speed_10m,wind_direction_10m&daily=temperature_2m_max,temperature_2m_min&temperature_unit=fahrenheit&wind_speed_unit=mph&timezone=auto&forecast_days=1",
		c.base, c.lat, c.lon)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("open-meteo: %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var r struct {
		Current *struct {
			T    float64 `json:"temperature_2m"`
			RH   float64 `json:"relative_humidity_2m"`
			Code int     `json:"weather_code"`
			Day  int     `json:"is_day"`
			Spd  float64 `json:"wind_speed_10m"`
			Dir  float64 `json:"wind_direction_10m"`
		} `json:"current"`
		Daily struct {
			Max []float64 `json:"temperature_2m_max"`
			Min []float64 `json:"temperature_2m_min"`
		} `json:"daily"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, err
	}
	if r.Current == nil {
		return nil, fmt.Errorf("open-meteo: no current conditions")
	}
	if len(r.Daily.Max) == 0 || len(r.Daily.Min) == 0 {
		return nil, fmt.Errorf("open-meteo: no daily high/low")
	}
	cur := r.Current
	return wxNow{T: int(math.Round(cur.T)), RH: int(math.Round(cur.RH)), Code: cur.Code, Day: cur.Day == 1,
		Text: wmoText(cur.Code), Hi: int(math.Round(r.Daily.Max[0])), Lo: int(math.Round(r.Daily.Min[0])),
		Wind: windStr(cur.Spd, cur.Dir)}, nil
}
