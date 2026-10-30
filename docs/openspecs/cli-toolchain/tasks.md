# Implementation Map: CLI toolchain

**Goal:** CLIs installed once on the host and mounted read-only into sandboxes; upgrade without rebuilding images.
**Tech Stack:** Go standard library, existing `sandbox` / `run`; installer runs `npm` and `curl` inside the sandbox image.

## Phase 1: Sandbox support

### Read-only mounts and tool pins
**Files:** `sandbox/sandbox.go`, `sandbox/policy.go`, `sandbox/docker/docker.go`, `sandbox/sandboxmock/sandboxmock.go`, tests
**Changes:**
- [x] `CommandRequest.ReadOnlyMounts map[string]string` (container target → host source; file or directory) and `ToolVersions map[string]string`
- [x] Validation: clean absolute targets, absolute sources
- [x] docker: read-only bind mounts in `Run` and `RunInteractive` (interactive today only mounts the workspace)
**Verify:**
- [x] Docker test: a write to a read-only mount fails and the host file is unchanged; a single-file mount works

### Pass pins through `run`
**Files:** `run/run.go`, `run/run_test.go`
**Changes:**
- [x] `run.Request.ToolVersions` copied into the sandbox request in `Run` and `Preflight`
**Verify:**
- [x] Test: both requests carry the pins

## Phase 2: `toolchain` package
**Files:** `toolchain/*.go`
**Changes:**
- [x] Tool registry (claude, codex, agy): container path, how to install, how to check the version, auto-update env
- [x] Store: validate names, `Installed`, `List`, `Current`, `Use` (atomic), `Remove` (refuse current), `Resolve(pins)`
- [x] Installer: resolve `latest`, run install in a container with network, staging dir + rename, version check, agy SHA-512
- [x] `Wrap(runtime, store)`: mounts, PATH env, profile.d script, auto-update env; errors for bad pins
**Verify:**
- [x] Table tests for every Failure Scenario, with sandboxmock
- [x] Docker test (gated): install agy, run `bash -lc 'agy --version'` through the wrapper; write to the mount fails

## Phase 3: Admin command
**Files:** `cmd/aicli-tools/main.go`
**Changes:**
- [x] `install <tool> [version|latest] [--no-use]` (makes the version current unless `--no-use`; `--use` is accepted and a no-op), `setup` (all tools, latest), `use <tool> <version>`, `reset <tool>`, `list`, `remove <tool> <version>`; `--root`, `--image`; the default root is `$AICLI_TOOLS_ROOT`, else `~/.aicli/tools` (servers use `--root /var/lib/aicli/tools`)
**Verify:**
- [x] Real run on this machine: install all three latest, use, list, run a CLI through the wrapper

## Phase 4: Docs
**Files:** `docs/cli-toolchain.md`, `README.md`, `CLAUDE.md`, `Makefile`
**Changes:**
- [x] Integration guide for hosts (wrap, provision root, upgrade/rollback, canary pin)
**Verify:**
- [x] `make verify` green

## Results (2026-10-29)

- Installed for real on this machine with `aicli-tools`: agy 1.4.10, codex 0.165.0, claude 2.1.312 (latest at the time), each via a container and a read-only `--version` check.
- `TestDockerMountedTools` (gated): in `bash -lc` every CLI resolves to `/opt/aicli/tools/<tool>/bin/…` and reports the current version (the image still has agy 1.4.2, so the mount demonstrably wins); writes to the mount fail; `run.Runner.Preflight` finds the mounted CLI.
- Auto-update controls verified in the installed binaries: claude reads `DISABLE_AUTOUPDATER`; codex has no environment switch (config `check_for_update_on_startup`, notify only), so it relies on the read-only mount.

## Deviations from the first draft

- agy can only be newly installed at the version its release manifest offers (no checksums are published for older ones). Older installed versions can still be switched to.
- `RunInteractive` previously mounted only the workspace; it now also applies `ReadOnlyMounts`, so logins see the same CLIs. It still does not apply `SessionMounts` (unchanged behavior).

## Review follow-ups (2026-10-29)

- [x] Spec states the agy latest-only constraint (proposal Trade-offs, specs scenario)
- [x] `Store.Unuse` + `aicli-tools reset <tool>`; `list` shows when a tool falls back to the image
- [x] Composition with `sandbox.Manager` documented and tested in both orders
- [x] Shared storage: same-filesystem rename and same-uid requirement documented; `Install` prunes staging leftovers older than 24 h (`Store.PruneStaging`)
- [x] Fixed a wrong claim in `Store.Remove`'s doc: removing a version that a running sandbox still mounts breaks that run (the files are deleted in place), it does not "keep working"
