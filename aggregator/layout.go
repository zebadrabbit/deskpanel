package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const maxJSONSources = 64

// headerEnvRe: a JSON source may only read CARD_* variables (ruling J2), so a
// hand-edited layout can't send PANEL_TOKEN or the TrueNAS key to a URL.
var headerEnvRe = regexp.MustCompile(`^CARD_[A-Z0-9_]{1,40}$`)

// urlIDRe: a light or profile card's id goes into the path of the panel's requests (/api/v1/lights/<id>/toggle).
var urlIDRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// lookupEnv is swapped in tests.
var lookupEnv = os.LookupEnv

// generalProblem is what's wrong with a general card's unit, min, max, warn, bad and below, given its parsed value,
// or "" (parseLayout and the editor's Test both ask).
func (c Card) generalProblem(specs []valueSpec) string {
	switch {
	case len(c.Unit) > maxUnitLen:
		return fmt.Sprintf("unit over %d bytes", maxUnitLen)
	// fields a card would silently ignore are mistakes: say so
	case c.Type == "status" && (c.Warn != nil || c.Bad != nil):
		return "status takes no thresholds"
	case (c.Type == "gauge" || c.Type == "bar") && (c.Min != nil || c.Max != nil) && specs[0].Shortcut != "":
		return "min/max need a prometheus value" // a shortcut has its own scale
	case c.Min != nil && c.Max != nil && *c.Min >= *c.Max:
		return "min must be below max"
	case c.Warn != nil && c.Bad != nil && !c.Below && *c.Warn > *c.Bad:
		return "warn must not be above bad"
	case c.Warn != nil && c.Bad != nil && c.Below && *c.Warn < *c.Bad:
		return "warn must not be below bad"
	}
	return ""
}

// headerEnvProblem is what's wrong with a JSON source's header_env ("" is none), or "".
func headerEnvProblem(e string) string {
	if e == "" {
		return ""
	}
	if !headerEnvRe.MatchString(e) {
		return "header_env must be CARD_ then A-Z, 0-9, _"
	}
	if v, ok := lookupEnv(e); !ok || v == "" {
		return "header_env unset: " + e // the name last: the 88-byte cut only shortens it
	}
	return ""
}

// Layout is the owner-edited card layout: each tab is a 4 col x 6 row grid.
type Layout struct {
	Tour *Tour `json:"tour,omitempty"`
	Tabs []Tab `json:"tabs"`
}

// Tour is the optional "tour" block of layout.json: when the panel idles, how long it shows each stop.
// Every key is optional; tourDoc fills the defaults.
type Tour struct {
	On      *bool    `json:"on,omitempty"`
	IdleMin *int     `json:"idle_min,omitempty"`
	ClockS  *int     `json:"clock_s,omitempty"`
	TabS    *int     `json:"tab_s,omitempty"`
	Clock   *bool    `json:"clock,omitempty"`
	Skip    []string `json:"skip,omitempty"` // tab names
}

// docTour is the tour as served: always complete, skip as 0-based tab indexes.
type docTour struct {
	On     bool  `json:"on"`
	IdleS  int   `json:"idle_s"`
	ClockS int   `json:"clock_s"`
	TabS   int   `json:"tab_s"`
	Clock  bool  `json:"clock"`
	Skip   []int `json:"skip"`
}

// tourDefault is the tour where layout.json says nothing (today's: 5 min alone, 60 s on the clock, 20 s a tab),
// in layout.json's terms.
var tourDefault = struct {
	On      bool `json:"on"`
	IdleMin int  `json:"idle_min"`
	ClockS  int  `json:"clock_s"`
	TabS    int  `json:"tab_s"`
	Clock   bool `json:"clock"`
}{true, 5, 60, 20, true}

// tourDoc is l's tour with tourDefault filled in.
func tourDoc(l Layout) docTour {
	td := tourDefault
	d := docTour{On: td.On, IdleS: td.IdleMin * 60, ClockS: td.ClockS, TabS: td.TabS, Clock: td.Clock, Skip: []int{}}
	t := l.Tour
	if t == nil {
		return d
	}
	if t.On != nil {
		d.On = *t.On
	}
	if t.IdleMin != nil {
		d.IdleS = *t.IdleMin * 60
	}
	if t.ClockS != nil {
		d.ClockS = *t.ClockS
	}
	if t.TabS != nil {
		d.TabS = *t.TabS
	}
	if t.Clock != nil {
		d.Clock = *t.Clock
	}
	for i, tab := range l.Tabs {
		if contains(t.Skip, tab.Name) {
			d.Skip = append(d.Skip, i)
		}
	}
	return d
}

// checkTour returns the problem with l's tour block, or "".
func checkTour(l Layout) string {
	t := l.Tour
	if t == nil {
		return ""
	}
	if t.IdleMin != nil && (*t.IdleMin < 1 || *t.IdleMin > 120) {
		return fmt.Sprintf("tour idle_min %d: 1 to 120", *t.IdleMin)
	}
	if t.ClockS != nil && (*t.ClockS < 5 || *t.ClockS > 600) {
		return fmt.Sprintf("tour clock_s %d: 5 to 600", *t.ClockS)
	}
	if t.TabS != nil && (*t.TabS < 5 || *t.TabS > 600) {
		return fmt.Sprintf("tour tab_s %d: 5 to 600", *t.TabS)
	}
	seen := map[string]bool{}
	for _, n := range t.Skip {
		switch {
		case !slices.ContainsFunc(l.Tabs, func(tab Tab) bool { return tab.Name == n }):
			return fmt.Sprintf("tour skips no tab '%s'", short(n))
		case seen[n]:
			return fmt.Sprintf("tour skips '%s' twice", short(n))
		}
		seen[n] = true
	}
	if d := tourDoc(l); d.On && !d.Clock && len(d.Skip) == len(l.Tabs) {
		return "tour has nothing to show"
	}
	return ""
}

