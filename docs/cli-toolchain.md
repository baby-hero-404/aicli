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
store, err := toolchain.Open("/srv/aicli-tools")   // absolute path the Docker daemon can see
if err != nil { /* ... */ }
var rt sandbox.Runtime = toolchain.Wrap(docker, store)

// Use rt everywhere a runtime is passed today:
runner := &run.Runner{Runtime: rt}                  // runs and Preflight
login  := &capture.Session{Runtime: rt}             // interactive logins
svc, _ := conversation.NewService(conversation.Config{Runner: runner, /* ... */})
```

With `sandbox.Manager` (runtime detection, caches), wrap the Docker runtime first and give the
result to the Manager. The other order works too; both only add to the request.

```go
mgr := sandbox.NewManager(toolchain.Wrap(docker, store), registry, sandbox.WithCacheRoot("/var/cache/aicli"))
```

**2. Provision the tools root** where the service runs (a deploy step, an init container, or a
cron job), with the same image your sandboxes use:

```bash
aicli-tools --root /srv/aicli-tools --image aicli/base:latest install agy latest --use
aicli-tools --root /srv/aicli-tools --image aicli/base:latest install claude latest --use
aicli-tools --root /srv/aicli-tools --image aicli/base:latest install codex latest --use
aicli-tools --root /srv/aicli-tools list
```

If the service runs on several machines, `<root>` must be shared storage (or provisioned the same
way on each machine). On shared storage (NFS, EFS…) installs stay atomic because the staging
area is inside `<root>`, and two hosts installing the same version at once is handled; but every
host that runs `aicli-tools` must use the same uid, and it is simplest to install from one place
(a deploy job) only. Leftovers of killed installs are cleaned up after 24 hours.

**3. Upgrade, canary and roll back** without touching the service:

```bash
aicli-tools --root /srv/aicli-tools install agy latest     # install only; nothing changes yet
aicli-tools --root /srv/aicli-tools use agy 1.4.11          # switch: the next run uses it
aicli-tools --root /srv/aicli-tools use agy 1.4.10          # roll back
aicli-tools --root /srv/aicli-tools reset agy               # back to the CLI baked in the image
aicli-tools --root /srv/aicli-tools remove agy 1.4.9        # clean up (not allowed for current)
```

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
`Store.Remove`, `Store.Versions`) for hosts that want an admin endpoint instead of the command.

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
