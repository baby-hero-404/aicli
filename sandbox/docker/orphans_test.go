package docker

import (
	"context"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

func TestOrphanState(t *testing.T) {
	for state, want := range map[container.ContainerState]bool{
		container.StateExited:     true,
		container.StateDead:       true,
		container.StateCreated:    true,
		container.StateRunning:    false,
		container.StateRestarting: false,
		container.StatePaused:     false,
		container.StateRemoving:   false,
	} {
		if got := orphanState(state); got != want {
			t.Errorf("orphanState(%s) = %v, want %v", state, got, want)
		}
	}
}

// PruneOrphaned against a real daemon: only stopped, labeled containers that stopped long enough
// ago are removed; a running one and one without the label stay.
func TestRuntime_PruneOrphaned(t *testing.T) {
	requireDockerTests(t)
	rt, err := New(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := rt.Prewarm(ctx); err != nil {
		t.Fatalf("prewarm: %v", err)
	}

	make := func(name string, labels map[string]string, cmd ...string) string {
		t.Helper()
		resp, err := rt.client.ContainerCreate(ctx, client.ContainerCreateOptions{
			Name:   "aicli-orphan-test-" + name,
			Config: &container.Config{Image: rt.config.Image, Cmd: cmd, Labels: labels},
		})
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		t.Cleanup(func() {
			_, _ = rt.client.ContainerRemove(context.Background(), resp.ID, client.ContainerRemoveOptions{Force: true})
		})
		if _, err := rt.client.ContainerStart(ctx, resp.ID, client.ContainerStartOptions{}); err != nil {
			t.Fatalf("start %s: %v", name, err)
		}
		return resp.ID
	}
	exists := func(id string) bool {
		_, err := rt.client.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
		return err == nil
	}
	wait := func(id string) {
		t.Helper()
		w := rt.client.ContainerWait(ctx, id, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
		select {
		case <-w.Result:
		case err := <-w.Error:
			t.Fatal(err)
		case <-time.After(30 * time.Second):
			t.Fatal("container did not stop")
		}
	}

	ours := map[string]string{LabelApp: "aicli", LabelSandbox: "true", LabelManagedBy: "true"}
	stopped := make("stopped", ours, "true")
	running := make("running", ours, "sleep", "120")
	foreign := make("foreign", map[string]string{"app": "someone-else"}, "true")
	wait(stopped)
	wait(foreign)

	// just stopped: protected by minAge
	if n, err := rt.PruneOrphaned(ctx, time.Hour); err != nil || n != 0 || !exists(stopped) {
		t.Fatalf("a container that stopped a moment ago was pruned: n=%d err=%v", n, err)
	}
	// old enough: removed; the running and the unlabeled container are not
	if n, err := rt.PruneOrphaned(ctx, 0); err != nil || n != 1 {
		t.Fatalf("PruneOrphaned = %d, %v, want 1", n, err)
	}
	if exists(stopped) {
		t.Error("the stopped aicli container was not removed")
	}
	if !exists(running) || !exists(foreign) {
		t.Errorf("running kept = %v, foreign kept = %v, want both kept", exists(running), exists(foreign))
	}
}