// tourLine is the line of the root "tour" key, 1 if there is none.
func tourLine(data []byte) int {
	dec := json.NewDecoder(bytes.NewReader(data))
	depth, wantKey := 0, false
	for {
		tok, err := dec.Token()
		if err != nil {
			return 1
		}
		if d, ok := tok.(json.Delim); ok {
			if d == '{' || d == '[' {
				depth++
			} else {
				depth--
			}
			wantKey = depth == 1 // the root object's next token is a key
			continue
		}
		if depth == 1 {
			if s, ok := tok.(string); ok && wantKey && strings.EqualFold(s, "tour") { // as json.Unmarshal matches it
				return lineAt(data, int(dec.InputOffset())-len(`"tour"`))
			}
			wantKey = !wantKey
		}
	}
}

type Tab struct {
	Name  string `json:"name"`
	Cards []Card `json:"cards"`
}

type Card struct {
	ID    string   `json:"id"`
	Type  string   `json:"type"`
	At    [2]int   `json:"at"`   // col,row 1-based
	Size  cardSize `json:"size"` // "WxH" in layout.json; served as [w,h]
	Src   string   `json:"src,omitempty"`
	Title string   `json:"title,omitempty"`
	Tap   string   `json:"tap,omitempty"`

	// General cards (number, gauge, bar, graph, status).
	Value json.RawMessage `json:"value,omitempty"`
	Unit  string          `json:"unit,omitempty"`
	Warn  *float64        `json:"warn,omitempty"`
	Bad   *float64        `json:"bad,omitempty"`
	Min   *float64        `json:"min,omitempty"`
	Max   *float64        `json:"max,omitempty"`
	Below bool            `json:"below,omitempty"`

	Slots *gpuSlots `json:"slots,omitempty"` // gpu cards only

	parsed []valueSpec // filled by parseLayout
}

// gpuSlots picks what a GPU card shows; every part is optional.
type gpuSlots struct {
	Gauge string    `json:"gauge,omitempty"`
	Line1 *slotLine `json:"line1,omitempty"`
	As    string    `json:"as,omitempty"` // not a layout.json key (as goes inside line1); kept only to reject it
	Line2 []string  `json:"line2,omitzero"`
}

type slotLine struct {
	Value string `json:"value"`
	As    string `json:"as"`
}

var (
	gaugeSlots = []string{"temp", "util", "vram", "fan"}
	line1Slots = []string{"temp", "util", "vram", "power", "fan"}
	asSlots    = []string{"bar", "graph", "text"} // how line 1 is drawn
	noBarSlots = []string{"power"}                // line 1 values with no bar (no scale)
	line2Slots = []string{"temp", "util", "vram", "power", "procs", "fan"}
	// slotDefault is what a gpu card shows where its slots say nothing.
	slotDefault = docSlots{Gauge: "temp", Line1: "vram", As: "bar", Line2: []string{"util", "power"}}
)

// slots is the GPU card's slot choice with the defaults filled in.
func (c Card) slots() (gauge, line1, as string, line2 []string) {
	gauge, line1, as, line2 = slotDefault.Gauge, slotDefault.Line1, slotDefault.As, slices.Clone(slotDefault.Line2)
	if s := c.Slots; s != nil {
		if s.Gauge != "" {
			gauge = s.Gauge
		}
		if s.Line1 != nil {
			if s.Line1.Value != "" {
				line1 = s.Line1.Value
			}
			if s.Line1.As != "" {
				as = s.Line1.As
			}

		}
		if s.Line2 != nil {
			line2 = s.Line2
		}
	}
	return
}

// usesFan: some slot shows the fan, which the card reads from its cards-document entry (J7), not /panel.
func (c Card) usesFan() bool {
	gauge, line1, _, line2 := c.slots()
	return gauge == "fan" || line1 == "fan" || contains(line2, "fan")
}

// specs is the parsed value; only set after parseLayout succeeded (nil for hand-built cards).
func (c Card) specs() []valueSpec { return c.parsed }

// checkSlots returns the problem with c's slots, or "".
func (c Card) checkSlots() string {
	gauge, line1, as, line2 := c.slots()
	all := line2Slots
	if c.Slots.As != "" {
		return "slot as: goes inside line1"
	}
	switch {
	case !contains(all, gauge):
		return fmt.Sprintf("slot gauge: unknown value '%s'", short(gauge))
	case !contains(gaugeSlots, gauge):
		return fmt.Sprintf("slot gauge: %s can't be a gauge", gauge)
	case !contains(all, line1):
		return fmt.Sprintf("slot line1: unknown value '%s'", short(line1))
	case !contains(asSlots, as):
		return "slot line1: as must be bar, graph or text"
	case !contains(line1Slots, line1) || contains(noBarSlots, line1) && as == "bar":
		return fmt.Sprintf("slot line1: %s can't be a %s", line1, as)
	case len(line2) > 2:
		return "slot line2: at most 2 values"
	}
	for _, v := range line2 {
		if !contains(all, v) {
			return fmt.Sprintf("slot line2: unknown value '%s'", short(v))
		}
	}
	return ""
}

const (
	gridCols, gridRows = 4, 6
	maxTabs            = 6
	maxCardsPerTab     = 24
	maxErrBytes        = 88 // panel notification limit
	// maxLayoutDoc is the panel's layout buffer, lx_body[12 * 1024] in firmware/main/net.c. It keeps a body only while
	// it is shorter than that (on_http: len + data_len < cap; a byte stays free for the terminator), and logs a longer
	// one as "too big, unreadable". Keep in sync with net.c.
	maxLayoutDoc = 12 * 1024

	// Firmware storage limits (firmware/main/layout.h: name[16], id[24], src/title[28], tap[24], minus the NUL).
	// The firmware truncates silently, so longer values are rejected here. Keep in sync with layout.h.
	maxTabName = 15
	maxIDLen   = 23
	maxSrcLen  = 27
	maxTitle   = 27
	maxTapLen  = 23
)

