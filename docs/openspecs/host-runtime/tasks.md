# Implementation Map: Host process runtime

**Goal:** `sandbox/host`, a `sandbox.Runtime` that runs tool-less CLIs as host processes, usable by `run`, `capture` and `conversation` unchanged.
**Tech Stack:** Go standard library (`os/exec`, `syscall`), `github.com/creack/pty` v1.1.24, existing `sandbox` / `run` / `capture` / `profile`.

---

## Phase 1: Layout and profile marker (no behavior change)

### Runtime-reported layout

**Why:**
`run` and `capture` must stop hard-coding container paths before a runtime with different paths can work.

**Files:**
- `sandbox/layout.go`, `sandbox/layout_test.go`, `sandbox/sandbox.go`
- `sandbox/manager.go`, `toolchain/runtime.go`, `sandbox/sandboxmock/sandboxmock.go`
- `run/run.go`, `run/run_test.go`, `capture/capture.go`, `capture/capture_test.go`

**Changes:**
- [x] `sandbox.WorkspaceDir`, `Layout`, `ContainerLayout`, `LayoutProvider`, `LayoutOf`
- [x] `Manager` and `toolchain.Runtime` implement `Layout` by delegating
- [x] `sandboxmock` gets an optional layout so tests can simulate a host-style runtime
- [x] `run`: replace `containerRoot` and every `sandbox.HomeDir` use with the computed layout (`sessionLayout`, `credentialTargets`, `relativeToHome`, `withDefaults`, `hostPath`, `Preflight`)
- [x] `capture`: `HOME=layout.Workspace`; optional `Session.Profile` merges `profile.Env(layout.Workspace)`

**Verify:**
- [x] All existing tests pass unchanged (container layout by default)
- [x] New table test: with a mock layout `{Workspace: /tmp/x/ws, Home: /tmp/x/home}`, the request `run` builds has no `/workspace` or `/home/agent` anywhere in Command, Env or mount keys
- [x] Decorators return the inner runtime's layout
- [x] `toolchain` test: with an empty store and no pins, `Wrap` leaves the request identical (no `ReadOnlyMounts`, no env change) — documents the host-runtime passthrough

### Profile marker

**Why:**
The host runtime needs a server-side, registry-backed signal that a run has no tools.

**Files:**
- `profile/profile.go`, `profile/profile_test.go`, `sandbox/sandbox.go`, `run/run.go`, `run/run_test.go`

**Changes:**
- [x] `Profile.NoTools`, true for `claude_code_notools` and `antigravity_notools`
- [x] `CommandRequest.Profile`, set in `Run` only when `Command` and `Args` equal the registry's and `ExtraArgs`, `Setup`, `Cleanup` are empty; in `Preflight` only when `Command` and `AuthCheckCommand` equal the registry's

**Verify:**
- [x] `TestProfiles_NoTools` asserts the marker; every profile without the tool-disabling flags has `NoTools == false`
- [x] Table test: unmodified args → `Profile` set; changed `Args` or non-empty `ExtraArgs` → empty

## Phase 2: Shared watchdog

### Move idle/loop detection out of sandbox/docker

**Why:**
Both runtimes must report `Killed`/`KillReason` identically.

**Depends on:**
- none

**Files:**
- `sandbox/internal/watch/watch.go`, `sandbox/internal/watch/watch_test.go`
- `sandbox/docker/activity.go`, `sandbox/docker/activity_test.go`, `sandbox/docker/docker.go`

**Changes:**
- [x] Move `activityWriter`, `lineLoopDetector` and the stall-watch loop to `sandbox/internal/watch` with a kill callback
- [x] `sandbox/docker` uses it

**Cleanup:**
- [x] Delete the moved code and tests from `sandbox/docker`

**Verify:**
- [x] Moved tests pass with `-race`; `make test-docker` still passes the idle and loop cases

## Phase 3: Host runtime — Run

### Dependency check

**Why:**
R2: a new module must be verified before it is added.

**Files:**
- `go.mod`, `go.sum`

**Changes:**
- [x] Confirm `github.com/creack/pty` v1.1.24 on the official module proxy, add it at that exact version, review the `go.sum` diff (only pty and nothing else)
- [x] After `go mod download`, check `git status` and `~` for new AI-instruction files

**Verify:**
- [x] `make tidy` leaves no diff

