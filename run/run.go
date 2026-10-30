package run

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/baby-hero-404/aicli/profile"
	"github.com/baby-hero-404/aicli/rules"
	"github.com/baby-hero-404/aicli/run/stream"
	"github.com/baby-hero-404/aicli/sandbox"
)

// ErrInvalidRequest marks a permanent configuration problem (missing command, bad path).
// Retrying cannot help: the same request fails identically until it is fixed.
var ErrInvalidRequest = errors.New("run: invalid request")

const (
	preflightTimeout = 30 * time.Second
	// DefaultTimeout is the wall-clock backstop when a request sets none. Stalls are caught by
	// the idle timeout, not by this cap, so it is generous: long agentic runs are normal.
	DefaultTimeout = 5 * time.Hour

	// stateDirName is created inside the working directory for the prompt and context files
	// and removed afterwards.
	stateDirName = ".aicli"

	captureStart = "===AICLI_CAPTURE_START:"
	captureEnd   = "===AICLI_CAPTURE_END==="
)

// Request describes one headless CLI invocation.
type Request struct {
	// Profile is the profile ref (e.g. "claude_code") selecting output-classification rules;
	// empty or unknown refs use the generic rules.
	Profile string
	// Command and Args are the CLI to run. Args may contain {prompt_file} and {workdir}.
	Command string
	Args    []string
	// AuthCheckCommand, when set, is run by Preflight to verify the login.
	AuthCheckCommand string
	// Prompt is the task text. It is written to a file the CLI is pointed at, not passed in
	// argv, to avoid argument length and escaping limits.
	Prompt string

	// Workspace is the host directory the runtime exposes to the CLI (bind-mounted at
	// /workspace in a container).
	Workspace string
	// WorkDir is the directory to run in, in the runtime's own paths (see sandbox.Layout);
	// default is the layout's workspace, /workspace in a container.
	WorkDir string
	Image   string
	Env     map[string]string
	// NetworkMode is passed to the sandbox runtime.
	NetworkMode string
	// Timeout is the absolute cap (default DefaultTimeout); IdleTimeout kills a silent
	// process (zero uses the sandbox default).
	Timeout     time.Duration
	IdleTimeout time.Duration

	// CredentialFiles maps a path relative to the sandbox HOME (e.g. ".claude/.credentials.json")
	// to its content. Paths cannot escape HOME.
	CredentialFiles map[string]string
	// SessionDir is this account's host directory for persistent CLI state (see
	// sandbox.SessionDir). When set, each of the profile's StateDirs is mounted from a
	// subdirectory of it, so logins and sessions persist across runs and accounts never share
	// state. Requires a known Profile.
	SessionDir string
	// SessionMounts maps additional container directories to host directories. Entries here
	// take precedence over the ones derived from SessionDir.
	SessionMounts map[string]string
	// ContextFiles are extra files (relative path -> content) placed under
	// <WorkDir>/.aicli/context; the directory is exported as AICLI_CONTEXT_DIR.
	ContextFiles map[string]string
	// CaptureFiles are paths relative to WorkDir whose content is read back after the run.
	CaptureFiles []string
	// ResumeSessionID continues a previous session when the profile supports it.
	ResumeSessionID string
	// ToolVersions pins host-installed CLI versions for this run (tool -> version); empty
	// means each tool's current version. It only has an effect when the runtime is wrapped with
	// toolchain.Wrap. Set it from server-side state, never from client input.
	ToolVersions map[string]string
	// ExtraArgs are appended after Args and any resume arguments.
	ExtraArgs []string
	// Setup and Cleanup are shell snippets run before the CLI and after it; Cleanup always runs.
	Setup, Cleanup string
	// OnEvent, if set, receives the CLI's output as normalized events while it runs. The
	// profile must support streaming (profile.Profile.StreamArgs) or Run fails with
	// ErrInvalidRequest. The CLI is switched to its streaming output format, so Result.Output
	// then holds NDJSON events rather than a JSON summary. Events are redacted before they are
	// passed on and never carry the CLI's session ID (see Result.SessionID). OnEvent is called
	// from the runtime's output goroutine, one event at a time, and is not called after Run
	// returns.
	OnEvent func(stream.Event)

	// TaskID names the run for the sandbox runtime (workspace naming, logs).
	TaskID      string
	LogsHostDir string
	LogFilePath string
}

