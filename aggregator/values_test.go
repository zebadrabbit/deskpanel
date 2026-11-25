package main

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func specFor(t *testing.T, raw, typ string) valueSpec {
	t.Helper()
	s, err := parseValue(json.RawMessage(raw), typ)
	if err != nil || len(s) != 1 {
		t.Fatalf("parseValue(%s,%s) = %v, %v", raw, typ, s, err)
	}
	return s[0]
}

func TestShortcutQueries(t *testing.T) {
	const idle = `node_cpu_seconds_total{mode="idle"`
	ramGraph := `100 * (node_memory_MemTotal_bytes - node_memory_MemAvailable_bytes) / node_memory_MemTotal_bytes`
	ramInst := `100 * (node_memory_MemTotal_bytes{instance="node1:9100"} - node_memory_MemAvailable_bytes{instance="node1:9100"}) / node_memory_MemTotal_bytes{instance="node1:9100"}`
	netSel := `{device!~"lo|veth.*|docker.*|br-.*"}[5m]))*8`
	cases := []struct {
		raw, typ             string
		value, second, graph string
		unit                 string
		lo, hi               float64
		auto                 bool
	}{
		{`"cpu"`, "number", `100 - avg(rate(` + idle + `}[5m]))*100`, "", `100 - avg(rate(` + idle + `}[5m]))*100`, "%", 0, 100, false},
		{`"cpu@node1:9100"`, "gauge", `100 - avg(rate(` + idle + `,instance="node1:9100"}[5m]))*100`, "", `100 - avg(rate(` + idle + `,instance="node1:9100"}[5m]))*100`, "%", 0, 100, false},
		{`"ram"`, "bar", `node_memory_MemTotal_bytes - node_memory_MemAvailable_bytes`, `node_memory_MemTotal_bytes`, ramGraph, "bytes", 0, 100, false},
		{`"ram@node1:9100"`, "graph",
			`node_memory_MemTotal_bytes{instance="node1:9100"} - node_memory_MemAvailable_bytes{instance="node1:9100"}`,
			`node_memory_MemTotal_bytes{instance="node1:9100"}`, ramInst, "bytes", 0, 100, false},
		{`"disk:/"`, "number", `node_filesystem_size_bytes{mountpoint="/"} - node_filesystem_avail_bytes{mountpoint="/"}`,
			`node_filesystem_size_bytes{mountpoint="/"}`,
			`100 * (node_filesystem_size_bytes{mountpoint="/"} - node_filesystem_avail_bytes{mountpoint="/"}) / node_filesystem_size_bytes{mountpoint="/"}`, "bytes", 0, 100, false},
		{`"temp@node1:9100"`, "number", `max(node_hwmon_temp_celsius{instance="node1:9100"})`, "", `max(node_hwmon_temp_celsius{instance="node1:9100"})`, "°C", 0, 100, false},
		{`"load"`, "number", `node_load1`, `count(` + idle + `})`, `node_load1`, "", 0, 0, false},
		{`"net"`, "graph", `sum(rate(node_network_receive_bytes_total` + netSel, `sum(rate(node_network_transmit_bytes_total` + netSel,
			`sum(rate(node_network_receive_bytes_total` + netSel, "bps", 0, 0, true},
		{`"uptime"`, "number", `time() - node_boot_time_seconds`, "", `time() - node_boot_time_seconds`, "s", 0, 0, true},
		{`{"prometheus":"up"}`, "gauge", "up", "", "up", "", 0, 0, true},
	}
	for _, c := range cases {
		q := specFor(t, c.raw, c.typ).queries()
		want := promQueries{c.value, c.second, c.graph, c.unit, c.lo, c.hi, c.auto}
		if q != want {
			t.Errorf("%s:\n got %+v\nwant %+v", c.raw, q, want)
		}
	}
}

func TestParseValueErrors(t *testing.T) {
	cases := []struct{ raw, typ string }{
		{`"cpux"`, "number"}, {`"disk:"`, "number"}, {`"disk:/a\"b"`, "number"},
		{`"cpu@"`, "number"}, {`"cpu@a b"`, "number"},
		{`"net"`, "number"}, {`"uptime"`, "bar"},
		{`{"prometheus":""}`, "number"},
		{`{"prometheus":"` + strings.Repeat("a", 513) + `"}`, "number"},
		{`{"json":{}}`, "number"},
		{`["cpu"]`, "graph"}, {`["cpu","ram","temp"]`, "graph"},
		{`["cpu","ram"]`, "number"},
		{`"cpu"`, "status"},
		{`"disk:x"`, "number"}, {`"disk:/` + strings.Repeat("a", 40) + `"`, "number"},
		{`5`, "number"}, {``, "number"},
	}
	for _, c := range cases {
		_, err := parseValue(json.RawMessage(c.raw), c.typ)
		if err == nil {
			t.Errorf("%s in %s: want error", c.raw, c.typ)
		} else if len(err.Error()) > 50 {
			t.Errorf("%s: error too long (%d): %v", c.raw, len(err.Error()), err)
		}
	}
	// A long bad shortcut name must not blow the error budget.
	_, err := parseValue(json.RawMessage(`"`+strings.Repeat("x", 300)+`"`), "number")
	if err == nil || len(err.Error()) > 50 {
		t.Errorf("long name: %v", err)
	}
	if _, err := parseValue(json.RawMessage(`{"json":{}}`), "number"); err == nil || err.Error() != "json url must be http:// or https://" {
		t.Errorf("json: %v", err)
	}
}

