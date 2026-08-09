package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"composefile/internal/manifest"
	"composefile/internal/testutil"
)

const cfHome = "/home/u"

func destroyManifest(t *testing.T, stacks ...string) *manifest.Manifest {
	t.Helper()
	m := &manifest.Manifest{
		Name:   "prod",
		Target: "deploy@host",
		Dir:    t.TempDir(),
	}
	m.Defaults.RemoteRoot = "~/cf"
	for _, name := range stacks {
		root := filepath.Join(m.Dir, name)
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "compose.yaml"), []byte("services: {}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		m.Stacks = append(m.Stacks, manifest.Stack{
			Name:           name,
			SourceAbs:      root,
			ComposeAbs:     []string{filepath.Join(root, "compose.yaml")},
			HealthTimeoutD: 0,
		})
	}
	return m
}

const destroyRoot = cfHome + "/cf/prod"

func runDestroyCmd(t *testing.T, m *manifest.Manifest, rules ...testutil.Rule) (string, error) {
	t.Helper()
	sess, _, log := testutil.NewSession(t, rules...)
	var out bytes.Buffer
	var errb bytes.Buffer
	code := destroyWithSession(context.Background(), m, sess, &out, &errb)
	if code != ExitOK {
		return out.String(), &destroyError{code: code, err: errb.String(), log: log}
	}
	return out.String(), nil
}

type destroyError struct {
	code int
	err  string
	log  string
}

func (e *destroyError) Error() string { return e.err }

func TestDestroySuccessManagedAndOrphan(t *testing.T) {
	m := destroyManifest(t, "app")
	out, err := runDestroyCmd(t, m,
		testutil.Rule{Match: "printf", Out: cfHome + "\n"},
		testutil.Rule{Match: destroyRoot + "/metadata/stacks"},
		testutil.Rule{Match: "ls -1", Out: "ghost.json\n"},
		testutil.Rule{Match: "--project-name app"},
		testutil.Rule{Match: "workspaces/ghost"},
		testutil.Rule{Match: "--project-name ghost"},
		testutil.Rule{Match: "compose.project=app", Out: "Deleted Networks:\ncaddy_app_net\n"},
		testutil.Rule{Match: "compose.project=ghost", Out: "Deleted Networks:\n"},
		testutil.Rule{Match: "rm -rf"},
	)
	if err != nil {
		t.Fatalf("destroy failed: %v\nout=%s", err, out)
	}
	for _, want := range []string{"app", "ghost", "pruned leftover networks", "removed remote deployment state"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestDestroyManagedOnly(t *testing.T) {
	m := destroyManifest(t, "app")
	out, err := runDestroyCmd(t, m,
		testutil.Rule{Match: "printf", Out: cfHome + "\n"},
		testutil.Rule{Match: destroyRoot + "/metadata/stacks"},
		testutil.Rule{Match: "ls -1"},
		testutil.Rule{Match: "--project-name app"},
		testutil.Rule{Match: "compose.project=app"},
		testutil.Rule{Match: "rm -rf"},
	)
	if err != nil {
		t.Fatalf("destroy failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "stopped app") || !strings.Contains(out, "removed remote") {
		t.Errorf("expected app stopped and root removed:\n%s", out)
	}
}

func TestDestroyFailureLeavesRoot(t *testing.T) {
	m := destroyManifest(t, "app")
	sess, _, log := testutil.NewSession(t,
		testutil.Rule{Match: "printf", Out: cfHome + "\n"},
		testutil.Rule{Match: destroyRoot + "/metadata/stacks"},
		testutil.Rule{Match: "ls -1"},
		testutil.Rule{Match: "--project", Code: 1},
	)
	var out, errb bytes.Buffer
	if code := destroyWithSession(context.Background(), m, sess, &out, &errb); code != ExitError {
		t.Fatalf("expected exit 1, got %d", code)
	}
	if !strings.Contains(errb.String(), "left intact") {
		t.Errorf("expected 'left intact' message, got %q", errb.String())
	}
	if scripts := testutil.LogScripts(t, log); containsScript(scripts, "rm -rf") {
		t.Errorf("remote root must not be removed after a failed down:\n%v", scripts)
	}
}

func TestDestroyNothingWhenAbsent(t *testing.T) {
	m := destroyManifest(t, "app")
	out, err := runDestroyCmd(t, m,
		testutil.Rule{Match: "printf", Out: cfHome + "\n"},
		testutil.Rule{Match: "metadata/stacks", Code: 1},
	)
	if err != nil {
		t.Fatalf("destroy failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "nothing to destroy") {
		t.Errorf("expected nothing-to-destroy, got %q", out)
	}
}

func TestDestroyRejectsArgs(t *testing.T) {
	dir := t.TempDir()
	env := Env{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, WorkDir: dir}
	if code := Run(context.Background(), env, []string{"destroy", "extra"}); code != ExitError {
		t.Fatalf("expected error exit, got %d", code)
	}
}

func containsScript(scripts []string, needle string) bool {
	for _, s := range scripts {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}
