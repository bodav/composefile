// Package deploy orchestrates preflight, per-stack deployment, and completion
// for one manifest against one SSH target.
package deploy

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"composefile/internal/bundle"
	"composefile/internal/compose"
	"composefile/internal/manifest"
	"composefile/internal/remote"
)

// Deployer runs the full deployment for a loaded manifest and release bundle.
type Deployer struct {
	m          *manifest.Manifest
	sess       *remote.Session
	bundlePath string
	out        io.Writer
	root       string
}

// New builds a Deployer that connects via the real ssh executable.
func New(m *manifest.Manifest, bundlePath string, out io.Writer) *Deployer {
	return NewWithSession(m, remote.New(m.Target), bundlePath, out)
}

// NewWithSession builds a Deployer using an explicit remote session (tests).
func NewWithSession(m *manifest.Manifest, sess *remote.Session, bundlePath string, out io.Writer) *Deployer {
	return &Deployer{m: m, sess: sess, bundlePath: bundlePath, out: out}
}

// bundleName returns the archive filename (base name of bundlePath).
func (d *Deployer) bundleName() string { return filepath.Base(d.bundlePath) }

func (d *Deployer) bundleDir() string { return BundleDir(d.bundleName()) }

// Run preflights the whole deployment, deploys every stack in manifest order,
// and completes. Stops at the first failure.
func (d *Deployer) Run(ctx context.Context) error {
	if err := d.preflight(ctx); err != nil {
		d.cleanupStaging(ctx)
		return fmt.Errorf("preflight: %w", err)
	}
	fmt.Fprintf(d.out, "preflight passed for %d stack(s); deploying\n", len(d.m.Stacks))
	fmt.Fprintf(d.out, "deployment root: %s\n", d.root)

	for i := range d.m.Stacks {
		s := &d.m.Stacks[i]
		fmt.Fprintf(d.out, "\n== deploying stack %q (%d/%d) ==\n", s.Name, i+1, len(d.m.Stacks))
		if err := d.deployStack(ctx, s); err != nil {
			d.cleanupStaging(ctx)
			d.reportFailure(ctx, s, err)
			return err
		}
	}

	return d.complete(ctx)
}

// preflight performs every check before any running stack is changed.
func (d *Deployer) preflight(ctx context.Context) error {
	fmt.Fprintf(d.out, "preflight [1/5] checking remote prerequisites\n")
	if err := d.sess.Prereq(ctx); err != nil {
		return err
	}
	root, err := ExpandRoot(ctx, d.sess, d.m.Defaults.RemoteRoot, d.m.Name)
	if err != nil {
		return err
	}
	d.root = root
	fmt.Fprintf(d.out, "preflight [2/5] remote root: %s\n", root)

	fmt.Fprintf(d.out, "preflight [3/5] creating remote layout\n")
	for _, sub := range []string{metaSubdir, stackMetaSub, stagingSubdir, workspacesSub, logsSubdir} {
		if err := d.sess.MkdirAll(ctx, filepath.Join(root, sub)); err != nil {
			return fmt.Errorf("create remote layout: %w", err)
		}
	}

	for i := range d.m.Stacks {
		s := &d.m.Stacks[i]
		if err := d.refuseUnmanaged(ctx, s.Name); err != nil {
			return err
		}
	}
	fmt.Fprintf(d.out, "preflight [4/5] uploading and staging bundle %s\n", d.bundleName())
	archivePath := filepath.Join(StagingDir(root), d.bundleName())
	if err := d.sess.Upload(ctx, d.bundlePath, archivePath); err != nil {
		return fmt.Errorf("upload bundle: %w", err)
	}
	extractDir := filepath.Join(StagingDir(root), d.bundleDir())
	if err := d.sess.Extract(ctx, archivePath, extractDir); err != nil {
		return fmt.Errorf("stage bundle: %w", err)
	}

	fmt.Fprintf(d.out, "preflight [5/5] validating compose config for %d stack(s)\n", len(d.m.Stacks))
	for i := range d.m.Stacks {
		s := &d.m.Stacks[i]
		files := stagedComposeFiles(extractDir, s)
		if _, errb, err := d.sess.Exec(ctx, compose.New(s.Name, files...).ConfigQuiet()); err != nil {
			return fmt.Errorf("stack %q compose config: %w%s", s.Name, err, errb)
		}
	}
	return nil
}

