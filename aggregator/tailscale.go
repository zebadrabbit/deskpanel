package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"
)

// tailscale reads peer state from the local tailscaled LocalAPI over its unix socket
// (compose mounts /var/run/tailscale). Read-only status needs no special permission.
type tailscale struct{ hc http.Client }

func newTailscale(sock string) *tailscale {
	return &tailscale{hc: http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		},
	}}}
}

func (t *tailscale) Section() string         { return "homelab" }
func (t *tailscale) Key() string             { return "ts" }
func (t *tailscale) Interval() time.Duration { return 15 * time.Second }

const (
	maxPeers = 12
	peerMax  = 20 // bytes
)

type peer struct {
	Name string `json:"n"`
	On   bool   `json:"on"`
}

func (t *tailscale) Collect(ctx context.Context) (any, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://local-tailscaled.sock/localapi/v0/status", nil)
	resp, err := t.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tailscale localapi: %s", resp.Status)
	}
	type node struct {
		HostName string
		Online   bool
	}
	var st struct {
		Self node
		Peer map[string]node
	}
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		return nil, err
	}
	peers := []peer{{trunc(st.Self.HostName, peerMax), st.Self.Online}}
	for _, p := range st.Peer {
		peers = append(peers, peer{trunc(p.HostName, peerMax), p.Online})
	}
	sort.Slice(peers, func(i, j int) bool { return strings.ToLower(peers[i].Name) < strings.ToLower(peers[j].Name) })
	if len(peers) > maxPeers {
		peers = peers[:maxPeers]
	}
	return peers, nil
}
