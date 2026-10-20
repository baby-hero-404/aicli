package sandbox

import (
	"testing"
	"testing/fstest"
)

func TestExampleRegistryOrder(t *testing.T) {
	reg, err := testRegistry()
	if err != nil {
		t.Fatalf("testRegistry() error = %v", err)
	}

	wantIDs := []string{"node", "python", "java", "go", "flutter"}
	all := reg.All()
	if len(all) != len(wantIDs) {
		t.Fatalf("All() returned %d manifests, want %d", len(all), len(wantIDs))
	}
	for i, id := range wantIDs {
		if all[i].ID != id {
			t.Errorf("All()[%d].ID = %q, want %q (registry order must be deterministic)", i, all[i].ID, id)
		}
	}
}

func TestRegistryGet(t *testing.T) {
	reg, err := testRegistry()
	if err != nil {
		t.Fatalf("testRegistry() error = %v", err)
	}

	m, ok := reg.Get("python")
	if !ok {
		t.Fatal("Get(\"python\") ok = false, want true")
	}
	if m.Image == "" {
		t.Error("python manifest Image is empty")
	}
	if len(m.Detect) == 0 {
		t.Error("python manifest Detect is empty")
	}

	if _, ok := reg.Get("nonexistent"); ok {
		t.Error("Get(\"nonexistent\") ok = true, want false")
	}
}

func TestRegistryFlutterCacheUsesHomeDir(t *testing.T) {
	reg, err := testRegistry()
	if err != nil {
		t.Fatalf("testRegistry() error = %v", err)
	}
	m, ok := reg.Get("flutter")
	if !ok {
		t.Fatal("Get(\"flutter\") ok = false")
	}
	if len(m.Cache) == 0 {
		t.Fatal("flutter manifest has no cache entries")
	}
	for _, c := range m.Cache {
		if c.Container == "" || c.Name == "" {
			t.Errorf("flutter cache entry has empty field: %+v", c)
		}
	}
}

func TestNewRegistryValidation(t *testing.T) {
	tests := []struct {
		name      string
		manifests []Manifest
		wantErr   bool
	}{
		{"empty is valid", nil, false},
		{"valid", []Manifest{{ID: "go", Image: "x", Cache: []CacheMapping{{Name: "gomod", Container: "/go/pkg/mod"}}}}, false},
		{"empty id", []Manifest{{ID: ""}}, true},
		{"id with slash", []Manifest{{ID: "a/b"}}, true},
		{"duplicate id", []Manifest{{ID: "go"}, {ID: "go"}}, true},
		{"bad cache name", []Manifest{{ID: "go", Cache: []CacheMapping{{Name: "../x", Container: "/c"}}}}, true},
		{"relative cache path", []Manifest{{ID: "go", Cache: []CacheMapping{{Name: "c", Container: "rel"}}}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewRegistry(tt.manifests...); (err != nil) != tt.wantErr {
				t.Errorf("err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestNewRegistryKeepsPriorityOrder(t *testing.T) {
	reg, err := NewRegistry(Manifest{ID: "b"}, Manifest{ID: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if all := reg.All(); all[0].ID != "b" || all[1].ID != "a" {
		t.Errorf("order not preserved: %s, %s", all[0].ID, all[1].ID)
	}
}

func TestParseManifestAndLoadManifests(t *testing.T) {
	if _, err := ParseManifest([]byte("id: [unclosed")); err == nil {
		t.Error("malformed YAML must fail")
	}
	m, err := ParseManifest([]byte("id: rust\nimage: img\ndetect: [Cargo.toml]\n"))
	if err != nil || m.ID != "rust" || m.Detect[0] != "Cargo.toml" {
		t.Fatalf("ParseManifest = %+v, %v", m, err)
	}

	fsys := fstest.MapFS{
		"rust/manifest.yaml": {Data: []byte("id: rust\nimage: img\n")},
		"deno/manifest.yaml": {Data: []byte("id: deno\nimage: img\n")},
	}
	got, err := LoadManifests(fsys)
	if err != nil || len(got) != 2 || got[0].ID != "deno" || got[1].ID != "rust" {
		t.Fatalf("LoadManifests = %+v, %v (want sorted by directory)", got, err)
	}
	mismatch := fstest.MapFS{"rust/manifest.yaml": {Data: []byte("id: other\n")}}
	if _, err := LoadManifests(mismatch); err == nil {
		t.Error("id/directory mismatch must be rejected")
	}
}