// cardSizes is the catalogue: card type -> designed sizes.
var cardSizes = map[string][]string{
	"gpu":        {"2x3", "2x2", "1x3", "1x2", "1x1"},
	"pool":       {"1x2", "1x1", "2x2"},
	"nas-alerts": {"1x2", "1x1"},
	"dots":       {"4x1", "2x1", "4x2"},
	"board":      {"4x6"},
	"radar":      {"4x6"},
	"number":     {"1x1", "1x2", "2x2"},
	"gauge":      {"1x2", "2x3"},
	"bar":        {"1x1", "1x2", "2x1"},
	"graph":      {"2x2", "2x3", "4x2", "4x3"},
	"status":     {"1x1", "2x1"},
	"light":      {"1x1", "1x2", "2x1", "2x2"},
	"profile":    {"1x1", "2x1"},
}

var (
	// typeSrc is what a card's src names, by type: a GPU, a pool or a dots choice; the other types take none.
	typeSrc     = map[string]string{"gpu": "gpu", "pool": "pool", "dots": "dots"}
	dotsSrcs    = []string{"tailscale", "services", "both"} // a dots card's src; none is both
	tapNames    = []string{"notifications", "alert-details", "none"}
	ownTapTypes = []string{"board", "radar", "light", "profile"} // cards with their own touch handling: no tap
)

const maxUnitLen = 8

type layoutError struct {
	Line int
	Msg  string
}

// Error is capped at 88 bytes so it fits a panel notification.
func (e *layoutError) Error() string {
	s := fmt.Sprintf("layout.json line %d: %s", e.Line, e.Msg)
	if len(s) > maxErrBytes {
		s = s[:maxErrBytes]
		for !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
	}
	return s
}

// cardSize reads "WxH" or [w,h] (the served form); anything else is kept as text so
// validation reports it with the right line.
type cardSize string

func (s *cardSize) UnmarshalJSON(b []byte) error {
	var str string
	var wh []int
	if json.Unmarshal(b, &str) == nil {
		*s = cardSize(str)
	} else if json.Unmarshal(b, &wh) == nil && len(wh) == 2 {
		*s = cardSize(fmt.Sprintf("%dx%d", wh[0], wh[1]))
	} else {
		*s = cardSize(bytes.TrimSpace(b))
	}
	return nil
}

func (c Card) wh() (w, h int) {
	fmt.Sscanf(string(c.Size), "%dx%d", &w, &h)
	return
}

func lineAt(data []byte, off int) int {
	if off > len(data) {
		off = len(data)
	}
	if off < 0 {
		off = 0
	}
	return bytes.Count(data[:off], []byte{'\n'}) + 1
}

