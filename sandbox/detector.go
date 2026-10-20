package sandbox

import (
	"os"
	"path/filepath"
)

// DetectRuntime inspects workspaceRoot (the already-materialized, host-side
// workspace directory — WorkspacePath/bind-mount source, so this needs no
// container) for each registered manifest's marker files, in the registry's
// declared order (Registry.All, see runtimeOrder), returning the first
// manifest ID with at least one marker file present.
//
// A manifest's markers are OR'd, not AND'd: DetectRuntime.go's own manifest
// has a single go.mod marker, but python's has two (requirements.txt,
// pyproject.toml) precisely because either alone is sufficient to identify
// a Python project — see the python manifest's comment. Deliberately dumb
// and predictable (first match by declaration order wins), not an attempt
// to score/rank ambiguous polyglot repos.
func DetectRuntime(registry *Registry, workspaceRoot string) (string, bool) {
	for _, manifest := range registry.All() {
		for _, marker := range manifest.Detect {
			if fileExists(workspaceRoot, marker) {
				return manifest.ID, true
			}
		}
	}
	return "", false
}

func fileExists(root, name string) bool {
	if root == "" || name == "" {
		return false
	}
	info, err := os.Stat(filepath.Join(root, name))
	if err != nil {
		return false
	}
	return !info.IsDir()
}