// Result is the classified outcome of a run.
type Result struct {
	// Success is true only for a clean exit with none of the failure signals below.
	Success  bool
	ExitCode int
	// Output is the combined stdout/stderr with secrets redacted and capture blocks removed.
	Output string
	// Command is the redacted shell-quoted invocation, for logs.
	Command string
	// Error is a human-readable reason when Success is false.
	Error string

	LoopKilled     bool
	IdleTimeoutHit bool
	// QuotaExceeded means the credential hit a rate or usage limit (transient);
	// QuotaCooldown is the parsed wait, when the CLI reported one.
	QuotaExceeded bool
	QuotaCooldown time.Duration
	// AuthInvalid means the output matches a "not authenticated" signature;
	// AuthInvalidConfirmed means it matched a profile-specific one (safe to treat as permanent).
	AuthInvalid          bool
	AuthInvalidConfirmed bool
	// AwaitingInput means the process stopped to ask a question nobody could answer.
	AwaitingInput bool

	// Files holds the CaptureFiles that existed after the run.
	Files map[string]string
	// UpdatedCredentialFiles holds credential files the CLI rewrote during the run (a refreshed
	// token), keyed by path relative to HOME. Persist them or the next run replays a stale one.
	UpdatedCredentialFiles map[string]string
	// SessionID is the CLI's own session/conversation ID as reported on the event stream. It
	// is only set when Request.OnEvent was used and the CLI reported one. Treat it like a
	// credential for the conversation: keep it server side.
	SessionID string
	// SessionMismatch is true when the run was asked to resume ResumeSessionID but the CLI
	// started a different session (agy does this without any error for an unknown ID). The run
	// was stopped, no events were forwarded from that point, and SessionID holds the unwanted
	// new session's ID, which the caller should discard. Only set when OnEvent was used.
	SessionMismatch bool
	// SkippedLines counts stdout lines the stream decoder could not parse (OnEvent only).
	SkippedLines int
	// Telemetry is set when the CLI reported usage.
	Telemetry *Telemetry
}

// Runner runs CLIs on a sandbox runtime.
type Runner struct {
	Runtime sandbox.Runtime
}

// layout returns where the runtime places req's workspace and HOME.
func (r *Runner) layout(req Request) (sandbox.Layout, error) {
	l, err := sandbox.LayoutOf(r.Runtime, sandbox.CommandRequest{TaskID: req.TaskID, Workspace: req.Workspace})
	if err != nil {
		return sandbox.Layout{}, fmt.Errorf("%w: sandbox layout: %w", ErrInvalidRequest, err)
	}
	return l, nil
}

// registryRun returns req.Profile when the CLI invocation Run builds is exactly the built-in
// profile's own: same command and arguments, and no caller-supplied extra arguments or shell
// snippets. Otherwise it returns "". The result is sandbox.CommandRequest.Profile, which runtimes
// without isolation use to decide whether a run is allowed, so anything that could change what
// the CLI does must make it empty.
func registryRun(req Request) string {
	p, ok := profile.Lookup(req.Profile)
	if !ok || req.Command != p.Command || !slices.Equal(req.Args, p.Args) {
		return ""
	}
	if len(req.ExtraArgs) > 0 || req.Setup != "" || req.Cleanup != "" {
		return ""
	}
	return req.Profile
}

// registryPreflight is registryRun for Preflight, which runs the profile's auth check instead of
// its arguments.
func registryPreflight(req Request) string {
	p, ok := profile.Lookup(req.Profile)
	if !ok || req.Command != p.Command || req.AuthCheckCommand != p.AuthCheckCommand {
		return ""
	}
	return req.Profile
}

