// Package capture runs an interactive login for an AI coding CLI inside a sandbox and
// collects the credential files it writes. It is transport-agnostic: callers provide a
// Terminal (for example the WebSocket one in capture/wsgorilla).
package capture

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/baby-hero-404/aicli/sandbox"
)

const (
	// MaxFileBytes is the largest file collected from the session workspace. Credentials are
	// small; anything bigger is a cache or log and is skipped.
	MaxFileBytes = 1 << 20
	// MaxFiles bounds how many files a session may hand back.
	MaxFiles = 256
	// MaxTotalBytes bounds the combined size of the collected files.
	MaxTotalBytes = 8 << 20
)

// Terminal is the user's side of a session.
type Terminal interface {
	// Stdin streams the user's keystrokes into the sandbox PTY.
	Stdin() io.Reader
	// Stdout receives the PTY output.
	Stdout() io.Writer
	// Resize delivers terminal size changes; it may be nil.
	Resize() <-chan sandbox.TerminalSize
}

// Session runs capture sessions on a sandbox runtime.
type Session struct {
	// Runtime executes the interactive shell.
	Runtime sandbox.Runtime
	// WorkspacePrefix names the directory under os.TempDir() holding per-session workspaces.
	// Defaults to "aicli-capture".
	WorkspacePrefix string
	// Command is the program started in the sandbox. Defaults to /bin/bash.
	Command []string
	// Env is merged over the defaults TERM=xterm and HOME=/workspace. The session workspace
	// is mounted at /workspace, so files a CLI writes under $HOME are collected. Include the
	// profile's state environment (profile.Profile.Env("/workspace")) so captured files have
	// the same layout run uses.
	Env map[string]string
	// Image optionally overrides the runtime's default sandbox image.
	Image string
}

// Result is what a finished session produced.
type Result struct {
	// Files maps a path relative to the session HOME to the file's content.
	Files map[string]string
}

// Run executes one session: it creates a fresh workspace, runs the shell attached to term,
// and returns the regular files left in the workspace. The workspace is always removed.
func (s *Session) Run(ctx context.Context, term Terminal) (Result, error) {
	if s.Runtime == nil {
		return Result{}, errors.New("capture: Session.Runtime is required")
	}
	taskID, err := randomID()
	if err != nil {
		return Result{}, fmt.Errorf("capture: generate session id: %w", err)
	}
	prefix := s.WorkspacePrefix
	if prefix == "" {
		prefix = "aicli-capture"
	}
	dir := filepath.Join(os.TempDir(), prefix, taskID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Result{}, fmt.Errorf("capture: create workspace: %w", err)
	}
	defer os.RemoveAll(dir)

	command := s.Command
	if len(command) == 0 {
		command = []string{"/bin/bash"}
	}
	env := map[string]string{"TERM": "xterm", "HOME": "/workspace"}
	for k, v := range s.Env {
		env[k] = v
	}

	req := sandbox.CommandRequest{
		TaskID:      taskID,
		Workspace:   dir,
		Command:     command,
		NetworkMode: sandbox.NetworkModeBridge,
		Env:         env,
		Image:       s.Image,
		ResizeCh:    term.Resize(),
	}
	if err := s.Runtime.RunInteractive(ctx, req, term.Stdin(), term.Stdout(), term.Stdout()); err != nil {
		return Result{}, fmt.Errorf("capture: sandbox session: %w", err)
	}

	files, err := collectFiles(dir)
	if err != nil {
		return Result{}, fmt.Errorf("capture: collect files: %w", err)
	}
	return Result{Files: files}, nil
}

// collectFiles reads the regular files under root. Anything the sandboxed process could
// have planted to trick the host is ignored: symlinks (which would make the host read
// arbitrary files), devices, and oversized files. Count and total size are bounded too.
func collectFiles(root string) (map[string]string, error) {
	files := make(map[string]string)
	var total int64
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entry: skip it
		}
		if !d.Type().IsRegular() {
			return nil // directories are walked; symlinks, devices, sockets are ignored
		}
		info, err := d.Info()
		if err != nil || info.Size() > MaxFileBytes {
			return nil
		}
		if len(files) >= MaxFiles || total+info.Size() > MaxTotalBytes {
			return fs.SkipAll
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		files[filepath.ToSlash(rel)] = string(content)
		total += int64(len(content))
		return nil
	})
	return files, err
}

func randomID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
