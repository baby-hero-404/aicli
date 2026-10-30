package capture

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baby-hero-404/aicli/sandbox"
)

// fakeRuntime stands in for Docker: it lets a test write files into the session workspace
// as the "container" would, and records the request.
type fakeRuntime struct {
	sandbox.StubRuntime
	onRun func(req sandbox.CommandRequest) error
	got   sandbox.CommandRequest
}

func (f *fakeRuntime) RunInteractive(_ context.Context, req sandbox.CommandRequest, _ io.Reader, _, _ io.Writer) error {
	f.got = req
	if f.onRun != nil {
		return f.onRun(req)
	}
	return nil
}

type fakeTerm struct {
	out bytes.Buffer
	in  io.Reader
}

func (f *fakeTerm) Stdin() io.Reader {
	if f.in != nil {
		return f.in
	}
	return strings.NewReader("")
}
func (f *fakeTerm) Stdout() io.Writer                   { return &f.out }
func (f *fakeTerm) Resize() <-chan sandbox.TerminalSize { return nil }

func TestSession_CollectsFilesAndCleansUp(t *testing.T) {
	var workspace string
	rt := &fakeRuntime{onRun: func(req sandbox.CommandRequest) error {
		workspace = req.Workspace
		if err := os.MkdirAll(filepath.Join(workspace, ".claude"), 0o755); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(workspace, ".claude", ".credentials.json"), []byte(`{"t":1}`), 0o600)
	}}
	s := &Session{Runtime: rt, Env: map[string]string{"FOO": "bar"}}

	res, err := s.Run(context.Background(), &fakeTerm{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := res.Files[".claude/.credentials.json"]; got != `{"t":1}` {
		t.Errorf("credential not collected, files = %v", res.Files)
	}
	if _, err := os.Stat(workspace); !os.IsNotExist(err) {
		t.Errorf("workspace %s should be removed, stat err = %v", workspace, err)
	}
	if rt.got.Env["HOME"] != "/workspace" || rt.got.Env["TERM"] != "xterm" || rt.got.Env["FOO"] != "bar" {
		t.Errorf("env = %v", rt.got.Env)
	}
	if rt.got.NetworkMode != sandbox.NetworkModeBridge || len(rt.got.Command) != 1 || rt.got.Command[0] != "/bin/bash" {
		t.Errorf("request = %+v", rt.got)
	}
}

func TestSession_RunErrors(t *testing.T) {
	tests := []struct {
		name string
		s    *Session
	}{
		{"no runtime", &Session{}},
		{"runtime error", &Session{Runtime: &fakeRuntime{onRun: func(sandbox.CommandRequest) error { return errors.New("boom") }}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := tt.s.Run(context.Background(), &fakeTerm{}); err == nil {
				t.Error("expected error")
			}
		})
	}
}

// A process inside the sandbox controls the workspace contents. A symlink to a host file
// must never be followed, or the host server would read arbitrary files for it.
func TestCollectFiles_IgnoresUnsafeEntries(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "host-secret")
	if err := os.WriteFile(outside, []byte("TOP-SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.WriteFile(filepath.Join(root, "ok.json"), []byte("{}"), 0o600))
	must(os.Symlink(outside, filepath.Join(root, "link")))
	must(os.WriteFile(filepath.Join(root, "big.bin"), make([]byte, MaxFileBytes+1), 0o600))

	files, err := collectFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	if files["ok.json"] != "{}" {
		t.Errorf("regular file missing: %v", files)
	}
	if _, ok := files["link"]; ok {
		t.Error("symlink must not be followed")
	}
	for name, content := range files {
		if strings.Contains(content, "TOP-SECRET") {
			t.Errorf("host file content leaked via %s", name)
		}
	}
	if _, ok := files["big.bin"]; ok {
		t.Error("oversized file must be skipped")
	}
}

func TestCollectFiles_Bounds(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < MaxFiles+10; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("f%04d", i)), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	files, err := collectFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != MaxFiles {
		t.Errorf("collected %d files, want exactly the limit %d", len(files), MaxFiles)
	}
}

func TestSession_SeedsFilesBeforeTheShellStarts(t *testing.T) {
	var seen map[string]string
	rt := &fakeRuntime{onRun: func(req sandbox.CommandRequest) error {
		seen = map[string]string{}
		return filepath.WalkDir(req.Workspace, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			b, _ := os.ReadFile(p)
			rel, _ := filepath.Rel(req.Workspace, p)
			seen[filepath.ToSlash(rel)] = string(b)
			return nil
		})
	}}
	s := &Session{Runtime: rt}
	_, err := s.RunWith(context.Background(), &fakeTerm{}, RunOptions{Files: map[string]string{
		".claude/.credentials.json": `{"t":1}`, ".config/x/y.json": "y",
	}})
	if err != nil {
		t.Fatalf("RunWith: %v", err)
	}
	if seen[".claude/.credentials.json"] != `{"t":1}` || seen[".config/x/y.json"] != "y" || len(seen) != 2 {
		t.Errorf("the shell saw %v", seen)
	}
}

func TestSession_RefusesUnsafeSeedFiles(t *testing.T) {
	big := strings.Repeat("x", MaxFileBytes+1)
	many := map[string]string{}
	for i := 0; i <= MaxFiles; i++ {
		many[fmt.Sprintf("f%d", i)] = "x"
	}
	for name, files := range map[string]map[string]string{
		"traversal":          {"../escape": "x"},
		"deep traversal":     {"a/../../escape": "x"},
		"absolute":           {"/etc/cron.d/x": "x"},
		"empty path":         {"": "x"},
		"the workspace":      {".": "x"},
		"one file too large": {"big": big},
		"too many files":     many,
	} {
		t.Run(name, func(t *testing.T) {
			rt := &fakeRuntime{}
			_, err := (&Session{Runtime: rt}).RunWith(context.Background(), &fakeTerm{}, RunOptions{Files: files})
			if err == nil {
				t.Fatal("unsafe seed accepted")
			}
			if rt.got.Command != nil {
				t.Error("the shell must not start when seeding fails")
			}
		})
	}
	// nothing may have been written outside the workspace
	if _, err := os.Stat(filepath.Join(os.TempDir(), "aicli-capture", "escape")); err == nil {
		t.Error("a seed file escaped the workspace")
	}
}
