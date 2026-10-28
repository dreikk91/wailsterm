package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCopyTree(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	os.MkdirAll(filepath.Join(src, "a", "b"), 0o755)
	os.WriteFile(filepath.Join(src, "a", "x.txt"), []byte("hello"), 0o644)
	os.WriteFile(filepath.Join(src, "a", "b", "y.txt"), []byte("world!"), 0o644)

	var p Progress
	if p.Total = treeSize(localFS{}, filepath.Join(src, "a")); p.Total != 11 {
		t.Fatalf("treeSize = %d, want 11", p.Total)
	}
	if err := copyTree(localFS{}, localFS{}, filepath.Join(src, "a"), filepath.Join(dst, "a"), &p, func(bool) {}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dst, "a", "b", "y.txt")); string(got) != "world!" {
		t.Errorf("copied content = %q", got)
	}
	if p.Done != 11 {
		t.Errorf("done = %d, want 11", p.Done)
	}
	if got := pathBase(`C:\Users\me\dir`); got != "dir" {
		t.Errorf("pathBase = %q", got)
	}
}
