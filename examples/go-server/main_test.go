package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadFilesSkipsSymlinks(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".gemini"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".gemini", "creds.json"), []byte("c"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("s"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(dir, "link")); err != nil {
		t.Skip("symlinks unsupported:", err)
	}
	got, err := readFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[".gemini/creds.json"] != "c" {
		t.Errorf("readFiles = %v", got)
	}
	if none, err := readFiles(""); err != nil || none != nil {
		t.Errorf("readFiles(\"\") = %v, %v", none, err)
	}
}
