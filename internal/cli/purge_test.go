package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"composefile/internal/bundle"
)

func purgeEnv(dir string) Env {
	return Env{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, WorkDir: dir}
}

func writeManifest(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app", "compose.yaml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := "name: prod\ntarget: deploy@host\nstacks:\n  - name: app\n    source: ./app\n    compose:\n      - compose.yaml\n"
	if err := os.WriteFile(filepath.Join(dir, "composefile.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPurgeRemovesBundles(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir)
	bundleDir := filepath.Join(dir, bundle.DefaultBundleDir)
	if err := os.MkdirAll(bundleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(bundleDir, "20260805T080000Z-prod.tar.gz"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(bundleDir, "20260806T120000Z-prod.tar.gz"), []byte("x"), 0o644)
	sibling := filepath.Join(dir, "keepme.txt")
	os.WriteFile(sibling, []byte("x"), 0o644)

	env := purgeEnv(dir)
	if code := Run(context.Background(), env, []string{"purge"}); code != ExitOK {
		t.Fatalf("purge exit = %d, stderr = %s", code, env.Stderr)
	}
	if _, err := os.Stat(bundleDir); !os.IsNotExist(err) {
		t.Errorf("bundle dir still exists after purge")
	}
	out := env.Stdout.(*bytes.Buffer).String()
	if !strings.Contains(out, "2 bundle(s)") {
		t.Errorf("expected 2-bundle summary, got %q", out)
	}
	if _, err := os.Stat(sibling); err != nil {
		t.Errorf("sibling file should be untouched: %v", err)
	}
}

func TestPurgeNothingWhenNoBundle(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir)
	env := purgeEnv(dir)
	if code := Run(context.Background(), env, []string{"purge"}); code != ExitOK {
		t.Fatalf("purge exit = %d, stderr = %s", code, env.Stderr)
	}
	if !strings.Contains(env.Stdout.(*bytes.Buffer).String(), "nothing to purge") {
		t.Errorf("expected nothing-to-purge message, got %q", env.Stdout.(*bytes.Buffer).String())
	}
}

func TestPurgeRejectsArgs(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir)
	env := purgeEnv(dir)
	if code := Run(context.Background(), env, []string{"purge", "extra"}); code != ExitError {
		t.Fatalf("expected error exit, got %d", code)
	}
}

func TestHasBundleSuffix(t *testing.T) {
	for _, ok := range []string{"a.tar.gz", "20260806T120000Z-prod.tar.gz"} {
		if !hasBundleSuffix(ok) {
			t.Errorf("expected %q to be a bundle", ok)
		}
	}
	for _, bad := range []string{"notes.txt", ".tar.gz", "prod.tar"} {
		if hasBundleSuffix(bad) {
			t.Errorf("expected %q not to be a bundle", bad)
		}
	}
}
