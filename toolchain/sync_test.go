package toolchain

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/baby-hero-404/aicli/sandbox"
)

// claudeLatest is a fake container whose newest claude release is 2.1.312.
func claudeLatest() *fakeContainer {
	return &fakeContainer{latest: "2.1.312", writeBinary: true, versionOut: "2.1.312 (Claude Code)"}
}

func syncClaude(t *testing.T, in *Installer, opts SyncOptions) SyncResult {
	t.Helper()
	opts.Tools = []string{"claude"}
	res, err := Sync(context.Background(), in, opts)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	return res
}

func TestSync_FirstSetupInstallsActivatesAndPrunes(t *testing.T) {
	in, s := installer(t, claudeLatest())
	res := syncClaude(t, in, SyncOptions{Keep: 2})
	if res.Installed["claude"] != "2.1.312" || res.Activated["claude"] != "2.1.312" {
		t.Errorf("result = %+v", res)
	}
	if cur, _ := s.Current("claude"); cur != "2.1.312" {
		t.Errorf("current = %q", cur)
	}
}

func TestSync_InstallOnlyDoesNotSwitch(t *testing.T) {
	in, s := installer(t, claudeLatest())
	for _, v := range []string{"2.1.309", "2.1.310", "2.1.311"} {
		fakeInstall(t, s, "claude", v)
	}
	_ = s.Use("claude", "2.1.311")

	res := syncClaude(t, in, SyncOptions{Keep: 2, InstallOnly: true})
	if cur, _ := s.Current("claude"); cur != "2.1.311" || len(res.Activated) != 0 {
		t.Errorf("current = %q activated = %v, want the operator's choice untouched", cur, res.Activated)
	}
	got, _ := s.Versions("claude")
	if want := []string{"2.1.311", "2.1.312"}; !slices.Equal(got, want) {
		t.Errorf("versions = %v, want %v (keep 2)", got, want)
	}
}

func TestSync_FollowsLatestWhileCurrentIsOnTheTip(t *testing.T) {
	in, s := installer(t, claudeLatest())
	fakeInstall(t, s, "claude", "2.1.310")
	fakeInstall(t, s, "claude", "2.1.311")
	_ = s.Use("claude", "2.1.311") // the newest installed: the operator follows the tip

	res := syncClaude(t, in, SyncOptions{})
	if cur, _ := s.Current("claude"); cur != "2.1.312" || res.Activated["claude"] != "2.1.312" {
		t.Errorf("current = %q activated = %v", cur, res.Activated)
	}
}

// An operator who rolled back a bad release, or reset to the CLI in the image, is not overridden.
func TestSync_NeverUndoesARollbackOrAReset(t *testing.T) {
	for name, setup := range map[string]func(*Store){
		"rollback": func(s *Store) { _ = s.Use("claude", "2.1.311") },
		"reset":    func(s *Store) { _ = s.Unuse("claude") },
	} {
		t.Run(name, func(t *testing.T) {
			in, s := installer(t, claudeLatest())
			fakeInstall(t, s, "claude", "2.1.311")
			fakeInstall(t, s, "claude", "2.1.312") // the bad release is installed already
			setup(s)
			want, _ := s.Current("claude")

			res := syncClaude(t, in, SyncOptions{})
			if cur, _ := s.Current("claude"); cur != want || len(res.Activated) != 0 {
				t.Errorf("current = %q (was %q), activated %v", cur, want, res.Activated)
			}
		})
	}
	// and a still newer release does not move a rolled-back tool either
	f := claudeLatest()
	f.latest, f.versionOut = "2.1.313", "2.1.313 (Claude Code)"
	in, s := installer(t, f)
	fakeInstall(t, s, "claude", "2.1.311")
	fakeInstall(t, s, "claude", "2.1.312")
	_ = s.Use("claude", "2.1.311")
	syncClaude(t, in, SyncOptions{})
	if cur, _ := s.Current("claude"); cur != "2.1.311" {
		t.Errorf("a newer release moved a rolled-back tool to %q", cur)
	}
}

func TestSync_KeepsProtectedVersions(t *testing.T) {
	in, s := installer(t, claudeLatest())
	for _, v := range []string{"2.1.308", "2.1.309", "2.1.310", "2.1.311"} {
		fakeInstall(t, s, "claude", v)
	}
	_ = s.Use("claude", "2.1.311")
	pinned := func(tool, version string) bool { return tool == "claude" && version == "2.1.309" }

	res := syncClaude(t, in, SyncOptions{Keep: 2, Protected: pinned})
	got, _ := s.Versions("claude")
	if want := []string{"2.1.309", "2.1.311", "2.1.312"}; !slices.Equal(got, want) {
		t.Errorf("versions = %v, want %v (the pinned 2.1.309 survives, 2.1.308 and 2.1.310 go)", got, want)
	}
	if want := []string{"2.1.308", "2.1.310"}; !slices.Equal(res.Pruned["claude"], want) {
		t.Errorf("pruned = %v, want %v", res.Pruned["claude"], want)
	}
}

// selective fails the installation of one tool, like an upstream outage for it alone.
type selective struct {
	*fakeContainer
	broken string
}

func (s selective) Run(ctx context.Context, req sandbox.CommandRequest) (*sandbox.CommandResult, error) {
	if req.Env["MODE"] == "install" && req.Env["BINARY"] == s.broken {
		return &sandbox.CommandResult{ExitCode: 1, Stderr: "registry unavailable"}, nil
	}
	return s.fakeContainer.Run(ctx, req)
}

func TestSync_OneBrokenToolDoesNotBlockTheOthers(t *testing.T) {
	f := claudeLatest()
	in, s := installer(t, f)
	in.Runtime = selective{fakeContainer: f, broken: "agy"}

	res, err := Sync(context.Background(), in, SyncOptions{Tools: []string{"agy", "claude"}})
	if err == nil || !strings.Contains(err.Error(), "agy") {
		t.Fatalf("err = %v, want the agy failure reported", err)
	}
	if res.Installed["claude"] != "2.1.312" {
		t.Errorf("claude was not installed after agy failed: %+v", res)
	}
	if cur, _ := s.Current("claude"); cur != "2.1.312" {
		t.Errorf("claude current = %q", cur)
	}
}

func TestSync_RequiresAnInstaller(t *testing.T) {
	if _, err := Sync(context.Background(), nil, SyncOptions{}); err == nil {
		t.Error("nil installer accepted")
	}
}

func TestCompareVersions(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"1.4.2", "1.4.10", -1},
		{"1.4.10", "1.4.10", 0},
		{"2.0.0", "1.99.99", 1},
		{"1.0", "1.0.1", -1},
		{"1.4.10-beta", "1.4.10", -1}, // a pre-release is older than its release
		{"1.4.10", "1.4.10-rc.1", 1},
		{"1.0.0-alpha", "1.0.0-beta", -1},
		{"1.0.0-rc.2", "1.0.0-rc.10", -1},
		{"1.0.0+build5", "1.0.0", 0}, // build metadata is ignored
	} {
		got := compareVersions(tc.a, tc.b)
		if (got < 0) != (tc.want < 0) || (got > 0) != (tc.want > 0) {
			t.Errorf("compareVersions(%q, %q) = %d, want sign %d", tc.a, tc.b, got, tc.want)
		}
		if back := compareVersions(tc.b, tc.a); (back < 0) != (tc.want > 0) || (back > 0) != (tc.want < 0) {
			t.Errorf("compareVersions(%q, %q) is not the mirror of the reverse: %d", tc.b, tc.a, back)
		}
	}
}
