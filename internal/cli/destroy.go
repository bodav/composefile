package cli

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"composefile/internal/compose"
	"composefile/internal/deploy"
	"composefile/internal/manifest"
	"composefile/internal/remote"
)

// runDestroy stops every managed stack and removes the remote deployment state.
func runDestroy(ctx context.Context, env Env, args []string) int {
	if len(args) > 0 {
		fmt.Fprintln(env.Stderr, "composefile: destroy takes no arguments")
		return ExitError
	}
	m, err := manifest.Load(env.WorkDir)
	if err != nil {
		fmt.Fprintf(env.Stderr, "composefile: %v\n", err)
		return ExitError
	}
	return destroyWithSession(ctx, m, remote.New(m.Target), env.Stdout, env.Stderr)
}

// destroyWithSession tears down m through sess. It stops every stack (managed
// and orphaned) and, only if every stack was stopped cleanly, removes the whole
// remote deployment root. On any failure the root is left intact so state stays
// recoverable and the operation is retryable.
func destroyWithSession(ctx context.Context, m *manifest.Manifest, sess *remote.Session, out, errOut io.Writer) int {
	root, err := deploy.ExpandRoot(ctx, sess, m.Defaults.RemoteRoot, m.Name)
	if err != nil {
		fmt.Fprintf(errOut, "composefile: destroy: %v\n", err)
		return ExitError
	}

	managed := make(map[string]*manifest.Stack, len(m.Stacks))
	for i := range m.Stacks {
		managed[m.Stacks[i].Name] = &m.Stacks[i]
	}
	stacks, err := stacksToDestroy(ctx, sess, root, managed)
	if err != nil {
		fmt.Fprintf(errOut, "composefile: destroy: %v\n", err)
		return ExitError
	}
	if len(stacks) == 0 {
		fmt.Fprintln(out, "nothing to destroy")
		return ExitOK
	}

	var failed []string
	for i, name := range stacks {
		fmt.Fprintf(out, "\n== destroying stack %q (%d/%d) ==\n", name, i+1, len(stacks))
		if err := stopStack(ctx, sess, root, name, managed[name]); err != nil {
			fmt.Fprintf(errOut, "failed to stop %q: %v\n", name, err)
			failed = append(failed, name)
			continue
		}
		fmt.Fprintf(out, "stopped %s\n", name)
	}
	if len(failed) > 0 {
		fmt.Fprintf(errOut, "composefile: destroy aborted (%v failed to stop); remote state left intact\n", strings.Join(failed, ", "))
		return ExitError
	}

	for _, name := range stacks {
		pruneOut, errb, err := sess.Exec(ctx, compose.NetworkPrune(name))
		if err != nil {
			fmt.Fprintf(errOut, "warning: prune networks for %q: %v%s\n", name, err, errb)
		} else if pruneOut = strings.TrimSpace(pruneOut); pruneOut != "" {
			fmt.Fprintf(out, "pruned leftover networks for %s:\n%s\n", name, pruneOut)
		}
	}

	if err := sess.RemoveAll(ctx, root); err != nil {
		fmt.Fprintf(errOut, "composefile: destroy: remove remote root: %v\n", err)
		return ExitError
	}
	fmt.Fprintf(out, "removed remote deployment state: %s\n", root)
	fmt.Fprintln(out, "note: local ./.bundle, named volumes, and images were left untouched")
	return ExitOK
}

// stacksToDestroy returns the ordered set of stack names to tear down: manifest
// stacks plus any orphaned stacks tracked in metadata/stacks. It is empty when
// the metadata tree is absent.
func stacksToDestroy(ctx context.Context, sess *remote.Session, root string, managed map[string]*manifest.Stack) ([]string, error) {
	metaDir := deploy.StackMetaDir(root)
	exists, err := sess.Exists(ctx, metaDir)
	if err != nil {
		return nil, fmt.Errorf("check %s: %w", metaDir, err)
	}
	if !exists {
		return nil, nil
	}
	names := make([]string, 0, len(managed))
	seen := make(map[string]bool, len(managed))
	for name := range managed {
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	out, errb, err := sess.Exec(ctx, "ls -1 "+remote.Quote(metaDir))
	if err != nil {
		return nil, fmt.Errorf("list %s: %w%s", metaDir, err, errb)
	}
	for _, line := range strings.Split(out, "\n") {
		n := strings.TrimSuffix(strings.TrimSpace(line), filepath.Ext(line))
		if n == "" || n == "deployment" || seen[n] {
			continue
		}
		seen[n] = true
		names = append(names, n)
	}
	return names, nil
}

// stopStack stops one stack. Managed stacks use their exact workspace compose
// files; orphan stacks fall back to default-file discovery in the workspace.
func stopStack(ctx context.Context, sess *remote.Session, root, name string, s *manifest.Stack) error {
	var inner string
	if s != nil {
		ws := deploy.WorkspacePath(root, name)
		inner = compose.New(name, deploy.WorkspaceComposeFiles(ws, s)...).Down()
	} else {
		ws := deploy.WorkspacePath(root, name)
		exists, err := sess.Exists(ctx, ws)
		if err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("workspace %s is missing", ws)
		}
		inner = "cd " + remote.Quote(ws) + " && docker compose --project-name " + remote.Quote(name) + " down"
	}
	_, errb, err := sess.Exec(ctx, inner)
	if err != nil {
		return fmt.Errorf("%s%s", err, errb)
	}
	return nil
}
