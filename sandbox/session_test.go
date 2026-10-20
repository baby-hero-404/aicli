package sandbox

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestSessionDir(t *testing.T) {
	root := t.TempDir()
	tests := []struct {
		name, root, profile, account string
		wantErr                      bool
	}{
		{"valid", root, "claude_code", "acct-1", false},
		{"two accounts differ", root, "claude_code", "acct-2", false},
		{"empty root", "", "claude_code", "a", true},
		{"traversal in account", root, "claude_code", "../other", true},
		{"slash in account", root, "claude_code", "a/b", true},
		{"empty account", root, "claude_code", "", true},
		{"traversal in profile", root, "../x", "a", true},
		{"dot only", root, "claude_code", "..", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := SessionDir(tt.root, tt.profile, tt.account)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && !strings.HasPrefix(got, filepath.Clean(tt.root)+string(filepath.Separator)) {
				t.Errorf("%s escapes root %s", got, tt.root)
			}
		})
	}
	a, _ := SessionDir(root, "claude_code", "acct-1")
	b, _ := SessionDir(root, "claude_code", "acct-2")
	if a == b {
		t.Error("different accounts must get different directories")
	}
}
