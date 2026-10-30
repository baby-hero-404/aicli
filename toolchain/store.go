package toolchain

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// profileScript is mounted at ProfileTarget. It only reads directories that exist, so it is the
// same file whatever is installed.
const profileScript = `# Added by aicli: CLI tools mounted read-only from the host (package toolchain).
for d in ` + ContainerRoot + `/*/bin; do
  [ -d "$d" ] && PATH="$d:$PATH"
done
export PATH
`

// Store manages installed tool versions under a root directory on the host.
type Store struct {
	root string
	mu   sync.Mutex // serializes the small state files (current, previous, auto, bad) in this process
}

// DefaultRoot returns the default tools directory on the host: $AICLI_TOOLS_ROOT when set,
// otherwise ~/.aicli/tools. Servers should set AICLI_TOOLS_ROOT (or pass --root) to a system
// path such as /var/lib/aicli/tools.
//
// A default inside HOME is fine for the sandbox rule: only the per-version directory of each tool
// is mounted, read-only, never HOME itself (see sandbox.ValidateRequest). With neither the
// variable nor a HOME it returns an error rather than guessing a path.
func DefaultRoot() (string, error) {
	if env := os.Getenv("AICLI_TOOLS_ROOT"); env != "" {
		root, err := filepath.Abs(filepath.Clean(env))
		if err != nil {
			return "", fmt.Errorf("toolchain: AICLI_TOOLS_ROOT %q: %w", env, err)
		}
		return root, nil
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", errors.New("toolchain: no tools root: set AICLI_TOOLS_ROOT (or pass --root) when HOME is not known")
	}
	return filepath.Join(home, ".aicli", "tools"), nil
}

// OpenDefault opens the store at DefaultRoot().
func OpenDefault() (*Store, error) {
	return Open("")
}

// Open returns the store at root, creating the directory layout if needed. If root is empty,
// DefaultRoot() is used. root must be an absolute path visible to the container runtime.
func Open(root string) (*Store, error) {
	if root == "" {
		var err error
		root, err = DefaultRoot()
		if err != nil {
			return nil, fmt.Errorf("toolchain: default root: %w", err)
		}
	}
	if !filepath.IsAbs(root) {
		return nil, fmt.Errorf("toolchain: root %q must be an absolute path", root)
	}
	root = filepath.Clean(root)
	if err := os.MkdirAll(filepath.Join(root, ".staging"), 0o755); err != nil {
		return nil, fmt.Errorf("toolchain: create root: %w", err)
	}
	s := &Store{root: root}
	if err := writeAtomic(s.profilePath(), []byte(profileScript), 0o644); err != nil {
		return nil, fmt.Errorf("toolchain: write profile script: %w", err)
	}
	return s, nil
}

// Root returns the store's root directory.
func (s *Store) Root() string { return s.root }

func (s *Store) profilePath() string { return filepath.Join(s.root, "profile.sh") }

func (s *Store) versionDir(name, version string) string {
	return filepath.Join(s.root, name, version)
}

// Installed reports whether name@version is installed.
func (s *Store) Installed(name, version string) (bool, error) {
	if _, err := lookup(name); err != nil {
		return false, err
	}
	if err := checkVersion(version); err != nil {
		return false, err
	}
	fi, err := os.Stat(s.versionDir(name, version))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("toolchain: stat: %w", err)
	}
	return fi.IsDir(), nil
}

// Versions returns the installed versions of a tool, sorted.
func (s *Store) Versions(name string) ([]string, error) {
	if _, err := lookup(name); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Join(s.root, name))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("toolchain: list: %w", err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && checkVersion(e.Name()) == nil {
			out = append(out, e.Name())
		}
	}
	slices.SortFunc(out, compareVersions)
	return out, nil
}

// Current returns the tool's active version, or "" when none is set.
func (s *Store) Current(name string) (string, error) {
	if _, err := lookup(name); err != nil {
		return "", err
	}
	b, err := os.ReadFile(filepath.Join(s.root, name, "current"))
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("toolchain: read current: %w", err)
	}
	v := strings.TrimSpace(string(b))
	if checkVersion(v) != nil {
		return "", fmt.Errorf("toolchain: %s/current holds an invalid version %q", name, v)
	}
	return v, nil
}

// Use makes an installed version the tool's current one. Runs that already started keep the
// version they mounted.
func (s *Store) Use(name, version string) error {
	ok, err := s.Installed(name, version)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: %s@%s", ErrNotInstalled, name, version)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// A version the operator chooses is trusted: it is not on probation, so failures never roll it
	// back, even when it is the very release Sync had switched to.
	s.clearProbation(name)
	return s.switchTo(name, version, true)
}

// Unuse clears the tool's current version, so runs fall back to the CLI baked into the
// sandbox image (pinned runs are unaffected). It is a no-op when no version is current.
func (s *Store) Unuse(name string) error {
	if _, err := lookup(name); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clearProbation(name)
	err := os.Remove(filepath.Join(s.root, name, "current"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("toolchain: clear current: %w", err)
	}
	return nil
}

// Remove deletes an installed version. The current version cannot be removed; Use another
// version or Unuse first. A run that still has the version mounted (it started before a switch,
// or pins it) loses its files, so remove only versions nothing has used for a while.
func (s *Store) Remove(name, version string) error {
	ok, err := s.Installed(name, version)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: %s@%s", ErrNotInstalled, name, version)
	}
	cur, err := s.Current(name)
	if err != nil {
		return err
	}
	if cur == version {
		return fmt.Errorf("%w: %s@%s", ErrInUse, name, version)
	}
	// Rename first so the version disappears atomically, then delete at leisure.
	trash := filepath.Join(s.root, ".staging", "remove-"+name+"-"+version+"-"+randomSuffix())
	if err := os.Rename(s.versionDir(name, version), trash); err != nil {
		return fmt.Errorf("toolchain: remove: %w", err)
	}
	return os.RemoveAll(trash)
}

