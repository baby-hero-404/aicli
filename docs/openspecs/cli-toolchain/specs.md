# Expected Behavior: CLI toolchain

## Scenario: Run with the current version
**When:** `<root>/agy/current` says `1.4.10`, `<root>/agy/1.4.10/` exists, and a run goes through
a runtime wrapped with `toolchain.Wrap`.
**Then:** the container gets `<root>/agy/1.4.10` mounted read-only at `/opt/aicli/tools/agy`,
`PATH` starts with the tool `bin` directories, the profile script is mounted read-only at
`/etc/profile.d/aicli-tools.sh`, and `agy --version` inside a `bash -lc` prints `1.4.10`.

## Scenario: Switch and roll back
**When:** `Use(agy, 1.4.11)` then later `Use(agy, 1.4.10)`.
**Then:** each run started after a switch uses the new version; runs already started keep theirs.

## Scenario: Pin a version for one run
**When:** a request has `ToolVersions{"agy": "1.4.11"}` and 1.4.11 is installed.
**Then:** that run mounts 1.4.11; other runs still use `current`.

## Scenario: Install
**When:** `Install(ctx, agy, "latest")` (or an exact version).
**Then:** the version is resolved (agy manifest; `npm view` for claude/codex), installed in a
container into a staging directory, `--version` is checked, and the directory is renamed to
`<root>/<tool>/<version>`. Installing an already installed version is a no-op returning it.
`current` is not changed by Install. claude and codex accept any published exact version; agy accepts only the version its release manifest currently offers.

## Scenario: Login and preflight use the mounted CLI
**When:** `capture.Session` or `run.Runner.Preflight` runs on the wrapped runtime.
**Then:** the same mounts and PATH apply (they go through `RunInteractive` / `Run`).

## Scenario: Revert to the image's CLI
**When:** `Unuse(agy)` (`aicli-tools reset agy`).
**Then:** `current` is removed; unpinned runs mount nothing for agy and use the image's CLI; a
run pinning an installed agy version still mounts it; the former current version can now be
removed. Unuse with no current is a no-op.

## Scenario: Abandoned staging
**When:** an install was killed and left `<root>/.staging/<entry>` behind.
**Then:** the next Install removes entries older than 24 h and leaves younger ones (they may be
another host's install in progress).

## Failure Scenario: agy version not offered
**When:** `Install(agy, v)` and the manifest offers another version.
**Then:** fails with a message naming the offered version; nothing is installed.

## Failure Scenario: Pinned version not installed
**When:** `ToolVersions` names a version that is not installed, or an unknown tool.
**Then:** `Run`/`RunInteractive` return an error wrapping `toolchain.ErrNotInstalled` /
`ErrUnknownTool`; nothing starts.

## Failure Scenario: Checksum mismatch (agy)
**When:** the downloaded archive's SHA-512 differs from the manifest.
**Then:** Install fails, the staging directory is removed, nothing appears under `<root>/agy`.

## Failure Scenario: Installed binary does not run
**When:** `<bin> --version` fails or prints a different version.
**Then:** Install fails and removes the staging directory.

## Failure Scenario: Bad names
**When:** a tool is not one of `claude`, `codex`, `agy`, or a version does not match
`^[0-9A-Za-z][0-9A-Za-z.+-]{0,63}$` (`latest` is accepted only by Install).
**Then:** `ErrInvalidName`; no path is built from it.

## Failure Scenario: Remove the current version
**When:** `Remove(agy, v)` and `v` is current.
**Then:** refused with `ErrInUse`.

## Failure Scenario: Read-only really is read-only
**When:** a process in the sandbox writes under `/opt/aicli/tools/<tool>`.
**Then:** the write fails; the host directory is unchanged.

## Invariants
- Tool mounts are always read-only; the installer is the only writer.
- A version directory under `<root>/<tool>/` is complete or absent, never partial.
- `current` always names an installed version, or is absent.
- Names from callers never become path components without validation.
- No tool directory is mounted into a container unless installed under the configured root.

## Rules
- The wrapper never overrides a mount target the caller set explicitly; a conflict is an error.
- The wrapper prepends to an explicit `PATH` in the request instead of replacing it.

## Constraints
- No new Go module dependencies. No global state. The root must be an absolute path.
