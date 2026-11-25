package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
)

// valueSpec is one card value: a shortcut (with optional instance/mount) or a
// raw PromQL query.
type valueSpec struct {
	Shortcut string
	Instance string
	Mount    string
	Prom     string
	JSON     *jsonSpec // a JSON URL or a Home Assistant entity (HA set)
}

// promQueries are the PromQL strings behind a valueSpec. Second is total,
// cores or tx; Graph is what a graph or gauge plots.
type promQueries struct {
	Value, Second, Graph string
	Unit                 string
	Lo, Hi               float64
	Auto                 bool
}

type state string

const (
	stOK    state = "ok"
	stWarn  state = "warn"
	stBad   state = "bad"
	stStale state = "stale"
	stNone  state = "none"
)

const maxPromLen = 512

// generalTypes are the card types that take a value.
var generalTypes = []string{"number", "gauge", "bar", "graph", "status"}

// shortcuts: the card types each suits; instance, its query (in queries below) keeps one series per machine, so
// without an @instance a card shows "add @instance (N)" (the editor makes you pick one); arg "mount": disk:/path.
var shortcuts = map[string]struct {
	types    string
	instance bool
	arg      string
}{
	"cpu":    {types: "number gauge bar graph"},
	"ram":    {types: "number gauge bar graph", instance: true},
	"disk":   {types: "number gauge bar graph", instance: true, arg: "mount"},
	"temp":   {types: "number gauge bar graph"},
	"load":   {types: "number gauge bar graph", instance: true},
	"net":    {types: "graph"},
	"uptime": {types: "number", instance: true},
}

func clip(s string) string {
	if len(s) > 20 {
		return s[:20]
	}
	return s
}

func validInstance(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range []byte(s) {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte(".:_-", c) >= 0) {
			return false
		}
	}
	return true
}

// parseValue decodes a card's "value" field: 1 spec, or 2 for a graph list.
func parseValue(raw json.RawMessage, typ string) ([]valueSpec, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil, errors.New("value is required")
	}
	if !contains(generalTypes, typ) {
		return nil, errors.New("not a value card")
	}
	if raw[0] == '[' {
		if typ != "graph" {
			return nil, errors.New("only a graph takes a list")
		}
		var items []json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil || len(items) != 2 {
			return nil, errors.New("a list needs exactly two values")
		}
		out := make([]valueSpec, 2)
		for i, it := range items {
			it = bytes.TrimSpace(it)
			if len(it) > 0 && it[0] == '[' {
				return nil, errors.New("lists do not nest")
			}
			s, err := parseOne(it, typ)
			if err != nil {
				return nil, err
			}
			if s.Shortcut == "net" {
				return nil, errors.New("net is a graph by itself")
			}
			out[i] = s
		}
		return out, nil
	}
	s, err := parseOne(raw, typ)
	if err != nil {
		return nil, err
	}
	return []valueSpec{s}, nil
}

func parseOne(raw json.RawMessage, typ string) (valueSpec, error) {
	switch raw[0] {
	case '"':
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return valueSpec{}, errors.New("bad value string")
		}
		if typ == "status" {
			return valueSpec{}, errors.New("status takes a prometheus query")
		}
		return parseShortcut(s, typ)
	case '{':
		var m map[string]json.RawMessage
		if json.Unmarshal(raw, &m) != nil {
			return valueSpec{}, errors.New("bad value object")
		}
		if _, ok := m["ha"]; ok {
			js, err := parseHASpec(raw)
			return valueSpec{JSON: js}, err
		}
		if j, ok := m["json"]; ok {
			if len(m) != 1 {
				return valueSpec{}, errors.New("a json value takes nothing else")
			}
			js, err := parseJSONSpec(j)
			return valueSpec{JSON: js}, err
		}
		p, ok := m["prometheus"]
		var q string
		if !ok || len(m) != 1 || json.Unmarshal(p, &q) != nil {
			return valueSpec{}, errors.New("expected {\"prometheus\": \"...\"}")
		}
		if q == "" {
			return valueSpec{}, errors.New("prometheus query is empty")
		}
		if strings.HasPrefix(q, "ha:") || strings.HasPrefix(q, "json:") { // those keys go to the JSON source
			return valueSpec{}, errors.New("a Prometheus query can't start with ha: or json:")
		}
		if len(q) > maxPromLen {
			return valueSpec{}, errors.New("prometheus query over 512 bytes")
		}
		return valueSpec{Prom: q}, nil
	}
	return valueSpec{}, errors.New("value must be text or an object")
}