// PruneStaging deletes leftovers in the staging area older than olderThan: installs that were
// killed before they could clean up, and removals interrupted mid-delete. Entries younger than
// olderThan are kept, because on shared storage another host may be installing right now.
// It returns how many entries it removed.
func (s *Store) PruneStaging(olderThan time.Duration, now time.Time) (int, error) {
	dir := filepath.Join(s.root, ".staging")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, fmt.Errorf("toolchain: list staging: %w", err)
	}
	n := 0
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || now.Sub(info.ModTime()) < olderThan {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return n, fmt.Errorf("toolchain: prune staging: %w", err)
		}
		n++
	}
	return n, nil
}

// DefaultKeepVersions is the default number of recent versions to keep during prune.
const DefaultKeepVersions = 3

// compareVersions orders version strings such as "1.4.10", "1.4.2" or "2.0.0-rc.1": the dotted
// numbers before any "-" compare numerically segment by segment, a pre-release ("-rc.1") sorts
// before its release, and build metadata after "+" is ignored.
func compareVersions(a, b string) int {
	coreA, preA := splitVersion(a)
	coreB, preB := splitVersion(b)
	if c := compareDotted(coreA, coreB); c != 0 {
		return c
	}
	switch {
	case preA == preB:
		return 0
	case preA == "":
		return 1
	case preB == "":
		return -1
	}
	return compareDotted(preA, preB)
}

func splitVersion(v string) (core, pre string) {
	if i := strings.IndexByte(v, '+'); i >= 0 {
		v = v[:i]
	}
	core, pre, _ = strings.Cut(v, "-")
	return core, pre
}

// compareDotted compares "a.b.c" strings: numeric segments numerically, others as text, and a
// shorter string before a longer one it is a prefix of.
func compareDotted(a, b string) int {
	partsA, partsB := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(partsA) && i < len(partsB); i++ {
		pa, pb := partsA[i], partsB[i]
		na, errA := strconv.Atoi(pa)
		nb, errB := strconv.Atoi(pb)
		switch {
		case errA == nil && errB == nil && na != nb:
			if na < nb {
				return -1
			}
			return 1
		case (errA != nil || errB != nil) && pa != pb:
			if pa < pb {
				return -1
			}
			return 1
		}
	}
	return len(partsA) - len(partsB)
}

// Prune removes installed versions of tool name older than the keep most recent versions.
// The active (current) version and the previous one (see Rollback) are never removed. If keep <= 0, DefaultKeepVersions (3) is used.
// It returns the list of versions that were removed.
func (s *Store) Prune(name string, keep int) ([]string, error) {
	return s.PruneKeeping(name, keep, nil)
}

// PruneKeeping is Prune that also keeps every version protect reports as still needed, for example
// a version some requests pin (sandbox.CommandRequest.ToolVersions), which the store cannot see.
func (s *Store) PruneKeeping(name string, keep int, protect func(version string) bool) ([]string, error) {
	if keep <= 0 {
		keep = DefaultKeepVersions
	}
	versions, err := s.Versions(name)
	if err != nil {
		return nil, err
	}
	if len(versions) <= keep {
		return nil, nil
	}
	slices.SortFunc(versions, compareVersions)

	cur, err := s.Current(name)
	if err != nil {
		return nil, err
	}
	prev, _, err := s.Previous(name) // the version a rollback goes back to must survive
	if err != nil {
		return nil, err
	}

	var removed []string
	for _, v := range versions[:len(versions)-keep] {
		if v == cur || v == prev || (protect != nil && protect(v)) {
			continue // the active version, the one to roll back to, and any in use, must not be removed
		}
		if err := s.Remove(name, v); err != nil {
			return removed, err
		}
		removed = append(removed, v)
	}
	return removed, nil
}

// PruneAll prunes all registered tools, keeping at most keep versions each.
func (s *Store) PruneAll(keep int) (map[string][]string, error) {
	res := make(map[string][]string)
	for _, tool := range Tools() {
		removed, err := s.Prune(tool, keep)
		if err != nil {
			return res, fmt.Errorf("prune %s: %w", tool, err)
		}
		if len(removed) > 0 {
			res[tool] = removed
		}
	}
	return res, nil
}

// Resolve returns the host directory to mount for each tool: the pinned version when pins names
// one, otherwise the current version. Tools with neither are left out. A pin for an unknown tool
// or a version that is not installed is an error.
func (s *Store) Resolve(pins map[string]string) (map[string]string, error) {
	out := map[string]string{}
	for name, version := range pins {
		if _, err := lookup(name); err != nil {
			return nil, err
		}
		ok, err := s.Installed(name, version)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("%w: %s@%s is pinned but not installed", ErrNotInstalled, name, version)
		}
		out[name] = s.versionDir(name, version)
	}
	for _, name := range Tools() {
		if _, pinned := out[name]; pinned {
			continue
		}
		cur, err := s.Current(name)
		if err != nil {
			return nil, err
		}
		if cur == "" {
			continue
		}
		ok, err := s.Installed(name, cur)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("%w: %s/current names %s, which is not installed", ErrNotInstalled, name, cur)
		}
		out[name] = s.versionDir(name, cur)
	}
	return out, nil
}

// writeAtomic replaces path with data so that readers see the old or the new content, never a
// mix.
func writeAtomic(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp-" + randomSuffix()
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func randomSuffix() string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		// Only used for temporary names next to the target; a collision makes the rename fail
		// loudly rather than do harm.
		return "x"
	}
	return hex.EncodeToString(b)
}
