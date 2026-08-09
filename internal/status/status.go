// Package status inspects the Compose runtime state of manifest stacks and
// prints a human-readable summary. It returns an error unless every expected
// service is running and healthy.
package status

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"composefile/internal/compose"
	"composefile/internal/deploy"
	"composefile/internal/manifest"
	"composefile/internal/remote"
)

// Health states for a stack.
const (
	HealthHealthy  = "healthy"
	HealthDegraded = "degraded"
	HealthDown     = "down"
	HealthUnknown  = "unknown"
)

// StackStatus summarizes one stack's runtime state.
type StackStatus struct {
	Name    string
	Bundle  string
	Running int
	Total   int
	Health  string
}

// psEntry is one row of `docker compose ps --format json`.
type psEntry struct {
	Service string `json:"Service"`
	State   string `json:"State"`
	Health  string `json:"Health"`
}

// Run inspects the manifest's stacks through the real ssh executable.
func Run(ctx context.Context, m *manifest.Manifest, out io.Writer) error {
	return RunWithSession(ctx, m, remote.New(m.Target), out)
}

// RunWithSession inspects stacks through an explicit session.
func RunWithSession(ctx context.Context, m *manifest.Manifest, sess *remote.Session, out io.Writer) error {
	root, err := deploy.ExpandRoot(ctx, sess, m.Defaults.RemoteRoot, m.Name)
	if err != nil {
		return fmt.Errorf("status: %w", err)
	}

	statuses := make([]StackStatus, 0, len(m.Stacks))
	unhealthy := false
	for i := range m.Stacks {
		st := inspectStack(ctx, sess, root, &m.Stacks[i])
		if st.Health != HealthHealthy {
			unhealthy = true
		}
		statuses = append(statuses, st)
	}

	printTable(out, statuses)

	for _, warn := range orphanWarnings(ctx, sess, root, m) {
		fmt.Fprintf(out, "warning: %s\n", warn)
	}

	if unhealthy {
		return errors.New("one or more stacks are not healthy")
	}
	return nil
}

// inspectStack gathers metadata, expected services, and container state.
func inspectStack(ctx context.Context, sess *remote.Session, root string, s *manifest.Stack) StackStatus {
	st := StackStatus{Name: s.Name, Bundle: "-", Health: HealthUnknown}

	var meta deploy.StackMeta
	if err := deploy.ReadJSON(ctx, sess, deploy.StackMetaPath(root, s.Name), &meta); err != nil {
		st.Health = HealthUnknown
		return st
	}
	st.Bundle = meta.Bundle

	ws := deploy.WorkspacePath(root, s.Name)
	c := compose.New(s.Name, deploy.WorkspaceComposeFiles(ws, s)...)

	expected := meta.Services
	if svcOut, _, err := sess.Exec(ctx, c.Services()); err == nil && strings.TrimSpace(svcOut) != "" {
		expected = strings.Fields(svcOut)
	}
	st.Total = len(expected)

	psOut, _, err := sess.Exec(ctx, c.PsJSON())
	if err != nil {
		st.Health = HealthUnknown
		return st
	}
	entries := parsePs(psOut)
	classify(&st, expected, entries)
	return st
}

// classify derives health from expected services and running containers.
func classify(st *StackStatus, expected []string, entries []psEntry) {
	bySvc := make(map[string][]psEntry)
	for _, e := range entries {
		bySvc[e.Service] = append(bySvc[e.Service], e)
	}

	down := 0
	degraded := false
	for _, svc := range expected {
		es := bySvc[svc]
		running := false
		for _, e := range es {
			if e.State == "running" {
				running = true
			}
			if e.State != "running" || e.Health == "unhealthy" || e.Health == "starting" {
				degraded = true
			}
		}
		if len(es) == 0 || !running {
			down++
		}
	}

	switch {
	case st.Total == 0:
		st.Health = HealthUnknown
	case down == st.Total:
		st.Health = HealthDown
	case down > 0 || degraded:
		st.Health = HealthDegraded
	default:
		st.Health = HealthHealthy
	}
	st.Running = st.Total - down
}

// parsePs decodes `docker compose ps --format json` output.
func parsePs(out string) []psEntry {
	out = strings.TrimSpace(out)
	if out == "" || out == "null" || out == "[]" {
		return nil
	}
	var entries []psEntry
	if err := json.Unmarshal([]byte(out), &entries); err == nil {
		return entries
	}
	var one psEntry
	if err := json.Unmarshal([]byte(out), &one); err == nil {
		return []psEntry{one}
	}
	return nil
}

// orphanWarnings reports metadata directories for stacks not in the manifest.
func orphanWarnings(ctx context.Context, sess *remote.Session, root string, m *manifest.Manifest) []string {
	dir := deploy.StackMetaDir(root)
	out, _, err := sess.Exec(ctx, "ls -1 "+remote.Quote(dir))
	if err != nil {
		return nil
	}
	inManifest := make(map[string]bool, len(m.Stacks))
	for _, s := range m.Stacks {
		inManifest[s.Name] = true
	}
	var warns []string
	for _, line := range strings.Split(out, "\n") {
		name := strings.TrimSpace(line)
		name = strings.TrimSuffix(name, filepath.Ext(name))
		if name == "" || name == "deployment" {
			continue
		}
		if !inManifest[name] {
			warns = append(warns, fmt.Sprintf("managed project %q is no longer declared in the manifest; leaving it running", name))
		}
	}
	return warns
}

func printTable(out io.Writer, statuses []StackStatus) {
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "STACK\tBUNDLE\tSERVICES\tHEALTH")
	for _, st := range statuses {
		fmt.Fprintf(w, "%s\t%s\t%d/%d\t%s\n", st.Name, st.Bundle, st.Running, st.Total, st.Health)
	}
	w.Flush()
}
