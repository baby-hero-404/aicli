package toolchain

import (
	"context"
	"fmt"
	"io"
	"maps"
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
}

var _ sandbox.Runtime = (*Runtime)(nil)

// Wrap returns inner with the store's tools mounted read-only into every Run and
// RunInteractive. Requests are copied, never modified.
func Wrap(inner sandbox.Runtime, store *Store) *Runtime {
	return &Runtime{inner: inner, store: store}
}

// Run implements sandbox.Runtime.
func (r *Runtime) Run(ctx context.Context, req sandbox.CommandRequest) (*sandbox.CommandResult, error) {
	req, err := r.apply(req)
	if err != nil {
		return nil, err
	}
	return r.inner.Run(ctx, req)
}

// RunInteractive implements sandbox.Runtime.
func (r *Runtime) RunInteractive(ctx context.Context, req sandbox.CommandRequest, stdin io.Reader, stdout, stderr io.Writer) error {
	req, err := r.apply(req)
	if err != nil {
		return err
	}
	return r.inner.RunInteractive(ctx, req, stdin, stdout, stderr)
}

// Prewarm implements sandbox.Runtime.
func (r *Runtime) Prewarm(ctx context.Context) error { return r.inner.Prewarm(ctx) }

func (r *Runtime) apply(req sandbox.CommandRequest) (sandbox.CommandRequest, error) {
	dirs, err := r.store.Resolve(req.ToolVersions)
	if err != nil {
		return req, err
	}
	if len(dirs) == 0 {
		return req, nil // nothing installed: the image's own CLIs, if any, are used
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
	for name, dir := range dirs {
		if err := add(containerDir(name), dir); err != nil {
			return req, err
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
		return req, err
	}
	base := env["PATH"]
	if base == "" {
		base = defaultPath
	}
	env["PATH"] = strings.Join(binDirs(names), ":") + ":" + base

	req.ReadOnlyMounts = mounts
	req.Env = env
	return req, nil
}
