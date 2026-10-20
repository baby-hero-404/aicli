package capture

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	dockerrt "github.com/baby-hero-404/aicli/sandbox/docker"
)

// TestSession_DockerRoundTrip drives a real PTY session end to end. Opt in with
// AICLI_DOCKER_TESTS=1 and pick the image with AICLI_TEST_IMAGE.
func TestSession_DockerRoundTrip(t *testing.T) {
	if os.Getenv("AICLI_DOCKER_TESTS") != "1" {
		t.Skip("set AICLI_DOCKER_TESTS=1 to run Docker integration tests")
	}
	rt, err := dockerrt.New(dockerrt.Config{Image: os.Getenv("AICLI_TEST_IMAGE")})
	if err != nil {
		t.Fatalf("docker runtime: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := rt.Prewarm(ctx); err != nil {
		t.Fatalf("prewarm: %v", err)
	}

	// A PTY drops input written before the container's shell starts, so type after a delay,
	// the way a person at the browser terminal would.
	pr, pw := io.Pipe()
	go func() {
		time.Sleep(3 * time.Second)
		_, _ = pw.Write([]byte("mkdir -p ~/.cli && echo '{\"token\":\"abc\"}' > ~/.cli/cred.json\n"))
		time.Sleep(time.Second)
		_, _ = pw.Write([]byte("exit\n"))
	}()
	term := &fakeTerm{in: pr}
	res, err := (&Session{Runtime: rt}).Run(ctx, term)
	if err != nil {
		t.Fatalf("Run: %v; terminal output so far: %q", err, term.out.String())
	}
	if got := res.Files[".cli/cred.json"]; strings.TrimSpace(got) != `{"token":"abc"}` {
		t.Errorf("files = %v, terminal output = %q", res.Files, term.out.String())
	}
}
