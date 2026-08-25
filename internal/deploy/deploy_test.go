package deploy

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"composefile/internal/bundle"
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

// deployManifest builds a manifest whose stacks each contain a compose.yaml
// with the given contents. Stacks are ordered alphabetically for determinism.
func deployManifest(t *testing.T, contents map[string]string) *manifest.Manifest {
	t.Helper()
	m := makeManifest(t, manifest.PruneNone)
	m.Stacks = nil
	names := make([]string, 0, len(contents))
	for name := range contents {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		content := contents[name]
		root := filepath.Join(m.Dir, name)
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
		compose := filepath.Join(root, "compose.yaml")
		if err := os.WriteFile(compose, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		m.Stacks = append(m.Stacks, manifest.Stack{
			Name:           name,
			SourceAbs:      root,
			ComposeAbs:     []string{compose},
			HealthTimeoutD: 120 * time.Second,
		})
	}
	return m
}

// retainedBundle builds a bundle for m into m's .bundle directory and renames
// it to a deterministic name so tests can reference it from stack metadata.
func retainedBundle(t *testing.T, m *manifest.Manifest, name string) string {
	t.Helper()
	path, err := bundle.Build(m, filepath.Join(m.Dir, bundle.DefaultBundleDir))
	if err != nil {
		t.Fatal(err)
	}
	fixed := filepath.Join(m.Dir, bundle.DefaultBundleDir, name)
	if err := os.Rename(path, fixed); err != nil {
		t.Fatal(err)
	}
	return fixed
}

// freshBundle builds a new timestamped bundle for m into m's .bundle directory.
func freshBundle(t *testing.T, m *manifest.Manifest) string {
	t.Helper()
	path, err := bundle.Build(m, filepath.Join(m.Dir, bundle.DefaultBundleDir))
	if err != nil {
		t.Fatal(err)
	}
	return path
}

// stackMetaOut is the remote StackMeta JSON for a previously deployed stack.
func stackMetaOut(stack, bundleName string) string {
	return fmt.Sprintf(`{"stack":%q,"bundle":%q,"deployed_at":"2026-01-01T00:00:00Z","services":[]}`+"\n", stack, bundleName)
}

// neverDeployedRules emits test -e rules reporting that each stack has no
// per-stack metadata yet, covering the preflight unmanaged check and the
// change-detection check (both run in that order per stack).
func neverDeployedRules(stacks ...string) []testutil.Rule {
	var rules []testutil.Rule
	for _, name := range stacks {
		meta := StackMetaPath("/home/u/cf/prod", name)
		rules = append(rules,
			testutil.Rule{Match: "test -e " + meta, Code: 1},
			testutil.Rule{Match: "test -e " + meta, Code: 1},
		)
	}
	return rules
}

// deployedRules emits rules reporting that each stack was previously deployed
// from the given retained bundle, covering the preflight unmanaged check, the
// change-detection existence check, and the metadata read.
func deployedRules(stacks []string, bundleName string) []testutil.Rule {
	var rules []testutil.Rule
	for _, name := range stacks {
		meta := StackMetaPath("/home/u/cf/prod", name)
		rules = append(rules,
			testutil.Rule{Match: "test -e " + meta},
			testutil.Rule{Match: "test -e " + meta},
			testutil.Rule{Match: "cat " + meta, Out: stackMetaOut(name, bundleName)},
		)
	}
	return rules
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
	rules := []testutil.Rule{{Match: "printf", Out: home + "\n"}}
	rules = append(rules, neverDeployedRules("app")...)
	rules = append(rules, testutil.Rule{Match: "grep -q", Out: "yes\n"})
	log, sess := newSess(t, rules...)
	var out bytes.Buffer
	if err := NewWithSession(m, sess, makeBundle(t), &out).Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	scripts := testutil.LogScripts(t, log)

	pull, build, up := scriptIndex(scripts, "pull --ignore-buildable"), scriptIndex(scripts, "build --pull"), scriptIndex(scripts, "--wait-timeout")
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
	rules := []testutil.Rule{{Match: "printf", Out: home + "\n"}}
	rules = append(rules, neverDeployedRules("app")...)
	rules = append(rules, testutil.Rule{Match: "grep -q", Out: "no\n"})
	log, sess := newSess(t, rules...)
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
		testutil.Rule{Match: "ps -q", Out: "abc123\n"},
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
	for _, banned := range []string{"down", "pull", "build", "--wait-timeout", "prune"} {
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
	rules := []testutil.Rule{{Match: "printf", Out: home + "\n"}}
	rules = append(rules, neverDeployedRules("app1", "app2")...)
	rules = append(rules, testutil.Rule{Match: "app2;;;--wait-timeout", Code: 1})
	log, s := newSess(t, rules...)
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
	rules := []testutil.Rule{{Match: "printf", Out: home + "\n"}}
	rules = append(rules, neverDeployedRules("app")...)
	log, s := newSess(t, rules...)
	var out bytes.Buffer
	if err := NewWithSession(m, s, makeBundle(t), &out).Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	scripts := testutil.LogScripts(t, log)
	up, prune := scriptIndex(scripts, "--wait-timeout"), scriptIndex(scripts, "image")
	if prune < 0 || prune < up {
		t.Fatalf("image prune must run after up (up=%d prune=%d):\n%v", up, prune, scripts)
	}
	if scriptsContain(scripts, "system") {
		t.Errorf("system prune must not run with prune=images:\n%v", scripts)
	}
}

func TestDeploySystemPruneNeverVolumes(t *testing.T) {
	m := makeManifest(t, manifest.PruneSystem, "app")
	rules := []testutil.Rule{{Match: "printf", Out: home + "\n"}}
	rules = append(rules, neverDeployedRules("app")...)
	log, s := newSess(t, rules...)
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
	rules := []testutil.Rule{{Match: "printf", Out: home + "\n"}}
	rules = append(rules, neverDeployedRules("app")...)
	rules = append(rules, testutil.Rule{Match: "--wait-timeout", Code: 1})
	log, s := newSess(t, rules...)
	var out bytes.Buffer
	if err := NewWithSession(m, s, makeBundle(t), &out).Run(context.Background()); err == nil {
		t.Fatal("expected failure")
	}
	scripts := testutil.LogScripts(t, log)
	if scriptsContain(scripts, "prune") {
		t.Errorf("prune must not run after failure:\n%v", scripts)
	}
}

func TestDeploySkipsUnchangedStack(t *testing.T) {
	m := deployManifest(t, map[string]string{"app": "image: same\n"})
	retainedBundle(t, m, "baseline-prod.tar.gz")
	newPath := freshBundle(t, m)

	rules := []testutil.Rule{{Match: "printf", Out: home + "\n"}}
	rules = append(rules, deployedRules([]string{"app"}, "baseline-prod.tar.gz")...)
	rules = append(rules,
		testutil.Rule{Match: testutil.CAT},
		testutil.Rule{Match: testutil.CAT},
	)
	log, sess := newSess(t, rules...)
	var out bytes.Buffer
	if err := NewWithSession(m, sess, newPath, &out).Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	scripts := testutil.LogScripts(t, log)
	for _, banned := range []string{"down", "pull", "build", "--wait-timeout"} {
		if scriptsContain(scripts, banned) {
			t.Errorf("unchanged stack must not be deployed (found %q):\n%v", banned, scripts)
		}
	}
	if scriptsContainBoth(scripts, "/cf/prod/workspaces/app", "rm -rf -- ") {
		t.Errorf("unchanged stack workspace must not be replaced:\n%v", scripts)
	}
	if scriptsContainBoth(scripts, "/cf/prod/metadata/stacks/app.json", "cat > ") {
		t.Errorf("unchanged stack metadata must not be rewritten:\n%v", scripts)
	}
	if !scriptsContainBoth(scripts, "/cf/prod/metadata/deployment.json", "cat > ") {
		t.Errorf("deployment metadata must still be recorded:\n%v", scripts)
	}
	if !strings.Contains(out.String(), "nothing to deploy") || !strings.Contains(out.String(), "unchanged; skipping") {
		t.Errorf("expected skip messages, got:\n%s", out.String())
	}
}

func TestDeployOnlyChangedStacks(t *testing.T) {
	m := deployManifest(t, map[string]string{"app1": "image: v1\n", "app2": "image: same\n"})
	retainedBundle(t, m, "baseline-prod.tar.gz")
	app1 := &m.Stacks[0]
	if app1.Name != "app1" {
		app1 = &m.Stacks[1]
	}
	if err := os.WriteFile(filepath.Join(app1.SourceAbs, "compose.yaml"), []byte("image: v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	newPath := freshBundle(t, m)

	rules := []testutil.Rule{{Match: "printf", Out: home + "\n"}}
	rules = append(rules, deployedRules([]string{"app1", "app2"}, "baseline-prod.tar.gz")...)
	rules = append(rules, testutil.Rule{Match: "grep -q", Out: "no\n"})
	rules = append(rules,
		testutil.Rule{Match: testutil.CAT},
		testutil.Rule{Match: testutil.CAT},
		testutil.Rule{Match: testutil.CAT},
	)
	log, sess := newSess(t, rules...)
	var out bytes.Buffer
	if err := NewWithSession(m, sess, newPath, &out).Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	scripts := testutil.LogScripts(t, log)
	if !scriptsContainBoth(scripts, "/cf/prod/workspaces/app1", "rm -rf -- ") {
		t.Errorf("app1 should be deployed (workspace replaced):\n%v", scripts)
	}
	if scriptsContainBoth(scripts, "/cf/prod/workspaces/app2", "rm -rf -- ") {
		t.Errorf("app2 is unchanged and must be skipped:\n%v", scripts)
	}
	if !scriptsContainBoth(scripts, "/cf/prod/metadata/stacks/app1.json", "cat > ") {
		t.Errorf("app1 metadata should be rewritten:\n%v", scripts)
	}
	if scriptsContainBoth(scripts, "/cf/prod/metadata/stacks/app2.json", "cat > ") {
		t.Errorf("app2 metadata must not be rewritten:\n%v", scripts)
	}
	if !strings.Contains(out.String(), "1 of 2 stack(s) changed") {
		t.Errorf("expected change summary, got:\n%s", out.String())
	}
}

func TestDeployNeverDeployedDeploysAll(t *testing.T) {
	m := deployManifest(t, map[string]string{"app": "image: v1\n"})
	rules := []testutil.Rule{{Match: "printf", Out: home + "\n"}}
	rules = append(rules, neverDeployedRules("app")...)
	rules = append(rules, testutil.Rule{Match: "grep -q", Out: "no\n"})
	rules = append(rules,
		testutil.Rule{Match: testutil.CAT},
		testutil.Rule{Match: testutil.CAT},
		testutil.Rule{Match: testutil.CAT},
	)
	log, sess := newSess(t, rules...)
	var out bytes.Buffer
	if err := NewWithSession(m, sess, makeBundle(t), &out).Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	scripts := testutil.LogScripts(t, log)
	if !scriptsContain(scripts, "--wait-timeout") {
		t.Errorf("never-deployed stack should be deployed:\n%v", scripts)
	}
	if !scriptsContainBoth(scripts, "/cf/prod/metadata/stacks/app.json", "cat > ") {
		t.Errorf("stack metadata should be written:\n%v", scripts)
	}
	if !strings.Contains(out.String(), "1 of 1 stack(s) changed") {
		t.Errorf("expected change summary, got:\n%s", out.String())
	}
}

func TestDeployMissingRetainedBundleDeploys(t *testing.T) {
	m := deployManifest(t, map[string]string{"app": "image: v1\n"})
	rules := []testutil.Rule{{Match: "printf", Out: home + "\n"}}
	rules = append(rules, deployedRules([]string{"app"}, "purged-prod.tar.gz")...)
	rules = append(rules, testutil.Rule{Match: "grep -q", Out: "no\n"})
	rules = append(rules,
		testutil.Rule{Match: testutil.CAT},
		testutil.Rule{Match: testutil.CAT},
		testutil.Rule{Match: testutil.CAT},
	)
	log, sess := newSess(t, rules...)
	var out bytes.Buffer
	if err := NewWithSession(m, sess, makeBundle(t), &out).Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	scripts := testutil.LogScripts(t, log)
	if !scriptsContain(scripts, "--wait-timeout") {
		t.Errorf("stack with purged baseline should still be deployed:\n%v", scripts)
	}
	if !strings.Contains(out.String(), "not retained locally; deploying") {
		t.Errorf("expected missing-baseline note, got:\n%s", out.String())
	}
}

func TestDeployForceAllDeploysUnchanged(t *testing.T) {
	m := deployManifest(t, map[string]string{"app": "image: same\n"})
	retainedBundle(t, m, "baseline-prod.tar.gz")
	newPath := freshBundle(t, m)

	rules := []testutil.Rule{{Match: "printf", Out: home + "\n"}}
	rules = append(rules, deployedRules([]string{"app"}, "baseline-prod.tar.gz")...)
	rules = append(rules, testutil.Rule{Match: "grep -q", Out: "no\n"})
	rules = append(rules,
		testutil.Rule{Match: testutil.CAT},
		testutil.Rule{Match: testutil.CAT},
		testutil.Rule{Match: testutil.CAT},
	)
	log, sess := newSess(t, rules...)
	var out bytes.Buffer
	d := NewWithSession(m, sess, newPath, &out)
	d.WithForceAll()
	if err := d.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	scripts := testutil.LogScripts(t, log)
	if !scriptsContain(scripts, "--wait-timeout") {
		t.Errorf("--all must deploy even identical stacks:\n%v", scripts)
	}
	if !scriptsContainBoth(scripts, "/cf/prod/workspaces/app", "rm -rf -- ") {
		t.Errorf("--all should replace the workspace:\n%v", scripts)
	}
	if strings.Contains(out.String(), "unchanged; skipping") {
		t.Errorf("--all must not skip stacks:\n%s", out.String())
	}
}
