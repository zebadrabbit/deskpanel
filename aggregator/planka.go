package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// planka shows the boards the deskpanel user is a member of: adding that user to a board is
// how a board gets onto the panel, removing it takes it off. Per board, the most recently
// updated cards (comments bump a card's updatedAt; label changes don't).
type planka struct {
	url, user, pass string
	hc              http.Client
	mu              sync.Mutex
	token           string
}

func (p *planka) Section() string         { return "planka" }
func (p *planka) Key() string             { return "boards" }
func (p *planka) Interval() time.Duration { return 30 * time.Second }

// Caps that keep /api/v1/panel under ~8 KB (panel_test.go checks the worst case).
const (
	maxBoards    = 6
	maxCards     = 3
	maxLabels    = 2
	boardNameMax = 24 // bytes
	cardNameMax  = 56
	listNameMax  = 24
	labelNameMax = 16
)

type pkLabel struct {
	Name  string `json:"n"`
	Color int    `json:"c"` // 0xRRGGBB
}

type pkCard struct {
	ID     string    `json:"id"`
	Name   string    `json:"n"`
	List   string    `json:"l"`
	Due    int64     `json:"due,omitempty"` // unix seconds
	Done   bool      `json:"d,omitempty"`   // already in the board's done list
	Labels []pkLabel `json:"lb,omitempty"`
}

type pkBoard struct {
	ID      string   `json:"id"`
	Name    string   `json:"n"`
	CanDone bool     `json:"can_done"` // board has a done list
	Cards   []pkCard `json:"cards"`
}

// boardResp is the part of GET /api/boards/{id} we use (Planka 2.1.1).
type boardResp struct {
	Item struct {
		ID, Name string
	}
	Included struct {
		Lists []struct {
			ID, Name, Type string // type: active | closed | archive | trash
			Position       float64
		}
		Cards []struct {
			ID, Name, ListID string
			Position         float64
			DueDate          *time.Time
			CreatedAt        time.Time
			UpdatedAt        *time.Time
		}
		Labels []struct {
			ID, Name, Color string
		}
		CardLabels []struct {
			CardID, LabelID string
		}
	}
}

func (p *planka) Collect(ctx context.Context) (any, error) {
	var pr struct {
		Included struct {
			Boards []struct{ ID, Name string }
		}
	}
	if err := p.api(ctx, "GET", "/projects", nil, &pr); err != nil {
		return nil, err
	}
	boards := pr.Included.Boards
	sort.Slice(boards, func(i, j int) bool { return strings.ToLower(boards[i].Name) < strings.ToLower(boards[j].Name) })
	if len(boards) > maxBoards {
		boards = boards[:maxBoards]
	}
	out := []pkBoard{}
	for _, bd := range boards {
		var b boardResp
		if err := p.api(ctx, "GET", "/boards/"+bd.ID, nil, &b); err != nil {
			return nil, err
		}
		out = append(out, summarise(b))
	}
	return out, nil
}

// doneList is the board's first "closed"-type list, else an active list named "Done".
func doneList(b boardResp) string {
	lists := b.Included.Lists
	sort.SliceStable(lists, func(i, j int) bool { return lists[i].Position < lists[j].Position })
	for _, l := range lists {
		if l.Type == "closed" {
			return l.ID
		}
	}
	for _, l := range lists {
		if l.Type == "active" && strings.EqualFold(strings.TrimSpace(l.Name), "done") {
			return l.ID
		}
	}
	return ""
}

func summarise(b boardResp) pkBoard {
	done := doneList(b)
	lists := map[string]string{} // id -> name, shown lists only (not archive/trash)
	for _, l := range b.Included.Lists {
		if l.Type == "active" || l.Type == "closed" {
			lists[l.ID] = l.Name
		}
	}
	labels := map[string]pkLabel{}
	for _, l := range b.Included.Labels {
		labels[l.ID] = pkLabel{trunc(l.Name, labelNameMax), labelColor(l.Color)}
	}
	cardLabels := map[string][]pkLabel{}
	for _, cl := range b.Included.CardLabels {
		if l, ok := labels[cl.LabelID]; ok && len(cardLabels[cl.CardID]) < maxLabels {
			cardLabels[cl.CardID] = append(cardLabels[cl.CardID], l)
		}
	}

	cards := b.Included.Cards[:0:0]
	for _, c := range b.Included.Cards {
		if _, shown := lists[c.ListID]; shown {
			cards = append(cards, c)
		}
	}
	touched := func(i int) time.Time {
		if u := cards[i].UpdatedAt; u != nil {
			return *u
		}
		return cards[i].CreatedAt
	}
	sort.SliceStable(cards, func(i, j int) bool { return touched(i).After(touched(j)) })
	if len(cards) > maxCards {
		cards = cards[:maxCards]
	}

	out := pkBoard{ID: b.Item.ID, Name: trunc(b.Item.Name, boardNameMax), CanDone: done != "", Cards: []pkCard{}}
	for _, c := range cards {
		pc := pkCard{ID: c.ID, Name: trunc(c.Name, cardNameMax), List: trunc(lists[c.ListID], listNameMax),
			Done: done != "" && c.ListID == done, Labels: cardLabels[c.ID]}
		if c.DueDate != nil {
			pc.Due = c.DueDate.Unix()
		}
		out.Cards = append(out.Cards, pc)
	}
	return out
}

