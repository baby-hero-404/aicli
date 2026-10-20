package docker

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/baby-hero-404/aicli/sandbox"
)

func TestResolveNetworkMode(t *testing.T) {
	egressEnabled := &Runtime{config: Config{DisableNetworking: false}}
	egressDisabled := &Runtime{config: Config{DisableNetworking: true}}

	if got := egressEnabled.resolveNetworkMode(sandbox.NetworkModeDefault); got != "bridge" {
		t.Errorf("default with egress enabled: expected bridge (not host — see sandbox-network-isolation-fix), got %q", got)
	}
	if got := egressDisabled.resolveNetworkMode(sandbox.NetworkModeDefault); got != sandbox.NetworkModeNone {
		t.Errorf("default with egress disabled: expected none, got %q", got)
	}
	// Bridge is explicitly requested here even though DisableNetworking is
	// true on this runtime instance — resolveNetworkMode only consults
	// DisableNetworking for the sandbox.NetworkModeDefault case; an explicit
	// sandbox.NetworkModeBridge request always resolves to bridge regardless.
	if got := egressDisabled.resolveNetworkMode(sandbox.NetworkModeBridge); got != "bridge" {
		t.Errorf("explicit bridge: expected bridge, got %q", got)
	}
	if got := egressEnabled.resolveNetworkMode(sandbox.NetworkModeNone); got != sandbox.NetworkModeNone {
		t.Errorf("explicit none: expected none, got %q", got)
	}
}

func TestReadRegularFile_RejectsSymlinksAndHugeFiles(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(dir, "host-secret")
	if err := os.WriteFile(secret, []byte("TOP-SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	ok := filepath.Join(dir, "cred.json")
	if err := os.WriteFile(ok, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(secret, link); err != nil {
		t.Fatal(err)
	}
	big := filepath.Join(dir, "big.json")
	if err := os.WriteFile(big, make([]byte, maxCredentialReadBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}

	if b, err := readRegularFile(ok); err != nil || string(b) != "{}" {
		t.Errorf("regular file: %q, %v", b, err)
	}
	if b, err := readRegularFile(link); err == nil {
		t.Errorf("symlink must not be read, got %q", b)
	}
	if _, err := readRegularFile(big); err == nil {
		t.Error("oversized file must be rejected")
	}
	if _, err := readRegularFile(filepath.Join(dir, "missing")); err == nil {
		t.Error("missing file must error")
	}
}
