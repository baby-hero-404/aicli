// Command aicli-tools installs and switches the agent CLIs that sandboxes mount from the host
// (package toolchain).
//
//	aicli-tools --root /srv/aicli-tools install agy latest --use
//	aicli-tools --root /srv/aicli-tools use claude 2.1.312
//	aicli-tools --root /srv/aicli-tools reset agy
//	aicli-tools --root /srv/aicli-tools list
//	aicli-tools --root /srv/aicli-tools remove codex 0.163.0
//
// The root can also come from AICLI_TOOLS_ROOT. Installing runs a container from --image (the
// image your sandboxes use) with network access.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/baby-hero-404/aicli/sandbox"
	dockerrt "github.com/baby-hero-404/aicli/sandbox/docker"
	"github.com/baby-hero-404/aicli/toolchain"
)

const usage = `usage: aicli-tools [--root DIR] [--image IMAGE] <command> [args]

commands:
  install <tool> [version|latest] [--use]   install a version (default latest); --use makes it current
  use <tool> <version>                      make an installed version current (also: roll back)
  reset <tool>                              clear current: runs use the CLI in the image again
  list                                      installed versions; * marks current
  remove <tool> <version>                   delete a version that is not current

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
	if *root == "" {
		return errors.New("--root (or AICLI_TOOLS_ROOT) is required")
	}
	store, err := toolchain.Open(*root)
	if err != nil {
		return err
	}
	cmd, params := rest[0], rest[1:]
	switch cmd {
	case "install":
		use := false
		var pos []string
		for _, p := range params {
			if p == "--use" {
				use = true
				continue
			}
			pos = append(pos, p)
		}
		if len(pos) < 1 || len(pos) > 2 {
			return errors.New("usage: install <tool> [version|latest] [--use]")
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
			marked := make([]string, len(versions))
			for i, v := range versions {
				if v == cur {
					v = "*" + v
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
