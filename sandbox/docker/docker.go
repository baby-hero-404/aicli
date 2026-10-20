// Package docker implements sandbox.Runtime with Docker containers.
package docker

import (
	"bytes"
	"context"
	"fmt"
	"github.com/baby-hero-404/aicli/sandbox"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
	"go.opentelemetry.io/otel"
)

// DefaultImage is the image Runtime uses when Config.Image is empty: the reference image built
// from docker/base (make image-base).
const DefaultImage = "aicli/base:latest"

const tracerName = "github.com/baby-hero-404/aicli/sandbox/docker"

// Config configures a Docker runtime.
type Config struct {
	Image             string
	WorkspaceRoot     string
	MemoryBytes       int64
	NanoCPUs          int64
	DisableNetworking bool
}

// Runtime runs sandbox requests as Docker containers. It implements sandbox.Runtime.
type Runtime struct {
	client *client.Client
	config Config
}

var _ sandbox.Runtime = (*Runtime)(nil)

// New connects to the Docker daemon configured by the environment (DOCKER_HOST etc.).
func New(config Config) (*Runtime, error) {
	if config.Image == "" {
		config.Image = DefaultImage
	}
	if config.WorkspaceRoot == "" {
		config.WorkspaceRoot = sandbox.DefaultWorkspaceRoot
	}
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("create docker client: %w", err)
	}
	return &Runtime{client: cli, config: config}, nil
}

// resolveImage returns requestImage if set, falling back to the runtime's
// configured default image otherwise — the "empty = today's exact behavior"
// contract sandbox.CommandRequest.Image documents, so a caller that never opts into
// per-runtime image selection (i.e. everything before Manager
// existed) is unaffected.
func (r *Runtime) resolveImage(requestImage string) string {
	if requestImage != "" {
		return requestImage
	}
	return r.config.Image
}

// Health reports whether the Docker daemon is reachable.
func (r *Runtime) Health(ctx context.Context) error {
	if _, err := r.client.Ping(ctx, client.PingOptions{}); err != nil {
		return fmt.Errorf("ping docker daemon: %w", err)
	}
	return nil
}

// Prewarm pulls the configured image when it is not present locally.
func (r *Runtime) Prewarm(ctx context.Context) error {
	// First check if the image already exists locally.
	_, err := r.client.ImageInspect(ctx, r.config.Image)
	if err == nil {
		return nil
	}

	reader, err := r.client.ImagePull(ctx, r.config.Image, client.ImagePullOptions{})
	if err != nil {
		return fmt.Errorf("pull sandbox image: %w", err)
	}
	defer reader.Close()
	_, _ = io.Copy(io.Discard, reader)
	return nil
}

// resolveNetworkMode maps the requested mode to the mode the container will
// actually run with, resolving sandbox.NetworkModeDefault against the runtime's
// DisableNetworking config. This single resolution point feeds both policy
// validation and ContainerCreate, so the two can never disagree about
// whether the container has network access.
//
// "bridge"/default-with-networking resolve to Docker's own default bridge
// network ("bridge") — NAT'd egress, no host-namespace sharing. This used
// to literally return "host" (full host network-namespace access: every
// interface, every 127.0.0.1 service on the host, visible to the
// container), which contradicted both the sandbox.NetworkModeBridge name and
// policy.go's SecretEnv exposure gating, which assumes bridge is a lower-
// exposure mode than host. sandbox.NetworkModeNone is unaffected.
func (r *Runtime) resolveNetworkMode(requested string) string {
	if requested == sandbox.NetworkModeBridge || (requested == sandbox.NetworkModeDefault && !r.config.DisableNetworking) {
		return "bridge"
	}
	return sandbox.NetworkModeNone
}

// sessionMountsOverlap checks if the target path is already covered by a SessionMount
// (e.g. SessionMounts mounts "/home/agent/.claude" and target is "/home/agent/.claude/.credentials.json")
func sessionMountsOverlap(target string, sessionMounts map[string]string) bool {
	for smTarget := range sessionMounts {
		if target == smTarget || strings.HasPrefix(target, smTarget+"/") {
			return true
		}
	}
	return false
}