// refuseUnmanaged blocks adopting a Compose project that has containers but no
// matching composefile metadata.
func (d *Deployer) refuseUnmanaged(ctx context.Context, stackName string) error {
	metaPath := StackMetaPath(d.root, stackName)
	hasMeta, err := d.sess.Exists(ctx, metaPath)
	if err != nil {
		return fmt.Errorf("check metadata for %q: %w", stackName, err)
	}
	if hasMeta {
		return nil
	}
	out, errb, err := d.sess.Exec(ctx, compose.New(stackName).PsQ())
	if err != nil {
		return fmt.Errorf("check project %q: %w%s", stackName, err, errb)
	}
	if strings.TrimSpace(out) != "" {
		return fmt.Errorf("project %q is running without composefile metadata; refusing to adopt an unmanaged project", stackName)
	}
	return nil
}

// stagedComposeFiles returns the staged absolute compose file paths.
func stagedComposeFiles(extractDir string, s *manifest.Stack) []string {
	var out []string
	for _, cp := range s.ComposeAbs {
		rel, _ := filepath.Rel(s.SourceAbs, cp)
		out = append(out, filepath.Join(extractDir, bundle.ArchiveDir, s.Name, rel))
	}
	return out
}

// StagedComposeFiles is the exported form of stagedComposeFiles.
func StagedComposeFiles(extractDir string, s *manifest.Stack) []string {
	return stagedComposeFiles(extractDir, s)
}

// workspaceComposeFiles returns the workspace absolute compose file paths.
func workspaceComposeFiles(ws string, s *manifest.Stack) []string {
	var out []string
	for _, cp := range s.ComposeAbs {
		rel, _ := filepath.Rel(s.SourceAbs, cp)
		out = append(out, filepath.Join(ws, rel))
	}
	return out
}

// WorkspaceComposeFiles is the exported form of workspaceComposeFiles.
func WorkspaceComposeFiles(ws string, s *manifest.Stack) []string {
	return workspaceComposeFiles(ws, s)
}

// deployStack stops, replaces, builds, starts, and records one stack.
func (d *Deployer) deployStack(ctx context.Context, s *manifest.Stack) error {
	ws := WorkspacePath(d.root, s.Name)
	bundleDir := d.bundleDir()
	logPath := filepath.Join(LogsDir(d.root), bundleDir, s.Name+".log")
	if err := d.sess.MkdirAll(ctx, filepath.Dir(logPath)); err != nil {
		return fmt.Errorf("create log dir: %w", err)
	}

	c := compose.New(s.Name, workspaceComposeFiles(ws, s)...)

	// Stop the current project only if it has running containers.
	proj, errb, err := d.sess.Exec(ctx, compose.New(s.Name).PsQ())
	if err != nil {
		return fmt.Errorf("check current project: %w%s", err, errb)
	}
	if strings.TrimSpace(proj) != "" {
		fmt.Fprintf(d.out, "  stopping existing project (%d container(s))\n", len(strings.Fields(proj)))
		if err := d.sess.Stream(ctx, c.Down(), logPath, d.out); err != nil {
			return fmt.Errorf("down: %w", err)
		}
		fmt.Fprintln(d.out, "  project stopped")
	} else {
		fmt.Fprintln(d.out, "  project not running; skipping down")
	}

	// Replace the fixed workspace with validated-path deletion (no marker).
	fmt.Fprintf(d.out, "  replacing workspace %s\n", ws)
	if err := remote.ValidatePath(ws); err != nil {
		return fmt.Errorf("workspace path: %w", err)
	}
	if err := d.sess.RemoveAll(ctx, ws); err != nil {
		return fmt.Errorf("remove workspace: %w", err)
	}
	if err := d.sess.MkdirAll(ctx, ws); err != nil {
		return fmt.Errorf("recreate workspace: %w", err)
	}

	stagedStack := filepath.Join(StagingDir(d.root), bundleDir, bundle.ArchiveDir, s.Name)
	fmt.Fprintln(d.out, "  copying stack source to workspace")
	copyCmd := "cp -a " + remote.Quote(stagedStack+"/.", ws) + "/"
	if err := d.sess.Run(ctx, copyCmd); err != nil {
		return fmt.Errorf("stage source: %w", err)
	}

	// Pull registry-only images, then build Dockerfile services, then start.
	fmt.Fprintln(d.out, "  pulling registry-only images")
	if err := d.sess.Stream(ctx, c.Pull(), logPath, d.out); err != nil {
		return fmt.Errorf("pull: %w", err)
	}
	needsBuild, errb, err := d.sess.Exec(ctx, c.HasBuild())
	if err != nil {
		return fmt.Errorf("check build steps: %w%s", err, errb)
	}
	if strings.TrimSpace(needsBuild) == "yes" {
		fmt.Fprintln(d.out, "  building Dockerfile services")
		if err := d.sess.Stream(ctx, c.Build(), logPath, d.out); err != nil {
			return fmt.Errorf("build: %w", err)
		}
	} else {
		fmt.Fprintln(d.out, "  no Dockerfile services to build; skipping build")
	}
	fmt.Fprintf(d.out, "  starting services (waiting up to %d s)\n", int(s.HealthTimeoutD.Seconds()))
	if err := d.sess.Stream(ctx, c.Up(int(s.HealthTimeoutD.Seconds())), logPath, d.out); err != nil {
		return fmt.Errorf("up: %w", err)
	}

	fmt.Fprintln(d.out, "  reading running services")
	svc, _, err := d.sess.Exec(ctx, c.Services())
	if err != nil {
		return fmt.Errorf("read services: %w", err)
	}
	meta := StackMeta{
		Stack:      s.Name,
		Bundle:     d.bundleName(),
		DeployedAt: time.Now().UTC(),
		Services:   strings.Fields(svc),
	}
	if err := writeJSON(ctx, d.sess, StackMetaPath(d.root, s.Name), meta); err != nil {
		return fmt.Errorf("write metadata: %w", err)
	}
	fmt.Fprintf(d.out, "  recorded services: %s\n", strings.Join(meta.Services, ", "))
	fmt.Fprintf(d.out, "deployed %s\n", s.Name)
	return nil
}

