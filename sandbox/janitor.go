package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Default TTLs for filesystem housekeeping.
const (
	DefaultWorkspacesTTL = 30 * 24 * time.Hour // 30 days
	DefaultSessionsTTL   = 30 * 24 * time.Hour // 30 days
	DefaultCacheTTL      = 14 * 24 * time.Hour // 14 days
	DefaultLogsTTL       = 14 * 24 * time.Hour // 14 days
)

// ErrUnsafeRoot is returned instead of deleting anything when a cleanup root could cover data that
// is not the sandbox's: the filesystem root, the host user's HOME or a directory above it, or a
// path that is not absolute.
var ErrUnsafeRoot = errors.New("sandbox: unsafe cleanup root")

// JanitorConfig defines the directories and retention policies for aicli filesystem cleanup.
//
// A root left empty is not cleaned. For a root that is set, a zero TTL means the package default
// and a negative TTL switches that cleanup off. Every root must be a directory that holds only
// aicli's data: everything directly inside it that is older than the TTL is deleted.
type JanitorConfig struct {
	// WorkspacesRoot holds task/conversation workspace directories.
	WorkspacesRoot string
	// WorkspacesTTL is how long an inactive workspace is preserved before deletion.
	WorkspacesTTL time.Duration

	// SessionsRoot holds CLI persistent profile sessions: <SessionsRoot>/<profile>/<accountID>.
	SessionsRoot string
	// SessionsTTL is how long an inactive session is preserved before deletion.
	SessionsTTL time.Duration

	// CacheRoot holds sandbox package manager cache (npm, pip, go, etc.).
	CacheRoot string
	// CacheTTL is how long cache entries are preserved before deletion.
	CacheTTL time.Duration

	// LogsRoot holds host-side sandbox log files (CommandRequest.LogsHostDir).
	LogsRoot string
	// LogsTTL is how long log files are preserved before deletion.
	LogsTTL time.Duration

	// Keep, if set, is asked before a workspace, session or cache entry is deleted; returning true
	// keeps it. Use it for what the file times cannot show, for example a conversation that is
	// still open in the host's database.
	Keep func(path string) bool
	// DryRun reports what would be deleted and deletes nothing. Run it once before enabling the
	// cleanup on a directory you have not cleaned before.
	DryRun bool

	// Now returns the current time for age calculations. If nil, time.Now is used.
	Now func() time.Time
}

// JanitorReport summarizes paths deleted (or, in a dry run, that would be deleted) in a pass.
type JanitorReport struct {
	Workspaces []string
	Sessions   []string
	Cache      []string
	Logs       []string
}

// TotalPruned returns the sum of all pruned paths in the report.
func (r JanitorReport) TotalPruned() int {
	return len(r.Workspaces) + len(r.Sessions) + len(r.Cache) + len(r.Logs)
}

// PruneOptions adjusts one Prune call.
type PruneOptions struct {
	// Keep returns true to keep a path that is old enough to be deleted.
	Keep func(path string) bool
	// DryRun lists what would be deleted without deleting it.
	DryRun bool
}

