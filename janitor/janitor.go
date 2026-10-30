// Package janitor runs aicli's housekeeping in the background of a host application, with no cron:
// it recovers turns whose instance died, removes orphaned sandbox containers, deletes idle
// workspaces, sessions, caches and logs, and keeps the host-installed CLI tools current and pruned.
//
// It depends on small interfaces, not on Docker or the conversation package: pass a
// *docker.Runtime as Orphans and a *conversation.Service as Conversations, or leave any task out.
package janitor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/baby-hero-404/aicli/sandbox"
	"github.com/baby-hero-404/aicli/toolchain"
)

// Default intervals and limits.
const (
	DefaultConversationInterval = time.Minute
	DefaultOrphanInterval       = 10 * time.Minute
	DefaultOrphanMinAge         = 10 * time.Minute
	DefaultFilesystemInterval   = time.Hour
	DefaultToolchainInterval    = 24 * time.Hour
	DefaultTaskTimeout          = 15 * time.Minute
	// DefaultToolchainTimeout is longer: one pass can install three tools, each in a container
	// that may take up to 10 minutes on a slow registry.
	DefaultToolchainTimeout = time.Hour
)

// OrphanPruner removes sandbox containers left behind that stopped at least minAge ago
// (sandbox/docker.Runtime).
type OrphanPruner interface {
	PruneOrphaned(ctx context.Context, minAge time.Duration) (int, error)
}

// StaleRecoverer marks turns whose instance went away as interrupted (conversation.Service).
type StaleRecoverer interface {
	RecoverStale(ctx context.Context) (int, error)
}

// Config selects the tasks to run. A task whose dependency is left out is not run.
type Config struct {
	// Conversations recovers stale turns every ConversationInterval (default 1 minute).
	Conversations        StaleRecoverer
	ConversationInterval time.Duration

	// Orphans removes orphaned containers every OrphanInterval (default 10 minutes) once they
	// stopped OrphanMinAge ago (default 10 minutes).
	Orphans            OrphanPruner
	OrphanInterval     time.Duration
	OrphanMinAge       time.Duration
	Filesystem         sandbox.JanitorConfig // runs when any of its roots is set
	FilesystemInterval time.Duration         // default 1 hour

	// Toolchain installs new CLI releases and prunes old ones every ToolchainInterval (default 24
	// hours); ToolchainSync says which tools, how many versions to keep, and whether a new release
	// is only installed (see toolchain.SyncOptions). A new release becomes current at once and is
	// rolled back if it fails: wrap the sandbox runtime with toolchain.Wrap to get that.
	Toolchain         *toolchain.Installer
	ToolchainSync     toolchain.SyncOptions
	ToolchainInterval time.Duration

	// TaskTimeout bounds one run of one task (default 15 minutes); ToolchainTimeout bounds the
	// toolchain task (default 1 hour).
	TaskTimeout      time.Duration
	ToolchainTimeout time.Duration
	// Logger receives what the janitor does and the warnings; nil means slog.Default().
	Logger *slog.Logger
}

// Janitor runs the configured tasks.
type Janitor struct {
	log     *slog.Logger
	timeout time.Duration
	tasks   []*task
	wg      sync.WaitGroup
}

type task struct {
	name    string
	every   time.Duration
	timeout time.Duration
	run     func(ctx context.Context) error
	mu      sync.Mutex // a task never overlaps itself
}