// complete records the last successful bundle, prunes if configured, and
// removes staging data.
func (d *Deployer) complete(ctx context.Context) error {
	fmt.Fprintln(d.out, "recording deployment metadata")
	meta := DeployMeta{Bundle: d.bundleName(), DeployedAt: time.Now().UTC()}
	if err := writeJSON(ctx, d.sess, DeploymentMetaPath(d.root), meta); err != nil {
		return fmt.Errorf("record deployment: %w", err)
	}

	switch d.m.Defaults.Prune {
	case manifest.PruneImages:
		fmt.Fprintln(d.out, "pruning images")
		if err := d.sess.Run(ctx, compose.ImagePrune()); err != nil {
			return fmt.Errorf("image prune: %w", err)
		}
	case manifest.PruneSystem:
		fmt.Fprintln(d.out, "pruning system (images and build cache)")
		if err := d.sess.Run(ctx, compose.SystemPrune()); err != nil {
			return fmt.Errorf("system prune: %w", err)
		}
	}

	fmt.Fprintln(d.out, "cleaning up remote staging")
	d.cleanupStaging(ctx)
	fmt.Fprintf(d.out, "\ndone: %s deployed as %s\n", d.m.Name, d.bundleName())
	return nil
}

// cleanupStaging removes the uploaded archive and its extraction dir.
func (d *Deployer) cleanupStaging(ctx context.Context) {
	if d.root == "" {
		return
	}
	staging := StagingDir(d.root)
	d.sess.RemoveAll(ctx, filepath.Join(staging, d.bundleName()))
	d.sess.RemoveAll(ctx, filepath.Join(staging, d.bundleDir()))
}

// reportFailure prints diagnostics and recovery hints for a failed stack.
func (d *Deployer) reportFailure(ctx context.Context, s *manifest.Stack, err error) {
	fmt.Fprintf(d.out, "\nFAILED stack %q: %v\n", s.Name, err)
	logPath := filepath.Join(LogsDir(d.root), d.bundleDir(), s.Name+".log")
	fmt.Fprintf(d.out, "remote log: %s\n", logPath)

	if out, _, execErr := d.sess.Exec(ctx, compose.New(s.Name).PsJSON()); execErr == nil {
		fmt.Fprintf(d.out, "current container state:\n%s\n", out)
	}
	fmt.Fprintf(d.out, "manual recovery:\n")
	fmt.Fprintf(d.out, "  ssh %s 'cat %s'\n", d.sess.Target(), remote.Quote(logPath))
	fmt.Fprintf(d.out, "  fix the stack source, then rerun: composefile apply\n")
}
