// Command aicli-tools installs and switches the agent CLIs that sandboxes mount from the host
// (package toolchain).
//
//	aicli-tools setup                              all tools, latest, made current
//	aicli-tools install agy                        latest, made current
//	aicli-tools install claude 2.1.312 --no-use    installed only; current stays
//	aicli-tools use claude 2.1.312
//	aicli-tools reset agy
//	aicli-tools list
//	aicli-tools remove codex 0.163.0
//
// The root is --root, else AICLI_TOOLS_ROOT, else ~/.aicli/tools. On a server use a system path,
// for example --root /var/lib/aicli/tools.
// Installing runs a container from --image (the image your sandboxes use) with network access.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/baby-hero-404/aicli/sandbox"
	dockerrt "github.com/baby-hero-404/aicli/sandbox/docker"
	"github.com/baby-hero-404/aicli/toolchain"
)

const usage = `usage: aicli-tools [--root DIR] [--image IMAGE] <command> [args]

commands:
  setup [--keep N]                          install latest version of all tools, make current, and prune (default: keep 3)
  prune [--keep N] [tool]                   prune versions older than the N most recent (default: keep 3)
  install <tool> [version] [--no-use]       install a version (default: latest); makes current unless --no-use
  use <tool> <version>                      make an installed version current
  rollback <tool>                           go back to the version used before the last switch; the one left is marked bad
  unbad <tool> <version>                    clear the bad mark a rollback put on a version (then switch to it with use)
  reset <tool>                              clear current: runs use the CLI in the image again
  list                                      installed versions; * marks current
  remove <tool> <version>                   delete a version that is not current

root: --root, else $AICLI_TOOLS_ROOT, else ~/.aicli/tools (servers: --root /var/lib/aicli/tools)
tools: ` + "agy, claude, codex" + `
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, newDocker); err != nil {
		fmt.Fprintln(os.Stderr, "aicli-tools:", err)
		os.Exit(1)
	}
}

func newDocker() (sandbox.Runtime, error) { return dockerrt.New(dockerrt.Config{}) }

// run is main without the process: testable with a fake runtime.
func run(ctx context.Context, args []string, out io.Writer, newRuntime func() (sandbox.Runtime, error)) error {
	fs := flag.NewFlagSet("aicli-tools", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	root := fs.String("root", os.Getenv("AICLI_TOOLS_ROOT"), "tools root directory (absolute)")
	image := fs.String("image", "", "sandbox image to install with (default: the runtime's default)")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w\n%s", err, usage)
	}
	rest := fs.Args()
	if len(rest) == 0 {
		return errors.New("missing command\n" + usage)
	}
	store, err := toolchain.Open(*root)
	if err != nil {
		return err
	}
	cmd, params := rest[0], rest[1:]
	switch cmd {
	case "setup":
		keep := toolchain.DefaultKeepVersions
		for i := 0; i < len(params); i++ {
			if params[i] == "--keep" && i+1 < len(params) {
				if n, err := strconv.Atoi(params[i+1]); err == nil && n > 0 {
					keep = n
				}
				i++
			}
		}
		rt, err := newRuntime()
		if err != nil {
			return err
		}
		in := &toolchain.Installer{Store: store, Runtime: rt, Image: *image}
		for _, tool := range toolchain.Tools() {
			v, err := in.Install(ctx, tool, toolchain.Latest)
			if err != nil {
				return fmt.Errorf("install %s: %w", tool, err)
			}
			fmt.Fprintf(out, "installed %s %s\n", tool, v)
			if err := store.Use(tool, v); err != nil {
				return fmt.Errorf("use %s %s: %w", tool, v, err)
			}
			fmt.Fprintf(out, "current %s %s\n", tool, v)
		}
		pruned, err := store.PruneAll(keep)
		if err != nil {
			return fmt.Errorf("prune: %w", err)
		}
		for _, tool := range toolchain.Tools() {
			if vers := pruned[tool]; len(vers) > 0 {
				fmt.Fprintf(out, "pruned %s: %s\n", tool, strings.Join(vers, ", "))
			}
		}
		return nil
	case "prune":
		keep := toolchain.DefaultKeepVersions
		var toolTarget string
		for i := 0; i < len(params); i++ {
			p := params[i]
			if p == "--keep" && i+1 < len(params) {
				if n, err := strconv.Atoi(params[i+1]); err == nil && n > 0 {
					keep = n
				}
				i++
				continue
			}
			if !strings.HasPrefix(p, "-") && toolTarget == "" {
				toolTarget = p
			}
		}
		if toolTarget != "" {
			removed, err := store.Prune(toolTarget, keep)
			if err != nil {
				return err
			}
			if len(removed) > 0 {
				fmt.Fprintf(out, "pruned %s: %s\n", toolTarget, strings.Join(removed, ", "))
			} else {
				fmt.Fprintf(out, "nothing to prune for %s (keeping %d)\n", toolTarget, keep)
			}
			return nil
		}
		pruned, err := store.PruneAll(keep)
		if err != nil {
			return err
		}
		if len(pruned) == 0 {
			fmt.Fprintf(out, "nothing to prune (keeping %d versions)\n", keep)
		} else {
			for _, tool := range toolchain.Tools() {
				if vers := pruned[tool]; len(vers) > 0 {
					fmt.Fprintf(out, "pruned %s: %s\n", tool, strings.Join(vers, ", "))
				}
			}
		}
		return nil
	case "install":
		use := true
		var pos []string
		for _, p := range params {
			if p == "--no-use" {
				use = false
				continue
			}
			if p == "--use" { // the default since install activates; accepted for old scripts
				use = true
				continue
			}
			pos = append(pos, p)
		}
		if len(pos) < 1 || len(pos) > 2 {
			return errors.New("usage: install <tool> [version|latest] [--no-use]")
		}
		version := toolchain.Latest
		if len(pos) == 2 {
			version = pos[1]
		}
		rt, err := newRuntime()
		if err != nil {
			return err
		}
		in := &toolchain.Installer{Store: store, Runtime: rt, Image: *image}
		v, err := in.Install(ctx, pos[0], version)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "installed %s %s\n", pos[0], v)
		if use {
			if err := store.Use(pos[0], v); err != nil {
				return err
			}
			fmt.Fprintf(out, "current %s %s\n", pos[0], v)
		}
		return nil
	case "use":
		if len(params) != 2 {
			return errors.New("usage: use <tool> <version>")
		}
		if err := store.Use(params[0], params[1]); err != nil {
			return err
		}
		fmt.Fprintf(out, "current %s %s\n", params[0], params[1])
		return nil
	case "rollback":
		if len(params) != 1 {
			return errors.New("usage: rollback <tool>")
		}
		ev, err := store.Rollback(params[0], "rolled back by hand")
		if err != nil {
			return err
		}
		if ev.To == "" {
			fmt.Fprintf(out, "rolled back %s: %s marked bad, the image's CLI is used again\n", ev.Tool, ev.From)
		} else {
			fmt.Fprintf(out, "rolled back %s: %s marked bad, current %s\n", ev.Tool, ev.From, ev.To)
		}
		return nil
	case "unbad":
		if len(params) != 2 {
			return errors.New("usage: unbad <tool> <version>")
		}
		if err := store.ClearBad(params[0], params[1]); err != nil {
			return err
		}
		fmt.Fprintf(out, "%s %s is no longer marked bad; switch to it with: aicli-tools use %s %s\n", params[0], params[1], params[0], params[1])
		return nil
	case "reset":
		if len(params) != 1 {
			return errors.New("usage: reset <tool>")
		}
		if err := store.Unuse(params[0]); err != nil {
			return err
		}
		fmt.Fprintf(out, "current %s cleared: the image's CLI is used\n", params[0])
		return nil
	case "remove":
		if len(params) != 2 {
			return errors.New("usage: remove <tool> <version>")
		}
		if err := store.Remove(params[0], params[1]); err != nil {
			return err
		}
		fmt.Fprintf(out, "removed %s %s\n", params[0], params[1])
		return nil
	case "list":
		for _, tool := range toolchain.Tools() {
			versions, err := store.Versions(tool)
			if err != nil {
				return err
			}
			cur, err := store.Current(tool)
			if err != nil {
				return err
			}
			bad, err := store.BadVersions(tool)
			if err != nil {
				return err
			}
			marked := make([]string, len(versions))
			for i, v := range versions {
				if v == cur {
					v = "*" + v
				}
				if _, isBad := bad[versions[i]]; isBad {
					v += "(bad)"
				}
				marked[i] = v
			}
			switch {
			case len(marked) == 0:
				marked = []string{"(none installed: the image's CLI is used)"}
			case cur == "":
				marked = append(marked, "(no current: the image's CLI is used)")
			}
			fmt.Fprintf(out, "%-7s %s\n", tool, strings.Join(marked, " "))
		}
		return nil
	}
	return fmt.Errorf("unknown command %q\n%s", cmd, usage)
}
