package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baby-hero-404/aicli/sandbox"
)

func noRuntime() (sandbox.Runtime, error) { return sandbox.NewStubRuntime(), nil }

func TestCommands(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "agy", "1.4.10", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	do := func(args ...string) (string, error) {
		var b strings.Builder
		err := run(context.Background(), append([]string{"--root", root}, args...), &b, noRuntime)
		return b.String(), err
	}
	if out, err := do("list"); err != nil || !strings.Contains(out, "agy     1.4.10") || !strings.Contains(out, "claude  (none") {
		t.Errorf("list = %q, %v", out, err)
	}
	if out, err := do("use", "agy", "1.4.10"); err != nil || out != "current agy 1.4.10\n" {
		t.Errorf("use = %q, %v", out, err)
	}
	if out, _ := do("list"); !strings.Contains(out, "*1.4.10") {
		t.Errorf("list after use = %q", out)
	}
	if _, err := do("remove", "agy", "1.4.10"); err == nil {
		t.Error("removing the current version succeeded")
	}
	if out, err := do("reset", "agy"); err != nil || !strings.Contains(out, "cleared") {
		t.Errorf("reset = %q, %v", out, err)
	}
	if out, _ := do("list"); !strings.Contains(out, "(no current: the image's CLI is used)") {
		t.Errorf("list after reset = %q", out)
	}
	if _, err := do("use", "agy", "1.4.10"); err != nil {
		t.Fatal(err)
	}
	// install of an already installed version needs no container
	if out, err := do("install", "agy", "1.4.10"); err != nil || out != "installed agy 1.4.10\n" {
		t.Errorf("install = %q, %v", out, err)
	}
	for _, bad := range [][]string{{}, {"nope"}, {"use", "agy"}, {"use", "agy", "9.9"}, {"install"}, {"remove", "agy"}, {"use", "bash", "1"}, {"reset"}, {"reset", "bash"}} {
		if _, err := do(bad...); err == nil {
			t.Errorf("%v succeeded", bad)
		}
	}
	if err := run(context.Background(), []string{"list"}, &strings.Builder{}, noRuntime); err == nil && os.Getenv("AICLI_TOOLS_ROOT") == "" {
		t.Error("missing --root accepted")
	}
}
