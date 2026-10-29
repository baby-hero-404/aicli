package run

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baby-hero-404/aicli/sandbox"
)

// fakeRuntime records requests and replies from a script of results.
type fakeRuntime struct {
	sandbox.StubRuntime
	reqs    []sandbox.CommandRequest
	results []*sandbox.CommandResult
	err     error
	onRun   func(req sandbox.CommandRequest)
}

func (f *fakeRuntime) Run(_ context.Context, req sandbox.CommandRequest) (*sandbox.CommandResult, error) {
	f.reqs = append(f.reqs, req)
	if f.onRun != nil {
		f.onRun(req)
	}
	if f.err != nil {
		return nil, f.err
	}
	if len(f.results) == 0 {
		return &sandbox.CommandResult{}, nil
	}
	r := f.results[0]
	if len(f.results) > 1 {
		f.results = f.results[1:]
	}
	return r, nil
}

func (f *fakeRuntime) RunInteractive(context.Context, sandbox.CommandRequest, io.Reader, io.Writer, io.Writer) error {
	return nil
}

func newRunner(results ...*sandbox.CommandResult) (*Runner, *fakeRuntime) {
	rt := &fakeRuntime{results: results}
	return &Runner{Runtime: rt}, rt
}

func baseReq(t *testing.T) Request {
	return Request{Profile: "claude_code", Command: "claude", Args: []string{"-p", "Read {prompt_file} in {workdir}"}, Prompt: "do it", Workspace: t.TempDir()}
}

func TestRun_BuildsScriptAndCleansUp(t *testing.T) {
	req := baseReq(t)
	req.Env = map[string]string{"FOO": "bar"}
	req.ContextFiles = map[string]string{"a/b.md": "ctx"}
	req.ResumeSessionID = "sess-1"
	req.ExtraArgs = []string{"--extra"}
	req.Setup, req.Cleanup = "echo setup", "echo cleanup"

	runner, rt := newRunner(&sandbox.CommandResult{Stdout: "ok"})
	var promptSeen, ctxSeen string
	rt.onRun = func(sandbox.CommandRequest) {
		b, _ := os.ReadFile(filepath.Join(req.Workspace, ".aicli", "prompt.md"))
		promptSeen = string(b)
		c, _ := os.ReadFile(filepath.Join(req.Workspace, ".aicli", "context", "a", "b.md"))
		ctxSeen = string(c)
	}
	res, err := runner.Run(context.Background(), req)
	if err != nil || !res.Success {
		t.Fatalf("Run = %+v, %v", res, err)
	}
	if promptSeen != "do it" || ctxSeen != "ctx" {
		t.Errorf("prompt/context not on disk during the run: %q %q", promptSeen, ctxSeen)
	}
	if _, err := os.Stat(filepath.Join(req.Workspace, ".aicli")); !os.IsNotExist(err) {
		t.Errorf("state dir should be removed after the run, stat err = %v", err)
	}

	got := rt.reqs[0]
	script := got.Command[2]
	for _, want := range []string{
		"cd '/workspace' && echo setup && 'claude' '-p' 'Read /workspace/.aicli/prompt.md in /workspace' '--resume' 'sess-1' '--extra'",
		"echo cleanup; rm -rf '/workspace/.aicli'; exit $status",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("script missing %q:\n%s", want, script)
		}
	}
	if got.Env["CI"] != "1" || got.Env["HOME"] != sandbox.HomeDir || got.Env["FOO"] != "bar" || got.Env["AICLI_CONTEXT_DIR"] != "/workspace/.aicli/context" {
		t.Errorf("env = %v", got.Env)
	}
	if got.Timeout != DefaultTimeout {
		t.Errorf("Timeout = %v, want default %v", got.Timeout, DefaultTimeout)
	}
}

func TestRun_QuotesHostileArguments(t *testing.T) {
	req := baseReq(t)
	req.Args = []string{"it's; rm -rf /"}
	runner, rt := newRunner()
	if _, err := runner.Run(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rt.reqs[0].Command[2], `'it'\''s; rm -rf /'`) {
		t.Errorf("argument not safely quoted: %s", rt.reqs[0].Command[2])
	}
}

