package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestEditorOffWhenTokenIsAnotherToken(t *testing.T) {
	old := lookupEnv
	defer func() { lookupEnv = old }()
	for _, name := range []string{"PANEL_TOKEN", "NOTIFY_TOKEN"} {
		lookupEnv = func(k string) (string, bool) {
			if k == name {
				return edTok, true
			}
			return "", false
		}
		e := newEd(t, edTok)
		if res, _ := e.do("GET", "/api/v1/editor/ping", edTok, nil, ""); res.StatusCode != 404 {
			t.Errorf("EDITOR_TOKEN = %s: %d, want 404", name, res.StatusCode)
		}
	}
}

func TestEditorWaitStopsWithRequest(t *testing.T) {
	e := newEditor(newLayoutStore("x", nil))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	e.wait(ctx, 5*time.Second)
	if time.Since(start) > time.Second {
		t.Error("wait ignored the cancelled request")
	}
}

// "line2": [] means show nothing; it must survive a round trip, on disk and through the editor.
func TestEditorEmptyLine2Survives(t *testing.T) {
	const src = `{"tabs":[{"name":"Home","cards":[
 {"id":"gpu-1","type":"gpu","at":[1,1],"size":"2x3","src":"workstation","slots":{"line2":[]}}]}]}`
	l, err := parseLayout([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	l2, err := parseLayout(handLayout(l))
	if err != nil || !reflect.DeepEqual(l, l2) || l2.Tabs[0].Cards[0].Slots.Line2 == nil {
		t.Errorf("round trip lost line2: %v %+v", err, l2.Tabs[0].Cards[0].Slots)
	}
	e := newEd(t, edTok)
	if res, body := e.put(`"none"`, src); res.StatusCode != 200 {
		t.Fatalf("%d %s", res.StatusCode, body)
	}
	res, body := e.get("/api/v1/editor/layout")
	var got struct{ Layout json.RawMessage }
	json.Unmarshal([]byte(body), &got)
	if !strings.Contains(string(got.Layout), `"line2":[]`) {
		t.Errorf("GET lost line2: %s", got.Layout)
	}
	if res, body := e.put(res.Header.Get("ETag"), string(got.Layout)); res.StatusCode != 200 {
		t.Fatalf("%d %s", res.StatusCode, body)
	}
	if now, _ := os.ReadFile(e.path); !strings.Contains(string(now), `"line2": []`) {
		t.Errorf("save lost line2:\n%s", now)
	}
}

func TestEditorSaveKeepsModeAndOwner(t *testing.T) {
	e := newEd(t, edTok)
	os.WriteFile(e.path, []byte(edLayout), 0o640)
	os.Chmod(e.path, 0o640)
	type own struct {
		path     string
		uid, gid int
	}
	var calls []own
	e.e.chown = func(p string, uid, gid int) error {
		calls = append(calls, own{p, uid, gid})
		return errors.New("not root")
	}
	res, _ := e.get("/api/v1/editor/layout")
	if res, body := e.put(res.Header.Get("ETag"), strings.Replace(edLayout, `"tank"`, `"backup"`, 1)); res.StatusCode != 200 {
		t.Fatalf("a failing chown must be ignored: %d %s", res.StatusCode, body)
	}
	{
		for _, p := range []string{e.path, e.path + ".bak"} {
			if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != 0o640 {
				t.Errorf("%s: %v %v, want 0640", p, fi, err)
			}
		}
	}
	if fi, _ := os.Stat(e.path); fi != nil {
		if uid, gid, ok := fileOwner(fi); ok {
			if len(calls) < 2 || calls[0].uid != uid || calls[0].gid != gid {
				t.Errorf("chown calls %v, want the old file's %d:%d", calls, uid, gid)
			}
		}
	}
	// a brand-new file: 0644
	os.Remove(e.path)
	os.Remove(e.path + ".bak")
	res, _ = e.get("/api/v1/editor/layout")
	e.put(res.Header.Get("ETag"), edLayout)
	if fi, err := os.Stat(e.path); err != nil || (fi.Mode().Perm() != 0o644) {
		t.Errorf("new file: %v %v, want 0644", fi, err)
	}
}

// .bak goes through; .tmp fails half-way.
func TestEditorSaveTmpFails(t *testing.T) {
	e := newEd(t, edTok)
	e.put(`"none"`, edLayout)
	before, _ := os.ReadFile(e.path)
	res, _ := e.get("/api/v1/editor/layout")
	e.e.write = func(path string, data []byte) error {
		if path != e.path+".tmp" {
			return writeSynced(path, data)
		}
		os.WriteFile(path, data[:5], 0o644)
		return &os.PathError{Op: "write", Path: path, Err: errors.New("no space left on device")}
	}
	res, body := e.put(res.Header.Get("ETag"), strings.Replace(edLayout, `"tank"`, `"backup"`, 1))
	if res.StatusCode != 500 || !strings.Contains(body, "without :ro") {
		t.Errorf("%d %s", res.StatusCode, body)
	}
	if now, _ := os.ReadFile(e.path); !bytes.Equal(now, before) {
		t.Error("file changed")
	}
	if bak, _ := os.ReadFile(e.path + ".bak"); !bytes.Equal(bak, before) {
		t.Error(".bak is not the old file")
	}
	if _, err := os.Stat(e.path + ".tmp"); err == nil {
		t.Error(".tmp left")
	}
}

func TestEditorSaveRenameFails(t *testing.T) {
	e := newEd(t, edTok)
	e.put(`"none"`, edLayout)
	before, _ := os.ReadFile(e.path)
	res, _ := e.get("/api/v1/editor/layout")
	e.e.rename = func(from, to string) error {
		if to == e.path {
			return &os.LinkError{Op: "rename", Old: from, New: to, Err: errors.New("device busy")}
		}
		return os.Rename(from, to)
	}
	res, body := e.put(res.Header.Get("ETag"), strings.Replace(edLayout, `"tank"`, `"backup"`, 1))
	if res.StatusCode != 500 || !strings.Contains(body, "device busy") || !strings.Contains(body, "without :ro") {
		t.Errorf("%d %s", res.StatusCode, body)
	}
	if now, _ := os.ReadFile(e.path); !bytes.Equal(now, before) {
		t.Error("file changed")
	}
	if _, err := os.Stat(e.path + ".tmp"); err == nil {
		t.Error(".tmp left")
	}
}

// A failed backup keeps the previous good .bak.
func TestEditorBackupFailKeepsOldBak(t *testing.T) {
	e := newEd(t, edTok)
	e.put(`"none"`, edLayout)
	os.WriteFile(e.path+".bak", []byte("previous good backup"), 0o644)
	res, _ := e.get("/api/v1/editor/layout")
	e.e.write = func(path string, data []byte) error {
		if strings.HasPrefix(path, e.path+".bak") {
			os.WriteFile(path, data[:5], 0o644)
			return errors.New("disk full")
		}
		return writeSynced(path, data)
	}
	res, _ = e.put(res.Header.Get("ETag"), strings.Replace(edLayout, `"tank"`, `"backup"`, 1))
	if res.StatusCode != 500 {
		t.Errorf("%d", res.StatusCode)
	}
	if bak, _ := os.ReadFile(e.path + ".bak"); string(bak) != "previous good backup" {
		t.Errorf(".bak = %q", bak)
	}
	if m, _ := filepath.Glob(e.path + "*"); len(m) != 2 {
		t.Errorf("leftovers: %v", m)
	}
}

// A hand edit landing during the save is not overwritten.
func TestEditorSaveRacesHandEdit(t *testing.T) {
	e := newEd(t, edTok)
	e.put(`"none"`, edLayout)
	res, _ := e.get("/api/v1/editor/layout")
	hand := []byte(strings.Replace(edLayout, `"tank"`, `"hand"`, 1))
	e.e.write = func(path string, data []byte) error {
		if path == e.path+".tmp" {
			os.WriteFile(e.path, hand, 0o644) // the owner saves in their editor right now
		}
		return writeSynced(path, data)
	}
	res, body := e.put(res.Header.Get("ETag"), strings.Replace(edLayout, `"tank"`, `"backup"`, 1))
	if res.StatusCode != 412 {
		t.Errorf("%d %s", res.StatusCode, body)
	}
	if now, _ := os.ReadFile(e.path); !bytes.Equal(now, hand) {
		t.Error("hand edit overwritten")
	}
	if _, err := os.Stat(e.path + ".tmp"); err == nil {
		t.Error(".tmp left")
	}
}

func TestEditorLoadNotReady(t *testing.T) {
	st, path, _ := storeFor(t)
	e := newEd(t, edTok)
	e.e.store, e.e.path = st, path // never refreshed: nothing served yet
	res, body := e.get("/api/v1/editor/layout")
	if res.StatusCode != 503 || !strings.Contains(body, "layout not ready yet") {
		t.Errorf("%d %s", res.StatusCode, body)
	}
}

func TestEditorSaveUnknownField(t *testing.T) {
	e := newEd(t, edTok)
	res, body := e.put(`"none"`, strings.Replace(edLayout, `"src":"tank"`, `"src":"tank","colour":"red"`, 1))
	if res.StatusCode != 422 || !strings.Contains(body, "colour") || !strings.Contains(body, `"tab":-1`) {
		t.Errorf("%d %s", res.StatusCode, body)
	}
	if _, err := os.Stat(e.path); err == nil {
		t.Error("file written")
	}
}
