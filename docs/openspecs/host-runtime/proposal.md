# Proposal: Host process runtime

## Problem

`sandbox.Runtime` has one real implementation, `sandbox/docker`. A host application that runs
short, tool-less CLI calls (generate a text, grade an answer, explain an error) pays 1–3 s and a
container's memory per call, and cannot run at all where there is no Docker daemon (most PaaS
containers, small VMs, many developer machines).

Such hosts end up re-implementing what aicli already does — a temporary HOME per run, copying
credential files in and out, setting the profile's environment, faking a terminal with
`script -qfc` for logins — outside aicli, and lose `run.Runner`, `capture.Session` and
`conversation.Service` in the process.

## Goal

A second `sandbox.Runtime`, `sandbox/host`, that runs the CLI as a plain child process on the
host, so `run`, `capture` and `conversation` work unchanged on machines without Docker. Because a
host process is not isolated, the runtime only runs tool-less profiles unless the operator opts
in, and only runs allowlisted commands interactively.

## Success

- A host application on a machine without Docker builds `host.New(host.Config{...})`, passes it
  to `run.Runner` / `conversation.Service` / `capture.Session`, and runs `claude_code_notools` and
  `antigravity_notools` turns and logins with no aicli-external glue code.
- The runtime adds less than 50 ms to a run (p50, measured by benchmark), compared with seconds
  for a container start.
- A profile with tools, a bare login shell, or a request the host cannot honor (network `none`,
  read-only mounts) is refused with a clear error, never run with weaker guarantees than asked.
- Code written against the Docker runtime behaves the same on the host runtime for every field
  the host supports (shared conformance tests).

## Assumptions

- The CLIs (`claude`, `agy`, …) are installed on the host and resolvable through the configured
  `PATH`. Installing them is the host operator's job.
- The host runs on Linux or macOS. Windows is not supported (no process groups or PTYs as used
  here).
- The tool-less profiles really disable tools when given their flags (`--tools ""`,
  `--disable-slash-commands`, no permission bypass). aicli trusts the CLI for this; it is defense
  in depth, not isolation.
- The server process runs under a uid that is not a human's login account. The runtime cannot
  stop a child from reading what that uid can read; it only keeps credentials and state out of
  the child's HOME and environment.

## Decisions

- **New package `sandbox/host`**, named after what it adapts (OS processes), implementing
  `sandbox.Runtime` with `host.New(host.Config) (*host.Runtime, error)`. The `sandbox` package
  stays free of process code.
- **Runtime-reported layout instead of fixed container paths.** Today `run` and `capture` write
  `/workspace` and `/home/agent` into scripts, prompt sentences, env values and mount keys. A new
  optional interface `sandbox.LayoutProvider` lets a runtime say where a request's workspace and
  HOME are; `sandbox.LayoutOf` falls back to the container layout, so the Docker runtime, every
  existing caller and every existing test are unchanged. Rejected: rewriting path prefixes inside
  argv/env in the host runtime (paths are embedded in shell scripts and sentences, so a rewrite is
  guesswork), and Linux namespaces via bubblewrap (needs unprivileged user namespaces, which PaaS
  containers usually disable, and is Linux-only — it can be a later `sandbox/bwrap`).
- **Per-run HOME, never the server's.** Each request gets `<RunRoot>/<TaskID>/home` (mode
  `0700`), created at start and removed at the end. `SessionMounts` become symlinks inside it,
  `CredentialFiles` become `0600` files inside it. The initial contents are kept in memory
  (the request already holds them), and only files whose content differs after the run go into
  `UpdatedCredentialFiles`, exactly like the Docker runtime's write-back.
- **Session links must survive the run, or the run fails loudly.** A bind mount cannot be
  replaced by the process inside the container; a symlink can. If a CLI deletes a session link
  and recreates a real directory, its state would vanish with the run directory. After the
  process exits, the runtime checks every session link (`Lstat` + `Readlink`); if one is no
  longer the symlink it created, `Run` returns an error wrapping `host.ErrSessionDetached`
  naming the path, and the run directory is removed as usual. A silent loss of a refreshed
  login is worse than a failed turn. Rejected: copying the replaced directory back (merge rules
  for a tree the runtime does not own) and a bind-mount emulation (not possible without
  namespaces).
