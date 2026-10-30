//go:build linux || darwin

package host

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// PruneOrphaned removes run directories under RunRoot that were last changed at least minAge ago
// and belong to no run in progress. A run that crashed the server leaves its directory behind,
// with decrypted credential files in it; this removes them. It satisfies janitor.OrphanPruner.
func (r *Runtime) PruneOrphaned(ctx context.Context, minAge time.Duration) (int, error) {
	entries, err := os.ReadDir(r.cfg.RunRoot)
	if err != nil {
		return 0, fmt.Errorf("host: list run root: %w", err)
	}
	removed := 0
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return removed, err
		}
		r.mu.Lock()
		_, busy := r.active[e.Name()]
		r.mu.Unlock()
		info, err := e.Info()
		if busy || err != nil || time.Since(info.ModTime()) < minAge {
			continue
		}
		// RemoveAll removes a symlink itself and never follows it.
		if err := os.RemoveAll(filepath.Join(r.cfg.RunRoot, e.Name())); err != nil {
			return removed, fmt.Errorf("host: remove orphaned run directory: %w", err)
		}
		removed++
	}
	return removed, nil
}
