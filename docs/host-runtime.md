# Host runtime (`sandbox/host`)

`sandbox/host` is a `sandbox.Runtime` that runs the CLI as a **child process of your server**, with
no container. A run costs about a millisecond (`BenchmarkHostRun`), needs no Docker daemon, and
works where Docker is not available (small VMs, many PaaS containers, developer laptops).
`run.Runner`, `capture.Session` and `conversation.Service` work on it unchanged.

> **It is not a sandbox.** The CLI runs with your server's user: it can read every file that user
> can read, use the network, and use unlimited memory. Use it for short, tool-less calls
> (generate text, grade an answer, explain an error). Use `sandbox/docker` for anything that runs
> with tools.

## Use it

```go
rt, err := host.New(host.Config{
    RunRoot:       "/var/lib/myapp/aicli-runs",       // private per-run directories; this server only
    AllowedRoots:  []string{"/var/lib/myapp"},        // workspaces and session dirs must live here
    InteractiveCommands: [][]string{{"claude", "auth", "login"}, {"agy", "login"}},
})
runner := &run.Runner{Runtime: rt}                    // profiles: claude_code_notools, antigravity_notools
login  := &capture.Session{Runtime: rt, Profile: "claude_code", Command: []string{"claude", "auth", "login"}}
```

`host.New` returns `host.ErrUnsupportedOS` on systems other than Linux and macOS (the package
still compiles everywhere).

## What it does

- **Private HOME per run.** `<RunRoot>/<TaskID>/home` (mode 0700), created at the start and
  removed at the end. Session state (`SessionDir` / `SessionMounts`) is linked into it; credential
  files are written into it with mode 0600, and a refreshed credential comes back in
  `UpdatedCredentialFiles`, as with Docker. A credential that targets a session-mounted directory
  (the usual `SessionDir` + `.claude/.credentials.json`) is written into that session directory,
  also as with Docker.
- **Clean environment.** The child gets `HOME`, `TMPDIR`, `PATH`, `LANG` and what the request
  passes (`Env`, `SecretEnv`) and nothing from your server's own environment.
- **Process group.** Timeout, context cancel, idle timeout and loop detection kill the whole group,
  so a CLI's helper processes do not survive the run. The idle and loop watchdog is the same code
  as the Docker runtime's, so `Killed` / `KillReason` mean the same thing.
- **Layout.** Callers ask the runtime where the workspace and HOME are (`sandbox.LayoutOf`); on the
  host the workspace is the real directory and HOME is the private one. `run` and `capture` do this
  for you.

## What it refuses

| Request | Result |
|---|---|
| A profile with tools (`claude_code`, `openai_codex`, `antigravity`), or a run `run.Runner` cannot vouch for | `ErrToolsNotAllowed`, unless `Config.AllowTools` |
| `RunInteractive` of a command not in `Config.InteractiveCommands` (including a bare `/bin/bash`) | `ErrNotAllowlisted` |
| `NetworkModeNone`, `ReadOnlyMounts`, `ExtraCacheMounts` | `ErrUnsupported` (the host cannot honor them) |
| Nested session mounts; `SessionMounts` / `CredentialFiles` on an interactive run | `ErrUnsupported` |
| `SecretEnv` | refused: it is only allowed without network egress, and the host always has it |
| Session mount or credential target outside the run HOME; unsafe `TaskID` | error, nothing is created |
| Workspace or session directory that resolves (symlinks followed) to the user's HOME or an ancestor, or outside `AllowedRoots` | error, nothing is created |
| A second run for a `TaskID` that is still running | error |

Whether a profile "has tools" comes from aicli's own registry (`profile.Profile.NoTools`), and
`run.Runner` marks a request with its profile only when the invocation is exactly the registry's:
same command and arguments, no `ExtraArgs`, no `Setup` / `Cleanup` snippets. Nothing a client sends
can set either; clients choose only a profile name from your allowlist.

If a CLI deletes its session link and creates a real directory in its place, what it wrote would be
lost with the run directory. The runtime detects this after the run and returns
`ErrSessionDetached` rather than losing the state silently.

## Deployment checklist

1. Run the server under a **dedicated user** whose HOME holds nothing sensitive: the CLI can read
   whatever that user can.
2. Install the CLIs where a login shell finds them. `run` starts commands with `bash -lc`, and
   `/etc/profile` resets `PATH`, so `Config.PATH` only helps to find `bash` itself and commands that
   are started directly. Put `claude` / `agy` in `/usr/local/bin` (or add a script in
   `/etc/profile.d`). Do not wrap this runtime with `toolchain.Wrap` once tools are installed in
   its store: the wrapper adds read-only mounts, which the host cannot do (an empty store is fine).
3. Let `conversation` / your API offer only the `_notools` profiles.
4. Give `RunRoot` to this server alone and run the janitor with `Orphans: hostRuntime`: it removes
   run directories (and the credential files in them) left by a crash.
5. Set `AllowedRoots` to your data directories.
6. Keep `AllowTools` off unless the machine is trusted and single-user. It logs a warning at start.

## Limits

No memory or CPU limit and no `KillReasonOOM` (the kernel may kill the child; you see a non-zero
exit). A helper that daemonizes (`setsid`) leaves the process group and survives a kill. Links *inside*
a session or workspace directory that point elsewhere are not checked: the child has your user's
rights and could create them itself.
