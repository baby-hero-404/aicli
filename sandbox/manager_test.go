package sandbox

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// recordingRuntime captures the last CommandRequest.Run was called with, so
// tests can assert on what Manager resolved without a real Docker
// daemon.
type recordingRuntime struct {
	lastReq CommandRequest
	result  *CommandResult
	err     error
}

func (r *recordingRuntime) Run(ctx context.Context, req CommandRequest) (*CommandResult, error) {
	r.lastReq = req
	if r.result != nil {
		return r.result, r.err
	}
	return &CommandResult{ExitCode: 0}, r.err
}

func (r *recordingRuntime) RunInteractive(ctx context.Context, req CommandRequest, stdin io.Reader, stdout, stderr io.Writer) error {
	r.lastReq = req
	return r.err
}

func (r *recordingRuntime) Prewarm(ctx context.Context) error {
	return r.err
}

func TestManagerPassthroughWhenNoManifestMatches(t *testing.T) {
	reg, err := testRegistry()
	if err != nil {
		t.Fatalf("testRegistry() error = %v", err)
	}
	rt := &recordingRuntime{}
	mgr := NewManager(rt, reg)

	dir := t.TempDir()
	req := CommandRequest{Workspace: dir, Command: []string{"bash", "-lc", "echo hi"}}
	if _, err := mgr.Run(context.Background(), req); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if rt.lastReq.Image != "" {
		t.Errorf("Image = %q, want empty (no manifest matched)", rt.lastReq.Image)
	}
	if len(rt.lastReq.Command) != 3 || rt.lastReq.Command[2] != "echo hi" {
		t.Errorf("Command = %v, want unchanged original command", rt.lastReq.Command)
	}
}

func TestManagerResolvesManifestAndMergesCacheMounts(t *testing.T) {
	reg, err := testRegistry()
	if err != nil {
		t.Fatalf("testRegistry() error = %v", err)
	}
	rt := &recordingRuntime{}
	cacheRoot := t.TempDir()
	mgr := NewManager(rt, reg, WithCacheRoot(cacheRoot))

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("write package.json: %v", err)
	}

	req := CommandRequest{Workspace: dir, Command: []string{"bash", "-lc", "npm test"}, CacheScope: "proj-1"}
	if _, err := mgr.Run(context.Background(), req); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if rt.lastReq.Image == "" {
		t.Error("Image is empty, want the node manifest's resolved image")
	}
	want := filepath.Join(cacheRoot, "proj-1", "npm")
	if got := rt.lastReq.ExtraCacheMounts["/home/agent/.npm"]; got != want {
		t.Errorf("npm cache host path = %q, want %q", got, want)
	}
	if st, err := os.Stat(want); err != nil || !st.IsDir() {
		t.Errorf("cache dir %s should exist: %v", want, err)
	}
}

func TestMergeCacheMounts(t *testing.T) {
	cache := []CacheMapping{{Name: "npm", Container: "/home/agent/.npm"}}
	root := t.TempDir()

	tests := []struct {
		name      string
		root      string
		scope     string
		existing  map[string]string
		wantMount string // expected host path for /home/agent/.npm; "" = none
	}{
		{"no cache root means no cache", "", "p1", nil, ""},
		{"default scope", root, "", nil, filepath.Join(root, DefaultCacheScope, "npm")},
		{"explicit scope", root, "org-7", nil, filepath.Join(root, "org-7", "npm")},
		{"traversal scope rejected", root, "../escape", nil, ""},
		{"slash scope rejected", root, "a/b", nil, ""},
		{"caller mount wins", root, "p1", map[string]string{"/home/agent/.npm": "/custom"}, "/custom"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mergeCacheMounts(tt.existing, cache, tt.root, tt.scope)["/home/agent/.npm"]
			if got != tt.wantMount {
				t.Errorf("host path = %q, want %q", got, tt.wantMount)
			}
		})
	}

	a := mergeCacheMounts(nil, cache, root, "tenant-a")["/home/agent/.npm"]
	b := mergeCacheMounts(nil, cache, root, "tenant-b")["/home/agent/.npm"]
	if a == b {
		t.Error("different scopes must not share a cache directory")
	}
}

