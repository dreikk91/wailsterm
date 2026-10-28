package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"
)

func TestCopyTree(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	os.MkdirAll(filepath.Join(src, "a", "b"), 0o755)
	os.WriteFile(filepath.Join(src, "a", "x.txt"), []byte("hello"), 0o644)
	os.WriteFile(filepath.Join(src, "a", "b", "y.txt"), []byte("world!"), 0o644)

	p := &progress{ctx: context.Background(), emit: func(bool) {}}
	if p.total = treeSize(localFS{}, filepath.Join(src, "a")); p.total != 11 {
		t.Fatalf("treeSize = %d, want 11", p.total)
	}
	if err := copyTree(localFS{}, localFS{}, filepath.Join(src, "a"), filepath.Join(dst, "a"), p); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dst, "a", "b", "y.txt")); string(got) != "world!" {
		t.Errorf("copied content = %q", got)
	}
	if p.done.Load() != 11 {
		t.Errorf("done = %d, want 11", p.done.Load())
	}
}

func TestDB(t *testing.T) {
	d, err := NewDB(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	q := func(id string) map[string]any { return map[string]any{"_id": id} }
	d.Action("bookmarks", "insert", []any{[]any{map[string]any{"_id": "a", "host": "h1"}, map[string]any{"_id": "b", "host": "h2"}}})
	d.Action("bookmarks", "update", []any{q("a"), map[string]any{"$set": map[string]any{"host": "h3"}}, map[string]any{"upsert": true}})
	d.Action("bookmarks", "remove", []any{q("b"), map[string]any{}})

	// reopen from disk to check persistence and encryption round-trip
	d2, _ := NewDB(d.dir)
	all, _ := d2.Action("bookmarks", "find", []any{map[string]any{}})
	docs := all.([]map[string]any)
	if len(docs) != 1 || docs[0]["host"] != "h3" {
		t.Fatalf("find = %v", docs)
	}
	if _, err := d2.Action("nope", "find", nil); err == nil {
		t.Error("unknown table accepted")
	}
}

func TestSeedDB(t *testing.T) {
	d, _ := NewDB(t.TempDir())
	// a bookmark saved by a build without seeding must be adopted by the default group
	d.Action("bookmarks", "insert", []any{map[string]any{"_id": "orphan", "host": "h"}})
	if err := seedDB(d, "1.0.0"); err != nil {
		t.Fatal(err)
	}
	g, _ := d.Action("bookmarkGroups", "findOne", []any{map[string]any{"_id": "default"}})
	ids := g.(map[string]any)["bookmarkIds"].([]any)
	found := false
	for _, id := range ids {
		found = found || id == "orphan"
	}
	if !found {
		t.Fatalf("default group ids = %v", ids)
	}
	qc, _ := d.Action("quickCommands", "find", []any{map[string]any{}})
	themes, _ := d.Action("terminalThemes", "find", []any{map[string]any{}})
	if len(qc.([]map[string]any)) < 10 || len(themes.([]map[string]any)) != 2 {
		t.Fatalf("quick commands %d, themes %d", len(qc.([]map[string]any)), len(themes.([]map[string]any)))
	}
	// second run is a no-op
	d.Action("bookmarkGroups", "remove", []any{map[string]any{"_id": "default"}, map[string]any{}})
	seedDB(d, "1.0.0")
	if g, _ := d.Action("bookmarkGroups", "findOne", []any{map[string]any{"_id": "default"}}); g != nil {
		t.Fatal("seed ran twice")
	}
}

func TestTermLog(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.log")
	l, err := openTermLog(p, false)
	if err != nil {
		t.Fatal(err)
	}
	// colors, an OSC title, a progress line rewritten with \r, a backspace, and a split UTF-8 rune
	l.Write([]byte("\x1b[32mok\x1b[0m line\r\n\x1b]0;title\x07progress 10%\rprogress 99%\r\nab\bc\r\nпр"))
	l.Write([]byte("и\xd0"))
	l.Write([]byte("\xb2\n"))
	l.Close()
	got, _ := os.ReadFile(p)
	if want := "ok line\nprogress 99%\nac\nприв\n"; string(got) != want {
		t.Fatalf("log = %q, want %q", got, want)
	}
}

func TestEditorAndWatch(t *testing.T) {
	got, err := parseEditorCommand(`"C:\Program Files\Code\code.exe" --wait -n 'a b'`)
	if err != nil || len(got) != 4 || got[0] != `C:\Program Files\Code\code.exe` || got[3] != "a b" {
		t.Fatalf("parse = %q, %v", got, err)
	}
	if _, err := parseEditorCommand(`vim "x`); err == nil {
		t.Fatal("unmatched quote accepted")
	}

	p := filepath.Join(t.TempDir(), "f.txt")
	os.WriteFile(p, []byte("v1"), 0o644)
	events := make(chan string, 4)
	var w fileWatch
	w.watch(p, func(ev string, args ...any) { events <- ev + ":" + fmt.Sprint(args...) })
	defer w.unwatch()
	time.Sleep(600 * time.Millisecond)
	os.WriteFile(p, []byte("v2"), 0o644)
	os.Chtimes(p, time.Now().Add(time.Second), time.Now().Add(time.Second))
	select {
	case ev := <-events:
		if ev != "file-change:v2" {
			t.Fatalf("event = %q", ev)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no file-change event")
	}
}

func TestSystemFonts(t *testing.T) {
	fonts := systemFonts()
	if runtime.GOOS == "windows" && !slices.Contains(fonts, "Arial") {
		t.Fatalf("Arial missing from %d fonts: %v", len(fonts), fonts[:min(10, len(fonts))])
	}
}
