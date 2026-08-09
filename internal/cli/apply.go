package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"composefile/internal/bundle"
	"composefile/internal/deploy"
	"composefile/internal/manifest"
)

// runApply preflights and deploys every stack in manifest order.
func runApply(ctx context.Context, env Env, args []string) int {
	var bundleArg string
	i := 0
	for i < len(args) {
		switch args[i] {
		case "--bundle":
			if i+1 >= len(args) {
				fmt.Fprintln(env.Stderr, "composefile: --bundle requires a path")
				return ExitError
			}
			bundleArg = args[i+1]
			i += 2
		case "--help", "-h":
			fmt.Fprintln(env.Stdout, "Usage: composefile apply [--bundle FILE]")
			return ExitOK
		default:
			fmt.Fprintf(env.Stderr, "composefile: unknown apply argument %q\n", args[i])
			return ExitError
		}
	}

	m, err := manifest.Load(env.WorkDir)
	if err != nil {
		fmt.Fprintf(env.Stderr, "composefile: %v\n", err)
		return ExitError
	}

	bundlePath := bundleArg
	if bundlePath == "" {
		outDir := filepath.Join(m.Dir, bundle.DefaultBundleDir)
		bundlePath, err = bundle.Build(m, outDir)
		if err != nil {
			fmt.Fprintf(env.Stderr, "composefile: %v\n", err)
			return ExitError
		}
	} else {
		if _, statErr := os.Stat(bundlePath); statErr != nil {
			fmt.Fprintf(env.Stderr, "composefile: bundle %s: %v\n", bundlePath, statErr)
			return ExitError
		}
		if err := bundle.Validate(bundlePath, m); err != nil {
			fmt.Fprintf(env.Stderr, "composefile: %v\n", err)
			return ExitError
		}
	}

	d := deploy.New(m, bundlePath, env.Stdout)
	if err := d.Run(ctx); err != nil {
		fmt.Fprintf(env.Stderr, "composefile: apply failed: %v\n", err)
		return ExitError
	}
	return ExitOK
}
