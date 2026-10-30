# Expected Behavior: Host process runtime

## Scenario: Tool-less run through run.Runner
**When:**
- `run.Runner{Runtime: host.New(cfg)}` runs `claude_code_notools` with a `TaskID`, a host
  `Workspace` and a `SessionDir`

**Then:**
- the CLI starts as a child process with its working directory in the workspace
- `HOME` is `<RunRoot>/<TaskID>/home`; `CLAUDE_CONFIG_DIR` is `$HOME/.claude`, a symlink to
  `<SessionDir>/.claude`
- the prompt file path in argv is the host path `<Workspace>/.aicli/prompt.md`
- `Result` is classified exactly as for the Docker runtime (exit code, captured files, session
  ID, telemetry)
- after return, `<RunRoot>/<TaskID>` no longer exists; the session directory keeps whatever the
  CLI wrote

## Scenario: Preflight on the host
**When:**
- `run.Runner.Preflight` runs for `antigravity_notools`

**Then:**
- `command -v agy` and `agy models` run as children with the same per-run HOME layout and the
  configured `PATH`
- a missing binary yields the existing "not found in sandbox" error

## Scenario: Credential write-back
**When:**
- `CredentialFiles` holds `$HOME/.claude/.credentials.json` (no session mount over `.claude`)
  and the CLI refreshes the token during the run

**Then:**
- the file is written `0600` inside the per-run HOME before start
- `CommandResult.UpdatedCredentialFiles` holds the new content under the same key
- the file is deleted with the run directory

## Scenario: Unchanged credentials are not reported
**When:**
- the CLI runs and leaves every credential file byte-identical (or deletes none and rewrites the
  same content)

**Then:**
- `UpdatedCredentialFiles` is nil; the comparison is against the content in
  `req.CredentialFiles`, not a re-read of the disk before the run

## Scenario: Live output, log file and watchdog
**When:**
- `Output`, `LogFilePath`, `IdleTimeout` are set and the child prints, then stays silent past
  the idle timeout

**Then:**
- stdout reaches `Output` as produced; stdout and stderr reach `LogFilePath` as produced
- the process group is killed; `Run` returns a nil error with `Killed: true`,
  `KillReason: "idle_timeout"` and the output captured so far
- a line repeated past the loop threshold gives `KillReason: "loop_detected"` the same way

## Scenario: Login through capture.Session
**When:**
- `cfg.InteractiveCommands` contains `["claude", "auth", "login"]` and `capture.Session` runs with
  `Command: ["claude", "auth", "login"]`, `Profile: "claude_code"`

**Then:**
- the command runs on a PTY sized from `ResizeCh` and resized on every update
- `HOME` is the capture workspace, `CLAUDE_CONFIG_DIR` is `$HOME/.claude`
- the returned `Result.Files` has the same relative layout as with the Docker runtime

## Scenario: toolchain.Wrap over the host runtime
**When:**
- `toolchain.Wrap(hostRuntime, store)` runs a request and the store has no installed `current`
  and the request pins no version

**Then:**
- the request reaches the host runtime unchanged and runs; with an installed tool or a pin,
  `Run` returns an error wrapping `host.ErrUnsupported` naming `ReadOnlyMounts`

## Scenario: Operator allows tools
**When:**
- `cfg.AllowTools` is true and `run.Runner` runs `claude_code`

**Then:**
- the run proceeds; the runtime logs once per `New` that tools are enabled on an unisolated host

## Scenario: Layout of other runtimes
**When:**
- `sandbox.LayoutOf` is called with the Docker runtime, `sandboxmock` without a layout, or a
  `toolchain.Wrap` / `sandbox.Manager` decorator around either

**Then:**
- it returns `{Workspace: "/workspace", Home: "/home/agent"}`; around the host runtime, decorators
  return the host runtime's layout

## Failure Scenario: Profile with tools
**When:**
- `CommandRequest.Profile` is empty, unknown, or a profile without `NoTools`, and
  `cfg.AllowTools` is false

**Then:**
- `Run` returns an error wrapping `host.ErrToolsNotAllowed` naming the profile; nothing is started
  and no directory is created

## Failure Scenario: Registry args altered
**When:**
- `run.Request.Profile` is `claude_code_notools` but `Command` or `Args` differ from the
  registry's, or `ExtraArgs`, `Setup` or `Cleanup` is non-empty (a shell snippet can run anything).
  For `Preflight`: `Command` or `AuthCheckCommand` differ from the registry's

**Then:**
- `run.Runner` leaves `CommandRequest.Profile` empty, so the host runtime refuses with
  `ErrToolsNotAllowed` (the Docker runtime is unaffected)

## Failure Scenario: Interactive command not allowlisted
**When:**
- `RunInteractive` is called with an argv not exactly equal to one in `cfg.InteractiveCommands`
  (including the `capture.Session` default `/bin/bash`)

**Then:**
- it returns an error wrapping `host.ErrNotAllowlisted`; nothing is started

## Failure Scenario: Unsupported request fields
**When:**
- the request has `NetworkMode: "none"`, any `ReadOnlyMounts`, or any `ExtraCacheMounts`

