package sandbox

import (
	"path/filepath"
)

// WorkspacePath returns the absolute host workspace directory for taskID under root
// (DefaultWorkspaceRoot when root is empty).
func WorkspacePath(root, taskID string) string {
	if root == "" {
		root = DefaultWorkspaceRoot
	}
	absRoot, err := filepath.Abs(root)
	if err == nil {
		root = absRoot
	}
	return filepath.Join(root, taskID)
}
