package sandbox

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

const day = 24 * time.Hour

var bg = context.Background()

// write creates dir/rel with content, making parent directories.
func write(t *testing.T, root, rel, content string) string {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// ageTree sets the modification time of root and everything below it, deepest first.
func ageTree(t *testing.T, root string, at time.Time) {
	t.Helper()
	var paths []string
	_ = filepath.WalkDir(root, func(p string, _ fs.DirEntry, err error) error {
		if err == nil {
			paths = append(paths, p)
		}
		return nil
	})
	slices.Reverse(paths)
	for _, p := range paths {
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
	}
}

func exists(p string) bool { _, err := os.Lstat(p); return err == nil }

func TestPruneDirEntries(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	old := write(t, root, "old-workspace/a.txt", "x")
	fresh := write(t, root, "new-workspace/a.txt", "x")
	hidden := write(t, root, ".hidden/a.txt", "x")
	ageTree(t, filepath.Join(root, "old-workspace"), now.Add(-40*day))
	ageTree(t, filepath.Join(root, ".hidden"), now.Add(-40*day))

	pruned, err := PruneDirEntries(bg, root, 30*day, now, PruneOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{filepath.Join(root, "old-workspace")}; !slices.Equal(pruned, want) {
		t.Fatalf("pruned = %v, want %v", pruned, want)
	}
	if exists(old) || !exists(fresh) || !exists(hidden) {
		t.Errorf("old kept=%v fresh kept=%v hidden kept=%v", exists(old), exists(fresh), exists(hidden))
	}
}

// The directory's own time only changes when entries are added or removed directly in it: a CLI
// that edits files deep inside a workspace must still count as activity.
func TestPruneDirEntries_AWorkspaceWithRecentWorkInsideIsKept(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	ws := filepath.Join(root, "conv1")
	file := write(t, ws, "src/pkg/main.go", "v1")
	ageTree(t, ws, now.Add(-40*day))
	if err := os.WriteFile(file, []byte("edited today"), 0o644); err != nil { // does not touch any directory time
		t.Fatal(err)
	}

	pruned, err := PruneDirEntries(bg, root, 30*day, now, PruneOptions{})
	if err != nil || len(pruned) != 0 || !exists(file) {
		t.Fatalf("a workspace edited today was pruned: pruned=%v err=%v", pruned, err)
	}

	// once the work is old as well, it goes
	ageTree(t, ws, now.Add(-40*day))
	if pruned, _ = PruneDirEntries(bg, root, 30*day, now, PruneOptions{}); len(pruned) != 1 || exists(ws) {
		t.Errorf("an idle workspace survived: pruned=%v", pruned)
	}
}

func TestPruneSessions(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	oldA := write(t, root, "p1/old-account/.gemini/oauth_creds.json", "t")
	write(t, root, "p1/fresh-account/.gemini/oauth_creds.json", "t")
	write(t, root, "p2/only-old/.gemini/oauth_creds.json", "t")
	ageTree(t, filepath.Join(root, "p1", "old-account"), now.Add(-35*day))
	ageTree(t, filepath.Join(root, "p2"), now.Add(-35*day))

	pruned, err := PruneSessions(bg, root, 30*day, now, PruneOptions{})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(pruned)
	want := []string{filepath.Join(root, "p1", "old-account"), filepath.Join(root, "p2", "only-old")}
	if !slices.Equal(pruned, want) {
		t.Fatalf("pruned = %v, want %v", pruned, want)
	}
	if exists(oldA) || !exists(filepath.Join(root, "p1", "fresh-account")) {
		t.Error("wrong sessions removed")
	}
	if exists(filepath.Join(root, "p2")) {
		t.Error("the profile directory left empty should be removed")
	}
	if !exists(filepath.Join(root, "p1")) {
		t.Error("a profile directory that still has sessions must stay")
	}
}

// A login the CLI refreshes inside its state directory is activity, however old the account
// directory itself is.
func TestPruneSessions_ALoginRefreshedRecentlyIsKept(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	acct := filepath.Join(root, "antigravity", "acct1")
	token := write(t, acct, ".gemini/oauth_creds.json", "t1")
	ageTree(t, acct, now.Add(-40*day))
	if err := os.WriteFile(token, []byte("refreshed today"), 0o600); err != nil {
		t.Fatal(err)
	}

	pruned, err := PruneSessions(bg, root, 30*day, now, PruneOptions{})
	if err != nil || len(pruned) != 0 || !exists(token) {
		t.Fatalf("a session whose login was refreshed today was pruned: %v %v", pruned, err)
	}
}

func TestPrune_KeepHookAndDryRun(t *testing.T) {
	now := time.Now()
	setup := func() (root, open, closed string) {
		root = t.TempDir()
		open = filepath.Join(root, "open-conversation")
		closed = filepath.Join(root, "closed-conversation")
		write(t, open, "a", "x")
		write(t, closed, "a", "x")
		ageTree(t, open, now.Add(-90*day))
		ageTree(t, closed, now.Add(-90*day))
		return
	}

	root, open, closed := setup()
	keep := func(p string) bool { return p == open } // the host still has this conversation open
	pruned, err := PruneDirEntries(bg, root, 30*day, now, PruneOptions{Keep: keep})
	if err != nil || !slices.Equal(pruned, []string{closed}) || !exists(open) || exists(closed) {
		t.Errorf("Keep: pruned=%v err=%v open kept=%v closed kept=%v", pruned, err, exists(open), exists(closed))
	}

	root, open, closed = setup()
	pruned, err = PruneDirEntries(bg, root, 30*day, now, PruneOptions{DryRun: true})
	if err != nil || len(pruned) != 2 || !exists(open) || !exists(closed) {
		t.Errorf("DryRun: pruned=%v err=%v; it must report both and delete neither", pruned, err)
	}
}

func TestPruneFileEntries(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	oldLog := write(t, root, "task.log", "x")
	nested := write(t, root, "run/step.log", "x")
	fresh := write(t, root, "fresh.log", "x")
	hidden := write(t, root, ".keep/old.log", "x")
	for _, p := range []string{oldLog, nested, hidden} {
		_ = os.Chtimes(p, now.Add(-20*day), now.Add(-20*day))
	}

	pruned, err := PruneFileEntries(bg, root, 14*day, now, PruneOptions{})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(pruned)
	if want := []string{nested, oldLog}; !slices.Equal(pruned, want) {
		t.Fatalf("pruned = %v, want %v", pruned, want)
	}
	if !exists(fresh) || !exists(hidden) {
		t.Error("a fresh or hidden file was removed")
	}
}

func TestRunJanitor(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	ws, sess, cache, logs := filepath.Join(root, "workspaces"), filepath.Join(root, "sessions"), filepath.Join(root, "cache"), filepath.Join(root, "logs")
	oldWS := write(t, ws, "ws1/a", "x")
	oldSess := write(t, sess, "p1/s1/a", "x")
	oldCache := write(t, cache, "npm/pkg", "x")
	oldLog := write(t, logs, "task.log", "x")
	for _, p := range []string{filepath.Join(ws, "ws1"), filepath.Join(sess, "p1", "s1"), filepath.Join(cache, "npm")} {
		ageTree(t, p, now.Add(-40*day))
	}
	_ = os.Chtimes(oldLog, now.Add(-40*day), now.Add(-40*day))

	// TTLs left at zero use the package defaults
	report, err := RunJanitor(bg, JanitorConfig{
		WorkspacesRoot: ws, SessionsRoot: sess, CacheRoot: cache, LogsRoot: logs,
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.TotalPruned() != 4 || exists(oldWS) || exists(oldSess) || exists(oldCache) || exists(oldLog) {
		t.Errorf("report = %+v", report)
	}
}

func TestRunJanitor_NegativeTTLSwitchesACleanupOff(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	f := write(t, root, "ws1/a", "x")
	ageTree(t, filepath.Join(root, "ws1"), now.Add(-400*day))
	report, err := RunJanitor(bg, JanitorConfig{WorkspacesRoot: root, WorkspacesTTL: -1, Now: func() time.Time { return now }})
	if err != nil || report.TotalPruned() != 0 || !exists(f) {
		t.Errorf("report = %+v err = %v", report, err)
	}
}

// A root that could cover data that is not the sandbox's must stop the whole pass before anything
// is deleted, not just that one cleanup.
func TestRunJanitor_RefusesUnsafeRoots(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	now := time.Now()
	old := now.Add(-400 * day)
	docs := write(t, home, "Documents/notes.txt", "mine")
	ageTree(t, filepath.Join(home, "Documents"), old)

	good := t.TempDir()
	goodFile := write(t, good, "ws1/a", "x")
	ageTree(t, filepath.Join(good, "ws1"), old)

	link := filepath.Join(t.TempDir(), "link-to-home")
	if err := os.Symlink(home, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	for name, root := range map[string]string{
		"HOME itself":             home,
		"above HOME":              filepath.Dir(home),
		"the filesystem root":     "/",
		"a symlink to HOME":       link,
		"a relative path":         "relative/dir",
		"root written with a dot": "/./",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := RunJanitor(bg, JanitorConfig{
				WorkspacesRoot: good, // a perfectly fine root next to the bad one
				SessionsRoot:   root,
				Now:            func() time.Time { return now },
			})
			if !errors.Is(err, ErrUnsafeRoot) {
				t.Fatalf("err = %v, want ErrUnsafeRoot", err)
			}
			if !exists(docs) || !exists(goodFile) {
				t.Error("something was deleted although the pass was refused")
			}
		})
	}

	// the exported functions guard themselves too
	if _, err := PruneDirEntries(bg, home, day, now, PruneOptions{}); !errors.Is(err, ErrUnsafeRoot) || !exists(docs) {
		t.Errorf("PruneDirEntries(HOME) = %v", err)
	}
	// a directory inside HOME is fine
	inside := filepath.Join(home, "aicli-data")
	write(t, inside, "ws1/a", "x")
	ageTree(t, filepath.Join(inside, "ws1"), old)
	if report, err := RunJanitor(bg, JanitorConfig{WorkspacesRoot: inside, Now: func() time.Time { return now }}); err != nil || report.TotalPruned() != 1 {
		t.Errorf("a data root inside HOME: %+v %v", report, err)
	}
}

// System directories hold other programs' data; a cleanup root must be a directory of the
// operator's own, below them.
func TestRunJanitor_RefusesSystemDirectories(t *testing.T) {
	for _, root := range []string{"/etc", "/usr", "/var", "/var/lib", "/var/log", "/tmp", "/opt", "/home", "/etc/"} {
		if _, err := os.Stat(root); err != nil {
			continue
		}
		if _, err := RunJanitor(bg, JanitorConfig{WorkspacesRoot: root, DryRun: true}); !errors.Is(err, ErrUnsafeRoot) {
			t.Errorf("%s: err = %v, want ErrUnsafeRoot", root, err)
		}
	}
	// below a system directory is fine
	below := filepath.Join(t.TempDir(), "aicli")
	if err := os.MkdirAll(below, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := RunJanitor(bg, JanitorConfig{WorkspacesRoot: below, DryRun: true}); err != nil {
		t.Errorf("a directory of the operator's own was refused: %v", err)
	}
}

func TestPrune_StopsWhenTheContextEnds(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	for _, n := range []string{"a", "b", "c"} {
		write(t, root, n+"/f", "x")
		ageTree(t, filepath.Join(root, n), now.Add(-90*day))
	}
	ctx, cancel := context.WithCancel(bg)
	cancel()
	pruned, err := PruneDirEntries(ctx, root, 30*day, now, PruneOptions{})
	if !errors.Is(err, context.Canceled) || len(pruned) != 0 {
		t.Errorf("pruned=%v err=%v, want nothing done and context.Canceled", pruned, err)
	}
}

// A removal that fails is reported, not swallowed, and does not stop the rest.
func TestPrune_ReportsRemovalFailures(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can remove anything")
	}
	root := t.TempDir()
	now := time.Now()
	stuck := filepath.Join(root, "stuck")
	write(t, stuck, "sub/f", "x")
	good := write(t, root, "good/f", "x")
	ageTree(t, stuck, now.Add(-90*day))
	ageTree(t, filepath.Join(root, "good"), now.Add(-90*day))
	sub := filepath.Join(stuck, "sub")
	if err := os.Chmod(sub, 0o500); err != nil { // cannot delete f from a read-only directory
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sub, 0o755) })

	pruned, err := PruneDirEntries(bg, root, 30*day, now, PruneOptions{})
	if err == nil {
		t.Error("a failed removal was not reported")
	}
	if exists(good) || !slices.Contains(pruned, filepath.Join(root, "good")) {
		t.Errorf("the entry that could be removed was not: pruned=%v", pruned)
	}
}