**Then:**
- it returns an error wrapping `host.ErrUnsupported` naming the field; nothing is started

## Failure Scenario: Secrets with egress
**When:**
- the request has `SecretEnv`

**Then:**
- the existing `ValidateRequest` refuses it (the resolved network mode is always bridge)

## Failure Scenario: Mount or credential target outside the per-run HOME
**When:**
- a `SessionMounts` or `CredentialFiles` target is not inside the request's layout `Home`

**Then:**
- the request is refused before anything is written; a target such as `/etc/x` or
  `$HOME/../x` never causes a host write

## Scenario: Credential file inside a session mount
**When:**
- a `CredentialFiles` target lies inside a `SessionMounts` target (`SessionDir` + `.claude/.credentials.json`)

**Then:**
- the file is written into the session directory (replacing, never following, a symlink at that
  path); a refreshed value is reported in `UpdatedCredentialFiles`; the run directory removal does
  not touch the session directory

## Failure Scenario: Nested session mounts
**When:**
- one `SessionMounts` target lies inside another

**Then:**
- the request is refused with `ErrUnsupported`

## Failure Scenario: Invalid or duplicate TaskID
**When:**
- `TaskID` is empty or not a plain name, or the task already has a run in progress (also when a
  directory of that name is left over from a crash, until `PruneOrphaned` removes it)

**Then:**
- the request is refused; the existing directory is not touched

## Failure Scenario: Workspace missing or unsafe
**When:**
- `Workspace` is empty and no `cfg.WorkspaceRoot` is set, is not absolute, or covers the host
  user's HOME

**Then:**
- the request is refused (the last case by the existing `validateNoHostHome`)

## Failure Scenario: Command not found or fails to start
**When:**
- the program is not on `cfg.PATH`

**Then:**
- `Run` returns a `CommandResult` with exit code 127 when started through `bash -lc` (as in a
  container), or an error wrapping the `exec` failure when started directly

## Failure Scenario: Timeout or cancel
**When:**
- `Timeout` elapses or `ctx` is cancelled

**Then:**
- the whole process group is killed, the run directory is removed, and `Run` returns the same
  error kind the Docker runtime returns in that case

## Failure Scenario: Session link replaced by the CLI
**When:**
- during the run the CLI removes `$HOME/.claude` (a symlink the runtime created) and creates a
  real directory in its place

**Then:**
- after the process exits, `Run` returns an error wrapping `host.ErrSessionDetached` naming the
  path; the run directory is still removed; the session directory is untouched

## Failure Scenario: Mount source resolves somewhere unsafe
**When:**
- `Workspace` or a `SessionMounts` source is a symlink (or contains one in its path) whose real
  location covers the server user's HOME, is not a directory, or lies outside every entry of
  `cfg.AllowedRoots` when that list is set

**Then:**
- the request is refused before anything is created; the error names the original and the
  resolved path

## Failure Scenario: Unsupported OS
**When:**
- `host.New` is called on a platform other than Linux or macOS

**Then:**
- it returns an error wrapping `host.ErrUnsupportedOS`; `go build ./...` and `go test ./...`
  succeed on every OS because `Config`, the errors and a stub `Runtime` compile everywhere

## Invariants
- No child process ever sees the server's own environment variables; its environment is exactly
  the request's merged env plus the fixed keys set by the runtime.
- No child process ever has the server user's HOME as `HOME`, and no symlink the runtime creates
  points at that HOME or an ancestor of it.
- Every file the runtime writes lies inside `<RunRoot>/<TaskID>`, which is `0700` and removed when
  the call returns, on every path including errors and kills — except credential files that
  target a session mount, which land in that session directory by design.
- `UpdatedCredentialFiles` contains a key only if its content differs from `req.CredentialFiles`.
- No child process outlives the call that started it.
- A request is either refused before anything is created or started, or runs; there is no
  partial start.
- `LayoutOf` for every runtime that does not implement `LayoutProvider` returns the container
  layout, so the Docker path is unchanged.

## Rules
- The tool guardrail decision uses only `CommandRequest.Profile` and aicli's built-in registry.
- `run.Runner` sets `CommandRequest.Profile` in both `Run` and `Preflight`.
- `RunInteractive` matches argv exactly: no prefix match, no shell parsing. It applies the workspace
  only, like the Docker runtime: `SessionMounts` and `CredentialFiles` are refused with
  `ErrUnsupported`.
- `sandbox.ValidateRequest` runs first, with resolved network mode `bridge`, as in every runtime.
- Decorators (`toolchain.Runtime`, `sandbox.Manager`) implement `LayoutProvider` by delegating to
  the runtime they wrap.

## Constraints
- No global state, no `init()`, no `panic`; errors wrapped with context.
- `Config`, the sentinel errors and `doc.go` carry no build tag; only the process code does.
- Tests are table-driven and pass with `-race`; host tests run in `make test` (no Docker needed),
  using small shell commands, not real CLIs.
- Only new dependency: `github.com/creack/pty v1.1.24`, exact version.
- `run` still does not import `conversation`; `sandbox` imports no runtime implementation;
  `sandbox/host` may import `profile`.
