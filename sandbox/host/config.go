package host

import (
	"errors"
	"log/slog"
)

// Errors returned (wrapped) by the host runtime; test them with errors.Is.
var (
	// ErrToolsNotAllowed: the request runs a profile that can use tools, or one the runtime
	// cannot identify, and Config.AllowTools is false.
	ErrToolsNotAllowed = errors.New("host: profile with tools is not allowed")
	// ErrNotAllowlisted: RunInteractive was asked to start a command that is not in
	// Config.InteractiveCommands.
	ErrNotAllowlisted = errors.New("host: interactive command is not allowlisted")
	// ErrUnsupported: the request needs isolation the host runtime cannot provide.
	ErrUnsupported = errors.New("host: request not supported by the host runtime")
	// ErrSessionDetached: a CLI replaced the link to its session state with a real directory, so
	// what it wrote would be lost with the run directory.
	ErrSessionDetached = errors.New("host: session state link was replaced during the run")
	// ErrUnsupportedOS is returned by New on systems other than Linux and macOS.
	ErrUnsupportedOS = errors.New("host: runtime is supported on linux and darwin only")
)

// Config configures a host runtime.
type Config struct {
	// RunRoot holds one private directory per run (<RunRoot>/<TaskID>). It must be owned by this
	// server alone and must not cover the host user's HOME. Default: a directory under
	// os.TempDir().
	RunRoot string
	// WorkspaceRoot is where a request without Workspace gets <WorkspaceRoot>/<TaskID>. Empty
	// means such requests are refused.
	WorkspaceRoot string
	// AllowedRoots, when set, restricts where the workspace and session directories may be: their
	// real locations (symlinks resolved) must lie inside one of these directories. Set it to the
	// application's data roots so a symlinked directory cannot lead the CLI somewhere else.
	AllowedRoots []string
	// PATH is used to find the command and is passed to it. Default
	// /usr/local/bin:/usr/bin:/bin. Note that a login shell (bash -l, which run uses) resets PATH
	// from /etc/profile, so the CLIs must be installed where it looks, or added there.
	PATH string
	// AllowTools lets profiles that can use tools (shell, file edits) run. They run unconfined
	// with the server's rights, so enable this only on trusted, single-user machines.
	AllowTools bool
	// InteractiveCommands lists the exact argv RunInteractive may start, for example
	// {"claude", "auth", "login"}. Anything else, including a bare shell, is refused.
	InteractiveCommands [][]string
	// Logger receives warnings and refusals; default slog.Default().
	Logger *slog.Logger
}

const defaultPATH = "/usr/local/bin:/usr/bin:/bin"