// Run executes req in a new container and removes it afterwards. The workspace is mounted at
// /workspace; credential files, session mounts, cache mounts and the logs directory are mounted
// as requested. Idle and loop watchdogs kill a stalled process, and credential files the process
// rewrote are returned in CommandResult.UpdatedCredentialFiles.
func (r *Runtime) Run(ctx context.Context, req sandbox.CommandRequest) (*sandbox.CommandResult, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "sandbox.docker.run")
	defer span.End()
	resolvedNetworkMode := r.resolveNetworkMode(req.NetworkMode)
	if err := sandbox.ValidateRequest(req, resolvedNetworkMode); err != nil {
		return nil, err
	}
	if len(req.Command) == 0 {
		return nil, fmt.Errorf("docker command is required")
	}
	workspace := req.Workspace
	if workspace == "" {
		workspace = sandbox.WorkspacePath(r.config.WorkspaceRoot, req.TaskID)
	}
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		return nil, fmt.Errorf("create sandbox workspace: %w", err)
	}

	envMap := req.MergedEnv()

	mounts := []mount.Mount{
		{
			Type:   mount.TypeBind,
			Source: workspace,
			Target: "/workspace",
		},
	}

	if req.LogsHostDir != "" {
		if err := os.MkdirAll(req.LogsHostDir, 0o755); err != nil {
			return nil, fmt.Errorf("create sandbox logs dir: %w", err)
		}
		mounts = append(mounts, mount.Mount{
			Type:   mount.TypeBind,
			Source: req.LogsHostDir,
			Target: sandbox.LogsContainerDir,
		})
	}

	for targetContainerPath, hostPath := range req.SessionMounts {
		// Ensure the host path exists
		if err := os.MkdirAll(hostPath, 0o777); err != nil {
			return nil, fmt.Errorf("create session mount dir: %w", err)
		}
		mounts = append(mounts, mount.Mount{
			Type:   mount.TypeBind,
			Source: hostPath,
			Target: targetContainerPath,
		})
	}

	// Package caches the caller asked for (sandbox.Manager fills these from its cache root).
	// Writable, since installing anything not yet cached writes into them.
	for target, host := range req.ExtraCacheMounts {
		if err := os.MkdirAll(host, 0o777); err != nil {
			return nil, fmt.Errorf("create cache mount dir: %w", err)
		}
		mounts = append(mounts, mount.Mount{Type: mount.TypeBind, Source: host, Target: target})
	}

	// Sandboxes never see the host user's HOME: no auth directories and no package caches
	// are mounted implicitly. Every run starts from the image's own (fresh) /home/agent and
	// receives only what the caller passes explicitly via CredentialFiles and SessionMounts,
	// so concurrent runs for different accounts cannot see each other's state or the host's.

	// For credential files, they are typically small files containing OAuth
	// tokens or API keys (e.g. .claude.json, .gemini/antigravity-oauth-token).
	// Because Docker cannot reliably bind-mount single files from the host
	// while supporting atomic renames (which these CLIs do when refreshing
	// tokens), we stage them in a temporary directory on the host and mount
	// the whole parent directories.

	credentialHostPaths := make(map[string]string, len(req.CredentialFiles))
	if len(req.CredentialFiles) > 0 {
		credDir, err := os.MkdirTemp("", "aicli-cred-*")
		if err != nil {
			return nil, fmt.Errorf("create credential staging dir: %w", err)
		}
		defer os.RemoveAll(credDir)

		// Recreate the exact file layout inside credDir OR write directly to a SessionMount
		for targetContainerPath, content := range req.CredentialFiles {
			var smContainerPath, smHostPath string
			isCovered := false
			for smTarget, smHost := range req.SessionMounts {
				if targetContainerPath == smTarget || strings.HasPrefix(targetContainerPath, smTarget+"/") {
					isCovered = true
					smContainerPath = smTarget
					smHostPath = smHost
					break
				}
			}

			if isCovered {
				// The path is already mapped by a SessionMount (e.g. ~/.claude).
				// Write the credential directly into the host session directory.
				relPath := strings.TrimPrefix(targetContainerPath, smContainerPath)
				hostPath := filepath.Join(smHostPath, relPath)

				if err := os.MkdirAll(filepath.Dir(hostPath), 0o777); err != nil {
					return nil, fmt.Errorf("create credential subdirs in session: %w", err)
				}
				// 0o666, not owner-only: bind mounts carry host-side
				// permission bits as-is with no UID remapping, and the container's fixed "agent" UID
				// is not generally the same UID as the host server process
				// writing this file.
				if err := os.WriteFile(hostPath, []byte(content), 0o666); err != nil {
					return nil, fmt.Errorf("write credential file in session: %w", err)
				}
				credentialHostPaths[targetContainerPath] = hostPath
				continue
			}

			// e.g. "/home/agent/.claude/.credentials.json" -> "home/agent/.claude/.credentials.json"
			relPath := strings.TrimPrefix(targetContainerPath, "/")
			hostPath := filepath.Join(credDir, relPath)

			if err := os.MkdirAll(filepath.Dir(hostPath), 0o777); err != nil {
				return nil, fmt.Errorf("create credential subdirs: %w", err)
			}

			// 0o666 allows read/write from any UID inside the container
			if err := os.WriteFile(hostPath, []byte(content), 0o666); err != nil {
				return nil, fmt.Errorf("write credential file: %w", err)
			}

			// We need this map for extracting updated files later
			credentialHostPaths[targetContainerPath] = hostPath
		}

		// Figure out the highest-level directories to mount so that atomic renames work.
		// Mounting individual files breaks atomic rename (rename(2) returns EXDEV).
		mountTargets := make(map[string]bool)
		for targetContainerPath := range req.CredentialFiles {
			// If it was written to a SessionMount, it doesn't need its own mount target
			if sessionMountsOverlap(targetContainerPath, req.SessionMounts) {
				continue
			}

			rel, err := filepath.Rel(sandbox.HomeDir, targetContainerPath)
			if err == nil && !strings.HasPrefix(rel, "..") && rel != "." && strings.Contains(rel, string(filepath.Separator)) {
				// E.g. target="/home/agent/.claude/.credentials.json", rel=".claude/.credentials.json"
				// Top-level child of /home/agent is ".claude"
				parts := strings.Split(rel, string(filepath.Separator))
				topLevel := filepath.Join(sandbox.HomeDir, parts[0])
				mountTargets[topLevel] = true
			} else {
				// Either outside sandbox.HomeDir, or it's a file right in the home directory
				// (e.g. "/home/agent/.claude.json"). In this case, we have to mount it directly.
				mountTargets[targetContainerPath] = true
			}
		}

		for targetContainerPath := range mountTargets {
			relPath := strings.TrimPrefix(targetContainerPath, "/")
			hostPath := filepath.Join(credDir, relPath)

			// If it's a directory, ensure it is fully writable by the agent user
			if stat, err := os.Stat(hostPath); err == nil && stat.IsDir() {
				os.Chmod(hostPath, 0o777)
			}

			mounts = append(mounts, mount.Mount{
				Type:   mount.TypeBind,
				Source: hostPath,
				Target: targetContainerPath,
			})
		}
	}

	env := make([]string, 0, len(envMap))
	for key, value := range envMap {
		env = append(env, key+"="+value)
	}

	networkMode := container.NetworkMode(resolvedNetworkMode)

	// The run budget starts here, after host-side staging, so copying credential
	// directories never eats into the command's own timeout.
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}

	createResp, err := r.client.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{
			Image:      r.resolveImage(req.Image),
			Cmd:        req.Command,
			Env:        env,
			WorkingDir: "/workspace",
		},
		HostConfig: &container.HostConfig{
			NetworkMode: networkMode,
			Resources: container.Resources{
				Memory:   r.config.MemoryBytes,
				NanoCPUs: r.config.NanoCPUs,
			},
			Mounts: mounts,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("create docker container: %w", err)
	}
	containerID := createResp.ID
	defer func() {
		timeout := 5
		_, _ = r.client.ContainerStop(context.Background(), containerID, client.ContainerStopOptions{Timeout: &timeout})
		_, _ = r.client.ContainerRemove(context.Background(), containerID, client.ContainerRemoveOptions{Force: true})
	}()

	if _, err := r.client.ContainerStart(ctx, containerID, client.ContainerStartOptions{}); err != nil {
		return nil, fmt.Errorf("start docker container: %w", err)
	}

	// Stream logs with Follow:true starting right after the container
	// starts, rather than fetching the whole buffer with ContainerLogs after
	// ContainerWait returns (the old behavior): a container killed mid-run
	// (OOM, host restart, context cancellation) left no trace at all under
	// the old approach, since ContainerWait never resolved and the
	// after-the-fact ContainerLogs call was never reached. Streaming
	// concurrently with the wait means a real-time on-disk copy (via
	// req.LogFilePath) survives up to the
	// point of failure even when the container itself never exits cleanly.
	stdout, stderr, logsErrCh, logCloser, activity, loopDet, err := r.streamContainerLogs(ctx, containerID, req.LogFilePath)
	if err != nil {
		return nil, err
	}
	defer logCloser()

	// watchForStall polls
	// the same activity/loop trackers streamContainerLogs just wired up and
	// force-kills the container the first time either condition trips,
	// independent of — and normally well before — req.Timeout's absolute
	// cap. watchdogDone stops it once the container has exited on its own
	// (success, normal failure, or ctx cancellation below) so it never
	// races a kill against an already-finished run.
	idleTimeout := req.IdleTimeout
	if idleTimeout <= 0 {
		idleTimeout = sandbox.DefaultIdleTimeout
	}
	watchdogDone := make(chan struct{})
	defer close(watchdogDone)
	var stallMu sync.Mutex
	var stallReason string
	go watchForStall(watchdogDone, activity, loopDet, idleTimeout, 5*time.Second, func(reason string) {
		stallMu.Lock()
		stallReason = reason
		stallMu.Unlock()
		_, _ = r.client.ContainerKill(context.Background(), containerID, client.ContainerKillOptions{Signal: "SIGKILL"})
	})

	waitResp := r.client.ContainerWait(ctx, containerID, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
	waitCh, errCh := waitResp.Result, waitResp.Error
	var statusCode int64
	select {
	case err := <-errCh:
		if err != nil {
			return nil, fmt.Errorf("wait for docker container: %w", err)
		}
	case waitResp := <-waitCh:
		statusCode = waitResp.StatusCode
	case <-ctx.Done():
		// Context cancellation (e.g. a user cancelling the job from a UI) must reach
		// and kill the container immediately, not just stop polling it: the
		// deferred ContainerStop below still runs, but it's a graceful
		// SIGTERM-then-wait-5s-then-SIGKILL — an explicit SIGKILL here skips
		// that grace period so a stuck/looping agent dies instantly instead
		// of continuing to run (and burn tokens/cost) for another 5s after
		// the user asked it to stop. Best-effort: ContainerKill errors are
		// swallowed since ctx.Err() is already the error being returned, and
		// the deferred ContainerStop/ContainerRemove are the backstop either
		// way.
		_, _ = r.client.ContainerKill(context.Background(), containerID, client.ContainerKillOptions{Signal: "SIGKILL"})
		return nil, ctx.Err()
	}

	// The container has stopped; the log stream should reach EOF on its own
	// shortly after (Follow:true ends once the container's output is fully
	// drained). Give it a bounded grace period rather than blocking forever
	// on a stream that, in principle, could hang.
	select {
	case <-logsErrCh:
	case <-time.After(5 * time.Second):
	case <-ctx.Done():
	}

	var updatedCredentialFiles map[string]string
	for targetContainerPath, hostPath := range credentialHostPaths {
		data, err := readRegularFile(hostPath)
		if err != nil {
			continue
		}
		if string(data) != req.CredentialFiles[targetContainerPath] {
			if updatedCredentialFiles == nil {
				updatedCredentialFiles = make(map[string]string)
			}
			updatedCredentialFiles[targetContainerPath] = string(data)
		}
	}

	stallMu.Lock()
	killed := stallReason != ""
	killReason := stallReason
	stallMu.Unlock()

	// stallReason only covers kills our own watchForStall watchdog
	// initiated. A container can also die from an external SIGKILL — most
	// commonly the kernel/cgroup OOM killer enforcing r.config.MemoryBytes —
	// which surfaces only as statusCode 137 via ContainerWait with no
	// stallReason set. Docker's inspect API carries the authoritative
	// OOMKilled flag for exactly this case, so check it rather than
	// guessing from the exit code alone.
	if !killed && statusCode == 137 {
		if inspect, err := r.client.ContainerInspect(context.Background(), containerID, client.ContainerInspectOptions{}); err == nil && inspect.Container.State != nil && inspect.Container.State.OOMKilled {
			killed = true
			killReason = sandbox.KillReasonOOM
		}
	}

	return &sandbox.CommandResult{
		ExitCode:               int(statusCode),
		Stdout:                 strings.TrimSpace(stdout.String()),
		Stderr:                 strings.TrimSpace(stderr.String()),
		UpdatedCredentialFiles: updatedCredentialFiles,
		Killed:                 killed,
		KillReason:             killReason,
	}, nil
}