// sessionLayout returns the mounts and environment that keep the profile's state in place:
// the profile's StateEnv (so the CLI writes only inside its StateDirs) and, when SessionDir is
// set, one mount per StateDir. Explicit Env and SessionMounts entries win.
func sessionLayout(req Request, layout sandbox.Layout) (map[string]string, map[string]string, error) {
	p, known := profile.Lookup(req.Profile)
	if req.SessionDir != "" {
		if !known {
			return nil, nil, fmt.Errorf("%w: SessionDir needs a known profile, got %q", ErrInvalidRequest, req.Profile)
		}
		if !filepath.IsAbs(req.SessionDir) {
			return nil, nil, fmt.Errorf("%w: SessionDir must be an absolute host path", ErrInvalidRequest)
		}
	}

	env := withDefaults(layout.Home, nil)
	if known {
		for k, v := range p.Env(layout.Home) {
			env[k] = v
		}
	}
	for k, v := range req.Env {
		env[k] = v
	}
	env["CI"] = "1" // never let a CLI think it may prompt

	var mounts map[string]string
	if req.SessionDir != "" {
		mounts = make(map[string]string, len(p.StateDirs)+len(req.SessionMounts))
		for _, dir := range p.StateDirs {
			host := filepath.Join(req.SessionDir, filepath.FromSlash(dir))
			if err := os.MkdirAll(host, 0o755); err != nil {
				return nil, nil, fmt.Errorf("run: create session state dir: %w", err)
			}
			mounts[layout.Home+"/"+dir] = host
		}
	}
	if len(req.SessionMounts) > 0 && mounts == nil {
		mounts = make(map[string]string, len(req.SessionMounts))
	}
	for k, v := range req.SessionMounts {
		mounts[k] = v
	}
	return mounts, env, nil
}

// Preflight verifies that the CLI binary exists in the sandbox and, when AuthCheckCommand is set,
// that the login is valid. A non-empty warning means the check could not be strict (no
// auth check configured); err is non-nil when the run should not proceed.
func (r *Runner) Preflight(ctx context.Context, req Request) (warning string, err error) {
	if strings.TrimSpace(req.Command) == "" {
		return "", fmt.Errorf("%w: command is required", ErrInvalidRequest)
	}
	layout, err := r.layout(req)
	if err != nil {
		return "", err
	}
	mounts, env, err := sessionLayout(req, layout)
	if err != nil {
		return "", err
	}
	creds := credentialTargets(layout.Home, req.CredentialFiles)
	base := sandbox.CommandRequest{
		TaskID: req.TaskID, Workspace: req.Workspace, Image: req.Image,
		NetworkMode: req.NetworkMode, Timeout: preflightTimeout, CredentialFiles: creds,
		SessionMounts: mounts, ToolVersions: req.ToolVersions, Profile: registryPreflight(req),
	}

	check := base
	check.Command = []string{"bash", "-lc", "command -v " + shellQuote(req.Command) + " >/dev/null 2>&1"}
	check.Env = map[string]string{"CI": "1", "HOME": layout.Home}
	res, err := r.Runtime.Run(ctx, check)
	if err != nil {
		return "", fmt.Errorf("run: preflight failed to run: %w", err)
	}
	if res.ExitCode != 0 {
		return "", fmt.Errorf("run: command %q not found in sandbox", req.Command)
	}

	if strings.TrimSpace(req.AuthCheckCommand) == "" {
		if len(req.Env) == 0 {
			return "no auth check command and no env configured: authentication is not verified, so a stale token fails mid-run instead of at preflight", nil
		}
		return "", nil
	}

	auth := base
	auth.Command = []string{"bash", "-lc", req.AuthCheckCommand}
	auth.Env = env
	authRes, err := r.Runtime.Run(ctx, auth)
	if err != nil {
		return "", fmt.Errorf("run: auth check failed to run: %w", err)
	}
	if authRes.ExitCode != 0 {
		return "", fmt.Errorf("run: auth check exited %d: %s", authRes.ExitCode, RedactSecrets(strings.TrimSpace(authRes.Stderr)))
	}
	// Some CLIs' status commands exit 0 regardless of login state and report it in their output,
	// so exit code alone would let a logged-out credential through. The output is the
	// command's own small response, so any confidence level blocks here.
	out := joinOutput(authRes.Stdout, authRes.Stderr)
	if rules.DetectAuthInvalid(req.Profile, out) != rules.AuthInvalidNone {
		return "", fmt.Errorf("run: auth check reports not authenticated: %s", RedactSecrets(strings.TrimSpace(out)))
	}
	return "", nil
}

