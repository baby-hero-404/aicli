package sandbox

// WorkspaceDir is where container runtimes mount CommandRequest.Workspace.
const WorkspaceDir = "/workspace"

// Layout is where a runtime places a request's workspace and HOME, as seen by the process it
// starts. Callers use it to build commands, environment and mount targets instead of assuming
// the container paths.
type Layout struct {
	// Workspace is the working directory root; run's default work dir and capture's HOME.
	Workspace string
	// Home is $HOME for the CLI; credential files and session mounts target paths below it.
	Home string
}

// ContainerLayout is the layout of container runtimes such as sandbox/docker: the workspace at
// WorkspaceDir and HOME at HomeDir.
func ContainerLayout() Layout {
	return Layout{Workspace: WorkspaceDir, Home: HomeDir}
}

// LayoutProvider is implemented by runtimes whose processes do not see the container layout.
// req carries the fields the layout depends on (TaskID, Workspace). Decorators such as Manager
// implement it by delegating to the runtime they wrap.
type LayoutProvider interface {
	Layout(req CommandRequest) (Layout, error)
}

// LayoutOf returns rt's layout for req, or ContainerLayout when rt does not implement
// LayoutProvider.
func LayoutOf(rt Runtime, req CommandRequest) (Layout, error) {
	if p, ok := rt.(LayoutProvider); ok {
		return p.Layout(req)
	}
	return ContainerLayout(), nil
}
