package toolchain

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/baby-hero-404/aicli/sandbox"
)

// DefaultManifestBase is where agy publishes its release manifests (linux_<arch>.json with
// version, url and sha512), the same source its official installer reads.
const DefaultManifestBase = "https://antigravity-cli-auto-updater-974169037036.us-central1.run.app/manifests"

// agyDownloadPrefix is the only place an agy archive may come from, whatever the manifest says.
const agyDownloadPrefix = "https://storage.googleapis.com/antigravity-public/"

const installMount = "/opt/aicli/install"

// staleStaging is how old a staging entry must be before Install deletes it as abandoned.
const staleStaging = 24 * time.Hour

// Installer installs tool versions into a Store by running the install inside a sandbox
// container, so binaries match the container's libc and architecture and nothing runs on the
// host. The container needs network access (npm registry, agy's release bucket).
type Installer struct {
	Store *Store
	// Runtime runs the installer containers. Use the plain runtime, not a wrapped one.
	Runtime sandbox.Runtime
	// Image is the sandbox image to install with (its Node and libc are what the tools will
	// run on). Empty means the runtime's default image.
	Image string
	// ManifestBase overrides DefaultManifestBase (tests).
	ManifestBase string
	// Timeout per container run; default 10 minutes.
	Timeout time.Duration
}

// Install installs name@version and returns the installed version. version may be Latest. An
// already installed version is not reinstalled. Install never changes the current version; call
// Store.Use for that.
func (i *Installer) Install(ctx context.Context, name, version string) (string, error) {
	t, err := lookup(name)
	if err != nil {
		return "", err
	}
	if version == Latest {
		if version, err = i.resolveLatest(ctx, t); err != nil {
			return "", err
		}
	}
	if err := checkVersion(version); err != nil {
		return "", err
	}
	if ok, err := i.Store.Installed(name, version); err != nil || ok {
		return version, err
	}
	// Leftovers of installs that were killed (a defer does not run on SIGKILL) would otherwise
	// pile up. Anything younger than a day may belong to an install running on another host.
	if _, err := i.Store.PruneStaging(staleStaging, time.Now()); err != nil {
		return "", err
	}

	staging, err := os.MkdirTemp(filepath.Join(i.Store.root, ".staging"), name+"-"+version+"-")
	if err != nil {
		return "", fmt.Errorf("toolchain: create staging dir: %w", err)
	}
	defer os.RemoveAll(staging) // a no-op once it has been renamed into place
	// The container user (uid 1000 in aicli/base) must be able to write it.
	if err := os.Chmod(staging, 0o777); err != nil {
		return "", fmt.Errorf("toolchain: staging permissions: %w", err)
	}

	if _, err := i.run(ctx, t, "install", version, sandbox.CommandRequest{
		SessionMounts: map[string]string{installMount: staging},
	}); err != nil {
		return "", err
	}
	if err := i.verify(ctx, t, version, staging); err != nil {
		return "", err
	}
	if err := os.Chmod(staging, 0o755); err != nil {
		return "", fmt.Errorf("toolchain: staging permissions: %w", err)
	}
	dest := i.Store.versionDir(name, version)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", fmt.Errorf("toolchain: create tool dir: %w", err)
	}
	if err := os.Rename(staging, dest); err != nil {
		if fi, statErr := os.Stat(dest); statErr == nil && fi.IsDir() {
			return version, nil // installed concurrently by someone else
		}
		return "", fmt.Errorf("toolchain: move into place: %w", err)
	}
	return version, nil
}

func (i *Installer) resolveLatest(ctx context.Context, t tool) (string, error) {
	out, err := i.run(ctx, t, "resolve", "", sandbox.CommandRequest{})
	if err != nil {
		return "", err
	}
	v := lastLine(out)
	if err := checkVersion(v); err != nil {
		return "", fmt.Errorf("toolchain: %s: could not resolve the latest version: %w", t.name, err)
	}
	return v, nil
}

// verify runs the installed binary from a read-only mount, as a sandbox will, and checks that
// it reports the expected version.
func (i *Installer) verify(ctx context.Context, t tool, version, dir string) error {
	res, err := i.Runtime.Run(ctx, sandbox.CommandRequest{
		Image:          i.Image,
		Command:        []string{"bash", "-c", `HOME="$(mktemp -d)" "$BIN" --version`},
		Env:            map[string]string{"BIN": containerDir(t.name) + "/bin/" + t.binary, "CI": "1"},
		ReadOnlyMounts: map[string]string{containerDir(t.name): dir},
		Timeout:        2 * time.Minute,
	})
	if err != nil {
		return fmt.Errorf("toolchain: %s@%s: run version check: %w", t.name, version, err)
	}
	out := res.Stdout + "\n" + res.Stderr
	if res.ExitCode != 0 || !regexp.MustCompile(`(^|[^0-9.])`+regexp.QuoteMeta(version)+`([^0-9.]|$)`).MatchString(out) {
		return fmt.Errorf("toolchain: %s@%s: installed binary reports %q (exit %d)", t.name, version, strings.TrimSpace(lastLine(out)), res.ExitCode)
	}
	return nil
}

