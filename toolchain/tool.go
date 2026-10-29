package toolchain

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
)

// Errors.
var (
	ErrUnknownTool  = errors.New("toolchain: unknown tool")
	ErrInvalidName  = errors.New("toolchain: invalid version")
	ErrNotInstalled = errors.New("toolchain: version not installed")
	ErrInUse        = errors.New("toolchain: version is current")
)

// ContainerRoot is where tools are mounted inside the sandbox: <ContainerRoot>/<tool>.
const ContainerRoot = "/opt/aicli/tools"

// ProfileTarget is where the PATH script is mounted, so login shells (bash -l resets PATH from
// /etc/profile) still find the tools.
const ProfileTarget = "/etc/profile.d/aicli-tools.sh"

// Latest asks Install for the newest released version.
const Latest = "latest"

var versionPattern = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.+-]{0,63}$`)

// tool describes how one CLI is installed and run.
type tool struct {
	name string
	// binary is the executable under bin/.
	binary string
	// npmPackage is set for CLIs installed from npm.
	npmPackage string
	// postinstall is a script inside the npm package to run after a scripts-disabled install.
	// It is the only install-time code that runs, and it has been read: claude-code's
	// install.cjs links the platform binary (from an integrity-checked optional dependency)
	// over its placeholder, with no network access.
	postinstall string
	// manifest is set for agy: its release manifest gives the latest version, URL and SHA-512.
	manifest bool
	// env turns the CLI's own auto-update off. The mount is read-only anyway; this avoids
	// wasted attempts and noise.
	env map[string]string
}

// Tools lists the supported tool names.
func Tools() []string { return []string{"agy", "claude", "codex"} }

func lookup(name string) (tool, error) {
	switch name {
	case "claude":
		return tool{name: name, binary: "claude", npmPackage: "@anthropic-ai/claude-code", postinstall: "install.cjs",
			env: map[string]string{"DISABLE_AUTOUPDATER": "1"}}, nil
	case "codex":
		return tool{name: name, binary: "codex", npmPackage: "@openai/codex"}, nil
	case "agy":
		return tool{name: name, binary: "agy", manifest: true, env: map[string]string{"AGY_CLI_DISABLE_AUTO_UPDATE": "1"}}, nil
	}
	return tool{}, fmt.Errorf("%w: %q (supported: %v)", ErrUnknownTool, name, Tools())
}

func checkVersion(v string) error {
	if !versionPattern.MatchString(v) || v == Latest || v == "current" {
		return fmt.Errorf("%w: %q", ErrInvalidName, v)
	}
	return nil
}

// containerDir is where a tool is mounted in the sandbox.
func containerDir(name string) string { return ContainerRoot + "/" + name }

// binDirs returns the PATH entries for the given tools, in a stable order.
func binDirs(names []string) []string {
	sorted := slices.Clone(names)
	slices.Sort(sorted)
	out := make([]string, len(sorted))
	for i, n := range sorted {
		out[i] = containerDir(n) + "/bin"
	}
	return out
}
