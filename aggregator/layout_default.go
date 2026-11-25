package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	maxDefaultGPUs = 4
	// maxDefaultPools = PANEL_MAX_POOLS (firmware/main/panel.h), the pools the panel keeps; an 8th pool
	// (the boot pool comes last) gets no card. 7 is also all that fit in the default's two rows.
	maxDefaultPools = 7
)

// found is what the collectors reported: the input to defaultLayout.
type found struct {
	GPUs, Pools              []string // one src per GPU ("host", "host#2", ...), pool names
	TrueNAS, Planka, Weather bool
	Waiting                  bool // the GPU or TrueNAS collector hasn't finished its first run: the default would be short
}

// foundSources reads the slots' current data. Works on demo data too.
func foundSources(slots []*slot) found {
	var f found
	per := map[string]int{} // GPUs seen per host, in collector order
	for _, s := range slots {
		s.mu.Lock()
		data, ran := s.data, s.ran
		s.mu.Unlock()
		key := s.c.Section() + "." + s.c.Key()
		if !ran && (key == "homelab.gpu" || key == "homelab.nas") {
			f.Waiting = true
		}
		switch key {
		case "homelab.gpu":
			if g, ok := data.([]gpuStat); ok {
				for _, st := range g {
					if st.Host != "" {
						per[st.Host]++
						src := st.Host
						if per[st.Host] > 1 {
							src += "#" + strconv.Itoa(per[st.Host])
						}
						f.GPUs = append(f.GPUs, src)
					}
				}
			}
		case "homelab.nas":
			if n, ok := data.(nas); ok {
				f.TrueNAS = true
				for _, p := range n.Pools {
					f.Pools = append(f.Pools, p.Name)
				}
			}
		case "planka.boards":
			f.Planka = true
		}
		if s.c.Section() == "weather" {
			f.Weather = true
		}
	}
	return f
}

func defaultLayout(f found) Layout {
	var cards []Card
	used := map[string]bool{}
	add := func(id, typ string, col, row int, size, src string) {
		cards = append(cards, Card{ID: safeID(id, used), Type: typ, At: [2]int{col, row}, Size: cardSize(size), Src: cutBytes(src, maxSrcLen), Tap: defaultTap(typ)}) // src cut to the firmware limit (maxSrcLen)
	}
	row := 1
	if gpus := f.GPUs[:min(len(f.GPUs), maxDefaultGPUs)]; len(gpus) > 0 {
		for i, h := range gpus {
			if len(gpus) <= 2 {
				add("gpu-"+h, "gpu", 2*i+1, row, "2x3", h)
			} else {
				add("gpu-"+h, "gpu", i+1, row, "1x3", h)
			}
		}
		row += 3
	}
	if f.TrueNAS {
		pools := f.Pools[:min(len(f.Pools), maxDefaultPools)]
		if len(pools) <= 3 {
			for i, p := range pools {
				add("pool-"+p, "pool", i+1, row, "1x2", p)
			}
			add("nas-alerts", "nas-alerts", 4, row, "1x2", "")
		} else {
			for i, p := range pools {
				add("pool-"+p, "pool", i%4+1, row+i/4, "1x1", p)
			}
			n := len(pools)
			add("nas-alerts", "nas-alerts", n%4+1, row+n/4, "1x1", "")
		}
		row += 2
	}
	add("dots", "dots", 1, row, "4x1", "both")
	return Layout{Tabs: []Tab{
		{Name: "Homelab", Cards: cards},
		{Name: "Planka", Cards: []Card{{ID: "board", Type: "board", At: [2]int{1, 1}, Size: "4x6", Tap: "none"}}},
		{Name: "Weather", Cards: []Card{{ID: "radar", Type: "radar", At: [2]int{1, 1}, Size: "4x6", Tap: "none"}}},
	}}
}

// layoutDefault is `deskpanel layout-default`: one collection pass over the .env sources
// (GPU agents, TrueNAS), then the default layout; a placeholder template if none answered.
func layoutDefault() error {
	var slots []*slot
	add := func(c Collector) { slots = append(slots, &slot{c: c}) }
	if url := os.Getenv("TRUENAS_URL"); url != "" {
		add(newTrueNAS(url, getenv("TRUENAS_USER", "deskpanel"), mustEnv("TRUENAS_API_KEY"), mustEnv("TRUENAS_CERT_SHA256")))
	}
	if agents := os.Getenv("GPU_AGENTS"); agents != "" {
		add(newGPU(agents))
	}
	for _, s := range slots {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		if d, err := s.c.Collect(ctx); err == nil {
			s.data = d
		}
		cancel()
	}
	f := foundSources(slots)
	if len(f.GPUs) == 0 && len(f.Pools) == 0 {
		f = found{GPUs: []string{"<gpu host>"}, Pools: []string{"<pool>"}, TrueNAS: true}
	}
	b := handLayout(defaultLayout(f))
	if _, err := parseLayout(b); err != nil {
		return fmt.Errorf("generated layout invalid: %w", err)
	}
	os.Stdout.Write(b)
	return nil
}

// handLayout writes a layout the way layout.example.json is laid out — one card per line — so
// the file `layout-default` starts people off with is easy to edit by hand.
func handLayout(l Layout) []byte {
	var b bytes.Buffer
	b.WriteString("{\n")
	if l.Tour != nil {
		b.WriteString("  \"tour\": " + oneLine(l.Tour) + ",\n")
	}
	b.WriteString("  \"tabs\": [\n")
	for i, t := range l.Tabs {
		fmt.Fprintf(&b, "    {\n      \"name\": %s,\n      \"cards\": [\n", oneLine(t.Name))
		for j, c := range t.Cards {
			b.WriteString("        " + oneLine(c) + comma(j, len(t.Cards)) + "\n")
		}
		b.WriteString("      ]\n    }" + comma(i, len(l.Tabs)) + "\n")
	}
	b.WriteString("  ]\n}\n")
	return b.Bytes()
}

// oneLine is compact JSON with a space after each ':' and ',' outside strings, and "<gpu host>"
// left readable.
func oneLine(v any) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.Encode(v) // ponytail: a Tour, a tab name or a Card (its value is raw JSON that was decoded, so valid): nothing here can fail to encode
	var out []byte
	in, esc := false, false
	for _, ch := range bytes.TrimSpace(buf.Bytes()) {
		out = append(out, ch)
		switch {
		case esc:
			esc = false
		case in && ch == '\\':
			esc = true
		case ch == '"':
			in = !in
		case !in && (ch == ':' || ch == ','):
			out = append(out, ' ')
		}
	}
	return string(out)
}

func comma(i, n int) string {
	if i < n-1 {
		return ","
	}
	return ""
}

// safeID makes a card id of [a-z0-9-], at most 23 bytes, unique within used.
func safeID(s string, used map[string]bool) string {
	b := []byte(strings.ToLower(s))
	for i, c := range b {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			b[i] = '-'
		}
	}
	base := string(b[:min(len(b), 23)])
	id := base
	for n := 2; used[id]; n++ {
		suf := "-" + strconv.Itoa(n)
		id = base[:min(len(base), 23-len(suf))] + suf
	}
	used[id] = true
	return id
}
