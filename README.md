# aicli

A Go toolkit for services that drive AI coding CLIs (Claude Code, Codex, Antigravity/agy and similar). It is application-agnostic: it knows how to run a CLI and capture a login, not about your database, users or tenants. Host applications plug in through small interfaces.

```go
import (
	"github.com/baby-hero-404/aicli/profile"
	"github.com/baby-hero-404/aicli/run"
	"github.com/baby-hero-404/aicli/sandbox"
	dockerrt "github.com/baby-hero-404/aicli/sandbox/docker"
)

docker, _ := dockerrt.New(dockerrt.Config{})                       // default image aicli/base:latest
dir, _ := sandbox.SessionDir("/var/lib/app/sessions", "claude_code", accountID)
p, _ := profile.Lookup("claude_code")
res, _ := (&run.Runner{Runtime: docker}).Run(ctx, run.Request{
	Profile: "claude_code", Command: p.Command, Args: p.Args, Prompt: task,
	Workspace: repoDir, SessionDir: dir, CredentialFiles: storedCredential,
})
// persist res.UpdatedCredentialFiles; react to res.QuotaExceeded / res.AuthInvalidConfirmed / ...
```

## Packages

| Package | Purpose |
|---|---|
| `profile` | Built-in CLI profiles (claude, codex, agy): command, args, auth check, timeout, and where each CLI keeps its state (`StateDirs`, `Env`) |
| `rules` | Classify CLI output: quota exhausted (with cooldown hint), authentication invalid (confirmed vs suspected), blocked waiting for input |
| `run` | Run a CLI headlessly: credentials injected, per-account state mounted from `SessionDir`, prompt file, resume, capture files, outcome classification (quota, auth, loop, stall, waiting for input), telemetry, secret redaction, refreshed-credential write-back |
| `sandbox` | `Runtime` interface and request/result types, execution policy and mount validation, `SessionDir` for per-account state, optional `Manager` for runtime detection and caches. No container dependency |
| `sandbox/docker` | Docker implementation of `Runtime`: containers, PTY sessions, credential write-back, idle/loop/OOM detection |
| `sandbox/host` | `Runtime` that runs the CLI as a plain child process (Linux/macOS, no Docker, milliseconds per run) with a private HOME, clean environment, process-group kill and PTY logins. Not a sandbox: only tool-less profiles and allowlisted logins by default. See [docs/host-runtime.md](docs/host-runtime.md) |
| `sandbox/sandboxmock` | In-memory `Runtime` for tests of code that uses aicli; no Docker needed |
| `capture` | Interactive CLI login in a sandbox; returns the credential files the CLI wrote. `RunOptions.Files` puts files in HOME first, to test a stored login |
| `capture/ticket` | Single-use, short-lived tickets that authorize a WebSocket without exposing a bearer token (`Store` interface + in-memory implementation) |
| `capture/wsgorilla` | Serves a capture session over WebSocket (gorilla/websocket), with origin allowlist, per-ticket `Seed` files and `DiscardFiles` for test sessions |
| `run/stream`, `run/stream/agy` | CLI-neutral event contract for live output, and the decoder for agy's `stream-json` |
| `conversation` | Resumable, streamable conversations: turn lifecycle, history, export, cancel. The host plugs in `Store`, `Authorizer`, `Resolver` |
| `conversation/memstore`, `conversation/storetest` | In-memory `Store` for tests and demos; the contract suite every `Store` should pass |
| `transport/httpapi` | Mountable `net/http` handler: REST + Server-Sent Events for `conversation` |
| `toolchain`, `cmd/aicli-tools` | Install CLI versions once on the host and mount them read-only into every sandbox; switch or roll back without rebuilding images |
| `web/` (`@aicli/ui`) | npm package: framework-free API client (`@aicli/ui/client`) and React components (`@aicli/ui/react`) |

## Design rules

