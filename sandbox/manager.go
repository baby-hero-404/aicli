package sandbox

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Manager sits between a caller and a sandbox.Runtime,
// resolving which runtime (image + cache mounts + setup/healthcheck) a
// task's workspace needs before delegating the actual container lifecycle
// to the wrapped Runtime. It implements the same Run/RunInteractive/Prewarm
// shape as Runtime, so it's a drop-in replacement anywhere a Runtime is
// already accepted — detection is purely
// additive: a workspace that matches no manifest (or a runtime with no
// detectable project, e.g. an empty repo, or StubRuntime in dev mode) falls
// through as a transparent passthrough to the wrapped Runtime with the
// request untouched.
type Manager struct {
	runtime   Runtime
	registry  *Registry
	cacheRoot string
}

// ManagerOption configures a Manager.
type ManagerOption func(*Manager)

// WithCacheRoot enables package-install caches (npm, pip, go modules, ...) stored under
// root, a host directory owned by the application. Each run mounts
// <root>/<CommandRequest.CacheScope>/<cache name> at the cache's container path. Without
// a cache root no cache is mounted and every run installs from scratch; the host user's
// HOME is never used.
func WithCacheRoot(root string) ManagerOption {
	return func(m *Manager) { m.cacheRoot = root }
}

// NewManager wraps runtime with manifest-driven runtime detection. registry
// must not be nil — callers construct it once at startup (an empty registry is valid and
// makes Manager a transparent passthrough).
func NewManager(runtime Runtime, registry *Registry, opts ...ManagerOption) *Manager {
	m := &Manager{runtime: runtime, registry: registry}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// Layout implements LayoutProvider by delegating to the wrapped Runtime.
func (m *Manager) Layout(req CommandRequest) (Layout, error) {
	return LayoutOf(m.runtime, req)
}

// Prewarm delegates directly: image pre-pulling has no per-runtime concept
// today (the wrapped Runtime pre-pulls whatever single image it was
// configured with), so there's nothing for the manager to add here yet.
func (m *Manager) Prewarm(ctx context.Context) error {
	return m.runtime.Prewarm(ctx)
}

// RunInteractive delegates directly, without manifest resolution. Setup/
// healthcheck hooks (see Run) only make sense for one-shot, non-interactive
// commands — an interactive session is a human/agent driving a shell
// directly, where injecting hidden setup commands ahead of the terminal
// they asked for would be surprising and where a failed healthcheck has no
// good way to abort before the user is already attached.
func (m *Manager) RunInteractive(ctx context.Context, req CommandRequest, stdin io.Reader, stdout, stderr io.Writer) error {
	return m.runtime.RunInteractive(ctx, req, stdin, stdout, stderr)
}

// Run detects req.Workspace's runtime, merges in the resolved manifest's
// image/cache mounts, and — if the manifest declares setup/healthcheck
// commands — runs them before the caller's real command.
func (m *Manager) Run(ctx context.Context, req CommandRequest) (*CommandResult, error) {
	manifest := m.resolveManifest(req.Workspace)
	if manifest == nil {
		return m.runtime.Run(ctx, req)
	}

	resolved := req
	if resolved.Image == "" {
		resolved.Image = manifest.Image
	}
	resolved.ExtraCacheMounts = mergeCacheMounts(resolved.ExtraCacheMounts, manifest.Cache, m.cacheRoot, req.CacheScope)

	if len(manifest.Setup) == 0 && manifest.Healthcheck == "" {
		return m.runtime.Run(ctx, resolved)
	}

	// Setup and healthcheck run chained (via "&&") inside the *same*
	// command sent to the wrapped Runtime, rather than as separate Run()
	// calls: the Docker runtime creates and tears down one container per
	// call today, so a separate call would run setup in a container that's
	// immediately discarded, gaining nothing (the caller's real command
	// would start from scratch in a fresh container anyway). Chaining is
	// the minimal change that keeps the one-container-per-call lifecycle
	// intact; a persistent/reusable container (so setup's side effects
	// — e.g. `flutter pub get`'s populated pub cache being visible to a
	// later `flutter run` in the *same* container — survive across calls)
	// would need a real lifecycle change and is deferred, not attempted
	// here.
	//
	// The healthcheck runs first and its own exit code is inspected
	// separately (via a sentinel marker) so a healthcheck failure is
	// reported as "sandbox not ready" rather than being indistinguishable
	// from the real command failing.
	const healthcheckFailedMarker = "__SANDBOX_HEALTHCHECK_FAILED__"
	var scriptParts []string
	if manifest.Healthcheck != "" {
		scriptParts = append(scriptParts, fmt.Sprintf("(%s) || { echo %s; exit 97; }", manifest.Healthcheck, healthcheckFailedMarker))
	}
	scriptParts = append(scriptParts, manifest.Setup...)
	originalCommand := shellJoinCommand(resolved.Command)
	scriptParts = append(scriptParts, originalCommand)
	resolved.Command = []string{"bash", "-lc", strings.Join(scriptParts, " && ")}

	result, err := m.runtime.Run(ctx, resolved)
	if err != nil {
		return nil, err
	}
	if result.ExitCode == 97 && strings.Contains(result.Stdout, healthcheckFailedMarker) {
		return nil, fmt.Errorf("sandbox not ready: healthcheck failed for runtime %q: %s", manifest.ID, strings.TrimSpace(result.Stderr))
	}
	return result, nil
}

// resolveManifest returns the manifest matching workspace's detected
// runtime, or nil if workspace is empty, unreadable, or matches no
// registered manifest — all of which mean "pass the request through
// untouched" to Run.
func (m *Manager) resolveManifest(workspace string) *Manifest {
	if workspace == "" || m.registry == nil {
		return nil
	}
	runtimeID, ok := DetectRuntime(m.registry, workspace)
	if !ok {
		return nil
	}
	manifest, ok := m.registry.Get(runtimeID)
	if !ok {
		return nil
	}
	return manifest
}

// DefaultCacheScope is the cache namespace used when a request sets no CacheScope.
const DefaultCacheScope = "shared"

var cacheScopeRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// mergeCacheMounts adds one mount per manifest cache entry (container path -> host path
// <cacheRoot>/<scope>/<name>) to existing, without overwriting an entry the caller already
// set explicitly. It adds nothing when cacheRoot is empty or scope is not a plain name, so a
// hostile scope such as "../x" can never escape the cache root.
func mergeCacheMounts(existing map[string]string, cache []CacheMapping, cacheRoot, scope string) map[string]string {
	if len(cache) == 0 || cacheRoot == "" {
		return existing
	}
	if scope == "" {
		scope = DefaultCacheScope
	}
	if !cacheScopeRe.MatchString(scope) {
		return existing
	}
	absRoot, err := filepath.Abs(cacheRoot)
	if err != nil {
		return existing
	}
	merged := existing
	if merged == nil {
		merged = make(map[string]string, len(cache))
	}
	for _, c := range cache {
		if _, ok := merged[c.Container]; ok || !cacheScopeRe.MatchString(c.Name) {
			continue
		}
		hostPath := filepath.Join(absRoot, scope, c.Name)
		// 0o777: bind mounts carry host permission bits as-is and the sandbox user's UID
		// is not necessarily the host process's UID.
		if err := os.MkdirAll(hostPath, 0o777); err != nil {
			continue
		}
		_ = os.Chmod(hostPath, 0o777)
		merged[c.Container] = hostPath
	}
	return merged
}

// shellJoinCommand re-quotes a Command slice into a single shell string so
// it can be appended after setup/healthcheck in a "&&"-joined script.
// Callers into Manager.Run already send req.Command as
// []string{"bash", "-lc", "<script>"}, so the
// common case is just re-emitting that inner script unchanged; the general
// quoting path below handles any other caller that passes a literal argv.
func shellJoinCommand(command []string) string {
	if len(command) == 3 && command[0] == "bash" && (command[1] == "-lc" || command[1] == "-c") {
		return command[2]
	}
	quoted := make([]string, len(command))
	for i, arg := range command {
		quoted[i] = shellQuote(arg)
	}
	return strings.Join(quoted, " ")
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	if !strings.ContainsAny(s, " \t\n'\"\\$`") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
