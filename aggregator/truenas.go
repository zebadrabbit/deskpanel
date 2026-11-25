package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/coder/websocket"
)

// trueNAS talks JSON-RPC 2.0 over wss to the TrueNAS middleware (/api/current). The REST API is
// deprecated, and an API key must only ever cross TLS: TrueNAS revokes a key it receives over an
// insecure transport. The NAS uses a self-signed cert, so we pin its SHA-256 rather than skip
// verification. Only the slot goroutine calls Collect, so conn needs no lock.
type trueNAS struct {
	url, user, key string
	pin            []byte
	conn           *websocket.Conn
	id             int
}

func newTrueNAS(url, user, key, pin string) *trueNAS {
	p, err := hex.DecodeString(strings.ReplaceAll(strings.ToLower(pin), ":", ""))
	if err != nil || len(p) != sha256.Size {
		log.Fatal("TRUENAS_CERT_SHA256 must be the cert's SHA-256 fingerprint (64 hex digits, colons optional)")
	}
	return &trueNAS{url: url, user: user, key: key, pin: p}
}

func (t *trueNAS) Section() string         { return "homelab" }
func (t *trueNAS) Key() string             { return "nas" }
func (t *trueNAS) Interval() time.Duration { return 30 * time.Second }

const (
	maxAlerts  = 8
	alertMax   = 120 // bytes; the panel shows the full text when the alerts tile is tapped
	poolNameMx = 24
)

type pool struct {
	Name   string `json:"n"`
	Health string `json:"h"` // ONLINE, DEGRADED, FAULTED...
	OK     bool   `json:"ok"`
	Used   int64  `json:"used"`  // GiB
	Avail  int64  `json:"avail"` // GiB
}

type alert struct {
	Level string `json:"lvl"` // INFO, NOTICE, WARNING, ERROR, CRITICAL, ALERT, EMERGENCY
	Msg   string `json:"msg"`
}

type nas struct {
	Pools  []pool  `json:"pools"`
	Alerts []alert `json:"alerts"` // undismissed, most severe first (INFO/NOTICE last), max 8
	Info   int     `json:"info"`   // how many undismissed alerts are INFO/NOTICE
}

func (t *trueNAS) Collect(ctx context.Context) (any, error) {
	if t.conn == nil {
		if err := t.connect(ctx); err != nil {
			return nil, err
		}
	}
	out, err := t.collect(ctx)
	if err != nil { // reconnect next time
		t.conn.CloseNow()
		t.conn = nil
	}
	return out, err
}

func (t *trueNAS) connect(ctx context.Context) error {
	tlsCfg := &tls.Config{
		InsecureSkipVerify: true, // replaced by the fingerprint pin below
		VerifyConnection: func(cs tls.ConnectionState) error {
			sum := sha256.Sum256(cs.PeerCertificates[0].Raw)
			if !bytes.Equal(sum[:], t.pin) {
				return fmt.Errorf("TrueNAS cert SHA-256 is %X, not TRUENAS_CERT_SHA256 (renewed cert? update the pin)", sum)
			}
			return nil
		},
	}
	c, _, err := websocket.Dial(ctx, t.url, &websocket.DialOptions{
		HTTPClient: &http.Client{Transport: &http.Transport{TLSClientConfig: tlsCfg}},
	})
	if err != nil {
		return err
	}
	c.SetReadLimit(8 << 20)
	t.conn = c
	var res struct {
		ResponseType string `json:"response_type"`
	}
	err = t.call(ctx, "auth.login_ex", []any{map[string]string{
		"mechanism": "API_KEY_PLAIN", "username": t.user, "api_key": t.key,
	}}, &res)
	if err == nil && res.ResponseType != "SUCCESS" {
		err = fmt.Errorf("auth.login_ex as %s: %s", t.user, res.ResponseType)
	}
	if err != nil {
		c.CloseNow()
		t.conn = nil
	}
	return err
}