// objectLines walks the tokens and returns the start line of every tab
// object and of every card object (per tab), in file order.
func objectLines(data []byte) (tabs []int, cards [][]int) {
	const (
		other = iota
		root
		tabsArr
		tabObj
		cardsArr
		cardObj
	)
	type frame struct {
		role    int
		obj     bool
		key     string
		wantKey bool
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	var st []frame
	for {
		tok, err := dec.Token()
		if err != nil { // EOF or syntax (already reported)
			return
		}
		var top *frame
		if len(st) > 0 {
			top = &st[len(st)-1]
		}
		d, isDelim := tok.(json.Delim)
		switch {
		case isDelim && (d == '{' || d == '['):
			role := other
			switch {
			case top == nil:
				role = root
			case top.role == root && top.key == "tabs" && d == '[':
				role = tabsArr
			case top.role == tabsArr && d == '{':
				role = tabObj
				tabs = append(tabs, lineAt(data, int(dec.InputOffset())-1))
				cards = append(cards, nil)
			case top.role == tabObj && top.key == "cards" && d == '[':
				role = cardsArr
			case top.role == cardsArr && d == '{':
				role = cardObj
				cards[len(cards)-1] = append(cards[len(cards)-1], lineAt(data, int(dec.InputOffset())-1))
			}
			st = append(st, frame{role: role, obj: d == '{', wantKey: d == '{'})
		case isDelim:
			st = st[:len(st)-1]
			if len(st) > 0 && st[len(st)-1].obj {
				st[len(st)-1].wantKey = true
			}
		case top != nil && top.obj:
			if top.wantKey {
				top.key, _ = tok.(string)
				top.wantKey = false
			} else {
				top.wantKey = true
			}
		}
	}
}

// short trims s to 16 bytes (rune-safe, with an ellipsis) for error messages.
func short(s string) string {
	if len(s) <= 16 {
		return s
	}
	s = s[:15]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s + "…"
}

func parseLayout(data []byte) (Layout, error) {
	var l Layout
	// Windows editors over SMB: a UTF-8 BOM is fine, UTF-16 (PowerShell 5.1, Notepad's "Unicode") is not.
	if bytes.HasPrefix(data, []byte{0xFF, 0xFE}) || bytes.HasPrefix(data, []byte{0xFE, 0xFF}) {
		return l, &layoutError{1, "save as UTF-8"}
	}
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	if err := json.Unmarshal(data, &l); err != nil {
		var se *json.SyntaxError
		var te *json.UnmarshalTypeError
		switch {
		case errors.As(err, &se):
			return l, &layoutError{lineAt(data, int(se.Offset)-1), "bad JSON: " + se.Error()}
		case errors.As(err, &te):
			return l, &layoutError{lineAt(data, int(te.Offset)-1), "bad value for " + te.Field}
		}
		return l, &layoutError{1, "bad JSON"}
	}
	tabLines, cardLines := objectLines(data)
	tl := func(i int) int {
		if i < len(tabLines) {
			return tabLines[i]
		}
		return 1
	}
	cl := func(i, j int) int {
		if i < len(cardLines) && j < len(cardLines[i]) {
			return cardLines[i][j]
		}
		return tl(i)
	}

	if len(l.Tabs) < 1 || len(l.Tabs) > maxTabs {
		return l, &layoutError{tl(maxTabs), fmt.Sprintf("need 1-%d tabs, got %d", maxTabs, len(l.Tabs))}
	}
	names := map[string]bool{}
	for i, t := range l.Tabs {
		if t.Name == "" || names[t.Name] {
			return l, &layoutError{tl(i), fmt.Sprintf("tab %d name empty or duplicate", i+1)}
		}
		if len(t.Name) > maxTabName {
			return l, &layoutError{tl(i), fmt.Sprintf("tab %d name over %d bytes", i+1, maxTabName)}
		}
		names[t.Name] = true
	}

	ids := map[string]bool{}
	jsonKeys := map[string]bool{}
	nLights := 0
	for i, t := range l.Tabs {
		if len(t.Cards) > maxCardsPerTab {
			return l, &layoutError{cl(i, maxCardsPerTab), fmt.Sprintf("tab '%s' has over %d cards", short(t.Name), maxCardsPerTab)}
		}
		grid := map[[2]int]string{}
		for j, c := range t.Cards {
			fail := func(f string, a ...any) (Layout, error) {
				return l, &layoutError{cl(i, j), fmt.Sprintf("'%s' ", short(c.ID)) + fmt.Sprintf(f, a...)}
			}
			if c.ID == "" || ids[c.ID] {
				return fail("duplicate or empty id (tab %s)", short(t.Name))
			}
			ids[c.ID] = true
			if len(c.ID) > maxIDLen || len(c.Src) > maxSrcLen || len(c.Title) > maxTitle || len(c.Tap) > maxTapLen {
				return fail("id/src/title/tap over %d/%d/%d/%d bytes", maxIDLen, maxSrcLen, maxTitle, maxTapLen)
			}
			sizes, ok := cardSizes[c.Type]
			if !ok {
				return fail("unknown type '%s'", short(c.Type))
			}
			if !contains(sizes, string(c.Size)) {
				return fail("size %s not allowed for %s", short(string(c.Size)), c.Type)
			}
			w, h := c.wh()
			if w < 1 || h < 1 || w > gridCols || h > gridRows || c.At[0] < 1 || c.At[1] < 1 || c.At[0] > gridCols || c.At[1] > gridRows || c.At[0]+w-1 > gridCols || c.At[1]+h-1 > gridRows {
				return fail("outside the %dx%d grid", gridCols, gridRows)
			}
			switch {
			case contains(generalTypes, c.Type):
				if c.Slots != nil {
					return fail("takes no slots")
				}
				if len(bytes.TrimSpace(c.Value)) == 0 {
					return fail("needs a value")
				}
				specs, err := parseValue(c.Value, c.Type)
				if err != nil {
					return fail("value: %s", err)
				}
				for _, sp := range specs {
					if sp.JSON == nil {
						continue
					}
					if msg := headerEnvProblem(sp.JSON.HeaderEnv); msg != "" {
						return fail("%s", msg)
					}
					jsonKeys[sp.JSON.key()] = true
					if len(jsonKeys) > maxJSONSources {
						return fail("makes over %d JSON sources", maxJSONSources)
					}
				}
				l.Tabs[i].Cards[j].parsed = specs
				if msg := c.generalProblem(specs); msg != "" {
					return fail("%s", msg)
				}
				if c.Src != "" {
					return fail("takes no src")
				}
			default:
				if (c.Type == "light" || c.Type == "profile") && (!urlIDRe.MatchString(c.ID) || strings.Trim(c.ID, ".") == "") { // the panel puts this id in a request path ("." and ".." aren't one)
					return l, &layoutError{cl(i, j), fmt.Sprintf("id '%s': letters, digits, - _ . only", c.ID)}
				}
				switch {
				case c.Type == "light":
					if _, ok := c.lightEntity(); !ok {
						return fail(`light takes {"ha": "light.x"} or a switch`)
					}
					if nLights++; nLights > maxLights { // each is one Home Assistant request every 5 s
						return fail("at most %d light cards", maxLights)
					}
				case len(c.Value) > 0:
					return fail("takes no value")
				case c.Type == "profile" && c.Title == "":
					return fail("profile needs a title")
				}
				if (c.Type == "light" || c.Type == "profile") && c.Src != "" {
					return fail("takes no src")
				}
				if c.Unit != "" || c.Warn != nil || c.Bad != nil || c.Min != nil || c.Max != nil || c.Below {
					return fail("takes no unit, thresholds or min/max")
				}
				if c.Slots != nil {
					if c.Type != "gpu" {
						return fail("takes no slots")
					}
					if msg := c.checkSlots(); msg != "" {
						return fail("%s", msg)
					}
				}
			}
			if c.Type == "dots" && c.Src != "" && !contains(dotsSrcs, c.Src) {
				return fail("src must be tailscale, services or both")
			}
			if tabName, isTab := strings.CutPrefix(c.Tap, "tab:"); isTab {
				if !names[tabName] {
					return fail("tap: no tab '%s'", short(tabName))
				}
			} else if c.Tap != "" && !contains(tapNames, c.Tap) {
				return fail("unknown tap '%s'", short(c.Tap))
			}
			if contains(ownTapTypes, c.Type) && c.Tap != "" && c.Tap != "none" {
				return fail("a %s card takes no tap (it has its own)", c.Type)
			}
			if c.Size == "4x6" && j > 0 {
				return fail("4x6 card must be alone on its tab")
			}
			if j > 0 && t.Cards[0].Size == "4x6" {
				return fail("tab has a 4x6 card; must be alone")
			}
			for col := c.At[0]; col < c.At[0]+w; col++ {
				for row := c.At[1]; row < c.At[1]+h; row++ {
					if other, taken := grid[[2]int{col, row}]; taken {
						return fail("overlaps '%s' at col %d row %d", short(other), col, row)
					}
					grid[[2]int{col, row}] = c.ID
				}
			}
		}
	}
	if msg := checkTour(l); msg != "" {
		return l, &layoutError{tourLine(data), msg}
	}
	if n := len(encodeLayoutDoc(l)); n >= maxLayoutDoc { // the panel's own test: a body is kept while len < its buffer
		return l, &layoutError{1, fmt.Sprintf("too big for the panel (%d bytes, at most %d): use fewer cards", n, maxLayoutDoc-1)}
	}
	return l, nil
}

// cutBytes cuts s to at most n bytes on a rune boundary.
func cutBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// defaultTap is a card's tap when the file gives none: the NAS alerts card opens its alerts.
func defaultTap(typ string) string {
	if typ == "nas-alerts" {
		return "alert-details"
	}
	return "none"
}

// The served document: size as [w,h], tap always present (the type's default if unset), title/src only when set.
type docCard struct {
	ID    string    `json:"id"`
	Type  string    `json:"type"`
	At    [2]int    `json:"at"`
	Size  [2]int    `json:"size"`
	Src   string    `json:"src,omitempty"`
	Title string    `json:"title,omitempty"`
	Tap   string    `json:"tap"`
	Slots *docSlots `json:"slots,omitempty"` // gpu cards only
}

// docSlots is a gpu card's slots as served: always complete.
type docSlots struct {
	Gauge string   `json:"gauge"`
	Line1 string   `json:"line1"`
	As    string   `json:"as"`
	Line2 []string `json:"line2"`
}

type docTab struct {
	Name  string    `json:"name"`
	Cards []docCard `json:"cards"`
}

type docLayout struct {
	Tour docTour  `json:"tour"`
	Tabs []docTab `json:"tabs"`
}

func layoutDoc(l Layout) docLayout {
	d := docLayout{Tour: tourDoc(l), Tabs: []docTab{}}
	for _, t := range l.Tabs {
		dt := docTab{Name: t.Name, Cards: []docCard{}}
		for _, c := range t.Cards {
			w, h := c.wh()
			tap := c.Tap
			if tap == "" {
				tap = defaultTap(c.Type)
			}
			src := c.Src
			if c.Type == "pool" { // the panel gets pool names cut the TrueNAS collector's way: cut the src to match
				src = trunc(src, poolNameMx)
			}
			var ds *docSlots
			if c.Type == "gpu" {
				g, l1, as, l2 := c.slots()
				ds = &docSlots{g, l1, as, append([]string{}, l2...)}
			}
			dt.Cards = append(dt.Cards, docCard{c.ID, c.Type, c.At, [2]int{w, h}, src, c.Title, tap, ds})
		}
		d.Tabs = append(d.Tabs, dt)
	}
	return d
}

func encodeLayoutDoc(l Layout) []byte {
	b, _ := json.Marshal(layoutDoc(l))
	return b
}

// layoutWait caps how long the default waits for the GPU and TrueNAS collectors' first runs (each run
// times out at 20 s).
const layoutWait = 25 * time.Second

// layoutStore holds the live layout: the file if it is valid, else the generated default.
type layoutStore struct {
	path  string
	post  func(src, msg, lvl string)
	start time.Time        // when the store was made, for layoutWait
	clock func() time.Time // time.Now; tests pin it

	mu       sync.Mutex // refresh (ticker) vs doc (handlers)
	cur      []byte     // served document
	lay      Layout     // the layout cur was made from
	fromFile bool
	// seen: mtime+size of the file version last looked at (good or bad). ponytail: two saves of the same
	// size within one mtime tick (a few ms on Linux) look like one version and the second is missed; hash
	// the content if a tool ever saves that fast.
	seen    string
	pending string // version seen once, not yet parsed
	missing int    // consecutive checks with the file absent
}

func newLayoutStore(path string, post func(src, msg, lvl string)) *layoutStore {
	return &layoutStore{path: path, post: post, start: time.Now(), clock: time.Now}
}

// doc is the served document; nil until there is one (the route answers 503).
func (s *layoutStore) doc() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur
}