func TestRun_Classification(t *testing.T) {
	loop := strings.Repeat("error: connection failed\n", 12)
	tests := []struct {
		name    string
		profile string
		out     *sandbox.CommandResult
		check   func(t *testing.T, r *Result)
	}{
		{"success", "claude_code", &sandbox.CommandResult{Stdout: "all done\n"}, func(t *testing.T, r *Result) {
			if !r.Success || r.Error != "" {
				t.Errorf("want clean success, got %+v", r)
			}
		}},
		{"confirmed auth failure", "claude_code", &sandbox.CommandResult{ExitCode: 1, Stderr: "Please run /login"}, func(t *testing.T, r *Result) {
			if r.Success || !r.AuthInvalid || !r.AuthInvalidConfirmed || !strings.Contains(r.Error, "permanent") {
				t.Errorf("got %+v", r)
			}
		}},
		{"suspected auth failure (generic rule)", "custom", &sandbox.CommandResult{ExitCode: 1, Stdout: "HTTP 401"}, func(t *testing.T, r *Result) {
			if !r.AuthInvalid || r.AuthInvalidConfirmed {
				t.Errorf("got %+v", r)
			}
		}},
		{"quota with cooldown", "antigravity", &sandbox.CommandResult{ExitCode: 1, Stderr: "quota exceeded. Resets in 1h30m"}, func(t *testing.T, r *Result) {
			if !r.QuotaExceeded || r.QuotaCooldown != 90*time.Minute || r.AuthInvalid {
				t.Errorf("got %+v", r)
			}
		}},
		{"auth wins over quota", "claude_code", &sandbox.CommandResult{ExitCode: 1, Stderr: "rate limit\nNot logged in"}, func(t *testing.T, r *Result) {
			if r.QuotaExceeded || !r.AuthInvalidConfirmed {
				t.Errorf("auth failure must mask quota: %+v", r)
			}
		}},
		{"loop detected in output", "claude_code", &sandbox.CommandResult{Stdout: loop}, func(t *testing.T, r *Result) {
			if r.Success || !r.LoopKilled {
				t.Errorf("got %+v", r)
			}
		}},
		{"idle stall", "claude_code", &sandbox.CommandResult{Killed: true, KillReason: sandbox.KillReasonIdleTimeout, ExitCode: 137}, func(t *testing.T, r *Result) {
			if !r.IdleTimeoutHit || !strings.Contains(r.Error, "idle") {
				t.Errorf("got %+v", r)
			}
		}},
		{"waiting for input despite exit 0", "claude_code", &sandbox.CommandResult{Stdout: "Overwrite the file? (y/n)"}, func(t *testing.T, r *Result) {
			if r.Success || !r.AwaitingInput {
				t.Errorf("got %+v", r)
			}
		}},
		{"plain failure with reason", "claude_code", &sandbox.CommandResult{ExitCode: 127}, func(t *testing.T, r *Result) {
			if r.Success || !strings.Contains(r.Error, "command not found") {
				t.Errorf("got %+v", r)
			}
		}},
		{"oom exit code", "claude_code", &sandbox.CommandResult{ExitCode: 137}, func(t *testing.T, r *Result) {
			if !strings.Contains(r.Error, "out of memory") {
				t.Errorf("got %+v", r)
			}
		}},
		{"secrets are redacted", "claude_code", &sandbox.CommandResult{ExitCode: 1, Stdout: `{"access_token": "abc123"} ghp_` + strings.Repeat("a", 36)}, func(t *testing.T, r *Result) {
			if strings.Contains(r.Output, "abc123") || strings.Contains(r.Output, "ghp_") {
				t.Errorf("secrets leaked: %q", r.Output)
			}
		}},
		{"telemetry", "claude_code", &sandbox.CommandResult{Stdout: `{"total_cost_usd":0.5,"duration_ms":1200,"session_id":"s9"}`}, func(t *testing.T, r *Result) {
			if r.Telemetry == nil || r.Telemetry.CostUSD != 0.5 || r.Telemetry.SessionID != "s9" {
				t.Errorf("telemetry = %+v", r.Telemetry)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := baseReq(t)
			req.Profile = tt.profile
			runner, _ := newRunner(tt.out)
			res, err := runner.Run(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			tt.check(t, res)
		})
	}
}

func TestRun_CapturedFiles(t *testing.T) {
	enc := base64.StdEncoding.EncodeToString([]byte("report body"))
	out := "log line\n" + captureStart + "out/report.md\n" + enc + "\n" + captureEnd + "\nbye"
	runner, rt := newRunner(&sandbox.CommandResult{Stdout: out})
	req := baseReq(t)
	req.CaptureFiles = []string{"out/report.md"}
	res, err := runner.Run(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if res.Files["out/report.md"] != "report body" {
		t.Errorf("files = %v", res.Files)
	}
	if strings.Contains(res.Output, "AICLI_CAPTURE") || strings.Contains(res.Output, enc) {
		t.Errorf("capture block leaked into output: %q", res.Output)
	}
	if !strings.Contains(rt.reqs[0].Command[2], "'/workspace/out/report.md'") {
		t.Errorf("capture command missing from script: %s", rt.reqs[0].Command[2])
	}
}

func TestRun_Credentials(t *testing.T) {
	req := baseReq(t)
	req.CredentialFiles = map[string]string{".claude/.credentials.json": "tok", "../../etc/passwd": "evil", "": "x"}
	runner, rt := newRunner(&sandbox.CommandResult{UpdatedCredentialFiles: map[string]string{
		sandbox.HomeDir + "/.claude/.credentials.json": "refreshed",
		"/elsewhere/file": "ignored",
	}})
	res, err := runner.Run(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	creds := rt.reqs[0].CredentialFiles
	if creds[sandbox.HomeDir+"/.claude/.credentials.json"] != "tok" {
		t.Errorf("credential not mapped under HOME: %v", creds)
	}
	for target := range creds {
		if !strings.HasPrefix(target, sandbox.HomeDir+"/") {
			t.Errorf("credential escaped HOME: %s", target)
		}
	}
	if res.UpdatedCredentialFiles[".claude/.credentials.json"] != "refreshed" || len(res.UpdatedCredentialFiles) != 1 {
		t.Errorf("UpdatedCredentialFiles = %v", res.UpdatedCredentialFiles)
	}
}

func TestRun_InvalidRequests(t *testing.T) {
	runner, _ := newRunner()
	tests := []struct {
		name string
		mut  func(*Request)
	}{
		{"no command", func(r *Request) { r.Command = " " }},
		{"no workspace", func(r *Request) { r.Workspace = "" }},
		{"workdir outside mount", func(r *Request) { r.WorkDir = "/etc" }},
		{"context path escape", func(r *Request) { r.ContextFiles = map[string]string{"../../x": "y"} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := baseReq(t)
			tt.mut(&req)
			if _, err := runner.Run(context.Background(), req); !errors.Is(err, ErrInvalidRequest) {
				t.Errorf("err = %v, want ErrInvalidRequest", err)
			}
		})
	}
}

func TestRun_RuntimeErrorPropagates(t *testing.T) {
	runner, rt := newRunner()
	rt.err = errors.New("docker down")
	if _, err := runner.Run(context.Background(), baseReq(t)); err == nil || errors.Is(err, ErrInvalidRequest) {
		t.Errorf("want a runtime error, got %v", err)
	}
}

func TestPreflight(t *testing.T) {
	ok := &sandbox.CommandResult{}
	tests := []struct {
		name        string
		mut         func(*Request)
		results     []*sandbox.CommandResult
		wantErr     string
		wantWarning bool
	}{
		{"binary missing", nil, []*sandbox.CommandResult{{ExitCode: 1}}, "not found in sandbox", false},
		{"no auth check and no env warns", nil, []*sandbox.CommandResult{ok}, "", true},
		{"no auth check but env set is quiet", func(r *Request) { r.Env = map[string]string{"K": "v"} }, []*sandbox.CommandResult{ok}, "", false},
		{"auth check ok", func(r *Request) { r.AuthCheckCommand = "claude auth status" }, []*sandbox.CommandResult{ok, {Stdout: `{"loggedIn": true}`}}, "", false},
		{"auth check nonzero", func(r *Request) { r.AuthCheckCommand = "x" }, []*sandbox.CommandResult{ok, {ExitCode: 2, Stderr: "boom"}}, "exited 2", false},
		{"auth check exit 0 but logged out", func(r *Request) { r.AuthCheckCommand = "claude auth status" }, []*sandbox.CommandResult{ok, {Stdout: `{"loggedIn": false}`}}, "not authenticated", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := baseReq(t)
			if tt.mut != nil {
				tt.mut(&req)
			}
			runner, _ := newRunner(tt.results...)
			warn, err := runner.Preflight(context.Background(), req)
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("unexpected error %v", err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
			}
			if (warn != "") != tt.wantWarning {
				t.Errorf("warning = %q, want warning: %v", warn, tt.wantWarning)
			}
		})
	}

	t.Run("command required", func(t *testing.T) {
		runner, _ := newRunner()
		req := baseReq(t)
		req.Command = ""
		if _, err := runner.Preflight(context.Background(), req); !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("err = %v", err)
		}
	})
}

func TestRun_SessionDirMountsProfileState(t *testing.T) {
	sessionDir := filepath.Join(t.TempDir(), "claude_code", "acct-1")
	req := baseReq(t)
	req.SessionDir = sessionDir
	req.SessionMounts = map[string]string{"/home/agent/.extra": "/abs/extra"}
	req.Env = map[string]string{"CI": "0", "FOO": "bar"}
	runner, rt := newRunner()
	if _, err := runner.Run(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	got := rt.reqs[0]
	wantHost := filepath.Join(sessionDir, ".claude")
	if got.SessionMounts[sandbox.HomeDir+"/.claude"] != wantHost {
		t.Errorf("state dir not mounted: %v", got.SessionMounts)
	}
	if got.SessionMounts["/home/agent/.extra"] != "/abs/extra" {
		t.Errorf("explicit session mount lost: %v", got.SessionMounts)
	}
	if st, err := os.Stat(wantHost); err != nil || !st.IsDir() {
		t.Errorf("session state dir should be created: %v", err)
	}
	if got.Env["CLAUDE_CONFIG_DIR"] != sandbox.HomeDir+"/.claude" {
		t.Errorf("profile state env missing: %v", got.Env)
	}
	if got.Env["CI"] != "1" || got.Env["FOO"] != "bar" {
		t.Errorf("env = %v (CI must stay 1, caller env kept)", got.Env)
	}
}

func TestRun_ProfileStateEnvWithoutSessionDir(t *testing.T) {
	runner, rt := newRunner()
	if _, err := runner.Run(context.Background(), baseReq(t)); err != nil {
		t.Fatal(err)
	}
	if rt.reqs[0].Env["CLAUDE_CONFIG_DIR"] != sandbox.HomeDir+"/.claude" {
		t.Errorf("state env must apply for a known profile even without SessionDir: %v", rt.reqs[0].Env)
	}
	if len(rt.reqs[0].SessionMounts) != 0 {
		t.Errorf("no mounts expected without SessionDir: %v", rt.reqs[0].SessionMounts)
	}
}

func TestRun_SessionDirValidation(t *testing.T) {
	tests := []struct {
		name string
		mut  func(*Request)
	}{
		{"unknown profile", func(r *Request) { r.Profile = "custom"; r.SessionDir = "/abs/dir" }},
		{"relative session dir", func(r *Request) { r.SessionDir = "rel/dir" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := baseReq(t)
			tt.mut(&req)
			runner, _ := newRunner()
			if _, err := runner.Run(context.Background(), req); !errors.Is(err, ErrInvalidRequest) {
				t.Errorf("Run err = %v, want ErrInvalidRequest", err)
			}
			if _, err := runner.Preflight(context.Background(), req); !errors.Is(err, ErrInvalidRequest) {
				t.Errorf("Preflight err = %v, want ErrInvalidRequest", err)
			}
		})
	}
}

func TestPreflight_UsesSessionState(t *testing.T) {
	sessionDir := filepath.Join(t.TempDir(), "acct")
	req := baseReq(t)
	req.SessionDir = sessionDir
	req.AuthCheckCommand = "claude auth status"
	runner, rt := newRunner(&sandbox.CommandResult{}, &sandbox.CommandResult{Stdout: `{"loggedIn": true}`})
	if _, err := runner.Preflight(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	auth := rt.reqs[1]
	if auth.SessionMounts[sandbox.HomeDir+"/.claude"] == "" || auth.Env["CLAUDE_CONFIG_DIR"] == "" {
		t.Errorf("auth check must see the persisted login: mounts=%v env=%v", auth.SessionMounts, auth.Env)
	}
}

func TestRunAndPreflightPassToolVersions(t *testing.T) {
	pins := map[string]string{"agy": "1.4.10"}
	r, rt := newRunner()
	req := baseReq(t)
	req.ToolVersions = pins
	req.AuthCheckCommand = "true"
	if _, err := r.Preflight(context.Background(), req); err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if _, err := r.Run(context.Background(), req); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(rt.reqs) != 3 {
		t.Fatalf("requests = %d, want 3 (binary check, auth check, run)", len(rt.reqs))
	}
	for i, sr := range rt.reqs {
		if sr.ToolVersions["agy"] != "1.4.10" {
			t.Errorf("request %d lacks the pin: %v", i, sr.ToolVersions)
		}
	}
}
