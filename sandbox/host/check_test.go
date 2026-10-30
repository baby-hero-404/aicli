package host

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/baby-hero-404/aicli/sandbox"
)

func testCfg(t *testing.T) Config {
	t.Helper()
	return Config{RunRoot: t.TempDir(), PATH: defaultPATH}
}

func okReq(t *testing.T) sandbox.CommandRequest {
	t.Helper()
	return sandbox.CommandRequest{
		TaskID: "t1", Workspace: t.TempDir(), Command: []string{"true"}, Profile: "claude_code_notools",
	}
}

func TestLayoutFor(t *testing.T) {
	cfg := testCfg(t)
	ws := t.TempDir()
	tests := []struct {
		name    string
		cfg     Config
		req     sandbox.CommandRequest
		want    sandbox.Layout
		wantErr string
	}{
		{"explicit workspace", cfg, sandbox.CommandRequest{TaskID: "t1", Workspace: ws},
			sandbox.Layout{Workspace: ws, Home: filepath.Join(cfg.RunRoot, "t1", "home")}, ""},
		{"workspace from root", Config{RunRoot: cfg.RunRoot, WorkspaceRoot: ws}, sandbox.CommandRequest{TaskID: "t1"},
			sandbox.Layout{Workspace: filepath.Join(ws, "t1"), Home: filepath.Join(cfg.RunRoot, "t1", "home")}, ""},
		{"no workspace and no root", cfg, sandbox.CommandRequest{TaskID: "t1"}, sandbox.Layout{}, "no workspace"},
		{"empty task id", cfg, sandbox.CommandRequest{Workspace: ws}, sandbox.Layout{}, "invalid task id"},
		{"traversing task id", cfg, sandbox.CommandRequest{TaskID: "../x", Workspace: ws}, sandbox.Layout{}, "invalid task id"},
		{"task id with slash", cfg, sandbox.CommandRequest{TaskID: "a/b", Workspace: ws}, sandbox.Layout{}, "invalid task id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := layoutFor(tt.cfg, tt.req)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Errorf("layout = %+v, %v, want %+v", got, err, tt.want)
			}
		})
	}
}