// layout is the layout being served (file or default); zero before the first.
func (s *layoutStore) layout() Layout {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lay
}

// refresh re-stats the file: a new version is loaded (a bad one keeps the last good layout and
// posts one note); with no file in use the default follows f, once f is no longer Waiting (or after
// layoutWait). The very first load skips the wait for a second look. Reports whether doc() changed.
func (s *layoutStore) refresh(f found) (changed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	set := func(l Layout, fromFile bool) {
		b := encodeLayoutDoc(l)
		// a new value query changes nothing in the served document, but the cards need to know
		changed = !bytes.Equal(b, s.cur) || !reflect.DeepEqual(l, s.lay)
		s.cur, s.lay, s.fromFile = b, l, fromFile
	}
	fi, err := os.Stat(s.path)
	switch {
	case err == nil:
		s.missing = 0
		key := fmt.Sprintf("%d.%d", fi.ModTime().UnixNano(), fi.Size())
		switch {
		case key == s.seen:
		case fi.Size() == 0:
			s.pending = "" // never parse an empty (mid-save) file
		case key != s.pending && s.cur != nil:
			s.pending = key // first sighting: wait to see it unchanged (not half-written)
		default: // seen twice unchanged, or nothing served yet (just started: no layout on screen to protect)
			data, rerr := os.ReadFile(s.path)
			if rerr != nil {
				break // retry next tick
			}
			s.seen, s.pending = key, ""
			l, perr := parseLayout(data)
			if perr != nil {
				log.Printf("layout %s: %v", s.path, perr)
				s.post("deskpanel", perr.Error(), "warn")
				break
			}
			set(l, true)
			return
		}
	case os.IsNotExist(err):
		// A rename-save or delete-then-create can leave the file absent for a tick: keep the file
		// layout until it has been missing 3 checks in a row, then reset to the generated default.
		s.pending = ""
		if s.fromFile {
			if s.missing++; s.missing < 3 {
				return
			}
		}
		s.seen, s.missing = "", 0
		s.fromFile = false
	}
	// Until the GPU and TrueNAS collectors have each run once, the default would be a short one the panel
	// rebuilds twice over (and saves); keep what's served, or nothing (503), instead.
	if !s.fromFile && (!f.Waiting || s.clock().Sub(s.start) >= layoutWait) {
		set(defaultLayout(f), false)
	}
	return
}