// Run executes the CLI and classifies the outcome. A non-nil error means the run could not be
// carried out (bad request, sandbox failure); a CLI that ran and failed is reported in the Result.
func (r *Runner) Run(ctx context.Context, req Request) (*Result, error) {
	if strings.TrimSpace(req.Command) == "" {
		return nil, fmt.Errorf("%w: command is required", ErrInvalidRequest)
	}
	var sink *eventSink
	if req.OnEvent != nil {
		var err error
		if sink, err = newEventSink(req.Profile, req.ResumeSessionID, req.OnEvent); err != nil {
			return nil, err
		}
	}
	layout, err := r.layout(req)
	if err != nil {
		return nil, err
	}
	workDir := req.WorkDir
	if workDir == "" {
		workDir = layout.Workspace
	}
	stateDir := workDir + "/" + stateDirName
	promptFile := stateDir + "/prompt.md"

	hostState, err := hostPath(req.Workspace, layout.Workspace, stateDir)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	if err := os.MkdirAll(hostState, 0o755); err != nil {
		return nil, fmt.Errorf("run: create state dir: %w", err)
	}
	// Backup cleanup for when the in-script removal never runs (e.g. container creation fails).
	defer os.RemoveAll(hostState)

	if err := writeContext(filepath.Join(hostState, "context"), req.ContextFiles); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(hostState, "prompt.md"), []byte(req.Prompt), 0o644); err != nil {
		return nil, fmt.Errorf("run: write prompt file: %w", err)
	}

	args := make([]string, 0, len(req.Args)+len(req.ExtraArgs)+2)
	for _, a := range req.Args {
		a = strings.ReplaceAll(a, "{prompt_file}", promptFile)
		a = strings.ReplaceAll(a, "{workdir}", workDir)
		args = append(args, a)
	}
	if sink != nil {
		var err error
		if args, err = profile.StreamingArgs(req.Profile, args); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
		}
	}
	args = append(args, profile.ResumeArgs(req.Profile, req.ResumeSessionID)...)
	args = append(args, req.ExtraArgs...)

	invocation := make([]string, 0, len(args)+1)
	invocation = append(invocation, shellQuote(req.Command))
	for _, a := range args {
		invocation = append(invocation, shellQuote(a))
	}
	quoted := strings.Join(invocation, " ")

	var capture strings.Builder
	for _, rel := range req.CaptureFiles {
		abs := workDir + "/" + strings.TrimPrefix(rel, "/")
		fmt.Fprintf(&capture, " ; echo %s ; if [ -f %s ]; then base64 %s; fi ; echo %s",
			shellQuote(captureStart+rel), shellQuote(abs), shellQuote(abs), shellQuote(captureEnd))
	}
	setup := ""
	if req.Setup != "" {
		setup = req.Setup + " && "
	}
	cleanup := ""
	if req.Cleanup != "" {
		cleanup = req.Cleanup + "; "
	}
	script := fmt.Sprintf("cd %s && %s%s; status=$?%s; %srm -rf %s; exit $status",
		shellQuote(workDir), setup, quoted, capture.String(), cleanup, shellQuote(stateDir))

	mounts, env, err := sessionLayout(req, layout)
	if err != nil {
		return nil, err
	}
	if len(req.ContextFiles) > 0 {
		env["AICLI_CONTEXT_DIR"] = stateDir + "/context"
	}
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	creds := credentialTargets(layout.Home, req.CredentialFiles)

	sreq := sandbox.CommandRequest{
		TaskID: req.TaskID, Workspace: req.Workspace, Image: req.Image,
		Command: []string{"bash", "-lc", script}, Env: env, NetworkMode: req.NetworkMode,
		Timeout: timeout, IdleTimeout: req.IdleTimeout, CredentialFiles: creds,
		SessionMounts: mounts, LogsHostDir: req.LogsHostDir, LogFilePath: req.LogFilePath,
		ToolVersions: req.ToolVersions, Profile: registryRun(req),
	}
	var lines *stream.LineWriter
	if sink != nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(ctx)
		defer cancel()
		sink.onMismatch = cancel
		lines = stream.NewLineWriter(0, sink.line)
		sreq.Output = lines
	}
	out, err := r.Runtime.Run(ctx, sreq)
	if sink != nil {
		lines.Flush()
		sink.flushText()
		if sink.mismatch {
			// The run was cut short on purpose, so whatever the runtime returned is moot.
			return &Result{
				SessionMismatch: true, SessionID: sink.sessionID,
				Error: "run: the CLI started a different session than the one to resume",
			}, nil
		}
	}
	if err != nil {
		return nil, fmt.Errorf("run: sandbox run failed: %w", err)
	}
	res := classify(req, layout.Home, out, quoted, sink != nil && sink.completed)
	if sink != nil {
		res.SessionID = sink.sessionID
		res.SkippedLines = sink.skipped + lines.Dropped()
	}
	return res, nil
}

