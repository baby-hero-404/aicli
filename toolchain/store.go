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
	"strings"
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
}

// Open returns the store at root, creating the directory layout if needed. root must be an
// absolute path visible to the container runtime.
func Open(root string) (*Store, error) {
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
	slices.Sort(out)
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
	return writeAtomic(filepath.Join(s.root, name, "current"), []byte(version+"\n"), 0o644)
}

// Unuse clears the tool's current version, so runs fall back to the CLI baked into the
// sandbox image (pinned runs are unaffected). It is a no-op when no version is current.
func (s *Store) Unuse(name string) error {
	if _, err := lookup(name); err != nil {
		return err
	}
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