- **No application knowledge.** No database, organization or user concepts. Persistence and authorization are the host's job (`ticket.Store`, the `OnComplete` hook, `CredentialFiles`, `SessionMounts`).
- **Isolation by default.** The host user's HOME is never mounted, nor any directory above it (`sandbox.ValidateRequest` rejects both); only directories the caller names are, such as a workspace or a read-only tool version. A run starts from the image's fresh `/home/agent` and receives only the credential files and session mounts the caller passes, so concurrent runs for different accounts cannot see each other or the host.
- **Opt-in caches.** Package-install caches (npm, pip, Go modules, Maven/Gradle, pub) are mounted only when the host sets `sandbox.WithCacheRoot(dir)`, namespaced per `CommandRequest.CacheScope` (e.g. a tenant ID).
- **Framework-free cores.** Transport and vendor adapters live in sub-packages named after what they adapt (`capture/wsgorilla`, `sandbox/docker`), so code that only needs the interfaces never builds the Docker SDK. Test doubles live in `<pkg>/<pkg>mock` (`sandbox/sandboxmock`).
- **Standalone.** aicli has no dependency on other internal libraries.

## Conversations and the UI

`conversation` + `transport/httpapi` + `@aicli/ui` give a host a chat UI over a headless CLI (currently Antigravity/agy): create a conversation, reopen an old one, send messages, watch the answer stream in, cancel, resume the CLI session and export the history. The host writes only authentication, authorization and a `Store`; see **[docs/conversation.md](docs/conversation.md)**, `examples/go-server` (a runnable host) and `examples/web` (a standalone Vite + React app that uses `@aicli/ui` against it).

Security model in one paragraph: the browser sends intent, never execution. A request carries a profile name from an allowlist, a message and an idempotency key; workspace, image, credentials and session directory come from the host's `Resolver`, and unknown JSON fields are rejected. Every read, send, cancel, export and stream goes through the host's `Authorizer` (default: deny), conversation IDs are random and the CLI's own session ID never leaves the server, output is redacted before it is stored or sent, and the UI renders it as text only.

## Multiple accounts

Give each account its own state directory with `sandbox.SessionDir(root, profile, accountID)` and pass it as `run.Request.SessionDir`. `run` mounts the profile's `StateDirs` from it and sets the profile's state environment (e.g. `CLAUDE_CONFIG_DIR`), so a login persists across that account's runs and is never visible to another account. For login sessions, set `capture.Session.Profile` so captured files use the same layout.

## Runtime detection (optional)

aicli ships no language runtimes. `sandbox.NewManager(runtime, registry)` can detect a project's runtime from marker files and choose its image, setup commands and caches, but only for runtimes **you** register:

```go
manifests, _ := sandbox.LoadManifests(os.DirFS("runtimes")) // <id>/manifest.yaml, or build sandbox.Manifest values in code
registry, _ := sandbox.NewRegistry(manifests...)            // registration order = detection priority
mgr := sandbox.NewManager(docker, registry, sandbox.WithCacheRoot("/var/lib/app/cache"))
```

An empty registry makes the manager a transparent passthrough. A manifest (`<id>/manifest.yaml` for `LoadManifests`) looks like:

```yaml
id: node
image: acme/sandbox-node:1   # your image: FROM aicli/base plus the toolchain
detect: [package.json]       # marker files in the workspace root (any one matches)
cache:                       # mounted only with WithCacheRoot, under <root>/<CacheScope>/<name>
  - name: npm
    container: /home/agent/.npm
setup: []                    # shell commands run before the caller's command
healthcheck: "node --version"
```

## Automatic Background Housekeeping & Janitor

Host applications can run the `janitor` in the background instead of cron. Every task is optional and runs once at startup, then on its own interval; an error or a panic is logged and never reaches the host:

```go
import "github.com/baby-hero-404/aicli/janitor"

j := janitor.New(janitor.Config{
    Conversations: svc,    // *conversation.Service: marks turns whose instance died as interrupted (every minute)
    Orphans:       docker, // *docker.Runtime: removes sandbox containers left behind, 10 minutes after they stopped
    Filesystem: sandbox.JanitorConfig{
        WorkspacesRoot: filepath.Join(data, "workspaces"), // idle for 30 days (default) -> deleted
        SessionsRoot:   filepath.Join(data, "sessions"),   // idle for 30 days (default) -> deleted
        CacheRoot:      "/var/cache/aicli",                // idle for 14 days (default) -> deleted
        LogsRoot:       "/var/log/aicli",                  // older than 14 days (default) -> deleted
    },
    Toolchain:     installer,                              // installs new CLI releases and prunes old versions, daily
    ToolchainSync: toolchain.SyncOptions{Keep: 3},         // new release -> current at once, rolled back if it fails
    Logger:        logger,
})
j.Start(ctx) // returns at once; after cancelling ctx, j.Wait() waits for the workers
```

