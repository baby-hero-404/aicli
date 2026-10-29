package sandbox

import (
	"context"
	"fmt"
	"io"
	"maps"
	"strings"
	"time"
)

// Network modes for CommandRequest.NetworkMode.
const (
	// NetworkModeDefault lets the runtime decide (the Docker runtime uses bridge unless
	// networking is disabled in its config).
	NetworkModeDefault = ""
	NetworkModeNone    = "none"
	NetworkModeBridge  = "bridge"
)

// HomeDir is $HOME for the sandbox image's runtime user (the
// sandbox image runs as USER "agent", not root — confirmed via
// `docker image inspect`). CLI credential injection (CredentialFiles) and
// SessionMounts must target paths under here, not /root: mounting to /root succeeds silently, but a
// CLI process running as "agent" has $HOME=/home/agent and never looks
// there, so auth always appears missing even though the mount worked.
const HomeDir = "/home/agent"

// SandboxHomeDir is an alias for HomeDir for backwards-compatibility.
const SandboxHomeDir = HomeDir

// CommandRequest describes one command to run in a sandbox.
type CommandRequest struct {
	// TaskID names the run; it picks the workspace directory when Workspace is empty.
	TaskID string
	// Workspace is the host directory mounted at /workspace.
	Workspace string
	// Command is the argv to execute.
	Command []string
	// Env is the process environment. SecretEnv is merged over it and is only allowed with
	// network egress disabled (see ValidateRequest).
	Env       map[string]string
	SecretEnv map[string]string
	// NetworkMode is NetworkModeDefault, NetworkModeNone or NetworkModeBridge.
	NetworkMode string
	// Timeout is the absolute run time limit; zero means none beyond the context.
	Timeout time.Duration
	// ResizeCh optionally delivers PTY size updates for RunInteractive
	// sessions (e.g. driven by the browser terminal's container size).
	// Runtimes that don't support TTY resize may ignore it.
	ResizeCh <-chan TerminalSize
	// CredentialFiles optionally materializes decrypted CLI credential
	// payloads into the container at specific absolute paths (e.g.
	// "/home/agent/.claude/.credentials.json" -> file content), keyed by target container path.
	// Runtimes that support it must bind-mount these read-write (some CLIs
	// refresh tokens on use) and must take priority over any other
	// convenience credential mount that would target the same path.
	CredentialFiles map[string]string

	// LogsHostDir, if set, is bind-mounted read-write at the fixed container
	// path LogsContainerDir. Tools running inside the container can write logs
	// there so they survive container removal and are visible on the host
	// outside the workspace. Runtimes that don't support
	// bind mounts (e.g. StubRuntime) may ignore it.
	LogsHostDir string

	// SessionMounts defines explicit bind mounts (container absolute path -> host absolute path).
	// The caller maps specific persistence directories (like /home/agent/.claude) to isolated
	// per-account folders under its own data root. Nothing from the host user's HOME is ever
	// mounted implicitly; a run with no SessionMounts starts from the image's fresh /home/agent.
	SessionMounts map[string]string

	// LogFilePath, if set, is an absolute host path a supporting runtime
	// streams the subprocess's combined stdout/stderr into in real time
	// (io.MultiWriter alongside the buffered CommandResult.Stdout/Stderr) as
	// it runs, not just after exit — so a persistent log survives a mid-run
	// container kill. Conventionally a
	// file under LogsHostDir, but the two are independent knobs.
	LogFilePath string

	// ReadOnlyMounts are bind mounts the sandbox cannot write: container target path -> host
	// source path, a file or a directory that must already exist. Used for CLI tools installed
	// on the host (see package toolchain). Unlike SessionMounts, nothing is created on the host.
	ReadOnlyMounts map[string]string

	// ToolVersions pins versions of host-installed CLI tools for this request (tool name ->
	// version, e.g. "agy" -> "1.4.10"). Runtimes ignore it; toolchain.Wrap reads it and turns it
	// into ReadOnlyMounts.
	ToolVersions map[string]string

	// Output, if set, receives the subprocess's stdout as it is produced, in addition to
	// CommandResult.Stdout. Only stdout: stderr is not forwarded. A supporting runtime calls
	// Write from its own goroutine while Run is in progress and stops calling it before Run
	// returns; writes are not synchronized with the caller, so Output must not be shared with
	// code that reads it concurrently. Runtimes without live output (StubRuntime) write the
	// final stdout once.
	Output io.Writer

	// IdleTimeout, if > 0, overrides DefaultIdleTimeout: a supporting runtime kills the container if no
	// stdout/stderr byte is observed for this long, distinct from Timeout
	// (an absolute cap regardless of activity) — a long but genuinely
	// active run (e.g. a real 1.5h refactor that keeps printing progress)
	// never trips this, while a hung/silent process does long before
	// Timeout would fire. Zero means "use DefaultIdleTimeout", never
	// "disabled" — callers that truly want no idle enforcement (there are
	// none today) would need a distinct negative sentinel, not added since
	// unused. Runtimes without live log streaming (e.g. StubRuntime) ignore
	// it.
	IdleTimeout time.Duration

	// Image, if non-empty, overrides the runtime's configured image for this single
	// request. Manager fills it from the detected runtime's manifest.
	Image string

	// ExtraCacheMounts are writable package-cache bind mounts (container path -> host path).
	// Manager fills them from the detected runtime manifest's cache list when it has a cache
	// root; nothing else is mounted for caches.
	ExtraCacheMounts map[string]string

	// CacheScope namespaces the package-install caches mounted by Manager when it has
	// a cache root (see WithCacheRoot): runs with the same scope share caches, runs with
	// different scopes never do. Use a project or organization ID to keep tenants apart;
	// empty means DefaultCacheScope. Must be a plain name ([A-Za-z0-9._-], max 64).
	CacheScope string
}

