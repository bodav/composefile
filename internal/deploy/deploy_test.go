package deploy

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"composefile/internal/manifest"
	"composefile/internal/remote"
	"composefile/internal/testutil"
)

const home = "/home/u"

func makeManifest(t *testing.T, prune manifest.PruneScope, stacks ...string) *manifest.Manifest {
	t.Helper()
	m := &manifest.Manifest{
		Name:   "prod",
		Target: "deploy@host",
		Dir:    t.TempDir(),
	}
	m.Defaults.RemoteRoot = "~/cf"
	m.Defaults.Prune = prune
	for _, name := range stacks {
		root := filepath.Join(m.Dir, name)
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
		m.Stacks = append(m.Stacks, manifest.Stack{
			Name:           name,
			SourceAbs:      root,
			ComposeAbs:     []string{filepath.Join(root, "compose.yaml")},
			HealthTimeoutD: 120 * time.Second,
		})
	}
	return m
}

func makeBundle(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "20260805T081500Z-prod.tar.gz")
	if err := os.WriteFile(path, []byte("fake"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// newSess creates a fake ssh session with the mandatory prereq outputs plus any
// test-specific rules, returning the invocation log path and session.
func newSess(t *testing.T, rules ...testutil.Rule) (string, *remote.Session) {
	t.Helper()
	sess, log := testutil.NewSessionWithPrereq(t, rules...)
	return log, sess
}

func scriptsContain(scripts []string, needle string) bool {
	for _, s := range scripts {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}

func scriptsContainBoth(scripts []string, a, b string) bool {
	for _, s := range scripts {
		if strings.Contains(s, a) && strings.Contains(s, b) {
			return true
		}
	}
	return false
}

func scriptIndex(scripts []string, needle string) int {
	for i, s := range scripts {
		if strings.Contains(s, needle) {
			return i
		}
	}
	return -1
}

func TestDeploySuccessOrdering(t *testing.T) {
	m := makeManifest(t, manifest.PruneNone, "app")
	log, sess := newSess(t,
		testutil.Rule{Match: "printf", Out: home + "\n"},
		testutil.Rule{Match: "grep -q", Out: "yes\n"},
	)
	var out bytes.Buffer
	if err := NewWithSession(m, sess, makeBundle(t), &out).Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	scripts := testutil.LogScripts(t, log)

	pull, build, up := scriptIndex(scripts, "'pull'"), scriptIndex(scripts, "'build'"), scriptIndex(scripts, "--wait-timeout")
	if pull < 0 || build < 0 || up < 0 {
		t.Fatalf("missing pull/build/up in scripts:\n%v", scripts)
	}
	if !(pull < build && build < up) {
		t.Errorf("expected pull < build < up, got indices %d %d %d", pull, build, up)
	}
	if !scriptsContainBoth(scripts, "/cf/prod/workspaces/app", "rm -rf -- ") {
		t.Errorf("expected workspace deletion:\n%v", scripts)
	}
	if !scriptsContainBoth(scripts, "/cf/prod/metadata/stacks/app.json", "cat > ") {
		t.Errorf("expected stack metadata upload:\n%v", scripts)
	}
	if !scriptsContainBoth(scripts, "/cf/prod/metadata/deployment.json", "cat > ") {
		t.Errorf("expected deployment metadata upload:\n%v", scripts)
	}
	if scriptsContain(scripts, "prune") {
		t.Errorf("prune must not run with prune=none:\n%v", scripts)
	}
	if !scriptsContainBoth(scripts, "/cf/prod/staging/20260805T081500Z-prod.tar.gz", "rm -rf -- ") {
		t.Errorf("expected staging archive cleanup:\n%v", scripts)
	}
}

func TestDeploySkipsBuildWhenNoBuildSteps(t *testing.T) {
	m := makeManifest(t, manifest.PruneNone, "app")
	log, sess := newSess(t,
		testutil.Rule{Match: "printf", Out: home + "\n"},
		testutil.Rule{Match: "grep -q", Out: "no\n"},
	)
	var out bytes.Buffer
	if err := NewWithSession(m, sess, makeBundle(t), &out).Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	scripts := testutil.LogScripts(t, log)
	if scriptsContain(scripts, "build --pull") {
		t.Errorf("build must not run when no build steps:\n%v", scripts)
	}
	if !scriptsContain(scripts, "--wait-timeout") {
		t.Errorf("up should still run after skipping build:\n%v", scripts)
	}
}

func TestDeployRefusesUnmanaged(t *testing.T) {
	m := makeManifest(t, manifest.PruneNone, "app")
	log, sess := newSess(t,
		testutil.Rule{Match: "printf", Out: home + "\n"},
		testutil.Rule{Match: "test -e ", Code: 1},
		testutil.Rule{Match: "'-q'", Out: "abc123\n"},
	)
	var out bytes.Buffer
	err := NewWithSession(m, sess, makeBundle(t), &out).Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "unmanaged") {
		t.Fatalf("want unmanaged error, got %v", err)
	}
	scripts := testutil.LogScripts(t, log)
	if scriptsContainBoth(scripts, "/cf/prod/workspaces", "rm -rf -- ") {
		t.Errorf("must not touch workspaces on refusal:\n%v", scripts)
	}
}

func TestDeployPreflightFailureStopsEarly(t *testing.T) {
	m := makeManifest(t, manifest.PruneNone, "app")
	log, s := newSess(t,
		testutil.Rule{Match: "printf", Out: home + "\n"},
		testutil.Rule{Match: "--quiet", Code: 1},
	)
	var out bytes.Buffer
	err := NewWithSession(m, s, makeBundle(t), &out).Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "preflight") {
		t.Fatalf("want preflight error, got %v", err)
	}
	scripts := testutil.LogScripts(t, log)
	for _, banned := range []string{"'down'", "'pull'", "'build'", "--wait-timeout", "prune"} {
		if scriptsContain(scripts, banned) {
			t.Errorf("destructive command %q ran after preflight failure:\n%v", banned, scripts)
		}
	}
	if !scriptsContainBoth(scripts, "/cf/prod/staging/20260805T081500Z-prod.tar.gz", "rm -rf -- ") {
		t.Errorf("expected staging cleanup after preflight failure:\n%v", scripts)
	}
}

func TestDeployLaterStackFailure(t *testing.T) {
	m := makeManifest(t, manifest.PruneNone, "app1", "app2")
	log, s := newSess(t,
		testutil.Rule{Match: "printf", Out: home + "\n"},
		testutil.Rule{Match: "'app2';;;--wait-timeout", Code: 1},
	)
	var out bytes.Buffer
	err := NewWithSession(m, s, makeBundle(t), &out).Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "app2") {
		t.Fatalf("want app2 failure, got %v", err)
	}
	scripts := testutil.LogScripts(t, log)
	if !scriptsContainBoth(scripts, "/cf/prod/metadata/stacks/app1.json", "cat > ") {
		t.Errorf("app1 should have been deployed:\n%v", scripts)
	}
	if scriptsContainBoth(scripts, "/cf/prod/metadata/stacks/app2.json", "cat > ") {
		t.Errorf("app2 metadata must not be written on failure:\n%v", scripts)
	}
	if scriptsContain(scripts, "deployment.json") {
		t.Errorf("last-success deployment metadata must not be written on failure:\n%v", scripts)
	}
	if scriptsContain(scripts, "prune") {
		t.Errorf("prune must not run after failure:\n%v", scripts)
	}
}

