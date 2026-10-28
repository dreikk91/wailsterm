package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
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