func TestParseValueControlBytesAndExactType(t *testing.T) {
	for _, m := range []string{"disk:/a\\nb", "disk:/a\\u007fb", "disk:/a\\tb"} {
		if _, err := parseValue(json.RawMessage(`"`+m+`"`), "number"); err == nil {
			t.Errorf("%s: want error", m)
		}
	}
	// a type that is only a substring of the list is not a type
	for _, typ := range []string{"num", "bar graph", "ber", " "} {
		if _, err := parseValue(json.RawMessage(`"cpu"`), typ); err == nil {
			t.Errorf("type %q: want error", typ)
		}
	}
}

func TestParseValueOK(t *testing.T) {
	s, err := parseValue(json.RawMessage(`["cpu",{"prometheus":"up"}]`), "graph")
	if err != nil || len(s) != 2 || s[0].Shortcut != "cpu" || s[1].Prom != "up" {
		t.Errorf("list: %v %v", s, err)
	}
	if v := specFor(t, `"disk:/mnt/a b@node1:9100"`, "gauge"); v.Mount != "/mnt/a b" || v.Instance != "node1:9100" {
		t.Errorf("disk: %+v", v)
	}
	specFor(t, `{"prometheus":"up"}`, "status")
}

func TestFormat(t *testing.T) {
	exact := []struct {
		v    float64
		unit string
		want string
	}{
		{87.2, "%", "87 %"},
		{9173499904, "bytes", "9.2 GB"}, {75973189632, "bytes", "76 GB"}, {2.4e12, "bytes", "2.4 TB"}, {512, "bytes", "512 B"},
		{999.6, "bytes", "1.0 KB"}, {9.96e9, "bytes", "10 GB"}, {999.96e3, "bytes", "1.0 MB"},
		{139444.6, "bps", "0.14 Mb/s"}, {3.2e6, "bps", "3.2 Mb/s"}, {45e6, "bps", "45 Mb/s"}, {1.5e9, "bps", "1.5 Gb/s"},
		{4349495, "s", "50 d"}, {7300, "s", "2 h"}, {150, "s", "2 min"}, {45, "s", "45 s"},
		{50, "°C", "50°"},
		{3.07, "", "3.07"}, {87.24, "", "87.2"}, {1234.5, "", "1235"}, {-0.5, "", "-0.50"},
		{9.999, "", "10.0"}, {99.96, "", "100"},
		{12, "req/s", "12.0 req/s"},
		{99999, "", "99999"}, {1234567, "", "1.23 M"}, {1e15, "", "1.00 P"}, {1234567, "req/s", "1.23 M req/s"},
		{999960, "", "1.00 M"}, {-0.001, "", "0.00"},
		{math.NaN(), "%", "—"}, {math.Inf(1), "bytes", "—"}, {math.Inf(-1), "", "—"},
	}
	for _, c := range exact {
		if got := format(c.v, c.unit); got != c.want {
			t.Errorf("format(%v,%q) = %q, want %q", c.v, c.unit, got, c.want)
		}
	}
	sweep := func(unit string, max float64) {
		for _, sign := range []float64{1, -1} {
			for v := 0.0; v <= max; v = v*1.07 + 0.013 {
				if got := format(sign*v, unit); len(got) > 15 {
					t.Fatalf("format(%v,%q) = %q (%d bytes)", sign*v, unit, got, len(got))
				}
			}
			if got := format(sign*max, unit); len(got) > 15 {
				t.Fatalf("format(%v,%q) = %q too long", sign*max, unit, got)
			}
		}
	}
	for _, u := range []string{"bytes", "bps", "s", ""} {
		sweep(u, 1e15)
	}
	for _, u := range []string{"%", "°C"} {
		sweep(u, 1e5)
	}
}

func TestThreshold(t *testing.T) {
	w, b := 80.0, 95.0
	for v, want := range map[float64]state{87: stWarn, 96: stBad, 50: stOK} {
		if got := threshold(v, &w, &b, false); got != want {
			t.Errorf("threshold(%v) = %s, want %s", v, got, want)
		}
	}
	if got := threshold(1e9, nil, nil, false); got != stOK {
		t.Errorf("nil thresholds: %s", got)
	}
	if got := threshold(90, &w, nil, false); got != stWarn {
		t.Errorf("warn only: %s", got)
	}
	if got := threshold(99, nil, &b, false); got != stBad {
		t.Errorf("bad only: %s", got)
	}
	w, b = 20, 10
	for v, want := range map[float64]state{5: stBad, 15: stWarn, 30: stOK} {
		if got := threshold(v, &w, &b, true); got != want {
			t.Errorf("below threshold(%v) = %s, want %s", v, got, want)
		}
	}
}

func TestEncodePoints(t *testing.T) {
	if got := encodePoints([]float64{0, 50, 100}, 0, 100); got != "Ag_" {
		t.Errorf("got %q", got)
	}
	if got := encodePoints([]float64{-5, 500}, 0, 100); got != "A_" {
		t.Errorf("clamp: %q", got)
	}
	if got := encodePoints([]float64{3, 3, 3}, 3, 3); got != "AAA" {
		t.Errorf("lo==hi: %q", got)
	}
	if got := encodePoints(nil, 0, 100); got != "" {
		t.Errorf("empty: %q", got)
	}
	if scaleIdx(math.NaN(), 0, 1) != 0 || scaleIdx(1, 0, 1) != 63 || scaleIdx(5, 10, 0) != 0 {
		t.Error("scaleIdx edge cases")
	}
}