func TestDeployImagePruneAfterSuccess(t *testing.T) {
	m := makeManifest(t, manifest.PruneImages, "app")
	log, s := newSess(t, testutil.Rule{Match: "printf", Out: home + "\n"})
	var out bytes.Buffer
	if err := NewWithSession(m, s, makeBundle(t), &out).Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	scripts := testutil.LogScripts(t, log)
	up, prune := scriptIndex(scripts, "--wait-timeout"), scriptIndex(scripts, "'image'")
	if prune < 0 || prune < up {
		t.Fatalf("image prune must run after up (up=%d prune=%d):\n%v", up, prune, scripts)
	}
	if scriptsContain(scripts, "'system'") {
		t.Errorf("system prune must not run with prune=images:\n%v", scripts)
	}
}

func TestDeploySystemPruneNeverVolumes(t *testing.T) {
	m := makeManifest(t, manifest.PruneSystem, "app")
	log, s := newSess(t, testutil.Rule{Match: "printf", Out: home + "\n"})
	var out bytes.Buffer
	if err := NewWithSession(m, s, makeBundle(t), &out).Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	scripts := testutil.LogScripts(t, log)
	found := false
	for _, sc := range scripts {
		if strings.Contains(sc, "system") && strings.Contains(sc, "prune") {
			found = true
			if strings.Contains(sc, "--volumes") {
				t.Fatalf("system prune must never use --volumes: %s", sc)
			}
			if !strings.Contains(sc, "--all") {
				t.Errorf("system prune should use --all: %s", sc)
			}
		}
	}
	if !found {
		t.Fatalf("system prune not run:\n%v", scripts)
	}
}

func TestDeployPruneNotRunOnFailure(t *testing.T) {
	m := makeManifest(t, manifest.PruneImages, "app")
	log, s := newSess(t,
		testutil.Rule{Match: "printf", Out: home + "\n"},
		testutil.Rule{Match: "--wait-timeout", Code: 1},
	)
	var out bytes.Buffer
	if err := NewWithSession(m, s, makeBundle(t), &out).Run(context.Background()); err == nil {
		t.Fatal("expected failure")
	}
	scripts := testutil.LogScripts(t, log)
	if scriptsContain(scripts, "prune") {
		t.Errorf("prune must not run after failure:\n%v", scripts)
	}
}
