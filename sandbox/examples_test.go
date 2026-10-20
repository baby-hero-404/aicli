package sandbox

import "testing"

// testRegistry is a typical host registry, defined in code so tests need no files on disk.
func testRegistry() (*Registry, error) {
	return NewRegistry(
		Manifest{ID: "node", Image: "img/node", Detect: []string{"package.json"},
			Cache: []CacheMapping{{Name: "npm", Container: "/home/agent/.npm"}}, Healthcheck: "node --version"},
		Manifest{ID: "python", Image: "img/python", Detect: []string{"requirements.txt", "pyproject.toml"},
			Cache: []CacheMapping{{Name: "pip", Container: "/home/agent/.cache/pip"}}, Healthcheck: "python3 --version"},
		Manifest{ID: "java", Image: "img/java", Detect: []string{"pom.xml"},
			Cache: []CacheMapping{{Name: "m2", Container: "/home/agent/.m2"}, {Name: "gradle", Container: "/home/agent/.gradle"}}, Healthcheck: "java -version"},
		Manifest{ID: "go", Image: "img/go", Detect: []string{"go.mod"},
			Cache: []CacheMapping{{Name: "gomod", Container: "/go/pkg/mod"}}, Healthcheck: "go version"},
		Manifest{ID: "flutter", Image: "img/flutter", Detect: []string{"pubspec.yaml"},
			Cache: []CacheMapping{{Name: "pub-cache", Container: "/home/agent/.pub-cache"}, {Name: "gradle", Container: "/home/agent/.gradle"}},
			Setup: []string{"flutter pub get"}, Healthcheck: "flutter doctor"},
	)
}

func TestTestRegistryIsValid(t *testing.T) {
	if _, err := testRegistry(); err != nil {
		t.Fatal(err)
	}
}