func (t *trueNAS) call(ctx context.Context, method string, params []any, out any) error {
	t.id++
	req, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": t.id, "method": method, "params": params})
	if err := t.conn.Write(ctx, websocket.MessageText, req); err != nil {
		return err
	}
	for {
		_, msg, err := t.conn.Read(ctx)
		if err != nil {
			return err
		}
		var resp struct {
			ID     *int            `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string          `json:"message"`
				Data    json.RawMessage `json:"data"`
			} `json:"error"`
		}
		if err := json.Unmarshal(msg, &resp); err != nil {
			return fmt.Errorf("%s: %w", method, err)
		}
		if resp.ID == nil || *resp.ID != t.id {
			continue // event or a reply to an abandoned call
		}
		if resp.Error != nil {
			return fmt.Errorf("%s: %s %s", method, resp.Error.Message, trunc(string(resp.Error.Data), 300))
		}
		return json.Unmarshal(resp.Result, out)
	}
}

func (t *trueNAS) collect(ctx context.Context) (nas, error) {
	var out nas
	var pools []struct {
		Name    string `json:"name"`
		Status  string `json:"status"`
		Healthy bool   `json:"healthy"`
	}
	if err := t.call(ctx, "pool.query", []any{[]any{}, map[string]any{"select": []string{"name", "status", "healthy"}}}, &pools); err != nil {
		return out, err
	}
	names := []string{}
	for _, p := range pools {
		names = append(names, p.Name)
	}
	// Root-dataset used/available matches the TrueNAS UI and is right for RAIDZ; pool.query's
	// size/allocated count raw vdev space including parity.
	var ds []struct {
		ID        string                 `json:"id"`
		Used      struct{ Parsed int64 } `json:"used"`
		Available struct{ Parsed int64 } `json:"available"`
	}
	if err := t.call(ctx, "pool.dataset.query", []any{
		[]any{[]any{"id", "in", names}},
		map[string]any{"extra": map[string]any{"retrieve_children": false, "properties": []string{"used", "available"}}},
	}, &ds); err != nil {
		return out, err
	}
	space := map[string][2]int64{}
	for _, d := range ds {
		space[d.ID] = [2]int64{d.Used.Parsed >> 30, d.Available.Parsed >> 30}
	}
	for _, p := range pools {
		s := space[p.Name]
		out.Pools = append(out.Pools, pool{trunc(p.Name, poolNameMx), p.Status, p.Healthy, s[0], s[1]})
	}

	var boot struct {
		Name      string `json:"name"`
		Status    string `json:"status"`
		Healthy   bool   `json:"healthy"`
		Allocated int64  `json:"allocated"`
		Free      int64  `json:"free"`
	}
	if err := t.call(ctx, "boot.get_state", []any{}, &boot); err != nil {
		return out, err
	}
	out.Pools = append(out.Pools, pool{trunc(boot.Name, poolNameMx), boot.Status, boot.Healthy, boot.Allocated >> 30, boot.Free >> 30})

	var alerts []struct {
		Level     string `json:"level"`
		Formatted string `json:"formatted"`
		Dismissed bool   `json:"dismissed"`
	}
	if err := t.call(ctx, "alert.list", []any{}, &alerts); err != nil {
		return out, err
	}
	out.Alerts = []alert{}
	for _, a := range alerts {
		if a.Dismissed {
			continue
		}
		if severity[a.Level] == 0 {
			out.Info++
		}
		out.Alerts = append(out.Alerts, alert{a.Level, trunc(htmlTag.ReplaceAllString(a.Formatted, " "), alertMax)})
	}
	sort.SliceStable(out.Alerts, func(i, j int) bool { return severity[out.Alerts[i].Level] > severity[out.Alerts[j].Level] })
	if len(out.Alerts) > maxAlerts {
		out.Alerts = out.Alerts[:maxAlerts]
	}
	return out, nil
}

var (
	severity = map[string]int{"WARNING": 1, "ERROR": 2, "CRITICAL": 3, "ALERT": 4, "EMERGENCY": 5}
	htmlTag  = regexp.MustCompile(`<[^>]*>`) // some alert texts carry <br> etc.
)