func TestCheckRequest(t *testing.T) {
	cfg := testCfg(t)
	home := filepath.Join(cfg.RunRoot, "t1", "home")
	tests := []struct {
		name        string
		cfg         func(*Config)
		mutate      func(*sandbox.CommandRequest)
		interactive bool
		wantErr     error  // matched with errors.Is
		wantText    string // or substring of the message
	}{
		{name: "notools profile is fine"},
		{name: "no profile", mutate: func(r *sandbox.CommandRequest) { r.Profile = "" }, wantErr: ErrToolsNotAllowed},
		{name: "profile with tools", mutate: func(r *sandbox.CommandRequest) { r.Profile = "claude_code" }, wantErr: ErrToolsNotAllowed},
		{name: "unknown profile", mutate: func(r *sandbox.CommandRequest) { r.Profile = "nope" }, wantErr: ErrToolsNotAllowed},
		{name: "tools allowed by operator", cfg: func(c *Config) { c.AllowTools = true }, mutate: func(r *sandbox.CommandRequest) { r.Profile = "claude_code" }},
		{name: "network none", mutate: func(r *sandbox.CommandRequest) { r.NetworkMode = sandbox.NetworkModeNone }, wantErr: ErrUnsupported},
		{name: "network bridge", mutate: func(r *sandbox.CommandRequest) { r.NetworkMode = sandbox.NetworkModeBridge }},
		{name: "read-only mounts", mutate: func(r *sandbox.CommandRequest) { r.ReadOnlyMounts = map[string]string{"/opt/x": "/srv/x"} }, wantErr: ErrUnsupported},
		{name: "cache mounts", mutate: func(r *sandbox.CommandRequest) { r.ExtraCacheMounts = map[string]string{"/root/.npm": "/srv/c"} }, wantErr: ErrUnsupported},
		{name: "secret env", mutate: func(r *sandbox.CommandRequest) { r.SecretEnv = map[string]string{"K": "v"} }, wantText: "secrets"},
		{name: "blocked command", mutate: func(r *sandbox.CommandRequest) { r.Command = []string{"sh", "-c", "mkfs /dev/sda"} }, wantText: "blocked"},
		{name: "session target outside HOME", mutate: func(r *sandbox.CommandRequest) {
			r.SessionMounts = map[string]string{"/etc/x": "/srv/x"}
		}, wantText: "outside the run HOME"},
		{name: "session target is HOME itself", mutate: func(r *sandbox.CommandRequest) {
			r.SessionMounts = map[string]string{home: "/srv/x"}
		}, wantText: "outside the run HOME"},
		{name: "session target climbs out", mutate: func(r *sandbox.CommandRequest) {
			r.SessionMounts = map[string]string{home + "/../x": "/srv/x"}
		}, wantText: "clean absolute"},
		{name: "credential outside HOME", mutate: func(r *sandbox.CommandRequest) {
			r.CredentialFiles = map[string]string{"/etc/cred": "x"}
		}, wantText: "outside the run HOME"},
		{name: "credential inside session mount is allowed", mutate: func(r *sandbox.CommandRequest) {
			r.SessionMounts = map[string]string{home + "/.claude": "/srv/x"}
			r.CredentialFiles = map[string]string{home + "/.claude/.credentials.json": "x"}
		}},
		{name: "credential next to session mount", mutate: func(r *sandbox.CommandRequest) {
			r.SessionMounts = map[string]string{home + "/.claude": "/srv/x"}
			r.CredentialFiles = map[string]string{home + "/.claude.json": "x"}
		}},
		{name: "nested session mounts", mutate: func(r *sandbox.CommandRequest) {
			r.SessionMounts = map[string]string{home + "/.a": "/srv/a", home + "/.a/b": "/srv/b"}
		}, wantErr: ErrUnsupported},
		{name: "workspace is HOME", mutate: func(r *sandbox.CommandRequest) { r.Workspace, _ = os.UserHomeDir() }, wantText: "HOME"},

		{name: "interactive allowlisted", interactive: true,
			cfg:    func(c *Config) { c.InteractiveCommands = [][]string{{"claude", "auth", "login"}} },
			mutate: func(r *sandbox.CommandRequest) { r.Command, r.Profile = []string{"claude", "auth", "login"}, "" }},
		{name: "interactive default shell", interactive: true,
			cfg:    func(c *Config) { c.InteractiveCommands = [][]string{{"claude", "auth", "login"}} },
			mutate: func(r *sandbox.CommandRequest) { r.Command = []string{"/bin/bash"} }, wantErr: ErrNotAllowlisted},
		{name: "interactive prefix of allowlisted", interactive: true,
			cfg:    func(c *Config) { c.InteractiveCommands = [][]string{{"claude", "auth", "login"}} },
			mutate: func(r *sandbox.CommandRequest) { r.Command = []string{"claude", "auth"} }, wantErr: ErrNotAllowlisted},
		{name: "interactive extension of allowlisted", interactive: true,
			cfg:    func(c *Config) { c.InteractiveCommands = [][]string{{"claude", "auth", "login"}} },
			mutate: func(r *sandbox.CommandRequest) { r.Command = []string{"claude", "auth", "login", "; sh"} }, wantErr: ErrNotAllowlisted},
		{name: "interactive with empty allowlist", interactive: true,
			mutate: func(r *sandbox.CommandRequest) { r.Command = []string{"claude"} }, wantErr: ErrNotAllowlisted},
		{name: "interactive with session mounts", interactive: true,
			cfg: func(c *Config) { c.InteractiveCommands = [][]string{{"true"}} },
			mutate: func(r *sandbox.CommandRequest) {
				r.SessionMounts = map[string]string{home + "/.a": "/srv/a"}
			}, wantErr: ErrUnsupported},
		{name: "interactive ignores the tool guardrail", interactive: true,
			cfg:    func(c *Config) { c.InteractiveCommands = [][]string{{"true"}} },
			mutate: func(r *sandbox.CommandRequest) { r.Profile = "" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := cfg
			if tt.cfg != nil {
				tt.cfg(&c)
			}
			req := okReq(t)
			if tt.mutate != nil {
				tt.mutate(&req)
			}
			_, err := checkRequest(c, req, tt.interactive)
			switch {
			case tt.wantErr != nil:
				if !errors.Is(err, tt.wantErr) {
					t.Errorf("err = %v, want %v", err, tt.wantErr)
				}
			case tt.wantText != "":
				if err == nil || !strings.Contains(err.Error(), tt.wantText) {
					t.Errorf("err = %v, want containing %q", err, tt.wantText)
				}
			case err != nil:
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestBuildEnv(t *testing.T) {
	t.Setenv("AICLI_HOST_TEST_SECRET", "leak")
	cfg := Config{PATH: "/usr/bin:/bin"}
	layout := sandbox.Layout{Workspace: "/w", Home: "/h"}
	req := sandbox.CommandRequest{
		Env:       map[string]string{"A": "1", "HOME": "/custom"},
		SecretEnv: map[string]string{"S": "2"},
	}
	tests := []struct {
		name        string
		interactive bool
		want        []string
	}{
		{"run", false, []string{"A=1", "HOME=/custom", "LANG=C.UTF-8", "PATH=/usr/bin:/bin", "S=2", "TMPDIR=/t"}},
		{"interactive", true, []string{"A=1", "HOME=/custom", "LANG=C.UTF-8", "PATH=/usr/bin:/bin", "S=2", "TERM=xterm", "TMPDIR=/t"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildEnv(cfg, req, layout, "/t", tt.interactive)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("env = %v, want %v", got, tt.want)
			}
		})
	}
	if got := buildEnv(cfg, sandbox.CommandRequest{}, layout, "/t", false); got[0] != "HOME=/h" {
		t.Errorf("HOME defaults to the layout home, got %v", got)
	}
}

func TestResolveDir(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		dir     string
		allowed []string
		wantErr bool
	}{
		{"existing inside root", root, []string{root}, false},
		{"missing inside root", filepath.Join(root, "new", "deep"), []string{root}, false},
		{"no allowed roots", outside, nil, false},
		{"outside every root", outside, []string{root}, true},
		{"symlink leading outside", filepath.Join(root, "escape"), []string{root}, true},
		{"missing dir below a symlink leading outside", filepath.Join(root, "escape", "new"), []string{root}, true},
		{"second root matches", outside, []string{root, outside}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := resolveDir(tt.dir, tt.allowed)
			if (err != nil) != tt.wantErr {
				t.Errorf("err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestLookPath(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "tool")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "data"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, cmd, path, want string
		wantErr               bool
	}{
		{"found", "tool", dir, exe, false},
		{"found later in list", "tool", "/nonexistent:" + dir, exe, false},
		{"not executable", "data", dir, "", true},
		{"missing", "nothing", dir, "", true},
		{"relative PATH entry ignored", "tool", ".", "", true},
		{"slash name used as is", "/bin/sh", "", "/bin/sh", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := lookPath(tt.cmd, tt.path)
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Errorf("lookPath = %q, %v, want %q (err %v)", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func TestReadRegularFile(t *testing.T) {
	dir := t.TempDir()
	plain := filepath.Join(dir, "plain")
	secret := filepath.Join(dir, "secret")
	big := filepath.Join(dir, "big")
	for p, c := range map[string]string{plain: "ok", secret: "host secret", big: strings.Repeat("x", maxCredentialReadBytes+1)} {
		if err := os.WriteFile(p, []byte(c), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(secret, link); err != nil {
		t.Fatal(err)
	}
	if b, err := readRegularFile(plain); err != nil || string(b) != "ok" {
		t.Errorf("plain = %q, %v", b, err)
	}
	for name, p := range map[string]string{"symlink": link, "oversized": big, "missing": filepath.Join(dir, "none"), "directory": dir} {
		if b, err := readRegularFile(p); err == nil {
			t.Errorf("%s: read %q, want an error", name, b)
		}
	}
}
