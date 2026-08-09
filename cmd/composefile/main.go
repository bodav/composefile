// Command composefile deploys multiple Docker Compose projects to one remote
// Linux server over SSH.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"composefile/internal/cli"
)

// version is overridden at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cli.SetVersion(version)
	os.Exit(cli.Run(ctx, cli.NewEnv(), os.Args[1:]))
}
