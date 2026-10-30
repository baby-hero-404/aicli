// Package sandbox defines how AI coding CLIs are run in isolation: the Runtime interface and its
// request/result types, the execution policy every runtime enforces, per-account session
// directories, and an optional Manager that picks an image, setup commands and caches from
// host-registered runtime manifests.
//
// This package has no container dependency. The Docker implementation lives in sandbox/docker,
// a process-based one for machines without Docker in sandbox/host, and a test double in
// sandbox/sandboxmock. Callers that build paths for the process (the work dir, HOME, mount
// targets) ask the runtime with LayoutOf instead of assuming the container's.
package sandbox

// DefaultWorkspaceRoot is the host directory under which per-task workspaces are created when no
// root is configured.
const DefaultWorkspaceRoot = "/tmp/aicli/workspaces"