// parseJSONSpec decodes {"url","path","every","header_env"} (ruling J1). The
// header_env check needs the environment, so parseLayout does it.
func parseJSONSpec(raw json.RawMessage) (*jsonSpec, error) {
	var in struct {
		URL       string `json:"url"`
		Path      string `json:"path"`
		HeaderEnv string `json:"header_env"`
		Every     *int   `json:"every"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&in) != nil {
		return nil, errors.New("json takes url, path, every, header_env")
	}
	u, err := url.Parse(in.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, errors.New("json url must be http:// or https://")
	}
	if len(in.URL) > 256 {
		return nil, errors.New("json url over 256 bytes")
	}
	if in.Path == "" || len(in.Path) > 128 {
		return nil, errors.New("json path is empty or over 128 bytes")
	}
	every := 30
	if in.Every != nil {
		every = *in.Every
	}
	if every < 10 || every > 3600 {
		return nil, errors.New("json every must be 10 to 3600 seconds")
	}
	return &jsonSpec{URL: in.URL, Path: in.Path, Every: every, HeaderEnv: in.HeaderEnv}, nil
}

func parseShortcut(s, typ string) (valueSpec, error) {
	var v valueSpec
	if i := strings.LastIndexByte(s, '@'); i >= 0 {
		v.Instance = s[i+1:]
		s = s[:i]
		if !validInstance(v.Instance) {
			return v, errors.New("bad instance after '@'")
		}
	}
	name := s
	if n, m, ok := strings.Cut(s, ":"); ok && shortcuts[n].arg == "mount" {
		name, v.Mount = n, m
		if v.Mount == "" || v.Mount[0] != '/' || len(v.Mount) > 40 || strings.ContainsAny(v.Mount, "\"\\\x7f") || strings.IndexFunc(v.Mount, func(r rune) bool { return r < 0x20 }) >= 0 {
			return v, errors.New(n + " needs a mount like " + n + ":/")
		}
	}
	sc, ok := shortcuts[name]
	if !ok || sc.arg == "mount" && v.Mount == "" {
		return v, fmt.Errorf("unknown shortcut '%s'", clip(s))
	}
	if !contains(strings.Fields(sc.types), typ) {
		if name == "uptime" {
			return v, errors.New("uptime needs a number")
		}
		return v, fmt.Errorf("%s needs a %s", name, sc.types)
	}
	v.Shortcut = name
	return v, nil
}

// sel builds metric{m1,m2}, with braces only when there are matchers.
func sel(metric string, m ...string) string {
	if len(m) == 0 {
		return metric
	}
	return metric + "{" + strings.Join(m, ",") + "}"
}

func (v valueSpec) queries() promQueries {
	if v.JSON != nil { // the card's unit applies; min/max come from the card
		k := v.JSON.key()
		return promQueries{Value: k, Graph: k, Auto: true}
	}
	if v.Prom != "" {
		return promQueries{Value: v.Prom, Graph: v.Prom, Auto: true}
	}
	var inst []string
	if v.Instance != "" {
		inst = []string{`instance="` + v.Instance + `"`}
	}
	with := func(m ...string) []string { return append(m, inst...) }
	pct := func(used, total string) string { return "100 * (" + used + ") / " + total }

	switch v.Shortcut {
	case "cpu":
		q := "100 - avg(rate(" + sel("node_cpu_seconds_total", with(`mode="idle"`)...) + "[5m]))*100"
		return promQueries{Value: q, Graph: q, Unit: "%", Hi: 100}
	case "ram":
		total := sel("node_memory_MemTotal_bytes", inst...)
		used := total + " - " + sel("node_memory_MemAvailable_bytes", inst...)
		return promQueries{Value: used, Second: total, Graph: pct(used, total), Unit: "bytes", Hi: 100}
	case "disk":
		m := with(`mountpoint="` + v.Mount + `"`)
		total := sel("node_filesystem_size_bytes", m...)
		used := total + " - " + sel("node_filesystem_avail_bytes", m...)
		return promQueries{Value: used, Second: total, Graph: pct(used, total), Unit: "bytes", Hi: 100}
	case "temp":
		q := "max(" + sel("node_hwmon_temp_celsius", inst...) + ")"
		return promQueries{Value: q, Graph: q, Unit: "°C", Hi: 100}
	case "load":
		q := sel("node_load1", inst...)
		return promQueries{Value: q, Second: "count(" + sel("node_cpu_seconds_total", with(`mode="idle"`)...) + ")", Graph: q}
	case "net":
		q := func(dir string) string {
			return "sum(rate(" + sel("node_network_"+dir+"_bytes_total", with(`device!~"lo|veth.*|docker.*|br-.*"`)...) + "[5m]))*8"
		}
		return promQueries{Value: q("receive"), Second: q("transmit"), Graph: q("receive"), Unit: "bps", Auto: true}
	case "uptime":
		q := "time() - " + sel("node_boot_time_seconds", inst...)
		return promQueries{Value: q, Graph: q, Unit: "s", Auto: true}
	}
	return promQueries{}
}

// tiered picks the decimals for |v| from ascending limits (decs has one more
// entry than lims), judged on the rounded value so 9.96 never prints "10.0"
// where an integer is wanted.
func tiered(a float64, lims []float64, decs []int) string {
	for i, l := range lims {
		if a < l {
			if r, _ := strconv.ParseFloat(fixedDec(a, decs[i]), 64); r < l {
				return fixedDec(a, decs[i])
			}
		}
	}
	return fixedDec(a, decs[len(lims)])
}

func rounded(s string) float64 { f, _ := strconv.ParseFloat(s, 64); return f }

// scaled divides a by 1000 until the printed value is under 1000 (or units run out).
func scaled(a float64, i int, last int, lims []float64, decs []int) (string, int) {
	for {
		s := tiered(a, lims, decs)
		if rounded(s) < 1000 || i >= last {
			return s, i
		}
		a /= 1000
		i++
	}
}

func sign(v float64, s string) string {
	if v < 0 && rounded(s) != 0 {
		return "-" + s
	}
	return s
}

var (
	numLims = []float64{10, 100}
	numDecs = []int{2, 1, 0}
)

// formatUnits are the units format reads (the cases below); any other unit is printed after the number.
var formatUnits = []string{"%", "°C", "bytes", "bps", "s"}

func format(v float64, unit string) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return "—"
	}
	a := math.Abs(v)
	switch unit {
	case "%":
		return sign(v, fixedDec(a, 0)) + " %"
	case "°C":
		return sign(v, fixedDec(a, 0)) + "°"
	case "bytes":
		if a < 1000 && rounded(fixedDec(a, 0)) < 1000 {
			return sign(v, fixedDec(a, 0)) + " B"
		}
		s, i := scaled(a/1000, 1, 4, []float64{10}, []int{1, 0})
		return sign(v, s) + " " + []string{"B", "KB", "MB", "GB", "TB"}[i]
	case "bps":
		s, i := scaled(a/1e6, 0, 2, []float64{1, 10}, []int{2, 1, 0})
		return sign(v, s) + " " + []string{"Mb/s", "Gb/s", "Tb/s"}[i]
	case "s":
		switch {
		case a >= 2*86400:
			return sign(v, fixedDec(math.Floor(a/86400), 0)) + " d"
		case a >= 2*3600:
			return sign(v, fixedDec(math.Floor(a/3600), 0)) + " h"
		case a >= 120:
			return sign(v, fixedDec(math.Floor(a/60), 0)) + " min"
		}
		return sign(v, fixedDec(math.Floor(a), 0)) + " s"
	}
	// no unit, or a custom one
	var s, suffix string
	if a < 1e5 {
		s = tiered(a, numLims, numDecs)
	} else {
		var i int
		s, i = scaled(a/1000, 0, 4, numLims, numDecs)
		suffix = []string{"k", "M", "G", "T", "P"}[i]
	}
	out := sign(v, s)
	if suffix != "" {
		out += " " + suffix
	}
	if unit != "" {
		out += " " + unit
	}
	return out
}

// threshold grades v against optional warn/bad limits (inclusive).
func threshold(v float64, warn, bad *float64, below bool) state {
	past := func(limit *float64) bool {
		if limit == nil {
			return false
		}
		if below {
			return v <= *limit
		}
		return v >= *limit
	}
	switch {
	case past(bad):
		return stBad
	case past(warn):
		return stWarn
	}
	return stOK
}

const b64 = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"

// scaleIdx maps v onto 0..63, clamped; hi <= lo counts as lo+1.
func scaleIdx(v, lo, hi float64) int {
	if hi <= lo {
		hi = lo + 1
	}
	f := (v - lo) / (hi - lo)
	if math.IsNaN(f) || f < 0 {
		return 0
	}
	if f > 1 {
		return 63
	}
	return int(math.Round(f * 63))
}

// encodePoints is the S4 graph encoding: one base64url character per point.
func encodePoints(vals []float64, lo, hi float64) string {
	out := make([]byte, len(vals))
	for i, v := range vals {
		out[i] = b64[scaleIdx(v, lo, hi)]
	}
	return string(out)
}

// fixedDec prints a with d decimals, rounding halves up (FormatFloat rounds to even).
func fixedDec(a float64, d int) string {
	p := math.Pow10(d)
	return strconv.FormatFloat(math.Round(a*p)/p, 'f', d, 64)
}