func addLayoutRoute(mux *http.ServeMux, token string, s *layoutStore) {
	mux.HandleFunc("GET /api/v1/layout", auth(token, func(w http.ResponseWriter, r *http.Request) {
		body := s.doc()
		if body == nil { // just started, the collectors haven't run: the panel keeps what it shows
			http.Error(w, "layout not ready yet", http.StatusServiceUnavailable)
			return
		}
		serveETag(w, r, body)
	}))
}

// demoLayouts are the documents `deskpanel demo-layout <dir>` writes for the simulator.
// To add one, add an entry here.
func demoLayouts() map[string]Layout {
	var slots []*slot
	for _, c := range demoSources(time.Unix(0, 0)) {
		d, _ := c.Collect(context.Background())
		slots = append(slots, &slot{c: c, data: d})
	}
	f := foundSources(slots)
	withGPUs := func(g ...string) Layout { x := f; x.GPUs = g; return defaultLayout(x) }
	def := defaultLayout(f)
	card := func(id, typ string, col, row int, size, src string) Card { // tap: the type's default
		return Card{ID: id, Type: typ, At: [2]int{col, row}, Size: cardSize(size), Src: src}
	}
	custom := defaultLayout(f)
	custom.Tabs[0].Cards = []Card{
		card("gpu-workstation", "gpu", 1, 1, "1x2", "workstation"),
		card("pool-tank", "pool", 2, 1, "2x2", "tank"),
		card("nas-alerts", "nas-alerts", 4, 1, "1x1", ""),
		card("dots", "dots", 1, 3, "2x1", "both"),
		card("pool-tnak", "pool", 1, 4, "1x1", "tnak"),
	}
	// sizes: every Homelab card type at every designed size, for GPU hosts g and pools p. The 2x3 GPU card's
	// tap goes to the second tab (the simulator taps its bar: a tap anywhere on a card must reach it).
	sizesFor := func(g, p [2]string) Layout {
		l := Layout{Tabs: []Tab{
			{Name: "Sizes", Cards: []Card{
				card("gpu-2x3", "gpu", 1, 1, "2x3", g[0]),
				card("gpu-1x3", "gpu", 3, 1, "1x3", g[1]),
				card("gpu-1x2", "gpu", 4, 1, "1x2", g[0]),
				card("gpu-1x1", "gpu", 4, 3, "1x1", g[1]),
				card("gpu-2x2", "gpu", 1, 4, "2x2", g[1]),
				card("pool-2x2", "pool", 3, 4, "2x2", p[0]),
				card("pool-1x1", "pool", 1, 6, "1x1", p[1]),
				card("alerts-1x1", "nas-alerts", 2, 6, "1x1", ""),
				card("dots-2x1", "dots", 3, 6, "2x1", "both"),
			}},
			{Name: "Sizes 2", Cards: []Card{
				card("pool-1x2", "pool", 1, 1, "1x2", p[0]),
				card("alerts-1x2", "nas-alerts", 2, 1, "1x2", ""),
				card("pool-2x2-b", "pool", 3, 1, "2x2", p[1]),
				card("dots-4x1", "dots", 1, 3, "4x1", "both"),
				card("dots-4x2", "dots", 1, 4, "4x2", "both"),
				card("dots-2x1-services", "dots", 1, 6, "2x1", "services"),
				card("dots-2x1-tailscale", "dots", 3, 6, "2x1", "tailscale"),
			}},
		}}
		l.Tabs[0].Cards[0].Tap = "tab:Sizes 2"
		return l
	}
	// longnames: names as long as a real setup's (the simulator renames its demo data to match).
	long := found{GPUs: []string{"studio", "storage"}, Pools: []string{"media-vault1", "archive-4tb-02", "boot-pool"}, TrueNAS: true}
	ls := map[string]Layout{
		"default": def,
		"gpu1":    withGPUs("workstation"),
		"gpu4":    withGPUs("a", "b", "c", "d"),
		"gpu0":    withGPUs(),
		"custom":  custom,
		"noradar": {Tabs: def.Tabs[:2]},
		// radar-moved: the radar card on a tab of another name, at another place.
		"radar-moved": {Tabs: []Tab{def.Tabs[0], {Name: "Radar", Cards: def.Tabs[2].Cards}, def.Tabs[1]}},
		"sizes":       sizesFor([2]string{"workstation", "server"}, [2]string{"tank", "backup"}),
		// The same layouts for longer names.
		"longnames":       defaultLayout(long),
		"sizes-longnames": sizesFor([2]string{"studio", "storage"}, [2]string{"media-vault1", "archive-4tb-02"}),
	}
	for n, l := range demoGeneralLayouts(def) {
		ls[n] = l
	}
	return ls
}