// DefaultIdleTimeout is the idle-activity timeout applied when
// CommandRequest.IdleTimeout is unset.
const DefaultIdleTimeout = 15 * time.Minute

// LogsContainerDir is the fixed in-container path CommandRequest.LogsHostDir
// is bind-mounted to.
const LogsContainerDir = "/var/log/aicli"

// TerminalSize describes a PTY's dimensions in character cells.
type TerminalSize struct {
	Rows uint
	Cols uint
}

// CommandResult is the outcome of a Run.
type CommandResult struct {
	ExitCode int
	Stdout   string
	Stderr   string

	// UpdatedCredentialFiles holds the post-run content of every path in
	// CommandRequest.CredentialFiles whose content changed during the run
	// (e.g. a CLI silently refreshing an OAuth token on use), keyed the same
	// way as CredentialFiles (target container path -> content). Only
	// populated by runtimes where the credential mount is a live bind (not a
	// copy), so a write inside the container is visible on the host the
	// moment the container stops — nil/empty otherwise.
	UpdatedCredentialFiles map[string]string

	// Killed is true when the runtime itself force-terminated the container
	// (idle timeout or loop detection) rather than the subprocess
	// exiting on its own. Run still returns a normal (nil-error) result
	// with whatever Stdout/Stderr was captured up to the kill — the caller
	// decides how to treat it via KillReason, same as it already does for
	// non-zero ExitCode.
	Killed bool
	// KillReason is "idle_timeout", "loop_detected", or "oom_killed" when
	// Killed is true, "" otherwise.
	KillReason string
}

// Values of CommandResult.KillReason.
const (
	// KillReasonIdleTimeout: no output for longer than the idle timeout.
	KillReasonIdleTimeout = "idle_timeout"
	// KillReasonLoopDetected: the same output line repeated past the loop threshold.
	KillReasonLoopDetected = "loop_detected"
	// KillReasonOOM marks a container terminated by the kernel/cgroup OOM
	// killer rather than by our own watchForStall watchdog: the process
	// receives SIGKILL externally (exit code 137) with no stallReason set,
	// so without this the run looked like a plain crash instead of a
	// resumable interruption.
	KillReasonOOM = "oom_killed"
)

// Runtime executes sandboxed commands. sandbox/docker provides the Docker implementation.
type Runtime interface {
	Run(ctx context.Context, req CommandRequest) (*CommandResult, error)
	RunInteractive(ctx context.Context, req CommandRequest, stdin io.Reader, stdout, stderr io.Writer) error
	Prewarm(ctx context.Context) error
}

// StubRuntime validates requests but executes nothing. Useful for local development without
// Docker; for tests prefer sandbox/sandboxmock, which records requests and scripts results.
type StubRuntime struct{}

// NewStubRuntime returns a StubRuntime.
func NewStubRuntime() *StubRuntime {
	return &StubRuntime{}
}

// RunInteractive implements Runtime and does nothing.
func (r *StubRuntime) RunInteractive(ctx context.Context, req CommandRequest, stdin io.Reader, stdout, stderr io.Writer) error {
	return nil
}

// Prewarm implements Runtime and does nothing.
func (r *StubRuntime) Prewarm(ctx context.Context) error {
	return nil
}

// Run validates req and returns a fixed successful result without executing anything.
func (r *StubRuntime) Run(ctx context.Context, req CommandRequest) (*CommandResult, error) {
	// The stub runs nothing, so treat every request as network-isolated.
	if err := ValidateRequest(req, NetworkModeNone); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	stdout := fmt.Sprintf("stub sandbox executed: %s", strings.Join(req.Command, " "))
	if req.Output != nil {
		_, _ = io.WriteString(req.Output, stdout)
	}
	return &CommandResult{
		ExitCode: 0,
		Stdout:   stdout,
		Stderr:   "",
	}, nil
}

// MergedEnv returns Env overlaid with SecretEnv, the environment a runtime passes to the process.
func (req CommandRequest) MergedEnv() map[string]string {
	env := make(map[string]string, len(req.Env)+len(req.SecretEnv))
	maps.Copy(env, req.Env)
	maps.Copy(env, req.SecretEnv)
	return env
}