- **Mount sources are resolved before use.** Workspace and every session source go through
  `filepath.EvalSymlinks`; the resolved path must be an existing directory and must pass the
  existing HOME check (`validateNoHostHome` already resolves symlinks). `Config.AllowedRoots`
  (optional) narrows this: when set, the resolved workspace and session sources must lie inside
  one of them, so a symlinked `SessionDir` pointing at `~/.ssh` is refused. Links *inside* those
  directories are not policed: the child runs with the server's uid and could create them itself.
- **Clean environment.** The child gets only `req.MergedEnv()` plus `HOME`, `TMPDIR`, `PATH`
  (from `Config.PATH`, default `/usr/local/bin:/usr/bin:/bin`), `LANG` and `TERM`. Nothing is
  inherited from the server's own environment, which typically holds database URLs and API keys.
- **Tool guardrail from the profile registry, enabled by the operator only.** `profile.Profile`
  gets `NoTools bool` (true for `claude_code_notools`, `antigravity_notools`);
  `sandbox.CommandRequest` gets `Profile string`, set by `run.Runner` only when the args are the
  registry's own for that profile (no `ExtraArgs`, `Args` equal to the profile's). The host
  runtime refuses `Run` when the profile is empty, unknown or not `NoTools`, unless
  `Config.AllowTools` is true. Nothing a client sends can set either value: the profile name is
  allowlisted server side and the marker lives in aicli's registry. Rejected: a per-request
  `AllowHostTools` flag, which would sit next to client-reachable fields.
- **Interactive runs are allowlisted argv.** `Config.InteractiveCommands` lists exact argv that
  `RunInteractive` may start (for example `["claude", "auth", "login"]`). A bare shell, the
  `capture.Session` default `/bin/bash`, is refused. The tool guardrail does not apply to
  interactive runs; the allowlist replaces it.
- **PTY via `github.com/creack/pty` v1.1.24** (approved), pinned exactly. Handles Linux and macOS
  and resizing from `ResizeCh`; replaces `script -qfc`.
- **Refuse what cannot be honored.** `NetworkModeNone` is refused (the host cannot cut egress),
  and because the resolved mode is always bridge, the existing policy already refuses `SecretEnv`.
  `ReadOnlyMounts` and `ExtraCacheMounts` are refused (no mount namespace). `Image` is ignored (no
  image). Nested session mounts are refused (a link under a link would write through it). A credential
  file inside a session mount (the normal case: `SessionDir` plus `.claude/.credentials.json`) is
  allowed and written into the session directory, exactly as the Docker runtime does; the host
  runtime writes it by temp file + rename so a symlink left at that path is replaced, never
  followed. *Correction:* the first draft refused this case believing Docker avoided it; Docker's
  "covered by a SessionMount" branch writes into the session directory too.
- **Same watchdog as Docker.** The idle-timeout and loop detection in `sandbox/docker/activity.go`
  move to `sandbox/internal/watch` and both runtimes use them, so `Killed`/`KillReason` mean the
  same thing on both.
- **Process-group kill.** The child is the leader of its own process group; timeout, idle kill,
  loop kill and context cancel send `SIGKILL` to the group, so a CLI's helper processes do not
  survive the run. `Run` sets `Setpgid`. `RunInteractive` sets nothing itself: `creack/pty`
  already starts the child with `Setsid` + `Setctty`, which makes it a session and group leader
  (pgid = pid), and adding `Setpgid` on top fails with `EPERM`. Both paths therefore kill
  `-pid`. A helper that calls `setsid`/`setpgid` itself escapes the group; that is accepted and
  listed under Trade-offs.
- **Crash leftovers are pruned by the janitor.** `host.Runtime.PruneOrphaned` satisfies
  `janitor.OrphanPruner` and removes run directories older than the minimum age, which would
  otherwise keep credential files on disk after a server crash.

## Trade-offs

- **No isolation.** A child can read anything the server's uid can read, use the network, and
  use unbounded memory. The guardrails narrow what runs (tool-less CLIs, allowlisted logins) but
  do not contain it. Documented prominently; the Docker runtime stays the choice for tools.