### `sandbox/host` package

**Why:**
The runtime itself.

**Depends on:**
- Phase 1, Phase 2

**Files:**
- `sandbox/host/doc.go`, `sandbox/host/host.go`, `sandbox/host/env.go`, `sandbox/host/host_test.go`

**Changes:**
- [x] `config.go` (no tag): `Config` incl. `AllowedRoots`, sentinel errors; `host_other.go` (`//go:build !linux && !darwin`): `New` → `ErrUnsupportedOS`; process files tagged `linux || darwin`
- [x] `New` (validate `RunRoot` does not cover HOME, defaults, WARN on `AllowTools`)
- [x] `Layout(req)`: plain-name `TaskID`, workspace resolution, `Home = <RunRoot>/<TaskID>/home`
- [x] Checks in the order of design.md, each with its sentinel error
- [x] `EvalSymlinks` of workspace and session sources; directory check; `AllowedRoots` check; links created from resolved paths
- [x] Run directory `0700`, symlinked session mounts, `0600` credential files, `defer RemoveAll`
- [x] Post-run session-link check → `ErrSessionDetached`
- [x] Clean env builder (`HOME`, `TMPDIR`, `PATH`, `LANG`, `TERM` + `MergedEnv`)
- [x] Process group, `Cancel` kills the group, `WaitDelay`, timeout
- [x] Output fan-out (`Output` stdout only, `LogFilePath`, buffers, watchdog)
- [x] `UpdatedCredentialFiles`: compare against `req.CredentialFiles`, include only changed files
- [x] `Prewarm` no-op

**Verify:**
- [x] Table tests (shell commands, no real CLI) for every Scenario and Failure Scenario in specs.md that concerns `Run`
- [x] Session-link test: a command that does `rm $HOME/.claude && mkdir $HOME/.claude` → `ErrSessionDetached`; a command that only writes inside `$HOME/.claude` → file appears in the session dir
- [x] Source test: `SessionDir` as a symlink to a temp dir standing in for HOME → refused; with `AllowedRoots` set, a source outside it → refused
- [x] Credential test: unchanged file → `UpdatedCredentialFiles` nil; rewritten file → present
- [x] `GOOS=windows go build ./...` and `GOOS=windows go vet ./sandbox/host` succeed; `New` returns `ErrUnsupportedOS` (checked by a `GOOS=windows` build only, no run)
- [x] Env test: a variable set in the test process is absent in the child's `env` output
- [x] Kill test: a child that spawns `sleep 600 &` leaves no process after timeout (check by pgid)
- [x] Every test asserts `<RunRoot>/<TaskID>` is gone afterwards, including refused requests (never created)
- [x] `BenchmarkHostRun` under 50 ms p50

## Phase 4: Host runtime — RunInteractive

### PTY sessions with allowlist

**Why:**
Logins through `capture.Session` without Docker or `script -qfc`.

**Depends on:**
- Phase 3

**Files:**
- `sandbox/host/interactive.go`, `sandbox/host/interactive_test.go`, `capture/capture_test.go`

**Changes:**
- [x] Exact-argv allowlist check, then the same layout/validation as `Run`
- [x] `pty.StartWithSize` (pty sets `Setsid`+`Setctty`; do not set `Setpgid`), stdin/stdout copy, `ResizeCh` → `pty.Setsize`, kill `-pid` on ctx cancel

**Verify:**
- [x] Allowlisted `stty size` prints the size sent on `ResizeCh`
- [x] Start succeeds without `EPERM` and a cancelled session leaves no process: the test starts `sh -c 'sleep 600 & wait'` through `RunInteractive`, cancels the context, and checks that neither the shell nor the `sleep` is alive (Linux, runs in `make test`)
- [x] `/bin/bash` and a prefix of an allowlisted argv are refused with `ErrNotAllowlisted`
- [x] `capture.Session` over the host runtime with a scripted command writing `$HOME/.claude/x` returns `Files[".claude/x"]`

## Phase 5: Orphan cleanup

### `PruneOrphaned`

**Why:**
A crash must not leave decrypted credentials on disk indefinitely.

**Depends on:**
- Phase 3

**Files:**
- `sandbox/host/prune.go`, `sandbox/host/prune_test.go`, `janitor/janitor.go` (doc comment only)