// classify works out how a run ended. streamCompleted means the CLI's event stream reported a
// successful turn: with a clean exit as well, the output is the assistant's own text and tool
// output (a reply about HTTP 429 or "401 Unauthorized"), so the failure signatures are not
// searched for in it.
func classify(req Request, home string, out *sandbox.CommandResult, quotedCommand string, streamCompleted bool) *Result {
	combined, files := extractCaptured(joinOutput(out.Stdout, out.Stderr))
	telemetry, telemetryOK := ParseTelemetry(combined)

	killed := DetectLoop(combined) || out.Killed
	idle := out.Killed && out.KillReason == sandbox.KillReasonIdleTimeout
	cleanSuccess := streamCompleted && out.ExitCode == 0 && !out.Killed
	var confidence rules.AuthInvalidConfidence
	var quota bool
	var cooldown time.Duration
	if !cleanSuccess {
		confidence = rules.DetectAuthInvalid(req.Profile, combined)
		// Auth failure is permanent while quota is transient, so it wins when both match: a
		// quota cooldown must not mask a credential that will never work.
		if confidence == rules.AuthInvalidNone {
			quota, cooldown = rules.DetectQuotaExceeded(req.Profile, combined, out.ExitCode)
		}
	}
	authInvalid := confidence != rules.AuthInvalidNone
	confirmed := confidence == rules.AuthInvalidConfirmed
	// Checked regardless of exit code: some CLIs print a question and exit 0.
	awaiting := !killed && !authInvalid && rules.DetectAwaitingInput(lastNonEmptyLine(combined))

	res := &Result{
		Success:  out.ExitCode == 0 && !killed && !awaiting && !authInvalid,
		ExitCode: out.ExitCode,
		Output:   RedactSecrets(combined),
		Command:  RedactSecrets(quotedCommand),

		LoopKilled: killed, IdleTimeoutHit: idle,
		QuotaExceeded: quota, QuotaCooldown: cooldown,
		AuthInvalid: authInvalid, AuthInvalidConfirmed: confirmed,
		AwaitingInput: awaiting, Files: files,
		UpdatedCredentialFiles: relativeToHome(home, out.UpdatedCredentialFiles),
	}
	if telemetryOK {
		res.Telemetry = &telemetry
	}
	last := lastNonEmptyLine(combined)
	switch {
	case idle:
		res.Error = "no stdout/stderr activity within the idle timeout; killed as stalled"
	case killed:
		res.Error = "repeated error output detected; killed as a stuck loop"
	case confirmed:
		res.Error = RedactSecrets("credential not authenticated (permanent, will not retry): " + last)
	case authInvalid:
		res.Error = RedactSecrets("possible auth failure, unconfirmed signature (will retry): " + last)
	case quota:
		res.Error = RedactSecrets("rate limit or quota exceeded (will cool down and retry): " + last)
	case awaiting:
		res.Error = RedactSecrets("process appears to be waiting for input: " + last)
	case out.ExitCode != 0:
		res.Error = fmt.Sprintf("cli exited with status %d%s", out.ExitCode, exitReason(out.ExitCode))
	}
	return res
}

