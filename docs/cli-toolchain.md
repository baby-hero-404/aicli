# CLI toolchain: install once on the host, mount into every sandbox

The agent CLIs (claude, codex, agy) release several times a week. Instead of rebuilding the
sandbox image for each release, install versions on the host and let aicli mount the active one,
read-only, into every container. Upgrading or rolling back becomes one command and takes effect on
the next run; no image, no deploy, no code change.

```
<root>/agy/1.4.10/bin/agy        ┐
<root>/agy/current  → "1.4.10"   ├─ mounted read-only at /opt/aicli/tools/agy (+ PATH)
<root>/claude/2.1.312/…          ┘  in every Run, Preflight and login (RunInteractive)
```

## Integrating into a project

Three steps, the same for every host:

**1. Wrap the runtime you already build** (one line; nothing else in the host changes):

```go
docker, _ := dockerrt.New(dockerrt.Config{})
var rt sandbox.Runtime
rt, err := toolchain.WrapDefault(docker)            // root: $AICLI_TOOLS_ROOT, else ~/.aicli/tools
if err != nil { /* ... */ }

// Or specify a custom root path (e.g. on shared storage):
// store, _ := toolchain.Open("/srv/aicli-tools")
// rt = toolchain.Wrap(docker, store)

// Use rt everywhere a runtime is passed today:
runner := &run.Runner{Runtime: rt}                  // runs and Preflight
login  := &capture.Session{Runtime: rt}             // interactive logins
svc, _ := conversation.NewService(conversation.Config{Runner: runner, /* ... */})
```

With `sandbox.Manager` (runtime detection, caches), wrap the Docker runtime first and give the
result to the Manager. The other order works too; both only add to the request.

```go
rt, _ := toolchain.WrapDefault(docker)
mgr := sandbox.NewManager(rt, registry, sandbox.WithCacheRoot("/var/cache/aicli"))
```

**2. Provision the tools root** with the same image your sandboxes use (a deploy step, an init
container, or a cron job). The root is `--root`, else `$AICLI_TOOLS_ROOT`, else
`~/.aicli/tools`, which needs no setup on a development machine. **On a server use a system
path**, either `--root /var/lib/aicli/tools` or `export AICLI_TOOLS_ROOT=/var/lib/aicli/tools`
(create it once, writable by the user that runs `aicli-tools`). The default inside HOME is safe for
the sandbox rule: only the per-version directory of each tool is mounted, read-only, never HOME
itself, and `sandbox.ValidateRequest` rejects a mount of HOME or of a directory above it.

```bash
# One-command setup for all tools (agy, claude, codex at latest version):
aicli-tools setup

# Or install individual tools (defaults to latest and makes it current):
aicli-tools install agy
aicli-tools list
```

If the service runs on several machines, `<root>` must be shared storage (or provisioned the same
way on each machine). On shared storage (NFS, EFS…) installs stay atomic because the staging
area is inside `<root>`, and two hosts installing the same version at once is handled; but every
host that runs `aicli-tools` must use the same uid, and it is simplest to install from one place
(a deploy job) only. Leftovers of killed installs are cleaned up after 24 hours.

`install` makes the installed version current. To install without switching (a canary), pass
`--no-use`; the old `--use` flag is still accepted and does nothing extra.

**3. Upgrade, prune, and roll back** without touching the service:

```bash
aicli-tools setup                           # checks & upgrades all tools, keeping the 3 most recent
aicli-tools setup --keep 2                  # keep only the 2 most recent versions
aicli-tools prune                           # clean up old versions, keeping top 3 (active is never deleted)
aicli-tools prune --keep 1 claude           # keep only 1 version of claude
aicli-tools install agy --no-use            # install only; does not switch current yet
aicli-tools use agy 1.4.11                  # switch: the next run uses it
aicli-tools use agy 1.4.10                  # roll back
aicli-tools reset agy                       # back to the CLI baked in the image
aicli-tools remove agy 1.4.9                # delete a specific version (not allowed for current)
```

**Automate with a daily cron job:**

To keep your servers automatically up to date and clean of old versions without manual intervention,
add a daily cron job on the host (e.g., at 03:00):

```bash
# Crontab: checks daily, installs latest versions, and prunes older than 3 versions
0 3 * * * aicli-tools setup
```

Because `setup` is idempotent, if no new version has been released, it completes in ~2 seconds
with zero disk writes.

agy publishes a checksum only for its latest release, so a new agy version can only be installed
while it is the latest; keep the previous one installed until the new one has proven itself, so
you can roll back. claude and codex can be installed at any published version.

