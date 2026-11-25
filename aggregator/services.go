package main

import (
	"context"
	"net/http"
	"sync"
	"time"
)

// services reports up/down for plain HTTP health endpoints in SERVICES ("name=url,..."), e.g.
// Jellyfin's GET /health. A 2xx within 3 s is up. Like the GPU agents, a down service is data
// (on:false), never a collector failure.
type services struct {
	list [][2]string
	hc   http.Client
}

const maxServices = 6

func (s *services) Section() string         { return "homelab" }
func (s *services) Key() string             { return "svc" }
func (s *services) Interval() time.Duration { return 15 * time.Second }

func (s *services) Collect(ctx context.Context) (any, error) {
	out := make([]peer, len(s.list))
	var wg sync.WaitGroup
	for i, sv := range s.list {
		wg.Go(func() {
			ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			req, _ := http.NewRequestWithContext(ctx, "GET", sv[1], nil)
			resp, err := s.hc.Do(req)
			if err == nil {
				resp.Body.Close()
			}
			out[i] = peer{trunc(sv[0], peerMax), err == nil && resp.StatusCode/100 == 2}
		})
	}
	wg.Wait()
	return out, nil
}
