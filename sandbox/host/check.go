package host

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/baby-hero-404/aicli/profile"
	"github.com/baby-hero-404/aicli/sandbox"
)

// taskNameRe is what a TaskID must look like, since it becomes a directory name.
var taskNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// maxCredentialReadBytes bounds a credential file read back after a run.
const maxCredentialReadBytes = 1 << 20

// layoutFor returns where cfg places req's workspace and HOME. It only looks at the request; it
// touches no file.
func layoutFor(cfg Config, req sandbox.CommandRequest) (sandbox.Layout, error) {
	if !taskNameRe.MatchString(req.TaskID) {
		return sandbox.Layout{}, fmt.Errorf("host: invalid task id %q: use letters, digits, '.', '_' or '-' (at most 64)", req.TaskID)
	}
	ws := req.Workspace
	if ws == "" {
		if cfg.WorkspaceRoot == "" {
			return sandbox.Layout{}, fmt.Errorf("host: request has no workspace and Config.WorkspaceRoot is not set")
		}
		ws = sandbox.WorkspacePath(cfg.WorkspaceRoot, req.TaskID)
	}
	abs, err := filepath.Abs(ws)
	if err != nil {
		return sandbox.Layout{}, fmt.Errorf("host: workspace %q: %w", ws, err)
	}
	return sandbox.Layout{Workspace: abs, Home: filepath.Join(cfg.RunRoot, req.TaskID, "home")}, nil
}

// checkRequest validates req for a run (interactive false) or a login session (true) and returns
// its layout. It touches no file, so a refused request leaves nothing behind.
func checkRequest(cfg Config, req sandbox.CommandRequest, interactive bool) (sandbox.Layout, error) {
	layout, err := layoutFor(cfg, req)
	if err != nil {
		return layout, err
	}
	// The host cannot cut the network, so the mode that applies is always bridge.
	if err := sandbox.ValidateRequest(req, sandbox.NetworkModeBridge); err != nil {
		return layout, err
	}
	if req.NetworkMode == sandbox.NetworkModeNone {
		return layout, fmt.Errorf("%w: NetworkMode %q (the host cannot cut network access)", ErrUnsupported, req.NetworkMode)
	}
	if len(req.ReadOnlyMounts) > 0 {
		return layout, fmt.Errorf("%w: ReadOnlyMounts (no mount namespace); do not wrap the host runtime with an installed toolchain", ErrUnsupported)
	}
	if len(req.ExtraCacheMounts) > 0 {
		return layout, fmt.Errorf("%w: ExtraCacheMounts (no mount namespace)", ErrUnsupported)
	}
	if interactive {
		if len(req.SessionMounts) > 0 || len(req.CredentialFiles) > 0 {
			return layout, fmt.Errorf("%w: SessionMounts and CredentialFiles are not applied to interactive sessions", ErrUnsupported)
		}
		if !slices.ContainsFunc(cfg.InteractiveCommands, func(c []string) bool { return slices.Equal(c, req.Command) }) {
			return layout, fmt.Errorf("%w: %q", ErrNotAllowlisted, req.Command[0])
		}
		return layout, nil
	}
	if err := checkProfile(cfg, req.Profile); err != nil {
		return layout, err
	}
	return layout, checkTargets(layout.Home, req)
}

// checkProfile applies the tool guardrail: only profiles the registry marks NoTools run, unless
// the operator allowed tools.
func checkProfile(cfg Config, ref string) error {
	if cfg.AllowTools {
		return nil
	}
	if p, ok := profile.Lookup(ref); !ok || !p.NoTools {
		return fmt.Errorf("%w: profile %q (set Config.AllowTools to run it unconfined)", ErrToolsNotAllowed, ref)
	}
	return nil
}

// checkTargets requires every session mount and credential target to lie strictly inside home and
// session mounts not to nest. A credential inside a session mount is allowed and ends up in the
// session directory, as with the Docker runtime.
func checkTargets(home string, req sandbox.CommandRequest) error {
	inside := func(p string) bool { return strings.HasPrefix(p, home+string(filepath.Separator)) }
	covered := func(p, dir string) bool { return strings.HasPrefix(p, dir+"/") }
	for target := range req.SessionMounts {
		if !inside(target) {
			return fmt.Errorf("host: session mount target %q is outside the run HOME %q", target, home)
		}
		for other := range req.SessionMounts {
			if other != target && covered(target, other) {
				return fmt.Errorf("%w: session mount %q is nested inside %q", ErrUnsupported, target, other)
			}
		}
	}
	for target := range req.CredentialFiles {
		if !inside(target) {
			return fmt.Errorf("host: credential file target %q is outside the run HOME %q", target, home)
		}
	}
	return nil
}

