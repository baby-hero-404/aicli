// Command go-server is a minimal host for the conversation API: it mounts the HTTP handler,
// keeps conversations in memory and runs agy in Docker. It exists to show the three things a
// real host provides (authentication, authorization/storage, and how a turn is executed); it
// is NOT safe to expose as is. Identity here is a plain request header anyone can set.
//
//	go run ./examples/go-server -data ./.demo-data -credentials ./.demo-credentials
//	curl -H 'X-Demo-User: me' -H 'Content-Type: application/json' \
//	     -d '{"profile":"antigravity"}' localhost:8787/api/aicli/conversations
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/baby-hero-404/aicli/conversation"
	"github.com/baby-hero-404/aicli/conversation/memstore"
	"github.com/baby-hero-404/aicli/janitor"
	"github.com/baby-hero-404/aicli/run"
	"github.com/baby-hero-404/aicli/sandbox"
	dockerrt "github.com/baby-hero-404/aicli/sandbox/docker"
	"github.com/baby-hero-404/aicli/transport/httpapi"
)

// host is the application side of the contract: where a turn runs and with which credentials.
type host struct {
	dataDir string
	// credDir holds the CLI's login files laid out like HOME (for example .gemini/...). A real
	// host loads these from its own secret store, per account.
	credDir string
}

// Resolve implements conversation.Resolver. Everything here is decided by the server; nothing
// comes from the client.
func (h host) Resolve(_ context.Context, _ conversation.Principal, c conversation.Conversation) (run.Request, error) {
	workspace := filepath.Join(h.dataDir, "workspaces", c.ID)
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		return run.Request{}, fmt.Errorf("create workspace: %w", err)
	}
	// One session directory per conversation: the CLI keeps the session that later turns resume.
	session, err := sandbox.SessionDir(filepath.Join(h.dataDir, "sessions"), c.Profile, c.ID)
	if err != nil {
		return run.Request{}, err
	}
	creds, err := readFiles(h.credDir)
	if err != nil {
		return run.Request{}, err
	}
	return run.Request{
		Workspace: workspace, SessionDir: session, CredentialFiles: creds, TaskID: c.ID,
		// Egress stays on so the CLI can reach its model API.
		Timeout: 30 * time.Minute, IdleTimeout: 5 * time.Minute,
	}, nil
}

// SaveCredentials implements conversation.Resolver: a CLI that refreshed its token during a
// turn hands the new files back, and the next Resolve must return them.
func (h host) SaveCredentials(_ context.Context, _ conversation.Principal, _ conversation.Conversation, files map[string]string) error {
	if h.credDir == "" {
		return nil
	}
	for rel, content := range files {
		dst := filepath.Join(h.credDir, filepath.Clean("/"+rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(dst, []byte(content), 0o600); err != nil {
			return err
		}
	}
	return nil
}

func readFiles(dir string) (map[string]string, error) {
	if dir == "" {
		return nil, nil
	}
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if !d.Type().IsRegular() { // never follow symlinks out of the credentials directory
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)] = string(b)
		return nil
	})
	return out, err
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8787", "listen address (keep it on loopback: the demo auth is a header)")
	data := flag.String("data", ".demo-data", "directory for workspaces and CLI sessions")
	creds := flag.String("credentials", "", "directory with the CLI's login files, laid out like HOME (optional)")
	origins := flag.String("origin", "", "allowed browser Origin, for example http://localhost:3000 (optional)")
	flag.Parse()
	if err := runServer(*addr, *data, *creds, *origins); err != nil {
		log.Fatal(err)
	}
}

func runServer(addr, data, creds, origin string) error {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	docker, err := dockerrt.New(dockerrt.Config{WorkspaceRoot: filepath.Join(data, "workspaces")})
	if err != nil {
		return fmt.Errorf("docker runtime: %w", err)
	}
	svc, err := conversation.NewService(conversation.Config{
		Runner:     &run.Runner{Runtime: docker},
		Store:      memstore.New(),
		Authorizer: conversation.OwnerOnly{}, // each conversation belongs to its creator
		Resolver:   host{dataDir: data, credDir: creds},
		Profiles:   []string{"antigravity"},
		Logger:     logger,
	})
	if err != nil {
		return err
	}
	var allowed []string
	if origin != "" {
		allowed = []string{origin}
	}
	api, err := httpapi.NewHandler(httpapi.Config{
		Service: svc,
		Authenticate: func(r *http.Request) (conversation.Principal, error) {
			if u := r.Header.Get("X-Demo-User"); u != "" {
				return conversation.Principal(u), nil
			}
			return "", errors.New("missing X-Demo-User")
		},
		AllowedOrigins: allowed,
		AllowNoOrigin:  true, // curl sends no Origin
		Logger:         logger,
	})
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.Handle("/api/aicli/", http.StripPrefix("/api/aicli", api))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// Background housekeeping, no cron: turns whose owner died (for example after a crash) are marked
	// interrupted, orphaned sandbox containers are removed, and workspaces and CLI sessions that saw
	// no activity for 30 days are deleted.
	j := janitor.New(janitor.Config{
		Conversations: svc,
		Orphans:       docker,
		Filesystem: sandbox.JanitorConfig{
			WorkspacesRoot: filepath.Join(data, "workspaces"),
			SessionsRoot:   filepath.Join(data, "sessions"),
		},
		Logger: logger,
	})
	j.Start(ctx)

	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = svc.Close(shutdown) // interrupt running turns and record them
		_ = srv.Shutdown(shutdown)
	}()
	logger.Info("listening", "addr", addr)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
