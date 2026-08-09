package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"composefile/internal/bundle"
	"composefile/internal/manifest"
	"composefile/internal/testutil"
)

const cfHost = "deploy@host"

func diffManifest(t *testing.T, content string) *manifest.Manifest {
	t.Helper()
	dir := t.TempDir()
	root := filepath.Join(dir, "app")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "compose.yaml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return &manifest.Manifest{
		Name:   "prod",
		Target: cfHost,
		Dir:    dir,
		Defaults: manifest.Defaults{
			RemoteRoot: "~/cf",
		},
		Stacks: []manifest.Stack{
			{Name: "app", SourceAbs: root, ComposeAbs: []string{filepath.Join(root, "compose.yaml")}},
		},
	}
}

func TestDiffIdentical(t *testing.T) {
	m := diffManifest(t, "services:\n  app:\n    image: x\n")
	bundleDir := filepath.Join(m.Dir, bundle.DefaultBundleDir)
	newPath, err := bundle.Build(m, bundleDir)
	if err != nil {
		t.Fatal(err)
	}
	meta := fmt.Sprintf(`{"Bundle": "%s"}`, filepath.Base(newPath))
	sess, _, _ := testutil.NewSession(t,
		testutil.Rule{Match: "printf", Out: "/home/u\n"},
		testutil.Rule{Match: "deployment.json"},
		testutil.Rule{Match: "deployment.json", Out: meta + "\n"},
	)
	var out, errb bytes.Buffer
	code := runDiffWithSession(context.Background(), m, sess, newPath, &out, &errb)
	if code != ExitOK {
		t.Fatalf("exit = %d, stderr = %s, stdout = %s", code, errb.String(), out.String())
	}
	if !strings.Contains(out.String(), "identical") {
		t.Errorf("expected identical message, got %q", out.String())
	}
}

func TestDiffDetectsChanges(t *testing.T) {
	m := diffManifest(t, "services:\n  app:\n    image: v1\n")
	bundleDir := filepath.Join(m.Dir, bundle.DefaultBundleDir)
	oldPath, err := bundle.Build(m, bundleDir)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond)
	// Change the source and rebuild from the same manifest.
	root := m.Stacks[0].SourceAbs
	if err := os.WriteFile(filepath.Join(root, "compose.yaml"), []byte("services:\n  app:\n    image: v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	newPath, err := bundle.Build(m, bundleDir)
	if err != nil {
		t.Fatal(err)
	}

	meta := fmt.Sprintf(`{"Bundle": %q}`, filepath.Base(oldPath))
	sess, _, _ := testutil.NewSession(t,
		testutil.Rule{Match: "printf", Out: "/home/u\n"},
		testutil.Rule{Match: "deployment.json"},
		testutil.Rule{Match: "deployment.json", Out: meta + "\n"},
	)
	var out, errb bytes.Buffer
	code := runDiffWithSession(context.Background(), m, sess, newPath, &out, &errb)
	if code != ExitError {
		t.Fatalf("expected diff exit, got %d (out=%q err=%q)", code, out.String(), errb.String())
	}
	for _, want := range []string{"app", "M", "compose.yaml", "1 file(s) differ"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out.String())
		}
	}
}

func TestDiffNeverDeployedReportsAllAdded(t *testing.T) {
	m := diffManifest(t, "services: {}\n")
	bundleDir := filepath.Join(m.Dir, bundle.DefaultBundleDir)
	newPath, err := bundle.Build(m, bundleDir)
	if err != nil {
		t.Fatal(err)
	}
	sess, _, _ := testutil.NewSession(t,
		testutil.Rule{Match: "printf", Out: "/home/u\n"},
		testutil.Rule{Match: "deployment.json", Code: 1},
	)
	var out, errb bytes.Buffer
	code := runDiffWithSession(context.Background(), m, sess, newPath, &out, &errb)
	if code != ExitError {
		t.Fatalf("expected exit 1, got %d (%s)", code, errb.String())
	}
	if !strings.Contains(out.String(), "A") || !strings.Contains(out.String(), "app") {
		t.Errorf("expected all-added rows:\n%s", out.String())
	}
	if strings.Contains(out.String(), "identical") {
		t.Errorf("never-deployed must not report identical:\n%s", out.String())
	}
}

func TestDiffMissingRetainedBundle(t *testing.T) {
	m := diffManifest(t, "services: {}\n")
	bundleDir := filepath.Join(m.Dir, bundle.DefaultBundleDir)
	newPath, err := bundle.Build(m, bundleDir)
	if err != nil {
		t.Fatal(err)
	}
	meta := `{"Bundle": "doesnotexist-prod.tar.gz"}`
	sess, _, _ := testutil.NewSession(t,
		testutil.Rule{Match: "printf", Out: "/home/u\n"},
		testutil.Rule{Match: "deployment.json"},
		testutil.Rule{Match: "deployment.json", Out: meta + "\n"},
	)
	var out, errb bytes.Buffer
	code := runDiffWithSession(context.Background(), m, sess, newPath, &out, &errb)
	if code != ExitError {
		t.Fatalf("expected exit 1, got %d", code)
	}
	if !strings.Contains(errb.String(), "not retained locally") {
		t.Errorf("expected retained-bundle error, got %q", errb.String())
	}
}

func TestDiffRejectsArgs(t *testing.T) {
	dir := t.TempDir()
	env := Env{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, WorkDir: dir}
	if code := Run(context.Background(), env, []string{"diff", "extra"}); code != ExitError {
		t.Fatalf("expected error exit, got %d", code)
	}
}