// resolveDir returns the resolved location of dir, which need not exist yet (the deepest existing
// ancestor is resolved), and refuses it when allowed is set and the location lies outside every
// entry of it.
func resolveDir(dir string, allowed []string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("host: %q: %w", dir, err)
	}
	resolved, err := evalExisting(abs)
	if err != nil {
		return "", fmt.Errorf("host: resolve %q: %w", dir, err)
	}
	if len(allowed) > 0 && !slices.ContainsFunc(allowed, func(root string) bool { return within(root, resolved) }) {
		return "", fmt.Errorf("host: %q resolves to %q, outside the allowed roots", dir, resolved)
	}
	return resolved, nil
}

// evalExisting resolves symlinks in the longest existing prefix of p and appends the rest.
func evalExisting(p string) (string, error) {
	var rest []string
	for cur := p; ; {
		if r, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(append([]string{r}, rest...)...), nil
		} else if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p, nil
		}
		rest = append([]string{filepath.Base(cur)}, rest...)
		cur = parent
	}
}

// within reports whether p is root or lies below it, comparing resolved locations.
func within(root, p string) bool {
	resolved, err := evalExisting(filepath.Clean(root))
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(resolved, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// coversHome reports whether dir is the current user's HOME or one of its ancestors.
func coversHome(dir string) bool {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return false
	}
	resolved, err := evalExisting(filepath.Clean(dir))
	if err != nil {
		return false
	}
	realHome, err := evalExisting(filepath.Clean(home))
	if err != nil {
		return false
	}
	return within(resolved, realHome)
}

// buildEnv returns the child's environment as KEY=VALUE pairs: fixed defaults, overlaid by the
// request's merged env (so a caller can set its own HOME or PATH). Nothing is inherited from this
// process.
func buildEnv(cfg Config, req sandbox.CommandRequest, layout sandbox.Layout, tmp string, interactive bool) []string {
	env := map[string]string{
		"HOME":   layout.Home,
		"TMPDIR": tmp,
		"PATH":   cfg.PATH,
		"LANG":   "C.UTF-8",
	}
	if interactive {
		env["TERM"] = "xterm"
	}
	for k, v := range req.MergedEnv() {
		env[k] = v
	}
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

// lookPath finds name in the directories of pathList, like exec.LookPath but independent of this
// process's own PATH. A name with a slash is returned as is.
func lookPath(name, pathList string) (string, error) {
	if strings.Contains(name, "/") {
		return name, nil
	}
	for _, dir := range filepath.SplitList(pathList) {
		if dir == "" || !path.IsAbs(dir) {
			continue
		}
		p := filepath.Join(dir, name)
		if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
			return p, nil
		}
	}
	return "", fmt.Errorf("host: %q not found in PATH %q", name, pathList)
}

// writeFileReplacing writes content to path (mode 0600) by writing a temporary file next to it and
// renaming it over path. Unlike os.WriteFile, a symlink already at path is replaced, never
// followed, so a link left in a session directory cannot redirect the write elsewhere.
func writeFileReplacing(path string, content []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".aicli-cred-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	_, werr := tmp.Write(content)
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(name, 0o600) // CreateTemp already uses 0600; explicit for clarity under any umask change
	}
	if werr == nil {
		werr = os.Rename(name, path)
	}
	if werr != nil {
		_ = os.Remove(name)
	}
	return werr
}

// readRegularFile reads a file the child could have replaced. Only a plain file up to
// maxCredentialReadBytes is read, so a symlink planted in its place cannot hand a host file's
// content back as a "refreshed credential".
func readRegularFile(p string) ([]byte, error) {
	info, err := os.Lstat(p)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", p)
	}
	if info.Size() > maxCredentialReadBytes {
		return nil, fmt.Errorf("%s exceeds %d bytes", p, maxCredentialReadBytes)
	}
	return os.ReadFile(p)
}
