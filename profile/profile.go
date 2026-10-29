// Package profile is the registry of built-in AI coding CLI profiles (claude, codex, agy):
// how to invoke each one headlessly and how to check its login state.
package profile

import (
	"errors"
	"fmt"
	"maps"
	"path"
	"slices"
)

// Profile is a built-in description of how to invoke one AI coding CLI headlessly.
type Profile struct {
	Command            string   `json:"command"`
	Args               []string `json:"args"`
	AuthCheckCommand   string   `json:"auth_check_command,omitempty"`
	TimeoutMinutes     int      `json:"timeout_minutes"`
	CredentialProvider string   `json:"credential_provider,omitempty"`
	// StateDirs are the directories, relative to HOME, where the CLI keeps its login and
	// session state. Mount one per-account host directory at each of them to keep accounts
	// apart and to persist a login across runs.
	StateDirs []string `json:"state_dirs"`
	// StateEnv maps an environment variable to a directory relative to HOME. Setting it makes
	// the CLI keep all of its state inside StateDirs (for example claude otherwise also writes
	// ~/.claude.json at the root of HOME). Resolve it for a given HOME with Env.
	StateEnv map[string]string `json:"state_env,omitempty"`
	// StreamArgs are the flags that make the CLI print its progress as machine-readable
	// events while it runs (replacing the "--output-format <fmt>" pair in Args). Empty means
	// the profile cannot stream. Apply them with StreamingArgs.
	StreamArgs []string `json:"stream_args,omitempty"`
}

// Env returns StateEnv resolved against home, e.g. {"CLAUDE_CONFIG_DIR": "/home/agent/.claude"}.
// Pass it to every process that runs this CLI, including login sessions, so state always lands
// in the same place.
func (p Profile) Env(home string) map[string]string {
	if len(p.StateEnv) == 0 {
		return nil
	}
	out := make(map[string]string, len(p.StateEnv))
	for k, rel := range p.StateEnv {
		out[k] = path.Join(home, rel)
	}
	return out
}

// promptFileInstruction is embedded (not the bare {prompt_file} path alone)
// as the literal prompt-text argument for claude/codex/agy: none of the
// three actually accept a raw file path as their headless prompt argument
// (per each tool's headless documentation, all three take literal instruction
// text — e.g. `claude -p "Analyze this project"`); a bare path there would
// be read as prompt text equal to the path string itself, not the file's
// contents. Wrapping it in a sentence works because all three are agentic
// coding CLIs with their own file-read tool, so they open the referenced
// file themselves. The caller's {prompt_file} substitution is expected to do a plain
// string replace, so this works for any future profile that instead wires
// a real --file/--prompt-file flag by using a bare {prompt_file} arg.
const promptFileInstruction = "Read and follow the complete task instructions in the file {prompt_file}, then carry them out."

// cliProfileTimeoutMinutes is the wall-clock cap on a single CLI step.
//
// This was 30 minutes, which silently overrode the engine's own 3h default
// *downward* and killed healthy long runs — a spec-first implement step on a
// real codebase routinely runs for hours. Five hours makes it a genuine
// backstop. Stall detection is not this knob's job and is deliberately
// untouched: sandbox.DefaultIdleTimeout (15m) kills a step with no
// stdout/stderr activity, so a working agent prints and a hung one does not.
// A CLI step is killed for silence, not for duration.
const cliProfileTimeoutMinutes = 300