Remove only versions nothing has used for a while: a run that still has a version mounted (it
started before the switch, or pins it) loses its files when that version is removed.

To try a version on some runs first, pin it server side; everyone else stays on `current`:

```go
req.ToolVersions = map[string]string{"agy": "1.4.11"}   // run.Request, e.g. set in conversation.Resolver
```

A pinned version that is not installed makes the run fail before it starts (never a silent
fallback). A tool with no `current` is not mounted, so the CLI baked into the image is used: hosts
can migrate one tool at a time, and nothing breaks before the root is provisioned.

The same operations exist in Go (`toolchain.Installer.Install`, `Store.Use`, `Store.Unuse`,
`Store.Remove`, `Store.Prune`, `Store.PruneKeeping`, `Store.Versions`). To automate upgrades and
garbage collection without cron, call `toolchain.Sync` or run it from the background `janitor`
(see the README). `Sync` installs the newest release of each tool that is missing, makes it
current and prunes old versions (default: keep 3, never the current or the previous one, never one
`SyncOptions.Protected` names). One tool failing does not stop the others.

**Automatic rollback.** The release `Sync` switched to is on probation (24 hours by default).
`Wrap`ped runtimes watch the runs: three failures in a row where the tool itself could not run or
crashed (exit 126, 127, 132 or 139; 134 and 137 are not counted, because a big task running out of memory ends that way) roll it back to the previous version at once, mark it bad
(`<root>/<tool>/bad.json`) and call `Runtime.OnRollback`; a success in between resets the count.
`Sync` never makes a bad version current again, and tries the next newer release as usual. Rolling back to a version that is itself marked bad is refused (`ErrBadVersion`). `aicli-tools unbad <tool> <version>` (`Store.ClearBad`) lifts a mark placed by mistake; it does not switch, so `use` the version afterwards. The
previous version is recorded in `<root>/<tool>/previous` (an empty value means the CLI in the
image), the probation in `auto.json`. For failures only your host can see, call
`Store.ReportOutcome(tool, version, ok, reason, cfg)`; for a last test before the switch set
`SyncOptions.Check`; to rollback by hand run `aicli-tools rollback <tool>`.

**What it will not do.** It does not undo your choices: a release is switched to only on first
setup or when current is the newest good version installed, so a `use` of an older version, a
`reset` or an earlier rollback stays until you act. It cannot know about failures that look like
ordinary task failures (a wrong answer, a quota, a login); those are not held against a release.
A tool is matched by name in the command line, so in a chain such as `claude ... && npm test` the 126/127 of `npm` can be charged to claude; it takes three in a row within the probation. A version you switch to yourself (`use`, `install`, `setup`, `reset`) is trusted: it ends the probation and is never rolled back automatically. `SyncOptions.InstallOnly` turns the switching off.

## How it works and why it is safe

- **Installed inside a container**, from the sandbox image, so binaries match its libc and
  architecture and nothing runs on the host. Install scripts are disabled (`npm install
  --ignore-scripts`); the only one that runs is claude-code's own `install.cjs`, which was read: it
  links the platform binary from an integrity-checked optional dependency, with no network access.
- **Integrity**: npm verifies package integrity hashes at the exact version; agy is downloaded
  only from its release bucket and checked against the SHA-512 in its release manifest (recorded
  in `<root>/agy/<version>/INSTALL.txt`). agy only publishes a manifest for its latest release, so
  older agy versions can be kept and switched to, but not newly installed.
- **Atomic**: a version is installed into `<root>/.staging`, its binary is run from a read-only
  mount and must report the expected version, and only then is it renamed into place. `current`
  is replaced atomically. A version directory is either complete or absent.
- **Read-only**: CLIs cannot update themselves inside a sandbox (and the update would be lost with
  the container anyway). Auto-update is also switched off where the CLI supports it
  (`AGY_CLI_DISABLE_AUTO_UPDATE=1`, `DISABLE_AUTOUPDATER=1` for claude; codex only notifies).
- **PATH**: tools are put first on `PATH`, and a read-only `/etc/profile.d/aicli-tools.sh` does the
  same in login shells, because `bash -l` (which `run` uses) resets `PATH` from `/etc/profile` on
  Debian images. Images not based on Debian only get the environment `PATH`.

Disk: roughly 220 MB (agy), 280 MB (claude) and 430 MB (codex) per version.

## Accounts

Unchanged and orthogonal: each account keeps its own state through `run.Request.SessionDir`
(`sandbox.SessionDir(root, profile, accountID)`) and its credentials through `CredentialFiles`.
Every account uses the same installed CLI versions; storing, choosing and rotating accounts is the
host's job.
