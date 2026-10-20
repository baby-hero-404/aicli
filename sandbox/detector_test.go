package sandbox

import (
	"os"
	"path/filepath"
	"testing"
)

func writeMarker(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("{}"), 0o644); err != nil {
		t.Fatalf("write marker %s: %v", name, err)
	}
}

func TestDetectRuntimeMatchesMarkerFile(t *testing.T) {
	reg, err := testRegistry()
	if err != nil {
		t.Fatalf("testRegistry() error = %v", err)
	}

	tests := []struct {
		name   string
		marker string
		want   string
	}{
		{"node", "package.json", "node"},
		{"python requirements", "requirements.txt", "python"},
		{"python pyproject", "pyproject.toml", "python"},
		{"java", "pom.xml", "java"},
		{"go", "go.mod", "go"},
		{"flutter", "pubspec.yaml", "flutter"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeMarker(t, dir, tt.marker)
			got, ok := DetectRuntime(reg, dir)
			if !ok {
				t.Fatalf("DetectRuntime() ok = false, want true")
			}
			if got != tt.want {
				t.Errorf("DetectRuntime() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDetectRuntimeNoMatch(t *testing.T) {
	reg, err := testRegistry()
	if err != nil {
		t.Fatalf("testRegistry() error = %v", err)
	}
	dir := t.TempDir()
	writeMarker(t, dir, "README.md")
	if _, ok := DetectRuntime(reg, dir); ok {
		t.Error("DetectRuntime() ok = true, want false for a workspace with no known markers")
	}
}

func TestDetectRuntimeEmptyWorkspace(t *testing.T) {
	reg, err := testRegistry()
	if err != nil {
		t.Fatalf("testRegistry() error = %v", err)
	}
	if _, ok := DetectRuntime(reg, ""); ok {
		t.Error("DetectRuntime(\"\") ok = true, want false")
	}
	if _, ok := DetectRuntime(reg, "/path/does/not/exist"); ok {
		t.Error("DetectRuntime() on nonexistent dir ok = true, want false")
	}
}

// TestDetectRuntimeAmbiguousUsesDeclarationOrder verifies the documented,
// deterministic tie-break: when a workspace matches more than one
// manifest's markers, the first manifest in registry declaration order
// (runtimeOrder) wins — not any attempt to "intelligently" pick a primary
// language.
func TestDetectRuntimeAmbiguousUsesDeclarationOrder(t *testing.T) {
	reg, err := testRegistry()
	if err != nil {
		t.Fatalf("testRegistry() error = %v", err)
	}
	dir := t.TempDir()
	// go.mod (order index 3) and package.json (order index 0) both present:
	// node must win since it's declared first.
	writeMarker(t, dir, "go.mod")
	writeMarker(t, dir, "package.json")

	got, ok := DetectRuntime(reg, dir)
	if !ok {
		t.Fatal("DetectRuntime() ok = false, want true")
	}
	if got != "node" {
		t.Errorf("DetectRuntime() = %q, want %q (declaration-order tie-break)", got, "node")
	}
}
