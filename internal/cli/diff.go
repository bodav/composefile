package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"text/tabwriter"

	"composefile/internal/bundle"
	"composefile/internal/deploy"
	"composefile/internal/manifest"
	"composefile/internal/remote"
)

// runDiff builds a fresh bundle and reports what changed relative to the
// currently deployed bundle.
func runDiff(ctx context.Context, env Env, args []string) int {
	if len(args) > 0 {
		fmt.Fprintln(env.Stderr, "composefile: diff takes no arguments")
		return ExitError
	}
	m, err := manifest.Load(env.WorkDir)
	if err != nil {
		fmt.Fprintf(env.Stderr, "composefile: %v\n", err)
		return ExitError
	}
	outDir := filepath.Join(m.Dir, bundle.DefaultBundleDir)
	newPath, err := bundle.Build(m, outDir)
	if err != nil {
		fmt.Fprintf(env.Stderr, "composefile: %v\n", err)
		return ExitError
	}
	return runDiffWithSession(ctx, m, remote.New(m.Target), newPath, env.Stdout, env.Stderr)
}

// runDiffWithSession compares newPath against the deployed bundle discovered
// through sess. It returns ExitError when the bundles differ and ExitOK when
// they are identical.
func runDiffWithSession(ctx context.Context, m *manifest.Manifest, sess *remote.Session, newPath string, out, errOut io.Writer) int {
	oldPath, cleanup, err := deployedBundle(ctx, sess, m)
	if err != nil {
		fmt.Fprintf(errOut, "composefile: diff: %v\n", err)
		return ExitError
	}
	defer cleanup()

	changes, err := bundle.Compare(newPath, oldPath)
	if err != nil {
		fmt.Fprintf(errOut, "composefile: diff: %v\n", err)
		return ExitError
	}
	printChanges(out, changes)
	if len(changes) == 0 {
		fmt.Fprintln(out, "bundles are identical")
		return ExitOK
	}
	return ExitError
}

// deployedBundle locates the locally retained bundle that is currently deployed
// on the remote host. When the deployment set has never been deployed it
// returns a freshly built empty baseline so the whole set is reported as new.
func deployedBundle(ctx context.Context, sess *remote.Session, m *manifest.Manifest) (path string, cleanup func(), err error) {
	cleanup = func() {}
	root, err := deploy.ExpandRoot(ctx, sess, m.Defaults.RemoteRoot, m.Name)
	if err != nil {
		return "", cleanup, fmt.Errorf("resolve remote root: %w", err)
	}
	metaPath := deploy.DeploymentMetaPath(root)
	exists, err := sess.Exists(ctx, metaPath)
	if err != nil {
		return "", cleanup, fmt.Errorf("check deployment metadata: %w", err)
	}
	if !exists {
		dir, mkErr := os.MkdirTemp("", "composefile-diff-*")
		if mkErr != nil {
			return "", cleanup, fmt.Errorf("create baseline dir: %w", mkErr)
		}
		cleanup = func() { os.RemoveAll(dir) }
		empty, buildErr := bundle.Build(&manifest.Manifest{Name: m.Name}, dir)
		if buildErr != nil {
			return "", cleanup, fmt.Errorf("build empty baseline: %w", buildErr)
		}
		return empty, cleanup, nil
	}

	var meta deploy.DeployMeta
	if err := deploy.ReadJSON(ctx, sess, metaPath, &meta); err != nil {
		return "", cleanup, fmt.Errorf("read deployment metadata: %w", err)
	}
	if meta.Bundle == "" {
		return "", cleanup, errors.New("deployment metadata has no recorded bundle")
	}
	bundleDir := filepath.Join(m.Dir, bundle.DefaultBundleDir)
	oldPath := filepath.Join(bundleDir, meta.Bundle)
	if _, err := os.Stat(oldPath); err != nil {
		return "", cleanup, fmt.Errorf("deployed bundle %q is not retained locally (looked in %s); run composefile apply to redeploy", meta.Bundle, bundleDir)
	}
	return oldPath, cleanup, nil
}

// printChanges renders one row per change, grouped by stack.
func printChanges(out io.Writer, changes []bundle.Change) {
	if len(changes) == 0 {
		return
	}
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "STACK\tSTATUS\tFILE")
	counts := map[bundle.ChangeKind]int{}
	for _, c := range changes {
		fmt.Fprintf(w, "%s\t%s\t%s\n", c.Stack, c.Kind, c.Rel)
		counts[c.Kind]++
	}
	w.Flush()
	fmt.Fprintf(out, "%d file(s) differ (%d added, %d modified, %d deleted)\n",
		len(changes), counts[bundle.ChangeAdd], counts[bundle.ChangeModify], counts[bundle.ChangeDelete])
}
