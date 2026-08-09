package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"composefile/internal/bundle"
	"composefile/internal/manifest"
)

// runPurge removes every retained bundle in ./.bundle. It is fully local and
// never touches the remote host.
func runPurge(ctx context.Context, env Env, args []string) int {
	if len(args) > 0 {
		fmt.Fprintln(env.Stderr, "composefile: purge takes no arguments")
		return ExitError
	}
	m, err := manifest.Load(env.WorkDir)
	if err != nil {
		fmt.Fprintf(env.Stderr, "composefile: %v\n", err)
		return ExitError
	}

	bundleDir := filepath.Join(m.Dir, bundle.DefaultBundleDir)
	if err := validateBundleDir(bundleDir); err != nil {
		fmt.Fprintf(env.Stderr, "composefile: %v\n", err)
		return ExitError
	}

	entries, err := os.ReadDir(bundleDir)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintln(env.Stdout, "nothing to purge (no ./.bundle)")
			return ExitOK
		}
		fmt.Fprintf(env.Stderr, "composefile: read %s: %v\n", bundleDir, err)
		return ExitError
	}

	count := 0
	for _, e := range entries {
		if !e.IsDir() && hasBundleSuffix(e.Name()) {
			count++
		}
	}

	if err := os.RemoveAll(bundleDir); err != nil {
		fmt.Fprintf(env.Stderr, "composefile: purge %s: %v\n", bundleDir, err)
		return ExitError
	}

	fmt.Fprintf(env.Stdout, "Purged %d bundle(s) from %s\n", count, bundleDir)
	if count > 0 {
		fmt.Fprintln(env.Stdout, "note: the diff baseline was removed; run `composefile apply` to re-establish it")
	}
	return ExitOK
}

// hasBundleSuffix reports whether name looks like a retained bundle file.
func hasBundleSuffix(name string) bool {
	const ext = ".tar.gz"
	return len(name) > len(ext) && name[len(name)-len(ext):] == ext
}

// validateBundleDir refuses unsafe removal targets derived from the manifest.
func validateBundleDir(bundleDir string) error {
	if bundleDir == "" || bundleDir == string(os.PathSeparator) {
		return fmt.Errorf("refusing to purge an empty or root path")
	}
	return nil
}
