package main

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"log"
	"math"
	"net/http"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// gpuStat is one GPU, as the agent reports it and as the panel shows it.
type gpuStat struct {
	Host      string `json:"host"`
	Name      string `json:"n"`
	Up        bool   `json:"up"`     // agent reachable
	Temp      int    `json:"temp"`   // °C
	Util      int    `json:"util"`   // %
	VRAM      int    `json:"vram"`   // MiB used
	VRAMTotal int    `json:"vram_t"` // MiB
	Power     int    `json:"pwr"`    // W
	Procs     int    `json:"procs"`  // compute (type "C") processes
	State     string `json:"st"`     // idle | loaded | busy | off

	// Fan speed travels agent -> collector only (see gpuWire); never in /panel (8 KB budget).
	Fan   int  `json:"-"` // %
	FanOK bool `json:"-"` // false on passively cooled GPUs and old agents
}

// gpuWire is the agent's /gpu wire form. The outer Fan shadows gpuStat.Fan for JSON; a nil Fan
// (N/A, or an old agent) is omitted, and 0 stays a real reading.
type gpuWire struct {
	gpuStat
	Fan *int `json:"fan,omitempty"`
}

// agent serves this host's GPUs at GET /gpu. LAN-only and read-only; firewall it to the
// aggregator host (deploy/install-agent.ps1 does this on Windows).
func agent(addr string) {
	http.HandleFunc("GET /gpu", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "nvidia-smi", "-q", "-x").Output()
		if err != nil {
			http.Error(w, "nvidia-smi: "+err.Error(), http.StatusInternalServerError)
			return
		}
		gpus, err := parseSMI(out)
		if err != nil {
			http.Error(w, "nvidia-smi xml: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		wire := make([]gpuWire, len(gpus))
		for i, g := range gpus {
			wire[i].gpuStat = g
			if g.FanOK {
				wire[i].Fan = &g.Fan
			}
		}
		json.NewEncoder(w).Encode(wire)
	})
	log.Printf("deskpanel agent on %s", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}

// parseSMI reads `nvidia-smi -q -x`. Only type "C" processes count as compute: on Windows every
// desktop app holds the GPU as "C+G", which would otherwise make the card look busy forever.
func parseSMI(b []byte) ([]gpuStat, error) {
	var doc struct {
		GPUs []struct {
			Name  string `xml:"product_name"`
			Temp  string `xml:"temperature>gpu_temp"`
			Util  string `xml:"utilization>gpu_util"`
			Fan   string `xml:"fan_speed"` // "45 %", or "N/A" when passively cooled
			Used  string `xml:"fb_memory_usage>used"`
			Total string `xml:"fb_memory_usage>total"`
			Power struct {
				Draw    string `xml:"power_draw"`         // driver 550
				Instant string `xml:"instant_power_draw"` // driver 610+
			} `xml:"gpu_power_readings"`
			Procs []struct {
				Type string `xml:"type"`
			} `xml:"processes>process_info"`
		} `xml:"gpu"`
	}
	if err := xml.Unmarshal(b, &doc); err != nil {
		return nil, err
	}
	out := []gpuStat{}
	for _, g := range doc.GPUs {
		s := gpuStat{
			Name: strings.TrimPrefix(strings.TrimPrefix(g.Name, "NVIDIA "), "GeForce "),
			Up:   true, Temp: num(g.Temp), Util: num(g.Util),
			VRAM: num(g.Used), VRAMTotal: num(g.Total), Power: num(g.Power.Instant),
		}
		if s.Power == 0 {
			s.Power = num(g.Power.Draw)
		}
		if n, err := strconv.ParseFloat(strings.Fields(g.Fan + " x")[0], 64); err == nil { // "N/A", "[Not Supported]" etc. are unknown, not 0
			s.Fan, s.FanOK = int(math.Round(n)), true
		}
		for _, p := range g.Procs {
			if p.Type == "C" {
				s.Procs++
			}
		}
		// ponytail: 5% util threshold separates "model loaded, idle" (ollama keeps llama-server
		// resident) from real work. Tune here if the tiles read wrong.
		switch {
		case s.Procs > 0 && s.Util >= 5:
			s.State = "busy"
		case s.Procs > 0:
			s.State = "loaded"
		default:
			s.State = "idle"
		}
		out = append(out, s)
	}
	return out, nil
}

// num reads the leading number of nvidia-smi values such as "43 C", "2328 MiB", "18.38 W".
// "N/A" and empty read as 0.
func num(s string) int {
	f, _ := strconv.ParseFloat(strings.Fields(s + " 0")[0], 64)
	return int(math.Round(f))
}

// gpuCollector polls every agent in GPU_AGENTS ("name=url,name=url"). An unreachable host is
// data (state "off"), not a collector failure, so a sleeping workstation never greys the page.
type gpuCollector struct {
	agents [][2]string
	hc     http.Client
}

func newGPU(spec string) *gpuCollector {
	return &gpuCollector{agents: nameURLs("GPU_AGENTS", spec)}
}

// nameURLs parses "name=url,name=url" (GPU_AGENTS, SERVICES).
func nameURLs(env, spec string) [][2]string {
	var out [][2]string
	for _, a := range strings.Split(spec, ",") {
		name, url, ok := strings.Cut(strings.TrimSpace(a), "=")
		if !ok {
			log.Fatalf("%s entry %q: want name=url", env, a)
		}
		out = append(out, [2]string{name, strings.TrimRight(url, "/")})
	}
	return out
}

func (g *gpuCollector) Section() string         { return "homelab" }
func (g *gpuCollector) Key() string             { return "gpu" }
func (g *gpuCollector) Interval() time.Duration { return 5 * time.Second }

func (g *gpuCollector) Collect(ctx context.Context) (any, error) {
	res := make([][]gpuStat, len(g.agents))
	var wg sync.WaitGroup
	for i, a := range g.agents {
		wg.Go(func() { res[i] = g.fetch(ctx, a[0], a[1]) })
	}
	wg.Wait()
	return slices.Concat(res...), nil
}

func (g *gpuCollector) fetch(ctx context.Context, host, url string) []gpuStat {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var wire []gpuWire
	req, _ := http.NewRequestWithContext(ctx, "GET", url+"/gpu", nil)
	resp, err := g.hc.Do(req)
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			err = fmt.Errorf("%s", resp.Status)
		} else {
			err = json.NewDecoder(resp.Body).Decode(&wire)
		}
	}
	if err != nil || len(wire) == 0 {
		return []gpuStat{{Host: host, State: "off"}}
	}
	gpus := make([]gpuStat, len(wire))
	for i, w := range wire {
		gpus[i] = w.gpuStat
		gpus[i].Host = host
		if w.Fan != nil {
			gpus[i].Fan, gpus[i].FanOK = *w.Fan, true
		}
	}
	return gpus
}
