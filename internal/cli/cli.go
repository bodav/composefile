// Package cli routes subcommands and owns process I/O and exit codes.
package cli

import (
	"context"
	"fmt"
	"io"
	"os"
)

// Exit codes used by every command.
const (
	ExitOK    = 0
	ExitError = 1
)

// version is set from main via SetVersion.
var version = "dev"

// SetVersion records the build-time version for --version output.
func SetVersion(v string) { version = v }

// Env carries the process environment for a single invocation.
type Env struct {
	Stdout io.Writer
	Stderr io.Writer
	Stdin  io.Reader
	// WorkDir is the directory composefile operates in; defaults to the
	// process working directory.
	WorkDir string
}

// NewEnv builds an Env from the real process.
func NewEnv() Env {
	wd, err := os.Getwd()
	if err != nil {
		wd = "."
	}
	return Env{
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
		Stdin:   os.Stdin,
		WorkDir: wd,
	}
}

// Run dispatches a subcommand and returns the process exit code.
func Run(ctx context.Context, env Env, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(env.Stderr, usage())
		return ExitError
	}

	switch args[0] {
	case "init":
		return runInit(ctx, env, args[1:])
	case "bundle":
		return runBundle(ctx, env, args[1:])
	case "apply":
		return runApply(ctx, env, args[1:])
	case "status":
		return runStatus(ctx, env, args[1:])
	case "diff":
		return runDiff(ctx, env, args[1:])
	case "purge":
		return runPurge(ctx, env, args[1:])
	case "destroy":
		return runDestroy(ctx, env, args[1:])
	case "help", "-h", "--help":
		fmt.Fprintln(env.Stdout, usage())
		return ExitOK
	case "version", "-v", "--version":
		fmt.Fprintf(env.Stdout, "composefile %s\n", version)
		return ExitOK
	default:
		fmt.Fprintf(env.Stderr, "composefile: unknown command %q\n\n%s", args[0], usage())
		return ExitError
	}
}

func usage() string {
	return `composefile - deploy Docker Compose stacks over SSH

Usage:
  composefile init                    create a starter composefile.yaml
  composefile bundle                  create ./.bundle/<timestamp>-<name>.tar.gz
  composefile apply [--all] [--bundle FILE]   preflight and deploy changed stacks
  composefile status                  report stack health
  composefile diff                    compare a new bundle with the deployed one
  composefile purge                   delete all retained bundles in ./.bundle
  composefile destroy                 stop all stacks and remove remote deployment state

Exit status:
  0  success
  1  usage, validation, SSH, remote, health, or deployment failure
`
}