**Changes:**
- [x] Remove entries under `RunRoot` older than `minAge`; refuse unsafe roots with `sandbox.ErrUnsafeRoot`
- [x] Mention `*host.Runtime` next to `*docker.Runtime` in the janitor doc

**Verify:**
- [x] Old entry removed, fresh entry kept, a symlink under `RunRoot` is removed without following it

## Phase 6: Conformance

### Same behavior on both runtimes

**Why:**
Keeps the two implementations from drifting.

**Depends on:**
- Phase 3, Phase 4

**Files:**
- `sandbox/internal/conformance/conformance.go`, `sandbox/host/conformance_test.go`, `sandbox/docker/conformance_test.go`

**Changes:**
- [x] Shared table of cases driven only through `sandbox.Runtime` + `LayoutOf`: exit codes, stdout/stderr, `Output`, `LogFilePath`, idle kill, loop kill, credential write-back, session-mount persistence, timeout
- [x] Host runs it in `make test`; Docker runs it in `make test-docker`

**Verify:**
- [x] Both suites green

## Phase 7: Documentation Sync

### Host runtime docs

**Why:**
New public package, new fields, and a security model that differs from Docker's.

**Files:**
- `docs/host-runtime.md`, `README.md`, `sandbox/doc.go`, `capture/capture.go` (Env doc comment), `CLAUDE.md` (only if a new rule is adopted)

**Changes:**
- [x] `docs/host-runtime.md`: when to use it, config, what it does and does not isolate, deployment checklist (dedicated uid, `_notools` allowlist, janitor, `PATH`), refused fields
- [x] README package table row for `sandbox/host`; the Docker-only wording in `sandbox/doc.go` updated
- [x] Capture `Env` doc refers to `Session.Profile` / the layout instead of `/workspace`

**Verify:**
- [x] `grep -rn '"/workspace"' run capture` returns only the `sandbox.WorkspaceDir` definition's users
- [x] `make verify` green

---

## Results (2026-10-30)

- `make verify` green; `make test-docker` green (image `aicli/base:latest`), including the new `TestDockerConformance`.
- The same 12-case conformance suite (`sandbox/internal/conformance`) passes against both the host runtime (`TestHostConformance`, in `make test`) and a real container.
- `BenchmarkHostRun` (`true` through `Run`): about 1.3 ms/op on this machine, against the 50 ms budget.
- `go test -race -count=10 ./sandbox/host` is stable; `GOOS=windows go build ./...` and `GOOS=darwin go build ./...` succeed.
- `creack/pty` v1.1.24: present on the official proxy, `go.sum` gained only its two lines, the module has no network, exec or `init` code, and no AI-instruction files appeared after the import.

## Deviations from the first draft

- **Credentials inside a session mount are allowed**, not refused. The draft claimed Docker avoids writing a decrypted token into the session directory; it does not (its "covered by a SessionMount" branch writes there). Refusing also broke the main use case (`SessionDir` + `.claude/.credentials.json`). The host runtime writes by temp file + rename so a symlink at the target is replaced, never followed.
- **`CommandRequest.Profile` is also withheld when `Setup` or `Cleanup` is set** (shell snippets run arbitrary commands); in `Preflight` it needs the registry's `Command` and `AuthCheckCommand`.
- **Nested session mounts are refused** (not in the draft): a link created under another link would write through it.
- **`RunInteractive` refuses `SessionMounts` and `CredentialFiles`** instead of ignoring them (Docker ignores them silently).
- **Session and workspace directories are created when missing** (like Docker), after the allowed-roots check on the resolved location.
- **`host.Runtime` has an in-memory set of active task IDs** so `PruneOrphaned` never removes the directory of a run that is still going (a run can outlive `minAge`). A directory left by a crash blocks a retry with the same `TaskID` until it is pruned.
- **The command is looked up in `Config.PATH`**, not the server's PATH. Note `bash -lc` (used by `run`) still resets PATH from `/etc/profile`; documented in `docs/host-runtime.md`.
- **Resize race found by `-race`:** `pty.Setsize` uses the descriptor directly, so the resize goroutine is joined before the PTY is closed.

---

## Rollback Plan

The change is additive: a host application opts in by constructing `host.New`. Rolling back means
constructing the Docker runtime again; Phase 1's layout changes are no-ops for container runtimes
and stay.