// profiles is the built-in registry of known CLI coding tools, keyed by
// the profile ref callers select (e.g. "claude_code").
var profiles = map[string]Profile{
	"claude_code": {
		Command: "claude",
		// --output-format json is appended last (valid ordering per claude's headless
		// docs: `claude -p "..." --output-format json`) so a caller can extract
		// total_cost_usd/duration_ms/usage from the trailing JSON summary object claude
		// prints on exit.
		Args: []string{"--allowedTools", "Read,Edit,Write,Bash", "-p", promptFileInstruction, "--output-format", "json"},
		// "claude auth status" is read-only (no side effects) and always
		// exits 0 regardless of login state, reporting it instead via a
		// `"loggedIn": bool` JSON field (verified against the real binary) —
		// exit-code-only checking would let a logged-out credential pass
		// Preflight, so rules.DetectAuthInvalid checks this output
		// for the loggedIn:false signature instead of trusting exit 0.
		AuthCheckCommand:   "claude auth status",
		TimeoutMinutes:     cliProfileTimeoutMinutes,
		CredentialProvider: "cli:claude",
		// Verified against the binary: with CLAUDE_CONFIG_DIR set, claude keeps .claude.json
		// inside that directory instead of at the root of HOME.
		StateDirs: []string{".claude"},
		StateEnv:  map[string]string{"CLAUDE_CONFIG_DIR": ".claude"},
	},
	"openai_codex": {
		Command: "codex",
		// Codex runs as plain text output for now, so telemetry and session IDs are not
		// available. codex 0.163.0 offers `--json` (JSONL events) and resumes with the
		// subcommand form `codex exec resume <SESSION_ID> <PROMPT>`; supporting both needs a
		// JSONL parser and argument placement that differs from the other CLIs.
		Args: []string{"exec", "--full-auto", promptFileInstruction},
		// "codex login status" is read-only and prints "Logged in using
		// ChatGPT" on success (verified against the real binary).
		AuthCheckCommand:   "codex login status",
		TimeoutMinutes:     cliProfileTimeoutMinutes,
		CredentialProvider: "cli:codex",
		// Verified against the binary: codex keeps its state in ${CODEX_HOME:-$HOME/.codex}.
		StateDirs: []string{".codex"},
	},
	"antigravity": {
		Command: "agy",
		// agy's flag parser is Go stdlib `flag` (its --help text is the
		// literal flag.PrintDefaults() format), where -p/--print/--prompt is
		// a value flag: whatever token immediately follows -p becomes its
		// value (the prompt), not the next positional. With
		// {"-p", "--dangerously-skip-permissions", instruction} the CLI
		// literally used "--dangerously-skip-permissions" as the prompt —
		// confirmed from a real run's captured output, which was agy
		// explaining that flag instead of analyzing the repo (the
		// requested output file was never written). -p's value must be the
		// very next arg, so boolean flags go first. The real binary is
		// `agy` (not `antigravity`) and `-p` must sit immediately before the prompt.
		// --output-format json appended last, same rationale/placement as
		// claude_code above.
		Args: []string{"--dangerously-skip-permissions", "-p", promptFileInstruction, "--output-format", "json"},
		// "agy models" needs a signed-in account: logged out it exits 1 with "Please sign in"
		// (verified against the real binary). "agy --version" always succeeds, so it would let a
		// logged-out credential through, after which a run blocks waiting for an auth code.
		AuthCheckCommand:   "agy models",
		TimeoutMinutes:     cliProfileTimeoutMinutes,
		CredentialProvider: "cli:antigravity",
		StateDirs:          []string{".gemini"},
		StreamArgs:         agyStreamArgs,
	},
	"claude_code_notools": {
		Command:            "claude",
		Args:               []string{"--tools", "", "--safe-mode", "--strict-mcp-config", "-p", promptFileInstruction, "--output-format", "json"},
		AuthCheckCommand:   "claude auth status",
		TimeoutMinutes:     cliProfileTimeoutMinutes,
		CredentialProvider: "cli:claude",
		StateDirs:          []string{".claude"},
		StateEnv:           map[string]string{"CLAUDE_CONFIG_DIR": ".claude"},
	},
	"antigravity_notools": {
		Command:            "agy",
		Args:               []string{"--disable-slash-commands", "--output-format", "json", "-p", promptFileInstruction},
		AuthCheckCommand:   "agy models",
		TimeoutMinutes:     cliProfileTimeoutMinutes,
		CredentialProvider: "cli:antigravity",
		StateDirs:          []string{".gemini"},
		StreamArgs:         agyStreamArgs,
	},
}

// agyStreamArgs makes agy print NDJSON events (see run/stream/agy) instead of one summary
// object. Replaces "--output-format json".
var agyStreamArgs = []string{"--output-format", "stream-json"}

// Lookup returns a copy of the built-in profile registered under key and true, or a zero value
// and false if key is unknown. The copy's slices are private to the caller, so they may be
// appended to without affecting the registry.
func Lookup(key string) (Profile, bool) {
	p, ok := profiles[key]
	if !ok {
		return Profile{}, false
	}
	return p.clone(), true
}

// List returns a copy of every built-in profile keyed by ref.
func List() map[string]Profile {
	out := make(map[string]Profile, len(profiles))
	for k, p := range profiles {
		out[k] = p.clone()
	}
	return out
}

// ResumeArgs returns the extra arguments that make the CLI identified by ref continue a
// previous session, or nil when the CLI has no resume support or sessionID is empty.
// openai_codex is not supported yet: its resume is a subcommand placed before the prompt
// (`codex exec resume <id> <prompt>`), not trailing arguments.
func ResumeArgs(ref, sessionID string) []string {
	if sessionID == "" {
		return nil
	}
	switch ref {
	case "claude_code", "claude_code_notools":
		return []string{"--resume", sessionID}
	case "antigravity", "antigravity_notools":
		return []string{"--conversation", sessionID}
	}
	return nil
}

// ErrStreamingUnsupported is returned by StreamingArgs for a profile that cannot stream.
var ErrStreamingUnsupported = errors.New("profile: streaming is not supported")

const outputFormatFlag = "--output-format"

// StreamingArgs returns args with the profile's streaming flags in place of its
// "--output-format <fmt>" pair, wherever that pair sits; if args has no such pair the
// streaming flags are appended. args is not modified. Flag placement is left as the caller
// had it on purpose: agy's -p takes the very next token as the prompt, so nothing may be
// inserted between -p and its value, and replacing in place guarantees that.
//
// It returns ErrStreamingUnsupported for an unknown ref or a profile without StreamArgs.
func StreamingArgs(ref string, args []string) ([]string, error) {
	p, ok := profiles[ref]
	if !ok || len(p.StreamArgs) == 0 {
		return nil, fmt.Errorf("%w: %q", ErrStreamingUnsupported, ref)
	}
	out := make([]string, 0, len(args)+len(p.StreamArgs))
	replaced := false
	for i := 0; i < len(args); i++ {
		if args[i] == outputFormatFlag && !replaced {
			out = append(out, p.StreamArgs...)
			replaced = true
			if i+1 < len(args) {
				i++ // skip the old format value (a dangling flag has none)
			}
			continue
		}
		out = append(out, args[i])
	}
	if !replaced {
		out = append(out, p.StreamArgs...)
	}
	return out, nil
}

func (p Profile) clone() Profile {
	p.Args = slices.Clone(p.Args)
	p.StreamArgs = slices.Clone(p.StreamArgs)
	p.StateDirs = slices.Clone(p.StateDirs)
	p.StateEnv = maps.Clone(p.StateEnv)
	return p
}
