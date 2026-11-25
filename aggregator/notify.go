package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// notifier is the message channel to the panel. Anything holding NOTIFY_TOKEN can POST a note
// ("Claude wants your attention"); the panel pops it up with an OK button and keeps a history.
// Read state lives here, so a panel reboot doesn't re-announce old notes. The ring of keepNotes is
// saved to NOTES_FILE (a Docker volume) on every change, so it survives restarts; with no file set
// it lives in memory only.
type notifier struct {
	mu    sync.Mutex
	next  int    // last id handed out; seeded from the clock so ids keep rising across restarts
	notes []note // oldest first
	file  string
	clock func() time.Time // nil = time.Now
}

type note struct {
	ID   int    `json:"id"`
	T    int64  `json:"t"`   // unix seconds
	Src  string `json:"src"` // who sent it, e.g. "Claude"
	Msg  string `json:"msg"`
	Lvl  string `json:"lvl"`           // info | warn | alert
	Act  string `json:"act,omitempty"` // panel takeover, e.g. "wx" (tornado)
	Read bool   `json:"r,omitempty"`
}

const (
	keepNotes  = 20
	panelNotes = 5  // newest notes in each panel document
	noteMax    = 88 // bytes
	noteSrcMax = 12
)

func newNotifier(file string) *notifier {
	n := &notifier{next: int(time.Now().Unix()), file: file}
	if file == "" {
		return n
	}
	b, err := os.ReadFile(file)
	if err == nil {
		err = json.Unmarshal(b, &n.notes)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("notes: can't load %s: %v (starting empty)", file, err)
	}
	for _, nt := range n.notes {
		n.next = max(n.next, nt.ID)
	}
	return n
}

// save writes the ring atomically (temp file + rename). Called with mu held.
func (n *notifier) save() {
	if n.file == "" {
		return
	}
	b, _ := json.Marshal(n.notes)
	tmp := n.file + ".tmp"
	err := os.WriteFile(tmp, b, 0o600)
	if err == nil {
		err = os.Rename(tmp, n.file)
	}
	if err != nil {
		log.Printf("notes: can't save %s: %v", n.file, err)
	}
}

func (n *notifier) Section() string         { return "notify" }
func (n *notifier) Key() string             { return "notes" }
func (n *notifier) Interval() time.Duration { return time.Second } // a memory copy; kicked on change anyway

// Collect hands the panel the newest panelNotes, newest first.
func (n *notifier) Collect(context.Context) (any, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := []note{}
	for i := len(n.notes) - 1; i >= 0 && len(out) < panelNotes; i-- {
		out = append(out, n.notes[i])
	}
	return out, nil
}

// now is the live clock unless a demo command pinned one (clock).
func (n *notifier) now() time.Time {
	if n.clock != nil {
		return n.clock()
	}
	return time.Now()
}

func (n *notifier) add(src, msg, lvl, act string) note {
	n.mu.Lock()
	defer n.mu.Unlock()
	now := n.now().Unix()
	n.next = max(n.next+1, int(now))
	nt := note{ID: n.next, T: now, Src: trunc(src, noteSrcMax), Msg: trunc(msg, noteMax), Lvl: lvl, Act: act}
	n.notes = append(n.notes, nt)
	if len(n.notes) > keepNotes {
		n.notes = n.notes[len(n.notes)-keepNotes:]
	}
	n.save()
	return nt
}

// ntfyNote maps an ntfy publish (https://docs.ntfy.sh/publish/) to a note, so anything that can
// notify an ntfy server can notify the panel. Two forms are accepted:
//   - POST/PUT /<topic> with the message as the body (Title / Priority headers, or their X- and
//     short forms)
//   - POST / with JSON {"topic", "message", "title", "priority"}
//
// Priority 5 (max/urgent) is red, 4 (high) amber, anything else blue.
func ntfyNote(topic string, h http.Header, body []byte) (src, msg, lvl string) {
	first := func(names ...string) string {
		for _, n := range names {
			if v := h.Get(n); v != "" {
				return v
			}
		}
		return ""
	}
	title, prio := first("Title", "X-Title", "T"), first("Priority", "X-Priority", "Prio", "P")
	msg = strings.TrimSpace(string(body))
	if topic == "" {
		var in struct {
			Topic, Message, Title string
			Priority              int
		}
		if json.Unmarshal(body, &in) == nil {
			topic, msg, title, prio = in.Topic, strings.TrimSpace(in.Message), in.Title, fmt.Sprint(in.Priority)
		}
	}
	if title != "" {
		msg = strings.TrimSuffix(title+": "+msg, ": ")
	}
	switch strings.ToLower(prio) {
	case "5", "max", "urgent":
		lvl = "alert"
	case "4", "high":
		lvl = "warn"
	default:
		lvl = "info"
	}
	return topic, msg, lvl
}

// alertmanagerNotes maps an Alertmanager webhook delivery to notes. Grafana's webhook contact point
// sends the same shape. Firing alerts are amber, or red when their severity label says critical;
// resolved ones are blue. At most 5 notes per delivery, so an alert storm can't wipe the history.
func alertmanagerNotes(body []byte) ([][3]string, error) {
	var in struct {
		Alerts []struct {
			Status      string
			Labels      map[string]string
			Annotations map[string]string
		}
	}
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, err
	}
	var out [][3]string
	for _, a := range in.Alerts {
		if len(out) == 5 {
			break
		}
		name := a.Labels["alertname"]
		text := cmp.Or(a.Annotations["summary"], a.Annotations["description"], name, "alert")
		if inst := a.Labels["instance"]; inst != "" && !strings.Contains(text, inst) {
			text += " (" + inst + ")"
		}
		lvl := "warn"
		switch strings.ToLower(a.Labels["severity"]) {
		case "critical", "page", "error", "high", "disaster":
			lvl = "alert"
		case "info", "none", "low":
			lvl = "info"
		}
		if a.Status == "resolved" {
			text, lvl = "Resolved: "+text, "info"
		}
		out = append(out, [3]string{cmp.Or(name, "Alertmanager"), text, lvl})
	}
	return out, nil
}

// markRead marks every note up to and including id as read (the panel's OK / opening its log).
func (n *notifier) markRead(id int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for i := range n.notes {
		if n.notes[i].ID <= id {
			n.notes[i].Read = true
		}
	}
	n.save()
}
