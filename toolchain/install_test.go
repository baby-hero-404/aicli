package toolchain

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/baby-hero-404/aicli/sandbox"
)

// fakeContainer plays the installer and version-check containers.
type fakeContainer struct {
	sandbox.StubRuntime
	mu          sync.Mutex
	latest      string // printed in resolve mode
	installExit int
	writeBinary bool
	versionOut  string // printed by the version check
	reqs        []sandbox.CommandRequest
}

func (f *fakeContainer) Run(_ context.Context, req sandbox.CommandRequest) (*sandbox.CommandResult, error) {
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	f.mu.Unlock()
	switch {
	case req.Env["MODE"] == "resolve":
		return &sandbox.CommandResult{Stdout: "some npm noise\n" + f.latest + "\n"}, nil
	case req.Env["MODE"] == "install":
		dest := req.SessionMounts[installMount]
		if f.writeBinary {
			_ = os.MkdirAll(filepath.Join(dest, "bin"), 0o755)
			_ = os.WriteFile(filepath.Join(dest, "bin", req.Env["BINARY"]), []byte("#!/bin/sh\n"), 0o755)
		}
		return &sandbox.CommandResult{ExitCode: f.installExit, Stderr: "npm ERR! boom"}, nil
	default: // version check
		return &sandbox.CommandResult{Stdout: f.versionOut + "\n"}, nil
	}
}

func installer(t *testing.T, f *fakeContainer) (*Installer, *Store) {
	t.Helper()
	s := newStore(t)
	return &Installer{Store: s, Runtime: f, Image: "aicli/base:latest"}, s
}

func stagingEmpty(t *testing.T, s *Store) bool {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(s.root, ".staging"))
	return err == nil && len(entries) == 0
}

func TestInstallLatest(t *testing.T) {
	f := &fakeContainer{latest: "2.1.312", writeBinary: true, versionOut: "2.1.312 (Claude Code)"}
	in, s := installer(t, f)
	v, err := in.Install(context.Background(), "claude", Latest)
	if err != nil || v != "2.1.312" {
		t.Fatalf("Install = %q, %v", v, err)
	}
	if ok, _ := s.Installed("claude", "2.1.312"); !ok {
		t.Error("not installed")
	}
	if _, err := os.Stat(filepath.Join(s.versionDir("claude", "2.1.312"), "bin", "claude")); err != nil {
		t.Error(err)
	}
	if cur, _ := s.Current("claude"); cur != "" {
		t.Errorf("Install must not change current, got %q", cur)
	}
	if !stagingEmpty(t, s) {
		t.Error("staging not cleaned up")
	}

	// Request shape: values travel as env, never in the script; the image is the one asked for.
	inst := f.reqs[1]
	if inst.Env["PKG"] != "@anthropic-ai/claude-code" || inst.Env["VERSION"] != "2.1.312" || inst.Env["POSTINSTALL"] != "install.cjs" {
		t.Errorf("install env = %v", inst.Env)
	}
	if strings.Contains(inst.Command[2], "2.1.312") || !strings.Contains(inst.Command[2], "--ignore-scripts") || inst.Image != "aicli/base:latest" {
		t.Errorf("install command = %q image %q", inst.Command, inst.Image)
	}
	check := f.reqs[2]
	if check.ReadOnlyMounts["/opt/aicli/tools/claude"] == "" || len(check.SessionMounts) != 0 {
		t.Errorf("version check must run from a read-only mount: %+v", check)
	}
}

func TestInstallAlreadyInstalledIsANoop(t *testing.T) {
	f := &fakeContainer{}
	in, s := installer(t, f)
	fakeInstall(t, s, "codex", "0.165.0")
	v, err := in.Install(context.Background(), "codex", "0.165.0")
	if err != nil || v != "0.165.0" || len(f.reqs) != 0 {
		t.Errorf("Install = %q, %v, %d container runs", v, err, len(f.reqs))
	}
}

func TestInstallFailures(t *testing.T) {
	tests := []struct {
		name    string
		f       *fakeContainer
		tool    string
		version string
		want    string
	}{
		{"install script fails", &fakeContainer{installExit: 3}, "codex", "0.165.0", "npm ERR! boom"},
		{"binary reports another version", &fakeContainer{writeBinary: true, versionOut: "codex-cli 0.164.0"}, "codex", "0.165.0", "reports"},
		{"version is a prefix of the reported one", &fakeContainer{writeBinary: true, versionOut: "1.4.100"}, "agy", "1.4.10", "reports"},
		{"latest resolves to garbage", &fakeContainer{latest: "../../etc"}, "agy", Latest, "could not resolve"},
		{"unknown tool", &fakeContainer{}, "gcc", "1.0", "unknown tool"},
		{"bad version", &fakeContainer{}, "agy", "1.0/../x", "invalid version"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in, s := installer(t, tt.f)
			_, err := in.Install(context.Background(), tt.tool, tt.version)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tt.want)
			}
			if vs, _ := s.Versions("agy"); len(vs) != 0 {
				t.Errorf("a failed install left %v", vs)
			}
			if vs, _ := s.Versions("codex"); len(vs) != 0 {
				t.Errorf("a failed install left %v", vs)
			}
			if !stagingEmpty(t, s) {
				t.Error("staging not cleaned up")
			}
		})
	}
}

func TestInstallAgyUsesManifestScript(t *testing.T) {
	f := &fakeContainer{writeBinary: true, versionOut: "1.4.10"}
	in, _ := installer(t, f)
	in.ManifestBase = "https://example.invalid/manifests"
	if _, err := in.Install(context.Background(), "agy", "1.4.10"); err != nil {
		t.Fatal(err)
	}
	inst := f.reqs[0]
	if inst.Env["MANIFEST_BASE"] != "https://example.invalid/manifests" || !strings.Contains(inst.Command[2], "sha512sum -c") || !strings.Contains(inst.Command[2], "$DOWNLOAD_PREFIX") {
		t.Errorf("agy install = %v\n%s", inst.Env, inst.Command[2])
	}
}

func TestConcurrentInstallOfTheSameVersion(t *testing.T) {
	f := &fakeContainer{writeBinary: true, versionOut: "0.165.0"}
	in, s := installer(t, f)
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := in.Install(context.Background(), "codex", "0.165.0")
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent install: %v", err)
		}
	}
	if ok, _ := s.Installed("codex", "0.165.0"); !ok {
		t.Error("not installed")
	}
	if !stagingEmpty(t, s) {
		t.Error("staging not cleaned up")
	}
}