// RunJanitor performs a full cleanup pass over the configured directories according to their TTLs.
// All roots are checked first, so a misconfigured one aborts the pass before anything is deleted.
// A removal that fails does not stop the pass; the failures are returned together, with the report
// of everything that was removed.
func RunJanitor(ctx context.Context, cfg JanitorConfig) (JanitorReport, error) {
	now := time.Now()
	if cfg.Now != nil {
		now = cfg.Now()
	}
	for _, root := range []string{cfg.WorkspacesRoot, cfg.SessionsRoot, cfg.CacheRoot, cfg.LogsRoot} {
		if root == "" {
			continue
		}
		if err := checkRoot(root); err != nil {
			return JanitorReport{}, err
		}
	}
	opt := PruneOptions{Keep: cfg.Keep, DryRun: cfg.DryRun}

	var report JanitorReport
	var errs []error
	run := func(name, root string, ttl, def time.Duration, prune func(string, time.Duration) ([]string, error), dst *[]string) {
		ttl = ttlOrDefault(ttl, def)
		if root == "" || ttl <= 0 {
			return
		}
		pruned, err := prune(root, ttl)
		*dst = pruned
		if err != nil {
			errs = append(errs, fmt.Errorf("prune %s: %w", name, err))
		}
	}
	run("workspaces", cfg.WorkspacesRoot, cfg.WorkspacesTTL, DefaultWorkspacesTTL,
		func(r string, ttl time.Duration) ([]string, error) { return PruneDirEntries(ctx, r, ttl, now, opt) }, &report.Workspaces)
	run("sessions", cfg.SessionsRoot, cfg.SessionsTTL, DefaultSessionsTTL,
		func(r string, ttl time.Duration) ([]string, error) { return PruneSessions(ctx, r, ttl, now, opt) }, &report.Sessions)
	run("cache", cfg.CacheRoot, cfg.CacheTTL, DefaultCacheTTL,
		func(r string, ttl time.Duration) ([]string, error) { return PruneDirEntries(ctx, r, ttl, now, opt) }, &report.Cache)
	run("logs", cfg.LogsRoot, cfg.LogsTTL, DefaultLogsTTL,
		func(r string, ttl time.Duration) ([]string, error) { return PruneFileEntries(ctx, r, ttl, now, opt) }, &report.Logs)
	return report, errors.Join(errs...)
}

// ttlOrDefault: 0 means def, a negative value switches the cleanup off (returned as is).
func ttlOrDefault(ttl, def time.Duration) time.Duration {
	if ttl == 0 {
		return def
	}
	return ttl
}

// systemDirs are directories no cleanup root may be: they hold other programs' data, whatever the
// operator meant. A directory below them (/var/lib/aicli) is fine.
var systemDirs = []string{
	"/bin", "/boot", "/dev", "/etc", "/home", "/lib", "/lib64", "/media", "/mnt", "/opt", "/proc",
	"/root", "/run", "/sbin", "/srv", "/sys", "/tmp", "/usr", "/usr/local", "/var", "/var/cache",
	"/var/lib", "/var/log", "/var/tmp",
}

// checkRoot refuses a cleanup root that could cover the user's own data or the system's.
func checkRoot(root string) error {
	if !filepath.IsAbs(root) {
		return fmt.Errorf("%w: %q is not an absolute path", ErrUnsafeRoot, root)
	}
	real := filepath.Clean(root)
	if r, err := filepath.EvalSymlinks(real); err == nil {
		real = r
	}
	if filepath.Dir(real) == real {
		return fmt.Errorf("%w: %q is the filesystem root", ErrUnsafeRoot, root)
	}
	for _, dir := range systemDirs {
		if real == dir || filepath.Clean(root) == dir {
			return fmt.Errorf("%w: %q is a system directory; use a directory of your own below it", ErrUnsafeRoot, root)
		}
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" && coversPath(real, home) {
		return fmt.Errorf("%w: %q is the host user's HOME or a directory above it", ErrUnsafeRoot, root)
	}
	return nil
}

// PruneDirEntries removes the entries (files or folders) directly inside root that have had no
// activity for maxAge relative to now. An entry's activity is the newest modification time of
// anything inside it: editing a file deep in a workspace keeps the whole workspace. Hidden entries
// (starting with ".") are skipped.
func PruneDirEntries(ctx context.Context, root string, maxAge time.Duration, now time.Time, opt PruneOptions) ([]string, error) {
	if root == "" || maxAge <= 0 {
		return nil, nil
	}
	if err := checkRoot(root); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var pruned []string
	var errs []error
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return pruned, errors.Join(append(errs, err)...)
		}
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		target := filepath.Join(root, entry.Name())
		if ok, err := removeIfIdle(ctx, target, maxAge, now, opt); err != nil {
			errs = append(errs, err)
		} else if ok {
			pruned = append(pruned, target)
		}
	}
	return pruned, errors.Join(errs...)
}