What to know before enabling it:

- **Idle means no activity anywhere inside.** A workspace or session is kept while anything in it changed within the TTL (a CLI editing a file deep in a workspace, or refreshing its login, counts). `JanitorConfig.Keep` can veto a deletion for what file times cannot show, e.g. a conversation still open in your database.
- **Roots must hold only aicli data.** Everything directly inside a root that is idle is deleted. A root that is `/`, the user's HOME, a directory above HOME, a system directory such as `/etc`, `/var`, `/var/lib` or `/tmp` itself (a directory below them is fine), a symlink to one of these, or not absolute is refused with `sandbox.ErrUnsafeRoot` before anything is deleted. If a deletion fails halfway (files a container created as root), the error names the path and the part already removed counts as activity, so the retry waits another TTL: fix the ownership or remove the directory by hand. Run once with `DryRun: true` to see what a new root would lose.
- **A new CLI release is used at once, and rolled back if it fails.** `Sync` installs the newest release (checked with `--version` from a read-only mount) and makes it current, remembering the previous version. For 24 hours (`HealthConfig.Probation`) the release is on probation: if runs through `toolchain.Wrap(...)` fail 3 times in a row with an exit code that means the program itself is broken (126 cannot execute, 127 not found, 132 illegal instruction, 139 segmentation fault; 134 and 137 are not counted, because Node aborts that way when a big task runs out of memory), it is rolled back to the previous version at once (not at the next daily check), marked bad, and never made current again by `Sync`; the next newer release is tried as usual. Hosts that can see more (an output format that no longer parses) call `Store.ReportOutcome`; `SyncOptions.Check` can add a smoke test before the switch; `OnRollback` lets you alert. `aicli-tools rollback <tool>` does the same by hand, `aicli-tools unbad <tool> <version>` lifts the mark (then `use` it; `list` shows marked versions as `1.4.10(bad)`). A version you switch to yourself (`use`, `install`, `setup`, `reset`) is trusted and never rolled back automatically. `SyncOptions.InstallOnly` restores "install, switch later with `use`".
- **It never overrides you.** A release is switched to only on first setup, or when current is the newest good version installed: your own `use` of an older version, a `reset`, and a rollback all stay. `Protected` lists versions that requests still pin so they are not pruned; the previous version is never pruned. The toolchain task has its own, longer time limit (`ToolchainTimeout`, 1 hour), because one pass can install three tools.
- **Containers** are removed only if labeled by aicli, stopped (not running, paused or restarting) and stopped at least `OrphanMinAge` ago.

## Images

To upgrade the CLIs without rebuilding images, run `aicli-tools setup` on the host and wrap your runtime with `toolchain.WrapDefault`; see **[docs/cli-toolchain.md](docs/cli-toolchain.md)**. The CLIs in the image remain the fallback for any tool not installed that way.

aicli does not decide what toolchains your agents need. `docker/base/Dockerfile` is a reference base image (pinned Claude Code, Codex and Antigravity `agy` CLIs, each version-locked and agy checksum-verified with self-update disabled; non-root `agent` user with home `/home/agent`; `tini`); build it with `make image-base` and extend it with `FROM aicli/base` for your own toolchains.

## Development

`make verify` runs everything: gofmt, `go vet`, the Go tests with `-race`, and the `web/` checks (typecheck, eslint, vitest, build, publint, attw). `web/` uses pnpm with a committed lockfile, exact versions, a 7-day minimum release age and no dependency build scripts; see `web/pnpm-workspace.yaml`.

## Docker integration tests

```bash
AICLI_TEST_IMAGE=<image> make test-docker
```

They need a Docker daemon and an image with `bash` and an `agent` user; they are skipped otherwise.

## Commands

```bash
make test   # go test ./... -race -cover
make lint   # gofmt + go vet
make tidy
```