// New builds a Janitor from cfg.
func New(cfg Config) *Janitor {
	j := &Janitor{log: cfg.Logger, timeout: orDefault(cfg.TaskTimeout, DefaultTaskTimeout)}
	if j.log == nil {
		j.log = slog.Default()
	}
	if cfg.Conversations != nil {
		j.add("recover stale turns", orDefault(cfg.ConversationInterval, DefaultConversationInterval), func(ctx context.Context) error {
			n, err := cfg.Conversations.RecoverStale(ctx)
			j.countf(n, "recovered stale turns")
			return err
		})
	}
	if cfg.Orphans != nil {
		minAge := orDefault(cfg.OrphanMinAge, DefaultOrphanMinAge)
		j.add("prune orphaned containers", orDefault(cfg.OrphanInterval, DefaultOrphanInterval), func(ctx context.Context) error {
			n, err := cfg.Orphans.PruneOrphaned(ctx, minAge)
			j.countf(n, "pruned orphaned containers")
			return err
		})
	}
	if fs := cfg.Filesystem; fs.WorkspacesRoot != "" || fs.SessionsRoot != "" || fs.CacheRoot != "" || fs.LogsRoot != "" {
		j.add("clean the filesystem", orDefault(cfg.FilesystemInterval, DefaultFilesystemInterval), func(ctx context.Context) error {
			report, err := sandbox.RunJanitor(ctx, fs)
			if report.TotalPruned() > 0 {
				verb := "removed"
				if fs.DryRun {
					verb = "would remove (dry run)"
				}
				j.log.Info("janitor: "+verb, "workspaces", len(report.Workspaces), "sessions", len(report.Sessions),
					"cache", len(report.Cache), "logs", len(report.Logs))
			}
			return err
		})
	}
	if cfg.Toolchain != nil && cfg.Toolchain.Store != nil {
		j.addWithTimeout("sync the CLI toolchain", orDefault(cfg.ToolchainInterval, DefaultToolchainInterval), orDefault(cfg.ToolchainTimeout, DefaultToolchainTimeout), func(ctx context.Context) error {
			res, err := toolchain.Sync(ctx, cfg.Toolchain, cfg.ToolchainSync)
			if len(res.Installed) > 0 || len(res.Activated) > 0 || len(res.Pruned) > 0 {
				j.log.Info("janitor: toolchain synced", "installed", res.Installed, "activated", res.Activated, "pruned", res.Pruned)
			}
			return err
		})
	}
	return j
}

func orDefault(d, def time.Duration) time.Duration {
	if d <= 0 {
		return def
	}
	return d
}

func (j *Janitor) add(name string, every time.Duration, run func(context.Context) error) {
	j.addWithTimeout(name, every, j.timeout, run)
}

func (j *Janitor) addWithTimeout(name string, every, timeout time.Duration, run func(context.Context) error) {
	j.tasks = append(j.tasks, &task{name: name, every: every, timeout: timeout, run: run})
}

func (j *Janitor) countf(n int, msg string) {
	if n > 0 {
		j.log.Info("janitor: "+msg, "count", n)
	}
}

// RunOnce runs every task once, one after the other. A task that fails or panics is logged and the
// next one still runs.
func (j *Janitor) RunOnce(ctx context.Context) {
	for _, t := range j.tasks {
		j.runTask(ctx, t)
	}
}

// Start runs every task once right away in the background, then each again on its own interval,
// until ctx ends. It returns at once; call Wait after cancelling ctx to wait for the workers.
func (j *Janitor) Start(ctx context.Context) {
	j.log.Info("janitor: starting", "tasks", len(j.tasks))
	j.wg.Go(func() { j.RunOnce(ctx) })
	for _, t := range j.tasks {
		j.wg.Go(func() {
			ticker := time.NewTicker(t.every)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					j.runTask(ctx, t)
				}
			}
		})
	}
}

// Wait blocks until the workers started by Start have stopped.
func (j *Janitor) Wait() { j.wg.Wait() }

// runTask runs t once with a timeout. It skips the run when the previous one is still going, and it
// never lets an error or a panic out: housekeeping must not take the host down.
func (j *Janitor) runTask(ctx context.Context, t *task) {
	if ctx.Err() != nil {
		return
	}
	if !t.mu.TryLock() {
		j.log.Debug("janitor: previous run still going, skipping", "task", t.name)
		return
	}
	defer t.mu.Unlock()
	parent := ctx
	ctx, cancel := context.WithTimeout(parent, t.timeout)
	defer cancel()
	defer func() {
		if r := recover(); r != nil {
			j.log.Error("janitor: task panicked", "task", t.name, "panic", fmt.Sprint(r))
		}
	}()
	err := t.run(ctx)
	switch {
	case parent.Err() != nil:
		// the host is shutting down: the task was cut short on purpose, say nothing
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		j.log.Warn("janitor: task timed out; the rest runs at the next interval", "task", t.name, "timeout", t.timeout, "err", err)
	case err != nil:
		j.log.Warn("janitor: task failed", "task", t.name, "err", err)
	}
}
