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
| `sandbox/sandboxmock` | In-memory `Runtime` for tests of code that uses aicli; no Docker needed |
| `capture` | Interactive CLI login in a sandbox; returns the credential files the CLI wrote |
| `capture/ticket` | Single-use, short-lived tickets that authorize a WebSocket without exposing a bearer token (`Store` interface + in-memory implementation) |
| `capture/wsgorilla` | Serves a capture session over WebSocket (gorilla/websocket), with origin allowlist |

## Design rules

- **No application knowledge.** No database, organization or user concepts. Persistence and authorization are the host's job (`ticket.Store`, the `OnComplete` hook, `CredentialFiles`, `SessionMounts`).
- **Isolation by default.** Nothing from the host user's HOME is ever mounted. A run starts from the image's fresh `/home/agent` and receives only the credential files and session mounts the caller passes, so concurrent runs for different accounts cannot see each other or the host.
- **Opt-in caches.** Package-install caches (npm, pip, Go modules, Maven/Gradle, pub) are mounted only when the host sets `sandbox.WithCacheRoot(dir)`, namespaced per `CommandRequest.CacheScope` (e.g. a tenant ID).
- **Framework-free cores.** Transport and vendor adapters live in sub-packages named after what they adapt (`capture/wsgorilla`, `sandbox/docker`), so code that only needs the interfaces never builds the Docker SDK. Test doubles live in `<pkg>/<pkg>mock` (`sandbox/sandboxmock`).
- **Standalone.** aicli has no dependency on other internal libraries.

## Multiple accounts

Give each account its own state directory with `sandbox.SessionDir(root, profile, accountID)` and pass it as `run.Request.SessionDir`. `run` mounts the profile's `StateDirs` from it and sets the profile's state environment (e.g. `CLAUDE_CONFIG_DIR`), so a login persists across that account's runs and is never visible to another account. For login sessions, pass `p.Env("/workspace")` in `capture.Session.Env` so captured files use the same layout.

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

## Images

aicli does not decide what toolchains your agents need. `docker/base/Dockerfile` is a reference base image (pinned Claude Code, Codex and Antigravity `agy` CLIs, each version-locked and agy checksum-verified with self-update disabled; non-root `agent` user with home `/home/agent`; `tini`); build it with `make image-base` and extend it with `FROM aicli/base` for your own toolchains.

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