func exitReason(code int) string {
	switch code {
	case 126:
		return " (command cannot execute: permission denied)"
	case 127:
		return " (command not found)"
	case 130:
		return " (terminated by SIGINT)"
	case 137:
		return " (killed, likely out of memory: SIGKILL)"
	case 139:
		return " (segmentation fault)"
	case 143:
		return " (terminated by SIGTERM)"
	}
	return ""
}

// extractCaptured pulls capture blocks out of combined output, returning the output without
// them and the decoded files keyed by requested path.
func extractCaptured(combined string) (string, map[string]string) {
	files := make(map[string]string)
	for {
		start := strings.Index(combined, captureStart)
		if start < 0 {
			break
		}
		end := strings.Index(combined[start:], captureEnd)
		if end < 0 {
			break
		}
		end += start
		block := combined[start+len(captureStart) : end]
		rel, encoded := block, ""
		if nl := strings.IndexByte(block, '\n'); nl >= 0 {
			rel, encoded = block[:nl], block[nl+1:]
		}
		rel, encoded = strings.TrimSpace(rel), strings.TrimSpace(encoded)
		if rel != "" && encoded != "" {
			if decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(encoded, "\n", "")); err == nil {
				files[rel] = string(decoded)
			}
		}
		combined = combined[:start] + combined[end+len(captureEnd):]
	}
	if len(files) == 0 {
		return combined, nil
	}
	return combined, files
}

// credentialTargets maps HOME-relative credential paths to absolute sandbox paths, dropping
// anything that would resolve outside HOME.
func credentialTargets(home string, files map[string]string) map[string]string {
	if len(files) == 0 {
		return nil
	}
	out := make(map[string]string, len(files))
	for rel, content := range files {
		clean := filepath.Clean("/" + rel)
		if clean == "/" {
			continue
		}
		out[home+clean] = content
	}
	return out
}

func relativeToHome(home string, files map[string]string) map[string]string {
	if len(files) == 0 {
		return nil
	}
	out := make(map[string]string, len(files))
	for target, content := range files {
		if rel, ok := strings.CutPrefix(target, home+"/"); ok {
			out[rel] = content
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func writeContext(root string, files map[string]string) error {
	for rel, content := range files {
		target := filepath.Join(root, filepath.FromSlash(rel))
		// Refuse anything resolving outside root rather than trusting the caller's paths.
		if r, err := filepath.Rel(root, target); err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
			return fmt.Errorf("%w: context file path %q escapes the context dir", ErrInvalidRequest, rel)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("run: create context dir for %s: %w", rel, err)
		}
		if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
			return fmt.Errorf("run: write context file %s: %w", rel, err)
		}
	}
	return nil
}

// hostPath maps a path under the runtime's workspace (workspaceDir, /workspace in a container)
// to its host location under hostWorkspace.
func hostPath(hostWorkspace, workspaceDir, containerPath string) (string, error) {
	if hostWorkspace == "" {
		return "", fmt.Errorf("workspace is required to map container path %q", containerPath)
	}
	if containerPath == workspaceDir {
		return hostWorkspace, nil
	}
	rel, ok := strings.CutPrefix(containerPath, workspaceDir+"/")
	if !ok {
		return "", fmt.Errorf("container path %q is outside the workspace mount %q", containerPath, workspaceDir)
	}
	return filepath.Join(hostWorkspace, rel), nil
}

func withDefaults(home string, env map[string]string) map[string]string {
	out := make(map[string]string, len(env)+2)
	for k, v := range env {
		out[k] = v
	}
	out["CI"] = "1"
	if _, ok := out["HOME"]; !ok {
		out["HOME"] = home
	}
	return out
}

func joinOutput(stdout, stderr string) string {
	if strings.TrimSpace(stderr) == "" {
		return stdout
	}
	if stdout == "" {
		return stderr
	}
	return stdout + "\n" + stderr
}

func lastNonEmptyLine(s string) string {
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if t := strings.TrimSpace(lines[i]); t != "" {
			return t
		}
	}
	return ""
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
