package cli

import (
	"context"
	"fmt"

	"composefile/internal/manifest"
	"composefile/internal/status"
)

// runStatus inspects and reports the health of manifest stacks.
func runStatus(ctx context.Context, env Env, args []string) int {
	if len(args) > 0 {
		fmt.Fprintln(env.Stderr, "composefile: status takes no arguments")
		return ExitError
	}
	m, err := manifest.Load(env.WorkDir)
	if err != nil {
		fmt.Fprintf(env.Stderr, "composefile: %v\n", err)
		return ExitError
	}
	if err := status.Run(ctx, m, env.Stdout); err != nil {
		fmt.Fprintf(env.Stderr, "composefile: status: %v\n", err)
		return ExitError
	}
	return ExitOK
}
