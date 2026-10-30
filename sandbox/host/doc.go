// Package host implements sandbox.Runtime by running the command as a child process of the
// current process, with no container. It starts in milliseconds and needs no Docker daemon, which
// suits short, tool-less CLI calls on machines that cannot run containers.
//
// It is not a sandbox. The child runs with the server's user and can read whatever that user can
// read, use the network and consume any amount of memory. What the runtime does provide:
//
//   - a private, per-run HOME and TMPDIR that are removed when the run ends;
//   - a clean environment: nothing is inherited from the server process;
//   - session state and credential files placed inside that HOME, with refreshed credentials
//     handed back like the Docker runtime does;
//   - a process group that is killed on timeout, cancel, idle and loop detection;
//   - refusal of runs it cannot make safe: profiles with tools (unless Config.AllowTools),
//     interactive commands that are not allowlisted, and requests for isolation it cannot give
//     (no network, read-only mounts).
//
// Use the Docker runtime for anything that runs with tools. Deploy this one under a dedicated
// user whose HOME holds nothing sensitive.
//
// The runtime is available on Linux and macOS. On other systems New returns ErrUnsupportedOS.
package host
