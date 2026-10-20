package sandbox

import (
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"

	"gopkg.in/yaml.v3"
)

// CacheMapping is one entry of a manifest's cache: list. Name identifies the cache
// ("npm", "gomod", ...) and Container is where it is mounted inside the sandbox. The host
// directory is never taken from the manifest: Manager derives it from the cache root
// the host application configured (see WithCacheRoot).
type CacheMapping struct {
	Name      string `yaml:"name"`
	Container string `yaml:"container"`
}

// Manifest describes one project runtime: how to detect it (marker files in the workspace
// root), which image to run it in, which caches to mount, and which setup/healthcheck commands
// to run before the caller's command. aicli ships no manifests: the host defines the runtimes
// it supports (see the README for the manifest format).
type Manifest struct {
	ID          string         `yaml:"id"`
	Image       string         `yaml:"image"`
	Detect      []string       `yaml:"detect"`
	Cache       []CacheMapping `yaml:"cache"`
	Setup       []string       `yaml:"setup"`
	Healthcheck string         `yaml:"healthcheck"`
}

var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// validate reports whether m is usable in a Registry.
func (m Manifest) validate() error {
	if !nameRe.MatchString(m.ID) {
		return fmt.Errorf("invalid runtime id %q", m.ID)
	}
	for _, c := range m.Cache {
		if !nameRe.MatchString(c.Name) {
			return fmt.Errorf("runtime %q: invalid cache name %q", m.ID, c.Name)
		}
		if !path.IsAbs(c.Container) {
			return fmt.Errorf("runtime %q: cache %q needs an absolute container path, got %q", m.ID, c.Name, c.Container)
		}
	}
	return nil
}

// ParseManifest decodes and validates one manifest in YAML form.
func ParseManifest(data []byte) (Manifest, error) {
	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return Manifest{}, fmt.Errorf("parse runtime manifest: %w", err)
	}
	if err := m.validate(); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

// LoadManifests reads every <dir>/manifest.yaml directly below the root of fsys (for example
// os.DirFS("runtimes") or an embed.FS) and returns them sorted by directory name. A manifest
// whose id differs from its directory name is rejected. Order the result yourself before
// passing it to NewRegistry when detection priority matters.
func LoadManifests(fsys fs.FS) ([]Manifest, error) {
	files, err := fs.Glob(fsys, "*/manifest.yaml")
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	out := make([]Manifest, 0, len(files))
	for _, f := range files {
		data, err := fs.ReadFile(fsys, f)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", f, err)
		}
		m, err := ParseManifest(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		if dir := path.Dir(f); m.ID != dir {
			return nil, fmt.Errorf("%s: id %q does not match directory %q", f, m.ID, dir)
		}
		out = append(out, m)
	}
	return out, nil
}

// Registry holds the runtimes a host supports. Detection priority is registration order: when a
// workspace matches several manifests (a polyglot repo), the first registered wins. This is a
// deliberately dumb, predictable tie-break, not a guess at the "primary" language.
type Registry struct {
	manifests []*Manifest
	byID      map[string]*Manifest
}

// NewRegistry builds a Registry from manifests, in priority order. It rejects invalid or
// duplicate manifests so a broken definition fails at startup instead of silently disabling a
// runtime. With no manifests the registry is empty and detection never matches.
func NewRegistry(manifests ...Manifest) (*Registry, error) {
	reg := &Registry{byID: make(map[string]*Manifest, len(manifests))}
	for _, m := range manifests {
		if err := m.validate(); err != nil {
			return nil, err
		}
		if _, dup := reg.byID[m.ID]; dup {
			return nil, fmt.Errorf("duplicate runtime id %q", m.ID)
		}
		m := m
		reg.manifests = append(reg.manifests, &m)
		reg.byID[m.ID] = &m
	}
	return reg, nil
}

// Get returns the manifest for runtimeID, if registered.
func (r *Registry) Get(runtimeID string) (*Manifest, bool) {
	m, ok := r.byID[runtimeID]
	return m, ok
}

// All returns every registered manifest in detection-priority order.
func (r *Registry) All() []*Manifest {
	return r.manifests
}
