package conversation

import (
	"testing"

	"github.com/baby-hero-404/aicli/sandbox"
)

// Hosts name the per-conversation session directory after the conversation ID, and
// sandbox.SessionDir only accepts names that start with a letter or digit.
func TestGeneratedIDsAreValidSessionDirNames(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 20000; i++ {
		id, err := randomID()
		if err != nil {
			t.Fatal(err)
		}
		if seen[id] {
			t.Fatalf("duplicate ID %q", id)
		}
		seen[id] = true
		if _, err := sandbox.SessionDir("/data/sessions", "antigravity", id); err != nil {
			t.Fatalf("ID %q is not a valid session directory name: %v", id, err)
		}
		if len(id) < 22 {
			t.Fatalf("ID %q carries fewer than 128 bits", id)
		}
	}
}
