// Package toolchain installs agent CLIs (claude, codex, agy) once on the host, in versioned
// directories, and mounts them read-only into every sandbox, so upgrading or rolling back a CLI
// does not need a new image.
//
// Layout under the root directory:
//
//	<root>/<tool>/<version>/bin/<binary>   an installed version (complete, or absent)
//	<root>/<tool>/current                  the active version, one line
//	<root>/profile.sh                      puts the tool bin directories on PATH in login shells
//	<root>/.staging/                       installs in progress
//
// Integration is one line: wrap the runtime you already use,
//
//	rt := toolchain.Wrap(dockerRuntime, store)
//
// and every Run and RunInteractive (runs, preflight checks, logins) gets each tool's current
// version mounted at /opt/aicli/tools/<tool>, unless the request pins another version in
// sandbox.CommandRequest.ToolVersions. A tool with no current version is not mounted, so the CLI
// in the image, if any, is used.
package toolchain