// markDone moves a card to the top of its board's done list, so the newest finished card reads
// first. ponytail: halving the top position runs out of float precision after ~1000 straight
// moves without Planka rebalancing; switch to bottom-of-list if that ever bites.
func (p *planka) markDone(ctx context.Context, cardID string) error {
	var c struct {
		Item struct{ BoardID, ListID string }
	}
	if err := p.api(ctx, "GET", "/cards/"+cardID, nil, &c); err != nil {
		return err
	}
	var b boardResp
	if err := p.api(ctx, "GET", "/boards/"+c.Item.BoardID, nil, &b); err != nil {
		return err
	}
	done := doneList(b)
	if done == "" {
		return httpErr{http.StatusConflict, "board has no done list"}
	}
	if c.Item.ListID == done {
		return nil
	}
	top := 0.0
	for _, x := range b.Included.Cards {
		if x.ListID == done && x.Position > 0 && (top == 0 || x.Position < top) {
			top = x.Position
		}
	}
	pos := 65536.0
	if top > 0 {
		pos = top / 2
	}
	return p.api(ctx, "PATCH", "/cards/"+cardID, map[string]any{"listId": done, "position": pos}, nil)
}

// api does one authenticated Planka request, logging in again once if the cached token is
// rejected. It is used by both the collector goroutine and the done handler.
func (p *planka) api(ctx context.Context, method, path string, body, out any) error {
	for retried := false; ; retried = true {
		tok, err := p.login(ctx, retried)
		if err != nil {
			return err
		}
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req, _ := http.NewRequestWithContext(ctx, method, p.url+"/api"+path, rd)
		req.Header.Set("Authorization", "Bearer "+tok)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := p.hc.Do(req)
		if err != nil {
			return err
		}
		if resp.StatusCode == http.StatusUnauthorized && !retried {
			resp.Body.Close()
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			msg, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
			return httpErr{resp.StatusCode, fmt.Sprintf("planka %s %s: %s %s", method, path, resp.Status, msg)}
		}
		if out == nil {
			return nil
		}
		return json.NewDecoder(resp.Body).Decode(out)
	}
}

func (p *planka) login(ctx context.Context, force bool) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.token != "" && !force {
		return p.token, nil
	}
	b, _ := json.Marshal(map[string]string{"emailOrUsername": p.user, "password": p.pass})
	req, _ := http.NewRequestWithContext(ctx, "POST", p.url+"/api/access-tokens", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var r struct{ Item, Message, Step string }
	json.NewDecoder(resp.Body).Decode(&r)
	if resp.StatusCode != http.StatusOK || r.Item == "" {
		return "", fmt.Errorf("planka login as %s: %s %s %s", p.user, resp.Status, r.Message, r.Step)
	}
	p.token = r.Item
	return p.token, nil
}

// labelColor maps Planka 2.1.1 label colour names to RGB, taken from the Planka web client's
// stylesheet. Unknown names (and silver-glint, a gradient there) read as grey.
func labelColor(name string) int {
	if c, ok := labelColors[name]; ok {
		return c
	}
	return 0x8b8680
}

var labelColors = map[string]int{
	"antique-blue": 0x6c99bb, "apricot-red": 0xfc736c, "autumn-leafs": 0xc9b037, "berry-red": 0xe83855,
	"bright-moss": 0x96b352, "coral-green": 0x2b6a6c, "dark-granite": 0x8b8680, "deep-ocean": 0x004c70,
	"desert-sand": 0xfad371, "egg-yellow": 0xf9c423, "french-coast": 0x00b4b1, "fresh-salad": 0xced85e,
	"grey-stone": 0xaab2bd, "gun-metal": 0x4f6573, "lagoon-blue": 0x109dc0, "lavender-fields": 0xb287bd,
	"light-cocoa": 0xa85540, "light-concrete": 0xafb0a4, "light-mud": 0xc7a57a, "light-orange": 0xfdae5f,
	"lilac-eyes": 0x406cbd, "midnight-blue": 0x0a63a0, "modern-green": 0x77ce87, "morning-sky": 0x52b9d5,
	"muddy-grey": 0x69655a, "navy-blue": 0x1d7299, "orange-peel": 0xde692f, "piggy-red": 0xf97394,
	"pink-tulip": 0xe34f7c, "pumpkin-orange": 0xed9223, "red-burgundy": 0xad5f7d, "shady-rust": 0x87564a,
	"sugar-plum": 0x7e86c7, "summer-sky": 0x5d9cec, "sunny-grass": 0xbeca02, "sweet-lilac": 0x975298,
	"tank-green": 0x8aa177, "turquoise-sea": 0x00858a, "wet-moss": 0x4a8753, "wet-rock": 0x83949b,
}
