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
