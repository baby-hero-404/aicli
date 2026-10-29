# Proposal: CLI toolchain mounted from the host

## Problem

The agent CLIs (claude, codex, agy) are baked into `aicli/base`. They release several times a
week, so keeping up means rebuilding and redeploying the image, and every host that extends it.
The CLIs also try to update themselves at run time, but a sandbox container is thrown away after
each run, so those updates are lost (and, while they happen, they make runs slower and change a
CLI's behavior mid-flight: the agy `stream-json` decoder is pinned by fixtures).

## Goal

Install CLI versions once on the host, in versioned directories, and mount them read-only into
every sandbox. Upgrading or rolling back a CLI becomes "install a version, switch `current`",
with no image rebuild and no code change in the hosts that use aicli.

## Success

- A host that wraps its runtime with `toolchain.Wrap` runs, preflights and logs in with the
  mounted version of each CLI; switching `current` takes effect on the next run.
- `aicli-tools install agy latest && aicli-tools use agy <v>` upgrades agy without rebuilding
  any image; `use` of the previous version rolls back.
- A version can be pinned for chosen runs (canary) while everyone else stays on `current`.

## Assumptions

- The sandbox image is Debian-based with Node (true for `aicli/base` and images `FROM` it);
  login shells source `/etc/profile.d/*.sh`.
- The tools root is a host directory visible to the Docker daemon (local disk, or a shared volume
  when several hosts run sandboxes).
- Installing needs network egress from the installer container (npm registry, agy's release
  bucket). Runs do not need it for the tools.

## Decisions

- **Runtime decorator.** `toolchain.Wrap(runtime, store)` returns a `sandbox.Runtime` that adds
  the tool mounts and PATH to every request, in `Run` and `RunInteractive`. `run`, `Preflight`,
  `capture` and `conversation` need no change to use it, and a host integrates by wrapping the
  runtime it already passes around: one line.
- **Install inside a container**, from the sandbox image itself, into
  `<root>/<tool>/<version>/`. Binaries match the container's libc and architecture, and package
  install scripts (claude-code's `postinstall` fetches its native binary) run sandboxed, never on
  the host. A staging directory is renamed into place only after `--version` succeeds, so a
  half-installed version is never visible.
- **Integrity.** agy is downloaded from the URL in agy's release manifest and checked against the
  manifest's SHA-512 (as the Dockerfile does). npm packages are installed at an exact version
  with `npm install --prefix`, which verifies the registry's integrity hashes.
- **Read-only mounts.** Mounted tool directories are read-only: a CLI's self-update cannot write,
  so the version that runs is the version that was installed. Auto-update is also disabled by
  environment (`AGY_CLI_DISABLE_AUTO_UPDATE=1`, `DISABLE_AUTOUPDATER=1`).
- **Fixed container paths, versioned host paths.** Each tool is mounted at
  `/opt/aicli/tools/<tool>`; only the host source changes with the version. PATH is therefore
  constant, so it is set by environment and, because `bash -l` resets PATH from `/etc/profile`,
  also by a read-only mounted `/etc/profile.d/aicli-tools.sh`.
- **`current` plus optional pin.** `<root>/<tool>/current` holds the active version (written
  atomically). `run.Request.ToolVersions` (and `sandbox.CommandRequest.ToolVersions`) pins a
  version per run; the host sets it server side (for example in `conversation.Resolver`).
- **Fallback to the image.** A tool with no installed `current` and no pin is not mounted, so the
  CLI baked into the image is used. Pinning a version that is not installed is an error (the run
  does not start), never a silent fallback.
- **Reverting to the image is explicit.** `Store.Unuse` / `aicli-tools reset <tool>` clears `current`, so runs use the image's CLI again; pinned runs are unaffected.
- **Composition with `sandbox.Manager`.** Both are `sandbox.Runtime` decorators that copy the request and change disjoint fields (Manager: `Image`, `ExtraCacheMounts`, `Command` wrapped in `bash -lc`; toolchain: `ReadOnlyMounts`, `Env`), so either order works; a test covers both. Recommended: `sandbox.NewManager(toolchain.Wrap(docker, store), registry, ...)`.
- **Shared storage.** Staging lives inside `<root>` so `rename` stays on one filesystem and is atomic (also on NFS/EFS); a concurrent install of the same version by another host is detected and treated as success. Every host that installs must run as the same uid (directories are `0755`). Install prunes staging leftovers older than 24 h (killed installs).
- **Accounts stay with the host.** Per-account isolation (`SessionDir`, `CredentialFiles`) is
  unchanged; account storage, selection and rotation are out of scope.

## Trade-offs

- **agy installs are latest-only.** npm tools (claude, codex) can be installed at any published version. agy's release manifest gives a URL and SHA-512 only for its current release, so only that version can be newly installed; rolling back works only to agy versions already in the store. Keep the previous agy version installed until the new one has proven itself.

- Hosts must provision the tools root (and share it across machines if they run several).
- Two places hold a CLI (image fallback and tools root). Accepted for a gradual migration; the
  image copy can be dropped later.
- A login shell that does not read `/etc/profile.d` (non-Debian image) only gets PATH from the
  environment. Documented.

## Non-goals

- Garbage-collecting old versions automatically (`remove` exists; policy is the host's).
- Managing other toolchains (Go, Python…): `sandbox.Manager` and images cover those.

## Out of Scope

- Account pool / rotation (separate spec if needed).
- Distributing the tools root across machines.

## Impact

### Components
New `toolchain` package and `cmd/aicli-tools`; small additive fields in `sandbox` and `run`.

### Files
- Create: `toolchain/{doc,toolchain,store,install,runtime}.go` + tests, `cmd/aicli-tools/main.go`
  + test, `docs/cli-toolchain.md`.
- Modify: `sandbox/sandbox.go` (`ReadOnlyMounts`, `ToolVersions`), `sandbox/policy.go`,
  `sandbox/docker/docker.go` (read-only mounts in `Run` and `RunInteractive`),
  `sandbox/sandboxmock`, `run/run.go` (`ToolVersions` passthrough in `Run` and `Preflight`),
  `README.md`, `CLAUDE.md`, `Makefile`.

### Public API
Additive: `sandbox.CommandRequest.ReadOnlyMounts`, `sandbox.CommandRequest.ToolVersions`,
`run.Request.ToolVersions`, package `toolchain`, command `aicli-tools`.

### Migration
None required. Hosts opt in by wrapping their runtime.

### Backward Compatibility
Zero values keep today's behavior: no wrap, no mounts, image CLIs.