- **No `KillReasonOOM`, no memory limit.** The kernel may OOM-kill the child; it is reported as
  a plain non-zero exit.
- **`toolchain.Wrap` over the host runtime works only with an empty store.** Wrap already passes
  a request through unchanged when no tool is installed and none is pinned; with an installed
  `current` or a pin it adds `ReadOnlyMounts`, which the host runtime refuses with
  `ErrUnsupported`. The host uses CLIs from its own `PATH`, so do not wrap it.
- **Daemonizing helpers escape the kill.** A CLI child that `setsid`s or double-forks leaves the
  process group and survives a kill. Without cgroups the runtime cannot prevent it.
- **A CLI that replaces its session link fails the turn** (see Decisions). In practice the
  profiles' state directories (`.claude`, `.gemini`, `.codex`) are written into, not replaced;
  the check exists to make a change in that behavior visible instead of silent.
- **Two runtimes to keep in step.** Mitigated by the shared conformance tests.
- **Callers that bypass `run`/`capture`** and build `CommandRequest` with container paths
  themselves must switch to `sandbox.LayoutOf` to work on the host runtime.

## Non-goals

- Resource limits (rlimits, cgroups) for the child.
- Running profiles with tools safely on the host. `AllowTools` exists for trusted single-user
  setups and makes no safety claim.
- Windows support. The package compiles there but `New` returns `host.ErrUnsupportedOS`.

## Out of Scope

- A namespace-based runtime (`sandbox/bwrap`).
- Installing CLIs on the host (`aicli-tools` installs into container-compatible directories).
- Migrating any particular host application to the new runtime.

## Impact

### Components
`sandbox`, `sandbox/host` (new), `sandbox/internal/watch` (new), `sandbox/docker`,
`sandbox/sandboxmock`, `profile`, `run`, `capture`, `toolchain`, `janitor` (docs only), docs.

### Files
- New: `sandbox/layout.go`, `sandbox/layout_test.go`, `sandbox/host/doc.go`,
  `sandbox/host/config.go` and `check.go` (Config, errors, request checks, env: all platforms),
  `sandbox/host/host.go`, `sandbox/host/interactive.go`, `sandbox/host/prune.go` (these three
  `//go:build linux || darwin`), `sandbox/host/host_other.go` (`//go:build !linux && !darwin`), `sandbox/host/*_test.go`, `sandbox/internal/watch/watch.go`,
  `sandbox/internal/watch/watch_test.go`, `sandbox/internal/conformance/conformance.go`, `docs/host-runtime.md`
- Modified: `sandbox/sandbox.go` (`Profile` field, `WorkspaceDir` const),
  `sandbox/manager.go`, `sandbox/sandboxmock/sandboxmock.go`, `sandbox/docker/docker.go`,
  `sandbox/docker/activity.go` (moved), `profile/profile.go`, `run/run.go`,
  `capture/capture.go`, `toolchain/runtime.go`, `go.mod`, `go.sum`, `README.md`
- Tests touched: `run/run_test.go`, `capture/capture_test.go`, `profile/profile_test.go`,
  `toolchain/toolchain_test.go` (empty-store passthrough test), `sandbox/docker/activity_test.go` (moved)

### Public API
Additive only:
- `sandbox.Layout`, `sandbox.LayoutProvider`, `sandbox.LayoutOf`, `sandbox.ContainerLayout`,
  `sandbox.WorkspaceDir`, `sandbox.CommandRequest.Profile`
- `profile.Profile.NoTools`
- `capture.Session.Profile` (optional; resolves the profile's state env against the session HOME)
- `host.Config`, `host.New`, `host.Runtime` (`Run`, `RunInteractive`, `Prewarm`, `Layout`,
  `PruneOrphaned`), `host.ErrToolsNotAllowed`, `host.ErrUnsupported`, `host.ErrNotAllowlisted`,
  `host.ErrSessionDetached`, `host.ErrUnsupportedOS`

### Migration
None. New go.mod dependency `github.com/creack/pty v1.1.24`.

### Backward Compatibility
Existing callers see no change: `LayoutOf` returns today's container paths for every runtime that
does not implement `LayoutProvider`, decorators forward the inner runtime's layout, and the Docker
runtime ignores `CommandRequest.Profile`.
