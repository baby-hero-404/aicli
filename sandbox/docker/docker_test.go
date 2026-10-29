package docker

import (
	"context"
	"github.com/baby-hero-404/aicli/sandbox"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// requireDockerTests gates tests that need a running Docker daemon and a sandbox image.
// Opt in with AICLI_DOCKER_TESTS=1; pick the image with AICLI_TEST_IMAGE (default: the
// runtime's built-in default).
func requireDockerTests(t *testing.T) {
	t.Helper()
	if os.Getenv("AICLI_DOCKER_TESTS") != "1" {
		t.Skip("set AICLI_DOCKER_TESTS=1 to run Docker integration tests")
	}
}

// leakyTempDir is t.TempDir for tests that bind-mount nested paths: Docker creates the mount
// points inside the host directory as root, so a strict RemoveAll can fail with permission
// denied. Cleanup is best-effort here.
func leakyTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "aicli-test-*")
	if err != nil {
		t.Fatalf("create temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func testConfig() Config {
	return Config{Image: os.Getenv("AICLI_TEST_IMAGE")}
}

func TestSessionMountsOverlap(t *testing.T) {
	cases := []struct {
		name          string
		target        string
		sessionMounts map[string]string
		want          bool
	}{
		{
			name:   "target is exactly the session mount",
			target: "/home/agent/.claude",
			sessionMounts: map[string]string{
				"/home/agent/.claude": "/host/session/.claude",
			},
			want: true,
		},
		{
			name:   "target is nested inside session mount",
			target: "/home/agent/.claude/.credentials.json",
			sessionMounts: map[string]string{
				"/home/agent/.claude": "/host/session/.claude",
			},
			want: true,
		},
		{
			name:   "target is outside session mount",
			target: "/home/agent/.config",
			sessionMounts: map[string]string{
				"/home/agent/.claude": "/host/session/.claude",
			},
			want: false,
		},
		{
			name:   "unrelated sibling path is not a false-positive prefix match",
			target: "/home/agent/.claudedata",
			sessionMounts: map[string]string{
				"/home/agent/.claude": "/host/session/.claude",
			},
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sessionMountsOverlap(tc.target, tc.sessionMounts)
			if got != tc.want {
				t.Errorf("sessionMountsOverlap(%q, %v) = %v, want %v", tc.target, tc.sessionMounts, got, tc.want)
			}
		})
	}
}

func TestRuntimeHomeBindMount(t *testing.T) {
	requireDockerTests(t)
	ctx := context.Background()
	rt, err := New(testConfig())
	if err != nil {
		t.Fatalf("Failed to create docker runtime: %v", err)
	}

	homeDir := leakyTempDir(t)

	if err := rt.Prewarm(ctx); err != nil {
		t.Fatalf("Prewarm failed: %v", err)
	}

	req := sandbox.CommandRequest{
		Command: []string{"bash", "-c", "echo 'test-home-content' > /home/agent/test_home_file.txt"},
		SessionMounts: map[string]string{
			"/home/agent": homeDir,
		},
		Timeout: 15 * time.Second,
	}

	res, err := rt.Run(ctx, req)
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("Run failed with exit code %d. Stderr: %s", res.ExitCode, res.Stderr)
	}

	hostFilePath := filepath.Join(homeDir, "test_home_file.txt")
	data, err := os.ReadFile(hostFilePath)
	if err != nil {
		t.Fatalf("Failed to read file from host home dir mount: %v", err)
	}

	if content := string(data); strings.TrimSpace(content) != "test-home-content" {
		t.Errorf("Expected content 'test-home-content', got %q", content)
	}
}

func TestRuntimeCredentialFilesSessionMountOverlap(t *testing.T) {
	requireDockerTests(t)
	ctx := context.Background()
	rt, err := New(testConfig())
	if err != nil {
		t.Fatalf("Failed to create docker runtime: %v", err)
	}

	sessionDir := leakyTempDir(t)

	if err := rt.Prewarm(ctx); err != nil {
		t.Fatalf("Prewarm failed: %v", err)
	}

	req := sandbox.CommandRequest{
		// The command reads the credential file then modifies it to test extraction
		Command: []string{"bash", "-c", "cat /home/agent/.claude/.credentials.json && echo -n 'updated-token' > /home/agent/.claude/.credentials.json"},
		SessionMounts: map[string]string{
			"/home/agent/.claude": sessionDir,
		},
		CredentialFiles: map[string]string{
			"/home/agent/.claude/.credentials.json": "injected-token-123",
		},
		Timeout: 15 * time.Second,
	}

	res, err := rt.Run(ctx, req)
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("Run failed with exit code %d. Stderr: %s", res.ExitCode, res.Stderr)
	}

	// Verify the container saw the initial credential
	if out := strings.TrimSpace(res.Stdout); out != "injected-token-123" {
		t.Errorf("Expected container stdout 'injected-token-123', got %q", out)
	}

	// Verify the updated credential file was written to the host session directory
	hostFilePath := filepath.Join(sessionDir, ".credentials.json")
	data, err := os.ReadFile(hostFilePath)
	if err != nil {
		t.Fatalf("Failed to read credential file from host session dir: %v", err)
	}

	if content := string(data); content != "updated-token" {
		t.Errorf("Expected host file content 'updated-token', got %q", content)
	}

	// Verify the UpdatedCredentialFiles was populated correctly
	if res.UpdatedCredentialFiles["/home/agent/.claude/.credentials.json"] != "updated-token" {
		t.Errorf("Expected UpdatedCredentialFiles to contain 'updated-token', got %v", res.UpdatedCredentialFiles)
	}
}