func TestManagerNoCacheRootMountsNothing(t *testing.T) {
	reg, _ := testRegistry()
	rt := &recordingRuntime{}
	mgr := NewManager(rt, reg)
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "package.json"), []byte("{}"), 0o644)
	if _, err := mgr.Run(context.Background(), CommandRequest{Workspace: dir, Command: []string{"bash", "-lc", "x"}}); err != nil {
		t.Fatal(err)
	}
	if len(rt.lastReq.ExtraCacheMounts) != 0 {
		t.Errorf("ExtraCacheMounts = %v, want none without a cache root", rt.lastReq.ExtraCacheMounts)
	}
}

func TestManagerExplicitImageNotOverridden(t *testing.T) {
	reg, err := testRegistry()
	if err != nil {
		t.Fatalf("testRegistry() error = %v", err)
	}
	rt := &recordingRuntime{}
	mgr := NewManager(rt, reg)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}

	req := CommandRequest{Workspace: dir, Command: []string{"bash", "-lc", "go build ./..."}, Image: "custom:tag"}
	if _, err := mgr.Run(context.Background(), req); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if rt.lastReq.Image != "custom:tag" {
		t.Errorf("Image = %q, want caller-supplied %q preserved", rt.lastReq.Image, "custom:tag")
	}
}

func TestManagerHealthcheckFailureReturnsSandboxNotReady(t *testing.T) {
	reg := &Registry{
		manifests: []*Manifest{{ID: "node", Image: "img:tag", Detect: []string{"package.json"}, Healthcheck: "false"}},
		byID:      map[string]*Manifest{},
	}
	reg.byID["node"] = reg.manifests[0]

	rt := &recordingRuntime{result: &CommandResult{ExitCode: 97, Stdout: "__SANDBOX_HEALTHCHECK_FAILED__"}}
	mgr := NewManager(rt, reg)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("write package.json: %v", err)
	}

	_, err := mgr.Run(context.Background(), CommandRequest{Workspace: dir, Command: []string{"bash", "-lc", "npm test"}})
	if err == nil {
		t.Fatal("Run() error = nil, want healthcheck failure error")
	}
}

func TestManagerRunInteractivePassthrough(t *testing.T) {
	reg, err := testRegistry()
	if err != nil {
		t.Fatalf("testRegistry() error = %v", err)
	}
	rt := &recordingRuntime{}
	mgr := NewManager(rt, reg)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("write package.json: %v", err)
	}
	req := CommandRequest{Workspace: dir, Command: []string{"bash"}}
	if err := mgr.RunInteractive(context.Background(), req, nil, io.Discard, io.Discard); err != nil {
		t.Fatalf("RunInteractive() error = %v", err)
	}
	// RunInteractive never runs manifest resolution — request must reach the
	// wrapped runtime completely unchanged.
	if rt.lastReq.Image != "" {
		t.Errorf("Image = %q, want empty (RunInteractive skips manifest resolution)", rt.lastReq.Image)
	}
}

func TestManagerEmptyRegistryIsPassthrough(t *testing.T) {
	reg, err := NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "package.json"), []byte("{}"), 0o644)
	rt := &recordingRuntime{}
	req := CommandRequest{Workspace: dir, Command: []string{"bash", "-lc", "x"}}
	if _, err := NewManager(rt, reg, WithCacheRoot(t.TempDir())).Run(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if rt.lastReq.Image != "" || len(rt.lastReq.ExtraCacheMounts) != 0 || rt.lastReq.Command[2] != "x" {
		t.Errorf("empty registry must leave the request untouched, got %+v", rt.lastReq)
	}
}