// streamContainerLogs attaches to containerID's log stream with Follow:true
// and copies it into in-memory stdout/stderr buffers as it arrives — plus,
// if logFilePath is non-empty, into a real-time combined on-disk copy via
// io.MultiWriter. The returned error
// channel receives the copy goroutine's terminal error (nil on clean EOF)
// exactly once; the returned closer must be deferred by the caller to stop
// the stream and release the log file handle.
// streamContainerLogs additionally returns the activityWriter/lineLoopDetector
// that observed every byte
// written to either stream, so Run's watchdog can poll them without a second
// pass over the output.
func (r *Runtime) streamContainerLogs(ctx context.Context, containerID, logFilePath string) (*bytes.Buffer, *bytes.Buffer, chan error, func(), *activityWriter, *lineLoopDetector, error) {
	logReader, err := r.client.ContainerLogs(ctx, containerID, client.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     true,
	})
	if err != nil {
		return nil, nil, nil, nil, nil, nil, fmt.Errorf("read docker container logs: %w", err)
	}

	var stdoutBuf, stderrBuf bytes.Buffer
	var stdoutW, stderrW io.Writer = &stdoutBuf, &stderrBuf

	var logFile *os.File
	if logFilePath != "" {
		if err := os.MkdirAll(filepath.Dir(logFilePath), 0o755); err != nil {
			logReader.Close()
			return nil, nil, nil, nil, nil, nil, fmt.Errorf("create run log dir: %w", err)
		}
		// O_APPEND, not O_TRUNC: a retried run reusing the same log file
		// path (e.g. task retry re-running the same step) should not erase
		// the previous attempt's trace, matching the checkpoint/artifact
		// append conventions used elsewhere in this package.
		logFile, err = os.OpenFile(logFilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			logReader.Close()
			return nil, nil, nil, nil, nil, nil, fmt.Errorf("open run log file: %w", err)
		}
		stdoutW = io.MultiWriter(&stdoutBuf, logFile)
		stderrW = io.MultiWriter(&stderrBuf, logFile)
	}

	// activityWriter/lineLoopDetector both wrap the already-composed
	// stdout/stderr writers so they observe exactly what's buffered/logged,
	// then feed watchForStall in Run — a single shared pair across stdout
	// and stderr, since a hallucinating agent's repeated error can land on
	// either stream depending on the CLI.
	activity := newActivityWriter(io.Discard)
	loopDet := newLineLoopDetector(io.Discard)
	stdoutW = io.MultiWriter(stdoutW, activity, loopDet)
	stderrW = io.MultiWriter(stderrW, activity, loopDet)

	errCh := make(chan error, 1)
	go func() {
		_, copyErr := stdcopy.StdCopy(stdoutW, stderrW, logReader)
		errCh <- copyErr
	}()

	closer := func() {
		_ = logReader.Close()
		if logFile != nil {
			_ = logFile.Close()
		}
	}
	return &stdoutBuf, &stderrBuf, errCh, closer, activity, loopDet, nil
}

