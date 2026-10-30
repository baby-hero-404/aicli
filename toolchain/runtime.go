package toolchain

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/baby-hero-404/aicli/sandbox"
)

// defaultPath is the PATH of Debian-based images (node, aicli/base) that tool directories are
// put in front of when the request sets none.
const defaultPath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

// Runtime is a sandbox.Runtime that mounts host-installed tools into every request.
type Runtime struct {
	inner sandbox.Runtime
	store *Store

	// Health tunes the automatic rollback (see Store.ReportOutcome); the zero value uses defaults.
	Health HealthConfig
	// OnRollback is called after a release was rolled back automatically, so the host can alert.
	// When nil the rollback is logged as a warning.
	OnRollback func(RollbackEvent)
}

var _ sandbox.Runtime = (*Runtime)(nil)

// Wrap returns inner with the store's tools mounted read-only into every Run and
// RunInteractive. Requests are copied, never modified.
func Wrap(inner sandbox.Runtime, store *Store) *Runtime {
	return &Runtime{inner: inner, store: store}
}

// WrapDefault opens the default toolchain store (at DefaultRoot()) and wraps inner.
func WrapDefault(inner sandbox.Runtime) (*Runtime, error) {
	store, err := OpenDefault()
	if err != nil {
		return nil, err
	}
	return Wrap(inner, store), nil
}

// Run implements sandbox.Runtime.
func (r *Runtime) Run(ctx context.Context, req sandbox.CommandRequest) (*sandbox.CommandResult, error) {
	req, mounted, err := r.apply(req)
	if err != nil {
		return nil, err
	}
	res, err := r.inner.Run(ctx, req)
	if err == nil {
		r.report(req, mounted, res)
	}
	return res, err
}

// RunInteractive implements sandbox.Runtime.
func (r *Runtime) RunInteractive(ctx context.Context, req sandbox.CommandRequest, stdin io.Reader, stdout, stderr io.Writer) error {
	req, _, err := r.apply(req)
	if err != nil {
		return err
	}
	return r.inner.RunInteractive(ctx, req, stdin, stdout, stderr)
}

// Prewarm implements sandbox.Runtime.
func (r *Runtime) Prewarm(ctx context.Context) error { return r.inner.Prewarm(ctx) }

// apply mounts the store's tools into req. It also returns which version of each tool was mounted,
// for reporting how the run went.
func (r *Runtime) apply(req sandbox.CommandRequest) (sandbox.CommandRequest, map[string]string, error) {
	dirs, err := r.store.Resolve(req.ToolVersions)
	if err != nil {
		return req, nil, err
	}
	if len(dirs) == 0 {
		return req, nil, nil // nothing installed: the image's own CLIs, if any, are used
	}
	mounts := maps.Clone(req.ReadOnlyMounts)
	if mounts == nil {
		mounts = map[string]string{}
	}
	add := func(target, source string) error {
		if _, taken := mounts[target]; taken {
			return fmt.Errorf("toolchain: read-only mount %s is already set by the caller", target)
		}
		if _, taken := req.SessionMounts[target]; taken {
			return fmt.Errorf("toolchain: %s is already a session mount", target)
		}
		mounts[target] = source
		return nil
	}
	env := maps.Clone(req.Env)
	if env == nil {
		env = map[string]string{}
	}
	names := make([]string, 0, len(dirs))
	mounted := make(map[string]string, len(dirs))
	for name, dir := range dirs {
		mounted[name] = filepath.Base(dir)
		if err := add(containerDir(name), dir); err != nil {
			return req, nil, err
		}
		names = append(names, name)
		t, _ := lookup(name)
		for k, v := range t.env {
			if _, set := env[k]; !set {
				env[k] = v
			}
		}
	}
	if err := add(ProfileTarget, r.store.profilePath()); err != nil {
		return req, nil, err
	}
	base := env["PATH"]
	if base == "" {
		base = defaultPath
	}
	env["PATH"] = strings.Join(binDirs(names), ":") + ":" + base

	req.ReadOnlyMounts = mounts
	req.Env = env
	return req, mounted, nil
}

// brokenExitCodes mean the program could not run or crashed, whatever the task: cannot execute (126),
// not found (127), illegal instruction (132), segmentation fault (139). 134 (abort) is left out on
// purpose: Node aborts that way when a big task runs out of heap, which says nothing about the
// release. 137 (SIGKILL, the OOM killer) is out for the same reason.
var brokenExitCodes = []int{126, 127, 132, 139}

// report tells the store how the run went for each mounted tool the command mentions. Only clear
// signals count: exit 0 is a success and one of brokenExitCodes is a failure of the tool; any other
// result (a failing task, quota, a login problem, a kill by the watchdog) says nothing about the
// release and is not reported. A tool is matched by name in the command line, so a command that
// chains other programs ("claude ... && npm test") can have their 126/127 charged to the tool; it
// takes MaxFailures in a row, so this stays rare. Hosts that can tell more (an output format that
// no longer parses) call Store.ReportOutcome themselves.
func (r *Runtime) report(req sandbox.CommandRequest, mounted map[string]string, res *sandbox.CommandResult) {
	if res == nil || res.Killed || len(mounted) == 0 {
		return
	}
	var ok bool
	switch {
	case res.ExitCode == 0:
		ok = true
	case slices.Contains(brokenExitCodes, res.ExitCode):
	default:
		return
	}
	command := strings.Join(req.Command, " ")
	for name, version := range mounted {
		if t, err := lookup(name); err != nil || !mentions(command, t.binary) {
			continue
		}
		ev, err := r.store.ReportOutcome(name, version, ok, fmt.Sprintf("exit code %d", res.ExitCode), r.Health)
		if err != nil {
			slog.Warn("toolchain: report outcome", "tool", name, "version", version, "err", err)
			continue
		}
		if ev == nil {
			continue
		}
		if r.OnRollback != nil {
			r.OnRollback(*ev)
		} else {
			slog.Warn("toolchain: a new release failed and was rolled back", "tool", ev.Tool, "from", ev.From, "to", ev.To, "reason", ev.Reason)
		}
	}
}

// mentions reports whether command contains the program name as a word (so "agy" is not found in
// "agency").
func mentions(command, program string) bool {
	for i := 0; ; {
		j := strings.Index(command[i:], program)
		if j < 0 {
			return false
		}
		start, end := i+j, i+j+len(program)
		before := start == 0 || !isNameChar(command[start-1])
		after := end == len(command) || !isNameChar(command[end])
		if before && after {
			return true
		}
		i = end
	}
}

func isNameChar(b byte) bool {
	return b == '-' || b == '_' || b == '.' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}
