package toolchain

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"
)

// SyncOptions configures Sync.
type SyncOptions struct {
	// Tools to check; empty means Tools() (agy, claude, codex).
	Tools []string
	// Keep is how many recent versions to retain per tool; <= 0 means DefaultKeepVersions.
	Keep int
	// InstallOnly stops Sync from switching: it only installs and prunes, and the operator
	// switches with `use` after trying the new release. By default a new release becomes current
	// at once, but only where that follows what the operator has set up: on first setup (nothing
	// installed yet), or when current is the newest good version installed. A rollback (`use` of an
	// older version, or an automatic one), a `reset` and a version marked bad are never overridden.
	// A release Sync makes current is on probation: see Store.ReportOutcome and Runtime.
	InstallOnly bool
	// Check, if set, is a last test of a new release before it becomes current (for example a run
	// of the CLI in a container with dir mounted). A release that fails it is marked bad and not
	// activated. The install itself already ran `--version` from a read-only mount.
	Check func(ctx context.Context, tool, version, dir string) error
	// Now is for tests.
	Now func() time.Time
	// Protected, if set, reports versions that must not be pruned because something still needs
	// them, for example versions pinned in requests.
	Protected func(tool, version string) bool
}

// SyncResult records the outcome of a synchronization cycle.
type SyncResult struct {
	Installed map[string]string   // tool -> newly installed version
	Activated map[string]string   // tool -> version made current
	Pruned    map[string][]string // tool -> versions removed
}

// Sync installs the newest release of each tool that is not installed yet, makes it current unless
// SyncOptions.InstallOnly (see there for when it does not), and prunes old versions. One tool failing does not stop the
// others: their results are returned together with the joined errors.
func Sync(ctx context.Context, installer *Installer, opts SyncOptions) (SyncResult, error) {
	res := SyncResult{Installed: map[string]string{}, Activated: map[string]string{}, Pruned: map[string][]string{}}
	if installer == nil || installer.Store == nil {
		return res, errors.New("toolchain: installer and store are required")
	}
	tools := opts.Tools
	if len(tools) == 0 {
		tools = Tools()
	}
	var errs []error
	for _, name := range tools {
		if err := ctx.Err(); err != nil {
			return res, errors.Join(append(errs, err)...)
		}
		if err := syncTool(ctx, installer, name, opts, &res); err != nil {
			errs = append(errs, fmt.Errorf("sync %s: %w", name, err))
		}
	}
	return res, errors.Join(errs...)
}

func syncTool(ctx context.Context, in *Installer, name string, opts SyncOptions, res *SyncResult) error {
	before, err := in.Store.Versions(name)
	if err != nil {
		return err
	}
	cur, err := in.Store.Current(name)
	if err != nil {
		return err
	}
	bad, err := in.Store.BadVersions(name)
	if err != nil {
		return err
	}
	latest, err := in.Install(ctx, name, Latest)
	if err != nil {
		return err
	}
	if !slices.Contains(before, latest) {
		res.Installed[name] = latest
	}

	var activateErr error
	if !opts.InstallOnly && latest != cur && bad[latest] == "" && followsLatest(withoutBad(before, bad), cur, latest) {
		activateErr = activate(ctx, in.Store, name, latest, opts)
		if activateErr == nil {
			res.Activated[name] = latest
		}
	}

	var protect func(string) bool
	if opts.Protected != nil {
		protect = func(v string) bool { return opts.Protected(name, v) }
	}
	pruned, err := in.Store.PruneKeeping(name, opts.Keep, protect)
	if len(pruned) > 0 {
		res.Pruned[name] = pruned
	}
	return errors.Join(activateErr, err)
}

// activate runs the optional check, then makes version current on probation. A release that fails
// the check is marked bad.
func activate(ctx context.Context, st *Store, name, version string, opts SyncOptions) error {
	if opts.Check != nil {
		if err := opts.Check(ctx, name, version, st.versionDir(name, version)); err != nil {
			reason := "failed its check: " + err.Error()
			if markErr := st.MarkBad(name, version, reason); markErr != nil {
				err = errors.Join(err, markErr)
			}
			return fmt.Errorf("%s %s not activated, it %s", name, version, reason)
		}
	}
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	if err := st.useOnProbation(name, version, now()); err != nil {
		return fmt.Errorf("activate %s: %w", version, err)
	}
	return nil
}

func withoutBad(versions []string, bad map[string]string) []string {
	good := make([]string, 0, len(versions))
	for _, v := range versions {
		if bad[v] == "" {
			good = append(good, v)
		}
	}
	return good
}

// followsLatest reports whether switching to latest respects the operator's setup: nothing good
// was installed before (first setup), or current is the newest good version and latest is newer.
func followsLatest(good []string, current, latest string) bool {
	if len(good) == 0 {
		return true
	}
	tip := slices.MaxFunc(good, compareVersions)
	return current == tip && compareVersions(latest, tip) > 0
}