// run executes the install script in mode "resolve" or "install".
func (i *Installer) run(ctx context.Context, t tool, mode, version string, req sandbox.CommandRequest) (string, error) {
	base := i.ManifestBase
	if base == "" {
		base = DefaultManifestBase
	}
	timeout := i.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	req.Image = i.Image
	req.Timeout = timeout
	// Values travel as environment variables, never spliced into the script text.
	req.Env = map[string]string{
		"MODE": mode, "VERSION": version, "DEST": installMount, "CI": "1",
		"PKG": t.npmPackage, "POSTINSTALL": t.postinstall, "BINARY": t.binary,
		"MANIFEST_BASE": base, "DOWNLOAD_PREFIX": agyDownloadPrefix,
	}
	script := npmScript
	if t.manifest {
		script = agyScript
	}
	req.Command = []string{"bash", "-c", script}
	res, err := i.Runtime.Run(ctx, req)
	if err != nil {
		return "", fmt.Errorf("toolchain: %s %s: %w", mode, t.name, err)
	}
	if res.ExitCode != 0 {
		return "", fmt.Errorf("toolchain: %s %s@%s failed (exit %d): %s", mode, t.name, version, res.ExitCode, tail(res.Stderr+"\n"+res.Stdout, 600))
	}
	return res.Stdout, nil
}

// npmScript installs an npm-distributed CLI with install scripts disabled, then runs only the
// package's own reviewed postinstall (if any).
const npmScript = `set -eu
export HOME="$(mktemp -d)" npm_config_cache="$(mktemp -d)" npm_config_update_notifier=false npm_config_fund=false npm_config_audit=false
if [ "$MODE" = resolve ]; then npm view "$PKG@latest" version; exit 0; fi
npm install --global --prefix "$DEST" --ignore-scripts "$PKG@$VERSION"
if [ -n "$POSTINSTALL" ]; then (cd "$DEST/lib/node_modules/$PKG" && node "$POSTINSTALL"); fi
test -x "$DEST/bin/$BINARY"
printf 'tool=%s\nversion=%s\nsource=npm:%s@%s\n' "$BINARY" "$VERSION" "$PKG" "$VERSION" > "$DEST/INSTALL.txt"
`

// agyScript installs agy the way its official installer does: read the release manifest,
// download that one archive, check its SHA-512.
const agyScript = `set -eu
case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) echo "agy: unsupported architecture $(uname -m)" >&2; exit 1 ;;
esac
manifest="$(curl -fsSL "$MANIFEST_BASE/linux_$arch.json")"
fields="$(printf '%s' "$manifest" | node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{const j=JSON.parse(s);for(const k of ["version","url","sha512"]){if(typeof j[k]!=="string"||/\s/.test(j[k]))process.exit(2)}process.stdout.write(j.version+" "+j.url+" "+j.sha512)})')"
set -- $fields
ver="$1"; url="$2"; sum="$3"
if [ "$MODE" = resolve ]; then echo "$ver"; exit 0; fi
if [ "$ver" != "$VERSION" ]; then
  echo "agy: the release manifest offers $ver; older versions cannot be installed (no checksum is published for them)" >&2
  exit 3
fi
case "$url" in "$DOWNLOAD_PREFIX"*) ;; *) echo "agy: unexpected download location $url" >&2; exit 1 ;; esac
tmp="$(mktemp -d)"
curl -fsSL -o "$tmp/agy.tgz" "$url"
echo "$sum  $tmp/agy.tgz" | sha512sum -c - >/dev/null
tar -xzf "$tmp/agy.tgz" -C "$tmp" antigravity
mkdir -p "$DEST/bin"
install -m 0755 "$tmp/antigravity" "$DEST/bin/agy"
printf 'tool=agy\nversion=%s\nsource=%s\nsha512=%s\n' "$ver" "$url" "$sum" > "$DEST/INSTALL.txt"
`

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return "…" + s[len(s)-n:]
	}
	return s
}
