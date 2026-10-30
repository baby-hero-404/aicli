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
	// install of an already installed version needs no container; defaults to activating it
	if out, err := do("install", "agy", "1.4.10"); err != nil || out != "installed agy 1.4.10\ncurrent agy 1.4.10\n" {
		t.Errorf("install = %q, %v", out, err)
	}
	if out, err := do("install", "agy", "1.4.10", "--no-use"); err != nil || out != "installed agy 1.4.10\n" {
		t.Errorf("install --no-use = %q, %v", out, err)
	}
	for _, bad := range [][]string{{}, {"nope"}, {"use", "agy"}, {"use", "agy", "9.9"}, {"install"}, {"remove", "agy"}, {"use", "bash", "1"}, {"reset"}, {"reset", "bash"}} {
		if _, err := do(bad...); err == nil {
			t.Errorf("%v succeeded", bad)
		}
	}
	t.Setenv("AICLI_TOOLS_ROOT", root)
	if err := run(context.Background(), []string{"list"}, &strings.Builder{}, noRuntime); err != nil {
		t.Errorf("list with default root from AICLI_TOOLS_ROOT failed: %v", err)
	}
}

// installRuntime plays the installer containers: resolve prints a version, install drops the
// binary into the mounted staging dir, and the version check prints the version.
type installRuntime struct {
	sandbox.StubRuntime
	failTool string
}

func (r *installRuntime) Run(_ context.Context, req sandbox.CommandRequest) (*sandbox.CommandResult, error) {
	switch req.Env["MODE"] {
	case "resolve":
		return &sandbox.CommandResult{Stdout: "3.4.5\n"}, nil
	case "install":
		if r.failTool != "" && req.Env["BINARY"] == r.failTool {
			return &sandbox.CommandResult{ExitCode: 1, Stderr: "boom"}, nil
		}
		for _, dest := range req.SessionMounts {
			if err := os.MkdirAll(filepath.Join(dest, "bin"), 0o755); err != nil {
				return nil, err
			}
			if err := os.WriteFile(filepath.Join(dest, "bin", req.Env["BINARY"]), []byte("#!/bin/sh\n"), 0o755); err != nil {
				return nil, err
			}
		}
		return &sandbox.CommandResult{}, nil
	}
	return &sandbox.CommandResult{Stdout: "3.4.5\n"}, nil
}

func TestSetupInstallsAndActivatesEveryTool(t *testing.T) {
	root := t.TempDir()
	var out strings.Builder
	rt := func() (sandbox.Runtime, error) { return &installRuntime{}, nil }
	if err := run(context.Background(), []string{"--root", root, "setup"}, &out, rt); err != nil {
		t.Fatalf("setup: %v", err)
	}
	var list strings.Builder
	if err := run(context.Background(), []string{"--root", root, "list"}, &list, noRuntime); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(list.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("list = %q", list.String())
	}
	for i, tool := range []string{"agy", "claude", "codex"} {
		if f := strings.Fields(lines[i]); len(f) != 2 || f[0] != tool || f[1] != "*3.4.5" {
			t.Errorf("%s not installed and current: %q", tool, lines[i])
		}
	}
}

func TestSetupStopsAtTheFirstFailureAndNamesTheTool(t *testing.T) {
	root := t.TempDir()
	rt := func() (sandbox.Runtime, error) { return &installRuntime{failTool: "claude"}, nil }
	err := run(context.Background(), []string{"--root", root, "setup"}, &strings.Builder{}, rt)
	if err == nil || !strings.Contains(err.Error(), "claude") {
		t.Fatalf("err = %v, want a failure naming claude", err)
	}
}

func TestPruneCommand(t *testing.T) {
	root := t.TempDir()
	// Create 4 versions for agy
	for _, v := range []string{"1.0.0", "1.1.0", "1.2.0", "1.3.0"} {
		if err := os.MkdirAll(filepath.Join(root, "agy", v, "bin"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "agy", "current"), []byte("1.3.0"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	// Prune with --keep 2 (should delete 1.0.0 and 1.1.0)
	if err := run(context.Background(), []string{"--root", root, "prune", "--keep", "2"}, &out, noRuntime); err != nil {
		t.Fatalf("prune: %v", err)
	}
	if !strings.Contains(out.String(), "1.0.0, 1.1.0") {
		t.Errorf("output = %q, want pruned 1.0.0, 1.1.0", out.String())
	}

	// Verify only 1.2.0 and 1.3.0 remain
	var list strings.Builder
	if err := run(context.Background(), []string{"--root", root, "list"}, &list, noRuntime); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(list.String(), "1.0.0") || strings.Contains(list.String(), "1.1.0") {
		t.Errorf("pruned versions still in list: %s", list.String())
	}
	if !strings.Contains(list.String(), "1.2.0") || !strings.Contains(list.String(), "*1.3.0") {
		t.Errorf("expected versions missing from list: %s", list.String())
	}
}

func TestRollbackCommand(t *testing.T) {
	root := t.TempDir()
	for _, v := range []string{"1.4.9", "1.4.10"} {
		if err := os.MkdirAll(filepath.Join(root, "agy", v, "bin"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	do := func(args ...string) (string, error) {
		var b strings.Builder
		err := run(context.Background(), append([]string{"--root", root}, args...), &b, noRuntime)
		return b.String(), err
	}
	if _, err := do("rollback", "agy"); err == nil {
		t.Error("rollback with nothing recorded succeeded")
	}
	for _, v := range []string{"1.4.9", "1.4.10"} {
		if _, err := do("use", "agy", v); err != nil {
			t.Fatal(err)
		}
	}
	if out, err := do("rollback", "agy"); err != nil || !strings.Contains(out, "1.4.10 marked bad") || !strings.Contains(out, "current 1.4.9") {
		t.Errorf("rollback = %q, %v", out, err)
	}
	if out, _ := do("list"); !strings.Contains(out, "*1.4.9") {
		t.Errorf("list after rollback = %q", out)
	}
	if _, err := do("rollback"); err == nil {
		t.Error("rollback without a tool succeeded")
	}
}

func TestUnbadCommandAndTheMarkInList(t *testing.T) {
	root := t.TempDir()
	for _, v := range []string{"1.4.9", "1.4.10"} {
		_ = os.MkdirAll(filepath.Join(root, "agy", v, "bin"), 0o755)
	}
	do := func(args ...string) (string, error) {
		var b strings.Builder
		err := run(context.Background(), append([]string{"--root", root}, args...), &b, noRuntime)
		return b.String(), err
	}
	for _, v := range []string{"1.4.9", "1.4.10"} {
		if _, err := do("use", "agy", v); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := do("rollback", "agy"); err != nil {
		t.Fatal(err)
	}
	if out, _ := do("list"); !strings.Contains(out, "1.4.10(bad)") || !strings.Contains(out, "*1.4.9") {
		t.Errorf("list = %q, want 1.4.10(bad) and *1.4.9", out)
	}
	if out, err := do("unbad", "agy", "1.4.10"); err != nil || !strings.Contains(out, "no longer marked bad") {
		t.Errorf("unbad = %q, %v", out, err)
	}
	if out, _ := do("list"); strings.Contains(out, "(bad)") {
		t.Errorf("list after unbad = %q", out)
	}
	for _, bad := range [][]string{{"unbad"}, {"unbad", "agy"}, {"unbad", "nope", "1.0"}} {
		if _, err := do(bad...); err == nil {
			t.Errorf("%v succeeded", bad)
		}
	}
}