// RunInteractive runs req attached to a PTY, streaming stdin and output until the process exits
// or ctx ends. Only the workspace is mounted (at /workspace); it is meant for login sessions.
func (r *Runtime) RunInteractive(ctx context.Context, req sandbox.CommandRequest, stdin io.Reader, stdout, stderr io.Writer) error {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "sandbox.docker.runInteractive")
	defer span.End()
	resolvedNetworkMode := r.resolveNetworkMode(req.NetworkMode)
	if err := sandbox.ValidateRequest(req, resolvedNetworkMode); err != nil {
		return err
	}
	if len(req.Command) == 0 {
		return fmt.Errorf("docker command is required")
	}

	workspace := req.Workspace
	if workspace == "" {
		workspace = sandbox.WorkspacePath(r.config.WorkspaceRoot, req.TaskID)
	}
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		return fmt.Errorf("create sandbox workspace: %w", err)
	}

	envMap := req.MergedEnv()
	mounts := []mount.Mount{
		{
			Type:   mount.TypeBind,
			Source: workspace,
			Target: "/workspace",
		},
	}
	env := make([]string, 0, len(envMap))
	for key, value := range envMap {
		env = append(env, key+"="+value)
	}
	networkMode := container.NetworkMode(resolvedNetworkMode)

	createResp, err := r.client.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{
			Image:        r.resolveImage(req.Image),
			Cmd:          req.Command,
			Env:          env,
			WorkingDir:   "/workspace",
			Tty:          true,
			OpenStdin:    true,
			AttachStdin:  true,
			AttachStdout: true,
			AttachStderr: true,
		},
		HostConfig: &container.HostConfig{
			NetworkMode: networkMode,
			Resources: container.Resources{
				Memory:   r.config.MemoryBytes,
				NanoCPUs: r.config.NanoCPUs,
			},
			Mounts: mounts,
		},
	})
	if err != nil {
		return fmt.Errorf("create interactive docker container: %w", err)
	}
	containerID := createResp.ID
	defer func() {
		timeout := 5
		_, _ = r.client.ContainerStop(context.Background(), containerID, client.ContainerStopOptions{Timeout: &timeout})
		_, _ = r.client.ContainerRemove(context.Background(), containerID, client.ContainerRemoveOptions{Force: true})
	}()

	attachResp, err := r.client.ContainerAttach(ctx, containerID, client.ContainerAttachOptions{
		Stream: true,
		Stdin:  true,
		Stdout: true,
		Stderr: true,
	})
	if err != nil {
		return fmt.Errorf("attach to docker container: %w", err)
	}
	defer attachResp.Close()

	// Connect streams
	go func() {
		_, _ = io.Copy(attachResp.Conn, stdin)
	}()
	// Tty=true merges stdout and stderr, but we can copy it to both just in case
	stdoutDone := make(chan struct{})
	go func() {
		defer close(stdoutDone)
		_, _ = io.Copy(stdout, attachResp.Reader)
	}()

	if _, err := r.client.ContainerStart(ctx, containerID, client.ContainerStartOptions{}); err != nil {
		return fmt.Errorf("start docker container: %w", err)
	}

	if req.ResizeCh != nil {
		go func() {
			for {
				select {
				case size, ok := <-req.ResizeCh:
					if !ok {
						return
					}
					_, _ = r.client.ContainerResize(ctx, containerID, client.ContainerResizeOptions{
						Height: size.Rows,
						Width:  size.Cols,
					})
				case <-ctx.Done():
					return
				}
			}
		}()
	}

	waitResp := r.client.ContainerWait(ctx, containerID, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
	waitCh, waitErrCh := waitResp.Result, waitResp.Error
	var waitErr error
	select {
	case err := <-waitErrCh:
		if err != nil {
			waitErr = fmt.Errorf("wait for docker container: %w", err)
		}
	case <-waitCh:
		// Completed
	case <-ctx.Done():
		return ctx.Err()
	}

	// The container has stopped, but attachResp.Reader may still have buffered
	// output in flight (e.g. the final line printed right before exit). Wait
	// for the copy goroutine to drain it before the deferred attachResp.Close()
	// tears down the stream out from under it, otherwise trailing output is lost.
	select {
	case <-stdoutDone:
	case <-time.After(2 * time.Second):
	case <-ctx.Done():
	}

	return waitErr
}

// maxCredentialReadBytes bounds a credential file read back from a session directory.
const maxCredentialReadBytes = 1 << 20

// readRegularFile reads a file the sandboxed process could have modified. A process inside the
// container controls these directories, so it can replace a credential file with a symlink to a
// host file; reading through it would hand that host file's content to the caller as a
// "refreshed credential". Only plain files up to maxCredentialReadBytes are read.
func readRegularFile(p string) ([]byte, error) {
	info, err := os.Lstat(p)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", p)
	}
	if info.Size() > maxCredentialReadBytes {
		return nil, fmt.Errorf("%s exceeds %d bytes", p, maxCredentialReadBytes)
	}
	return os.ReadFile(p)
}