// PruneSessions walks <root>/<profile>/<accountID> and removes the account session directories
// that have had no activity for maxAge relative to now (see PruneDirEntries: the login a CLI
// refreshes inside its state directory counts as activity). A profile directory left empty is
// removed too.
func PruneSessions(ctx context.Context, root string, maxAge time.Duration, now time.Time, opt PruneOptions) ([]string, error) {
	if root == "" || maxAge <= 0 {
		return nil, nil
	}
	if err := checkRoot(root); err != nil {
		return nil, err
	}
	profiles, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var pruned []string
	var errs []error
	for _, profile := range profiles {
		if !profile.IsDir() || strings.HasPrefix(profile.Name(), ".") {
			continue
		}
		profileDir := filepath.Join(root, profile.Name())
		accounts, err := os.ReadDir(profileDir)
		if err != nil {
			continue
		}
		for _, account := range accounts {
			if err := ctx.Err(); err != nil {
				return pruned, errors.Join(append(errs, err)...)
			}
			if strings.HasPrefix(account.Name(), ".") {
				continue
			}
			target := filepath.Join(profileDir, account.Name())
			if ok, err := removeIfIdle(ctx, target, maxAge, now, opt); err != nil {
				errs = append(errs, err)
			} else if ok {
				pruned = append(pruned, target)
			}
		}
		// Clean up an empty profile directory (os.Remove refuses a non-empty one, so a session
		// created meanwhile is safe).
		if !opt.DryRun {
			if remaining, err := os.ReadDir(profileDir); err == nil && len(remaining) == 0 {
				_ = os.Remove(profileDir)
			}
		}
	}
	return pruned, errors.Join(errs...)
}

// PruneFileEntries removes the regular files below root (including subdirectories) that were last
// modified at least maxAge before now. Hidden files and directories are skipped.
func PruneFileEntries(ctx context.Context, root string, maxAge time.Duration, now time.Time, opt PruneOptions) ([]string, error) {
	if root == "" || maxAge <= 0 {
		return nil, nil
	}
	if err := checkRoot(root); err != nil {
		return nil, err
	}
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return nil, nil
	}
	var pruned []string
	var errs []error
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if err != nil || path == root {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil || now.Sub(info.ModTime()) < maxAge {
			return nil
		}
		if opt.DryRun {
			pruned = append(pruned, path)
			return nil
		}
		if err := os.Remove(path); err != nil {
			errs = append(errs, err)
			return nil
		}
		pruned = append(pruned, path)
		return nil
	})
	return pruned, errors.Join(append(errs, err)...)
}

// removeIfIdle deletes target when nothing inside it changed within maxAge and Keep does not object.
// It reports whether target was (or, in a dry run, would be) removed.
func removeIfIdle(ctx context.Context, target string, maxAge time.Duration, now time.Time, opt PruneOptions) (bool, error) {
	active, err := recentActivity(ctx, target, now.Add(-maxAge))
	if err != nil || active {
		return false, err
	}
	if opt.Keep != nil && opt.Keep(target) {
		return false, nil
	}
	if opt.DryRun {
		return true, nil
	}
	if err := os.RemoveAll(target); err != nil {
		// Typically files the container created as another user (root). What was removed stays
		// removed, which also refreshes the directory's time, so a retry waits another TTL.
		return false, fmt.Errorf("remove %s (files owned by another user, for example created by a container as root?): %w", target, err)
	}
	return true, nil
}

// recentActivity reports whether target or anything inside it was modified at or after cutoff. It
// stops at the first such entry, so a busy tree is cheap; an idle one is walked completely, which a
// deletion needs anyway. Symlinks are not followed: their own time counts, not their target's.
func recentActivity(ctx context.Context, target string, cutoff time.Time) (bool, error) {
	active := false
	err := filepath.WalkDir(target, func(path string, d fs.DirEntry, err error) error {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if err != nil {
			// Something unreadable inside: do not guess that the tree is idle.
			active = true
			return fs.SkipAll
		}
		info, err := d.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			active = true
			return fs.SkipAll
		}
		return nil
	})
	return active, err
}
