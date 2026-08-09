package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitWritesManifest(t *testing.T) {
	dir := t.TempDir()
	env := Env{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, WorkDir: dir}
	if code := Run(context.Background(), env, []string{"init"}); code != ExitOK {
		t.Fatalf("init exit = %d, stderr=%s", code, env.Stderr)
	}
	data, err := os.ReadFile(filepath.Join(dir, "composefile.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"name:", "target:", "stacks:", "compose.yaml"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("init output missing %q:\n%s", want, data)
		}
	}
}

func TestInitDetectsCompose(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := Env{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, WorkDir: dir}
	Run(context.Background(), env, []string{"init"})
	data, _ := os.ReadFile(filepath.Join(dir, "composefile.yaml"))
	if !strings.Contains(string(data), "docker-compose.yml") {
		t.Errorf("expected detected compose file in output:\n%s", data)
	}
}

func TestInitRefusesOverwrite(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "composefile.yaml"), []byte("name: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := Env{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, WorkDir: dir}
	if code := Run(context.Background(), env, []string{"init"}); code != ExitError {
		t.Fatalf("expected error exit, got %d", code)
	}
	if !strings.Contains(env.Stderr.(*bytes.Buffer).String(), "refusing to overwrite") {
		t.Errorf("missing refuse message: %s", env.Stderr.(*bytes.Buffer).String())
	}
}

func TestInitSanitizesName(t *testing.T) {
	if got := sanitizeName("my app!@#"); got != "my-app" {
		t.Errorf("sanitizeName = %q", got)
	}
}