// TestRuntime_NoHostHomeLeak guards the isolation contract: a run must not see the
// host user's HOME files, only what the caller passes explicitly.
func TestRuntime_NoHostHomeLeak(t *testing.T) {
	requireDockerTests(t)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no host home dir")
	}
	marker := filepath.Join(home, ".aicli-leak-marker")
	if err := os.WriteFile(marker, []byte("host-secret"), 0o600); err != nil {
		t.Skipf("cannot write marker in HOME: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(marker) })

	rt, err := New(testConfig())
	if err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	ctx := context.Background()
	if err := rt.Prewarm(ctx); err != nil {
		t.Fatalf("prewarm: %v", err)
	}
	res, err := rt.Run(ctx, sandbox.CommandRequest{
		Command: []string{"bash", "-c", "ls -A /home/agent; test ! -e /home/agent/.aicli-leak-marker && echo isolated"},
		Timeout: 15 * time.Second,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(res.Stdout, "isolated") || strings.Contains(res.Stdout, ".aicli-leak-marker") {
		t.Errorf("host HOME leaked into sandbox: %q", res.Stdout)
	}
}

// TestRuntime_ManagerCacheMounts verifies opt-in package caches end to end: the cache is mounted
// writable, survives to the next run of the same scope, and is not shared with another scope.
func TestRuntime_ManagerCacheMounts(t *testing.T) {
	requireDockerTests(t)
	rt, err := New(testConfig())
	if err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	ctx := context.Background()
	if err := rt.Prewarm(ctx); err != nil {
		t.Fatalf("prewarm: %v", err)
	}
	reg, err := sandbox.NewRegistry(sandbox.Manifest{
		ID: "node", Detect: []string{"package.json"},
		Cache: []sandbox.CacheMapping{{Name: "npm", Container: "/home/agent/.npm"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	mgr := sandbox.NewManager(rt, reg, sandbox.WithCacheRoot(leakyTempDir(t)))

	run := func(scope, script string) string {
		ws := leakyTempDir(t)
		if err := os.WriteFile(filepath.Join(ws, "package.json"), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
		res, err := mgr.Run(ctx, sandbox.CommandRequest{
			Workspace: ws, CacheScope: scope, Timeout: 30 * time.Second,
			Command: []string{"bash", "-c", script},
		})
		if err != nil || res.ExitCode != 0 {
			t.Fatalf("run: %+v, %v", res, err)
		}
		return res.Stdout
	}
	run("tenant-a", "echo cached > /home/agent/.npm/pkg")
	if out := run("tenant-a", "cat /home/agent/.npm/pkg"); !strings.Contains(out, "cached") {
		t.Errorf("cache did not persist within a scope: %q", out)
	}
	if out := run("tenant-b", "cat /home/agent/.npm/pkg 2>/dev/null || echo none"); strings.Contains(out, "cached") {
		t.Errorf("cache leaked across scopes: %q", out)
	}
}

// syncBuffer collects what the runtime streams to CommandRequest.Output from its copy
// goroutine.
type syncBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
	at  []time.Time
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.at = append(b.at, time.Now())
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestRuntime_StreamsStdoutBeforeExit(t *testing.T) {
	requireDockerTests(t)
	ctx := context.Background()
	rt, err := New(testConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := rt.Prewarm(ctx); err != nil {
		t.Fatalf("Prewarm: %v", err)
	}

	var out syncBuffer
	start := time.Now()
	res, err := rt.Run(ctx, sandbox.CommandRequest{
		Command: []string{"bash", "-c", "echo first; sleep 3; echo second"},
		Timeout: 30 * time.Second,
		Output:  &out,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := out.String(); got != "first\nsecond\n" {
		t.Errorf("Output = %q, want both lines", got)
	}
	if res.Stdout != strings.TrimSpace(out.String()) {
		t.Errorf("Stdout = %q, Output = %q", res.Stdout, out.String())
	}
	out.mu.Lock()
	defer out.mu.Unlock()
	if len(out.at) == 0 || out.at[0].Sub(start) > 2500*time.Millisecond {
		t.Errorf("first line arrived %v after start; it should arrive before the 3s sleep ends", out.at[0].Sub(start))
	}
}

func TestRuntime_ReadOnlyMounts(t *testing.T) {
	requireDockerTests(t)
	ctx := context.Background()
	rt, err := New(testConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "tool.txt"), []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	single := filepath.Join(t.TempDir(), "profile.sh")
	if err := os.WriteFile(single, []byte("echo from-file"), 0o644); err != nil {
		t.Fatal(err)
	}
	req := sandbox.CommandRequest{
		Command:        []string{"bash", "-c", "cat /opt/t/tool.txt; bash /etc/profile.d/x.sh; echo changed > /opt/t/tool.txt && echo WROTE || echo DENIED"},
		ReadOnlyMounts: map[string]string{"/opt/t": dir, "/etc/profile.d/x.sh": single},
		Timeout:        30 * time.Second,
	}
	res, err := rt.Run(ctx, req)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, want := range []string{"original", "from-file", "DENIED"} {
		if !strings.Contains(res.Stdout, want) {
			t.Errorf("stdout lacks %q: %q (stderr %q)", want, res.Stdout, res.Stderr)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "tool.txt")); string(b) != "original" {
		t.Errorf("host file changed: %q", b)
	}

	// Interactive sessions (logins) get the same mounts.
	var out syncBuffer
	err = rt.RunInteractive(ctx, sandbox.CommandRequest{
		Command:        []string{"bash", "-c", "cat /opt/t/tool.txt"},
		ReadOnlyMounts: map[string]string{"/opt/t": dir},
	}, strings.NewReader(""), &out, &out)
	if err != nil {
		t.Fatalf("RunInteractive: %v", err)
	}
	if !strings.Contains(out.String(), "original") {
		t.Errorf("interactive output = %q", out.String())
	}
}