// demoGeneralLayouts are the demo layouts of the general cards and the GPU graph slots.
// Ids of cards that get a cards.json entry are unique across all demo layouts (demo-cards merges them).
func demoGeneralLayouts(def Layout) map[string]Layout {
	num := func(x float64) *float64 { return &x }
	gen := func(id, typ string, col, row int, size, title, value string) Card {
		return Card{ID: id, Type: typ, At: [2]int{col, row}, Size: cardSize(size), Title: title, Value: json.RawMessage(value)}
	}
	up := `{"prometheus":"up{job=\"prometheus\"}"}`
	down := `{"prometheus":"up{job=\"jellyfin\"}"}`

	// server: the approved mockup, beside the default tabs.
	cpu := gen("pl-cpu", "graph", 1, 1, "2x3", "server CPU", `"cpu"`)
	cpu.Unit, cpu.Warn, cpu.Bad, cpu.Tap = "%", num(80), num(95), "none"
	server := Tab{Name: "server", Cards: []Card{
		cpu,
		gen("pl-temp", "gauge", 3, 1, "1x2", "Temp", `"temp"`),
		gen("pl-load", "number", 4, 1, "1x2", "Load", `"load"`),
		gen("pl-up", "number", 3, 3, "1x1", "Up", `"uptime"`),
		gen("pl-prom", "status", 4, 3, "1x1", "Prometheus", up),
		gen("pl-ram", "bar", 1, 4, "1x2", "RAM", `"ram"`),
		gen("pl-disk", "bar", 2, 4, "1x2", "Disk /", `"disk:/"`),
		gen("pl-net", "graph", 3, 4, "2x2", "Network", `"net"`),
		{ID: "pl-dots", Type: "dots", At: [2]int{1, 6}, Size: "4x1", Src: "both"},
	}}

	// general-sizes: every general type at every designed size (a tab holds 24 cells).
	sizes := Layout{Tabs: []Tab{
		{Name: "Gen numbers", Cards: []Card{
			gen("gs-num-2x2", "number", 1, 1, "2x2", "CPU", `"cpu"`),
			gen("gs-num-1x2", "number", 3, 1, "1x2", "Load", `"load"`),
			gen("gs-num-1x1", "number", 4, 1, "1x1", "Up", `"uptime"`),
			gen("gs-st-1x1", "status", 4, 2, "1x1", "Prometheus", up),
			gen("gs-gauge-1x2", "gauge", 1, 3, "1x2", "Temp", `"temp"`),
			gen("gs-gauge-2x3", "gauge", 2, 3, "2x3", "CPU", `"cpu"`),
			gen("gs-bar-1x1", "bar", 4, 3, "1x1", "RAM", `"ram"`),
			gen("gs-bar-1x2", "bar", 4, 4, "1x2", "Disk /", `"disk:/"`),
			gen("gs-bar-2x1", "bar", 1, 6, "2x1", "RAM", `"ram"`),
			gen("gs-st-2x1", "status", 3, 6, "2x1", "Jellyfin", down),
		}},
		{Name: "Gen graphs", Cards: []Card{
			gen("gs-graph-4x3", "graph", 1, 1, "4x3", "CPU", `"cpu"`),
			gen("gs-graph-2x2", "graph", 1, 4, "2x2", "Network", `"net"`),
			gen("gs-graph-2x3", "graph", 3, 4, "2x3", "Temp", `"temp"`),
		}},
		{Name: "Gen graphs 2", Cards: []Card{
			gen("gs-graph-4x2", "graph", 1, 1, "4x2", "RAM", `"ram"`),
		}},
	}}

	// general-long: Review Focus 5, the longest title, unit and value (15 bytes) at every 1x1 and 1x2 size.
	longTitle, longQ := "Quarterly Throughput Report", `{"prometheus":"`+demoLongQ+`"}`
	long := func(id, typ string, col, row int, size string) Card {
		c := gen(id, typ, col, row, size, longTitle, longQ)
		c.Unit = "bits/sec"
		if typ != "number" {
			c.Min, c.Max = num(-200000), num(0)
		}
		return c
	}
	longL := Layout{Tabs: []Tab{{Name: "Gen long", Cards: []Card{
		long("gl-num-1x1", "number", 1, 1, "1x1"),
		long("gl-num-1x2", "number", 2, 1, "1x2"),
		long("gl-bar-1x1", "bar", 3, 1, "1x1"),
		long("gl-bar-1x2", "bar", 4, 1, "1x2"),
		long("gl-gauge-1x2", "gauge", 1, 3, "1x2"),
		gen("gl-st-1x1", "status", 2, 3, "1x1", longTitle, up),
		// a selector matching two series: a none card with a reason (the simulator makes it the longest, 23 bytes)
		gen("gl-many-1x1", "number", 3, 3, "1x1", longTitle, `"cpu@many:9100"`),
		gen("gl-many-1x2", "bar", 4, 3, "1x2", longTitle, `"ram@many:9100"`),
	}}}}

	// gpu-slots: the default card beside graph and text slots, and the two sizes that have a graph slot.
	gpu := func(id string, col, row int, size, src string, s *gpuSlots) Card {
		return Card{ID: id, Type: "gpu", At: [2]int{col, row}, Size: cardSize(size), Src: src, Slots: s}
	}
	slots := Layout{Tabs: []Tab{
		{Name: "GPU slots", Cards: []Card{
			gpu("gp-default", 1, 1, "2x3", "workstation", nil),
			gpu("gp-vram-graph", 3, 1, "2x3", "workstation", &gpuSlots{Line1: &slotLine{Value: "vram", As: "graph"}}),
			gpu("gp-power-text", 1, 4, "2x3", "server", &gpuSlots{Line1: &slotLine{Value: "power", As: "text"}, Line2: []string{"temp", "procs"}}),
			gpu("gp-2x2-graph", 3, 4, "2x2", "workstation", &gpuSlots{Line1: &slotLine{Value: "util", As: "graph"}}),
		}},
		{Name: "GPU slots 2", Cards: []Card{
			gpu("gp-1x3-graph", 1, 1, "1x3", "server", &gpuSlots{Line1: &slotLine{Value: "temp", As: "graph"}}),
		}},
	}}

	// gpu-fan (J7): the fan as line 1's text, bar and graph, as the gauge and in line 2, at every size, for a
	// spinning fan (workstation), one at 0 % (server, zero-RPM) and one with no reading (nas: the simulator
	// adds this GPU; the demo has no fan for it, so its entry reads "--", as a passively cooled GPU's does).
	fs := func(gauge, line1, as string, line2 ...string) *gpuSlots {
		s := &gpuSlots{Gauge: gauge, Line2: line2}
		if line1 != "" {
			s.Line1 = &slotLine{Value: line1, As: as}
		}
		return s
	}
	fan := Layout{Tabs: []Tab{
		{Name: "GPU fan", Cards: []Card{
			gpu("gf-ws-graph", 1, 1, "2x3", "workstation", fs("fan", "fan", "graph", "temp", "util")),
			gpu("gf-srv-bar", 3, 1, "2x3", "server", fs("", "fan", "bar", "temp", "power")),
			gpu("gf-nas-text", 1, 4, "2x3", "nas", fs("util", "fan", "text", "fan", "procs")),
			gpu("gf-ws-vram", 3, 4, "2x2", "workstation", fs("fan", "vram", "graph", "fan")), // another value's graph
			gpu("gf-srv-1x1", 3, 6, "1x1", "server", fs("fan", "", "")),
			gpu("gf-nas-1x1", 4, 6, "1x1", "nas", fs("fan", "", "")),
		}},
		{Name: "GPU fan 2", Cards: []Card{
			gpu("gf-ws-1x3", 1, 1, "1x3", "workstation", fs("fan", "fan", "graph")),
			gpu("gf-srv-1x3", 2, 1, "1x3", "server", fs("fan", "fan", "bar")),
			gpu("gf-ws-1x2", 3, 1, "1x2", "workstation", fs("", "fan", "bar")),
			gpu("gf-nas-1x2", 4, 1, "1x2", "nas", fs("fan", "fan", "text")),
			gpu("gf-srv-1x2", 3, 3, "1x2", "server", fs("fan", "fan", "graph")), // a 1x2 draws a bar
			gpu("gf-ws-1x1", 4, 3, "1x1", "workstation", fs("fan", "", "")),
			gpu("gf-ws-2x2", 1, 5, "2x2", "workstation", fs("util", "fan", "text", "temp", "power")),
			gpu("gf-srv-2x2", 3, 5, "2x2", "server", fs("fan", "fan", "graph")),
		}},
	}}

	// json-demo (Stage 3): an app's status JSON as a number with a unit, a status from a string, and a graph.
	// The demo answers any json: key with a wave, and a "status" path with the word "running".
	jv := func(path string) string {
		return `{"json":{"url":"http://app.lan/api/status","path":"` + path + `"}}`
	}
	jn := gen("js-queue", "number", 1, 1, "2x2", "Queue", jv("queue.depth"))
	jn.Unit = "jobs"
	jg := gen("js-latency", "graph", 1, 3, "2x2", "Latency", jv("stats.latency_ms"))
	jg.Unit = "ms"
	jsonDemo := Layout{Tabs: []Tab{{Name: "JSON", Cards: []Card{
		jn, jg,
		gen("js-app", "status", 3, 1, "2x1", "App", jv("status")),
		gen("js-gauge", "gauge", 3, 2, "1x2", "Cache", jv("cache.hit_pct")),
		gen("js-bar", "bar", 4, 2, "1x2", "Disk", jv("disk.used_pct")),
	}}}}

	// ha-demo: Home Assistant entities (the demo answers an ha: key from demoHA).
	hv := func(entity string) string { return `{"ha":"` + entity + `"}` }
	hl := gen("ha-light", "bar", 4, 1, "1x1", "Kitchen", `{"ha":"light.kitchen","attr":"brightness"}`)
	hl.Min, hl.Max = num(0), num(255)
	haDemo := Layout{Tabs: []Tab{{Name: "Home", Cards: []Card{
		gen("ha-temp", "number", 1, 1, "2x2", "Living room", hv("sensor.living_room_temperature")),
		gen("ha-humid", "gauge", 3, 1, "1x2", "Humidity", hv("sensor.living_room_humidity")),
		hl,
		gen("ha-power", "graph", 1, 3, "2x2", "Washer", hv("sensor.washer_power")),
		gen("ha-tv", "status", 3, 3, "2x1", "TV", hv("media_player.living_room_tv")),
	}}}}

	// lights-demo: a profile button per moment of the day, the three demo lights (demoLights: a colour light, one
	// that only dims, a switch) at two of the light sizes (a 2x2, two 1x2 side by side), and a wide profile. demoProfiles saves two of the five.
	prof := func(id string, col, row int, size, title string) Card {
		return Card{ID: id, Type: "profile", At: [2]int{col, row}, Size: cardSize(size), Title: title}
	}
	lightsDemo := Layout{Tabs: []Tab{{Name: "Lights", Cards: []Card{
		prof("lt-morning", 1, 1, "1x1", "Morning"),
		prof("lt-afternoon", 2, 1, "1x1", "Afternoon"),
		prof("lt-evening", 3, 1, "1x1", "Evening"),
		prof("lt-late", 4, 1, "1x1", "Late night"),
		gen("lt-desk", "light", 1, 2, "2x2", "Desk", hv("light.desk")),
		gen("lt-shelf", "light", 3, 2, "1x2", "Shelf", hv("light.shelf")),
		gen("lt-fan", "light", 4, 2, "1x2", "Fan", hv("switch.fan")),
		prof("lt-all-off", 1, 4, "2x1", "All off"),
	}}}}

	return map[string]Layout{
		"lights-demo":   lightsDemo,
		"ha-demo":       haDemo,
		"json-demo":     jsonDemo,
		"server":        {Tabs: append(slices.Clone(def.Tabs), server)},
		"general-sizes": sizes,
		"general-long":  longL,
		"gpu-slots":     slots,
		"gpu-fan":       fan,
	}
}

func writeDemoLayouts(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	ls := demoLayouts()
	names := make([]string, 0, len(ls))
	for n := range ls {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		b, err := json.MarshalIndent(layoutDoc(ls[n]), "", "  ")
		if err != nil {
			return err
		}
		if _, err := parseLayout(handLayout(ls[n])); err != nil { // the file form; TestDemoLayouts checks the served shape
			return fmt.Errorf("%s: %w", n, err)
		}
		if err := os.WriteFile(filepath.Join(dir, n+".json"), append(b, '\n'), 0o644); err != nil {
			return err
		}
	}
	return nil
}
