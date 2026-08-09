package cli

import (
	"context"
	"fmt"
	"path/filepath"

	"composefile/internal/bundle"
	"composefile/internal/manifest"
)

// runBundle validates the manifest and writes a retained tar.gz to ./.bundle.
func runBundle(ctx context.Context, env Env, args []string) int {
	if len(args) > 0 {
		fmt.Fprintln(env.Stderr, "composefile: bundle takes no arguments")
		return ExitError
	}
	m, err := manifest.Load(env.WorkDir)
	if err != nil {
		fmt.Fprintf(env.Stderr, "composefile: %v\n", err)
		return ExitError
	}
	outDir := filepath.Join(m.Dir, bundle.DefaultBundleDir)
	path, err := bundle.Build(m, outDir)
	if err != nil {
		fmt.Fprintf(env.Stderr, "composefile: %v\n", err)
		return ExitError
	}
	fmt.Fprintf(env.Stdout, "Created %s\n", path)
	return ExitOK
}
