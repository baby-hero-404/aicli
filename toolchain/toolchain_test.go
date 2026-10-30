package toolchain

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baby-hero-404/aicli/sandbox"
	"github.com/baby-hero-404/aicli/sandbox/sandboxmock"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// fakeInstall makes name@version look installed.
func fakeInstall(t *testing.T, s *Store, name, version string) string {
	t.Helper()
	dir := s.versionDir(name, version)
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestOpen(t *testing.T) {
	if _, err := Open("relative/root"); err == nil {
		t.Error("relative root accepted")
	}
	s := newStore(t)
	b, err := os.ReadFile(s.profilePath())
	if err != nil || !strings.Contains(string(b), ContainerRoot+"/*/bin") {
		t.Errorf("profile script = %q, %v", b, err)
	}

	// Test DefaultRoot, OpenDefault, and Open("")
	t.Setenv("AICLI_TOOLS_ROOT", t.TempDir())
	defStore, err := OpenDefault()
	if err != nil {
		t.Fatalf("OpenDefault failed: %v", err)
	}
	if defStore.Root() != os.Getenv("AICLI_TOOLS_ROOT") {
		t.Errorf("expected root %q, got %q", os.Getenv("AICLI_TOOLS_ROOT"), defStore.Root())
	}

	// Test WrapDefault
	stub := sandbox.NewStubRuntime()
	wrapped, err := WrapDefault(stub)
	if err != nil {
		t.Fatalf("WrapDefault failed: %v", err)
	}
	if wrapped == nil {
		t.Fatal("WrapDefault returned nil")
	}
}

func TestNamesAreValidated(t *testing.T) {
	s := newStore(t)
	for _, v := range []string{"../x", "a/b", "", "latest", "current", ".hidden", strings.Repeat("1", 65), "1.0 ", "1;rm"} {
		if _, err := s.Installed("agy", v); !errors.Is(err, ErrInvalidName) {
			t.Errorf("version %q: %v", v, err)
		}
	}
	if _, err := s.Installed("bash", "1.0"); !errors.Is(err, ErrUnknownTool) {
		t.Errorf("unknown tool: %v", err)
	}
	if _, err := s.Resolve(map[string]string{"../../etc": "1"}); !errors.Is(err, ErrUnknownTool) {
		t.Errorf("unknown pinned tool: %v", err)
	}
	if _, err := s.Resolve(map[string]string{"agy": "../1"}); !errors.Is(err, ErrInvalidName) {
		t.Errorf("bad pinned version: %v", err)
	}
}

func TestUseCurrentRemove(t *testing.T) {
	s := newStore(t)
	if err := s.Use("agy", "1.0.0"); !errors.Is(err, ErrNotInstalled) {
		t.Errorf("Use of a missing version: %v", err)
	}
	fakeInstall(t, s, "agy", "1.0.0")
	fakeInstall(t, s, "agy", "1.1.0")
	if cur, _ := s.Current("agy"); cur != "" {
		t.Errorf("current before Use = %q", cur)
	}
	if err := s.Use("agy", "1.1.0"); err != nil {
		t.Fatal(err)
	}
	if cur, _ := s.Current("agy"); cur != "1.1.0" {
		t.Errorf("current = %q", cur)
	}
	if err := s.Remove("agy", "1.1.0"); !errors.Is(err, ErrInUse) {
		t.Errorf("removing current: %v", err)
	}
	if err := s.Remove("agy", "1.0.0"); err != nil {
		t.Errorf("Remove: %v", err)
	}
	if vs, _ := s.Versions("agy"); len(vs) != 1 || vs[0] != "1.1.0" {
		t.Errorf("versions = %v", vs)
	}
	if err := s.Remove("agy", "1.0.0"); !errors.Is(err, ErrNotInstalled) {
		t.Errorf("removing twice: %v", err)
	}
	// A rollback is just Use of the older version.
	fakeInstall(t, s, "agy", "1.0.0")
	if err := s.Use("agy", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	if cur, _ := s.Current("agy"); cur != "1.0.0" {
		t.Errorf("after rollback current = %q", cur)
	}
}

func TestResolve(t *testing.T) {
	s := newStore(t)
	agy10 := fakeInstall(t, s, "agy", "1.0.0")
	agy11 := fakeInstall(t, s, "agy", "1.1.0")
	claude := fakeInstall(t, s, "claude", "2.1.312")
	if err := s.Use("agy", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	if err := s.Use("claude", "2.1.312"); err != nil {
		t.Fatal(err)
	}

	got, err := s.Resolve(nil)
	if err != nil || got["agy"] != agy10 || got["claude"] != claude || len(got) != 2 {
		t.Errorf("current: %v, %v (codex has no current, so it is left to the image)", got, err)
	}
	got, err = s.Resolve(map[string]string{"agy": "1.1.0"})
	if err != nil || got["agy"] != agy11 || got["claude"] != claude {
		t.Errorf("pinned: %v, %v", got, err)
	}
	if _, err := s.Resolve(map[string]string{"agy": "9.9.9"}); !errors.Is(err, ErrNotInstalled) {
		t.Errorf("pin to a missing version must fail, not fall back: %v", err)
	}
	// current pointing at a version that was deleted behind our back is an error, not a fallback.
	if err := os.RemoveAll(agy10); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Resolve(nil); !errors.Is(err, ErrNotInstalled) {
		t.Errorf("dangling current: %v", err)
	}
}

func TestWrapAddsMountsPathAndEnv(t *testing.T) {
	s := newStore(t)
	agy := fakeInstall(t, s, "agy", "1.4.10")
	claude := fakeInstall(t, s, "claude", "2.1.312")
	_ = s.Use("agy", "1.4.10")
	_ = s.Use("claude", "2.1.312")
	mock := &sandboxmock.Runtime{}
	rt := Wrap(mock, s)

	callerMounts := map[string]string{"/opt/other": "/srv/other"}
	callerEnv := map[string]string{"FOO": "bar"}
	if _, err := rt.Run(context.Background(), sandbox.CommandRequest{Command: []string{"true"}, ReadOnlyMounts: callerMounts, Env: callerEnv}); err != nil {
		t.Fatal(err)
	}
	req := mock.Requests()[0]
	want := map[string]string{
		"/opt/aicli/tools/agy":    agy,
		"/opt/aicli/tools/claude": claude,
		ProfileTarget:             s.profilePath(),
		"/opt/other":              "/srv/other",
	}
	for k, v := range want {
		if req.ReadOnlyMounts[k] != v {
			t.Errorf("mount %s = %q, want %q", k, req.ReadOnlyMounts[k], v)
		}
	}
	if _, ok := req.ReadOnlyMounts["/opt/aicli/tools/codex"]; ok {
		t.Error("codex has no current version and must not be mounted")
	}
	if p := req.Env["PATH"]; p != "/opt/aicli/tools/agy/bin:/opt/aicli/tools/claude/bin:"+defaultPath {
		t.Errorf("PATH = %q", p)
	}
	if req.Env["AGY_CLI_DISABLE_AUTO_UPDATE"] != "1" || req.Env["DISABLE_AUTOUPDATER"] != "1" || req.Env["FOO"] != "bar" {
		t.Errorf("env = %v", req.Env)
	}
	if len(callerMounts) != 1 || len(callerEnv) != 1 {
		t.Error("the caller's maps were modified")
	}
}

func TestWrapRespectsCallerPathAndEnv(t *testing.T) {
	s := newStore(t)
	fakeInstall(t, s, "agy", "1.4.10")
	_ = s.Use("agy", "1.4.10")
	mock := &sandboxmock.Runtime{}
	env := map[string]string{"PATH": "/custom/bin", "AGY_CLI_DISABLE_AUTO_UPDATE": "0"}
	if _, err := Wrap(mock, s).Run(context.Background(), sandbox.CommandRequest{Command: []string{"true"}, Env: env}); err != nil {
		t.Fatal(err)
	}
	got := mock.Requests()[0].Env
	if got["PATH"] != "/opt/aicli/tools/agy/bin:/custom/bin" || got["AGY_CLI_DISABLE_AUTO_UPDATE"] != "0" {
		t.Errorf("env = %v", got)
	}
}

func TestWrapWithNothingInstalledIsAPassthrough(t *testing.T) {
	mock := &sandboxmock.Runtime{}
	in := sandbox.CommandRequest{Command: []string{"true"}}
	if _, err := Wrap(mock, newStore(t)).Run(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if got := mock.Requests()[0]; got.ReadOnlyMounts != nil || got.Env != nil {
		t.Errorf("request changed: %+v", got)
	}
}

func TestWrapErrors(t *testing.T) {
	s := newStore(t)
	fakeInstall(t, s, "agy", "1.4.10")
	_ = s.Use("agy", "1.4.10")
	mock := &sandboxmock.Runtime{}
	rt := Wrap(mock, s)
	ctx := context.Background()
	tests := []struct {
		name string
		req  sandbox.CommandRequest
		want error
	}{
		{"pinned version missing", sandbox.CommandRequest{ToolVersions: map[string]string{"agy": "0.0.1"}}, ErrNotInstalled},
		{"pinned unknown tool", sandbox.CommandRequest{ToolVersions: map[string]string{"gcc": "1"}}, ErrUnknownTool},
		{"caller already mounts the tool path", sandbox.CommandRequest{ReadOnlyMounts: map[string]string{"/opt/aicli/tools/agy": "/elsewhere"}}, nil},
		{"session mount on the profile path", sandbox.CommandRequest{SessionMounts: map[string]string{ProfileTarget: "/elsewhere"}}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.req.Command = []string{"true"}
			_, err := rt.Run(ctx, tt.req)
			if err == nil || (tt.want != nil && !errors.Is(err, tt.want)) {
				t.Errorf("Run err = %v, want %v", err, tt.want)
			}
			if err := rt.RunInteractive(ctx, tt.req, strings.NewReader(""), &strings.Builder{}, &strings.Builder{}); err == nil {
				t.Error("RunInteractive accepted it")
			}
		})
	}
	if n := len(mock.Requests()); n != 0 {
		t.Errorf("%d requests reached the runtime", n)
	}
}

func TestWrapAppliesToInteractiveSessions(t *testing.T) {
	s := newStore(t)
	fakeInstall(t, s, "agy", "1.4.10")
	_ = s.Use("agy", "1.4.10")
	mock := &sandboxmock.Runtime{}
	if err := Wrap(mock, s).RunInteractive(context.Background(), sandbox.CommandRequest{Command: []string{"bash"}}, strings.NewReader(""), &strings.Builder{}, &strings.Builder{}); err != nil {
		t.Fatal(err)
	}
	if mock.Requests()[0].ReadOnlyMounts["/opt/aicli/tools/agy"] == "" {
		t.Error("login sessions must get the tools too")
	}
}

func TestUnuseFallsBackToTheImage(t *testing.T) {
	s := newStore(t)
	fakeInstall(t, s, "agy", "1.4.10")
	if err := s.Unuse("agy"); err != nil {
		t.Errorf("Unuse with no current: %v", err)
	}
	_ = s.Use("agy", "1.4.10")
	if err := s.Unuse("agy"); err != nil {
		t.Fatal(err)
	}
	if cur, _ := s.Current("agy"); cur != "" {
		t.Errorf("current after Unuse = %q", cur)
	}
	if got, _ := s.Resolve(nil); len(got) != 0 {
		t.Errorf("Resolve after Unuse = %v, want nothing mounted", got)
	}
	// Pinned runs still work, and the version can now be removed.
	if got, err := s.Resolve(map[string]string{"agy": "1.4.10"}); err != nil || got["agy"] == "" {
		t.Errorf("pinned after Unuse = %v, %v", got, err)
	}
	if err := s.Remove("agy", "1.4.10"); err != nil {
		t.Errorf("Remove after Unuse: %v", err)
	}
	if err := s.Unuse("bash"); !errors.Is(err, ErrUnknownTool) {
		t.Errorf("Unuse of an unknown tool: %v", err)
	}
	mock := &sandboxmock.Runtime{}
	if _, err := Wrap(mock, s).Run(context.Background(), sandbox.CommandRequest{Command: []string{"true"}}); err != nil {
		t.Fatal(err)
	}
	if m := mock.Requests()[0].ReadOnlyMounts; m != nil {
		t.Errorf("mounts after Unuse = %v", m)
	}
}

// toolchain.Wrap and sandbox.Manager touch disjoint request fields, so they compose in either
// order: Manager sets Image, ExtraCacheMounts and wraps Command in "bash -lc"; Wrap adds
// ReadOnlyMounts and Env.
func TestComposesWithSandboxManagerInEitherOrder(t *testing.T) {
	s := newStore(t)
	fakeInstall(t, s, "agy", "1.4.10")
	_ = s.Use("agy", "1.4.10")
	registry, err := sandbox.NewRegistry(sandbox.Manifest{
		ID: "node", Image: "acme/node:1", Detect: []string{"package.json"},
		Cache: []sandbox.CacheMapping{{Name: "npm", Container: "/home/agent/.npm"}}, Setup: []string{"true"},
	})
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "package.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	cacheRoot := t.TempDir()
	build := map[string]func(sandbox.Runtime) sandbox.Runtime{
		"Manager(Wrap(docker)) (recommended)": func(inner sandbox.Runtime) sandbox.Runtime {
			return sandbox.NewManager(Wrap(inner, s), registry, sandbox.WithCacheRoot(cacheRoot))
		},
		"Wrap(Manager(docker))": func(inner sandbox.Runtime) sandbox.Runtime {
			return Wrap(sandbox.NewManager(inner, registry, sandbox.WithCacheRoot(cacheRoot)), s)
		},
	}
	for name, mk := range build {
		t.Run(name, func(t *testing.T) {
			mock := &sandboxmock.Runtime{}
			if _, err := mk(mock).Run(context.Background(), sandbox.CommandRequest{Workspace: workspace, Command: []string{"agy", "--version"}, CacheScope: "t1"}); err != nil {
				t.Fatal(err)
			}
			req := mock.Requests()[0]
			if req.Image != "acme/node:1" || len(req.ExtraCacheMounts) == 0 || req.Command[0] != "bash" || req.Command[1] != "-lc" {
				t.Errorf("Manager's changes lost: %+v", req)
			}
			if req.ReadOnlyMounts["/opt/aicli/tools/agy"] == "" || req.ReadOnlyMounts[ProfileTarget] == "" || !strings.HasPrefix(req.Env["PATH"], "/opt/aicli/tools/agy/bin:") {
				t.Errorf("toolchain's changes lost: mounts %v env %v", req.ReadOnlyMounts, req.Env)
			}
		})
	}
}

func TestPruneStaging(t *testing.T) {
	s := newStore(t)
	staging := filepath.Join(s.root, ".staging")
	old := filepath.Join(staging, "agy-1.4.9-abandoned")
	fresh := filepath.Join(staging, "agy-1.4.10-in-progress")
	for _, d := range []string{old, fresh} {
		if err := os.MkdirAll(filepath.Join(d, "bin"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	if err := os.Chtimes(old, now.Add(-48*time.Hour), now.Add(-48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	n, err := s.PruneStaging(24*time.Hour, now)
	if err != nil || n != 1 {
		t.Fatalf("PruneStaging = %d, %v", n, err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("abandoned staging dir kept")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Error("an install that may still be running was deleted")
	}
}

func TestDefaultRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	t.Setenv("AICLI_TOOLS_ROOT", "")
	if root, err := DefaultRoot(); err != nil || root != filepath.Join(home, ".aicli", "tools") {
		t.Errorf("without the variable: %q, %v, want ~/.aicli/tools", root, err)
	}

	t.Setenv("AICLI_TOOLS_ROOT", "/var/lib/aicli/tools")
	if root, err := DefaultRoot(); err != nil || root != "/var/lib/aicli/tools" {
		t.Errorf("with the variable: %q, %v", root, err)
	}

	t.Setenv("AICLI_TOOLS_ROOT", "relative/dir")
	if root, err := DefaultRoot(); err != nil || !filepath.IsAbs(root) {
		t.Errorf("a relative AICLI_TOOLS_ROOT must become absolute: %q, %v", root, err)
	}

	t.Setenv("AICLI_TOOLS_ROOT", "")
	t.Setenv("HOME", "")
	if root, err := DefaultRoot(); err == nil {
		t.Errorf("no HOME and no variable returned %q, want an error", root)
	}
}

func TestPrune(t *testing.T) {
	s := newStore(t)
	// Install 5 versions of agy
	versions := []string{"1.4.8", "1.4.9", "1.4.10", "1.4.11", "1.4.12"}
	for _, v := range versions {
		fakeInstall(t, s, "agy", v)
	}
	if err := s.Use("agy", "1.4.12"); err != nil {
		t.Fatal(err)
	}

	// Prune keeping 3 versions (should remove 1.4.8 and 1.4.9)
	removed, err := s.Prune("agy", 3)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if len(removed) != 2 || removed[0] != "1.4.8" || removed[1] != "1.4.9" {
		t.Errorf("removed = %v, want [1.4.8, 1.4.9]", removed)
	}

	cur, err := s.Versions("agy")
	if err != nil {
		t.Fatal(err)
	}
	if len(cur) != 3 || cur[0] != "1.4.10" || cur[1] != "1.4.11" || cur[2] != "1.4.12" {
		t.Errorf("remaining = %v, want [1.4.10, 1.4.11, 1.4.12]", cur)
	}

	// Test that active version is never removed even if it's older
	if err := s.Use("agy", "1.4.10"); err != nil {
		t.Fatal(err)
	}
	// Prune keeping 1 version: 1.4.10 is current so it must survive
	removed2, err := s.Prune("agy", 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range removed2 {
		if v == "1.4.10" {
			t.Errorf("current version 1.4.10 was removed: %v", removed2)
		}
	}
}
