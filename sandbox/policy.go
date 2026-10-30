package sandbox

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

func validateCommand(command []string) error {
	if len(command) == 0 {
		return fmt.Errorf("sandbox command is required")
	}
	joined := strings.ToLower(strings.Join(command, " "))
	blocked := []string{"mkfs", ":(){", "dd if="}
	for _, pattern := range blocked {
		if strings.Contains(joined, pattern) {
			return fmt.Errorf("sandbox policy blocked command containing %q", pattern)
		}
	}
	if isDestructiveRm(joined) {
		return fmt.Errorf("sandbox policy blocked destructive rm command")
	}
	return nil
}

func isDestructiveRm(joined string) bool {
	if strings.Contains(joined, "--no-preserve-root") {
		return true
	}
	rmIdx := strings.Index(joined, "rm ")
	if rmIdx < 0 {
		return false
	}

	destructiveTargets := []string{
		"/",
		"/*",
		"/workspace",
		"/workspace/",
		"/workspace/*",
		"~",
		"~/",
		"~/*",
		"$home",
		"$home/",
		"$home/*",
	}

	for _, target := range destructiveTargets {
		patterns := []string{
			"rm -rf " + target,
			"rm -r " + target,
			"rm -f -r " + target,
			"rm -r -f " + target,
			"rm -rf '" + target + "'",
			"rm -rf \"" + target + "\"",
			"rm -r '" + target + "'",
			"rm -r \"" + target + "\"",
		}
		for _, p := range patterns {
			idx := 0
			for {
				found := strings.Index(joined[idx:], p)
				if found < 0 {
					break
				}
				matchPos := idx + found
				endIdx := matchPos + len(p)
				if endIdx == len(joined) {
					return true
				}
				nextChar := joined[endIdx]
				if nextChar == ' ' || nextChar == ';' || nextChar == '&' || nextChar == '|' || nextChar == '\n' || nextChar == '"' || nextChar == '\'' || nextChar == ')' || nextChar == '}' {
					return true
				}
				idx = matchPos + 1
			}
		}
	}
	return false
}

// validateExecutionPolicy enforces the secrets/network-egress exclusion.
// resolvedNetworkMode must be the mode the runtime will actually apply to
// the container (after resolving NetworkModeDefault against runtime config),
// not the raw requested mode — the two previously diverged, letting secrets
// be injected into containers that ended up with network access.
func validateExecutionPolicy(req CommandRequest, resolvedNetworkMode string) error {
	switch req.NetworkMode {
	case NetworkModeDefault, NetworkModeNone, NetworkModeBridge:
	default:
		return fmt.Errorf("unsupported sandbox network mode %q", req.NetworkMode)
	}
	if resolvedNetworkMode != NetworkModeNone && len(req.SecretEnv) > 0 {
		return fmt.Errorf("sandbox policy blocks injecting secrets when network egress is enabled")
	}
	return nil
}

// validateMounts rejects mount and credential targets that are not clean absolute container
// paths, and session-mount sources that are not absolute host paths. A target such as
// "/home/agent/.claude/../../x" would otherwise let a credential write escape the session
// directory it is nominally inside.
func validateMounts(req CommandRequest) error {
	cleanAbs := func(p string) bool { return path.IsAbs(p) && path.Clean(p) == p }
	for target := range req.CredentialFiles {
		if !cleanAbs(target) {
			return fmt.Errorf("sandbox: credential file target %q must be a clean absolute container path", target)
		}
	}
	for target, host := range req.SessionMounts {
		if !cleanAbs(target) {
			return fmt.Errorf("sandbox: session mount target %q must be a clean absolute container path", target)
		}
		if !filepath.IsAbs(host) {
			return fmt.Errorf("sandbox: session mount source %q must be an absolute host path", host)
		}
	}
	for target, host := range req.ReadOnlyMounts {
		if !cleanAbs(target) {
			return fmt.Errorf("sandbox: read-only mount target %q must be a clean absolute container path", target)
		}
		if !filepath.IsAbs(host) {
			return fmt.Errorf("sandbox: read-only mount source %q must be an absolute host path", host)
		}
	}
	for target := range req.ExtraCacheMounts {
		if !cleanAbs(target) {
			return fmt.Errorf("sandbox: cache mount target %q must be a clean absolute container path", target)
		}
	}
	return validateNoHostHome(req)
}

// validateNoHostHome rejects a bind-mount source that is the host user's HOME or one of its
// ancestors (such as "/"): mounting it would hand the sandbox every login, key and token the
// user has. A directory inside HOME is fine; data roots often live there.
func validateNoHostHome(req CommandRequest) error {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil // no HOME to protect
	}
	sources := []string{req.Workspace, req.LogsHostDir}
	for _, m := range []map[string]string{req.SessionMounts, req.ReadOnlyMounts, req.ExtraCacheMounts} {
		for _, host := range m {
			sources = append(sources, host)
		}
	}
	for _, src := range sources {
		if src != "" && coversPath(src, home) {
			return fmt.Errorf("sandbox policy: mount source %q would expose the host user's HOME", src)
		}
	}
	return nil
}

// coversPath reports whether dir is path or one of its ancestors, comparing real locations when
// they can be resolved so that a symlink to HOME is caught too.
func coversPath(dir, p string) bool {
	resolve := func(s string) string {
		s = filepath.Clean(s)
		if r, err := filepath.EvalSymlinks(s); err == nil {
			return r
		}
		return s
	}
	rel, err := filepath.Rel(resolve(dir), resolve(p))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// ValidateRequest checks req before a runtime executes it: the command policy, the
// secrets/network-egress rule (resolvedNetworkMode is the mode the runtime will actually apply),
// and mount paths. Runtime implementations call it first.
func ValidateRequest(req CommandRequest, resolvedNetworkMode string) error {
	if err := validateCommand(req.Command); err != nil {
		return err
	}
	if err := validateExecutionPolicy(req, resolvedNetworkMode); err != nil {
		return err
	}
	return validateMounts(req)
}
