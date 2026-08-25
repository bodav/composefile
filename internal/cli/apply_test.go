package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestApplyHelpIncludesAll(t *testing.T) {
	env := Env{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, WorkDir: t.TempDir()}
	if code := Run(context.Background(), env, []string{"apply", "--help"}); code != ExitOK {
		t.Fatalf("apply --help exit = %d", code)
	}
	out := env.Stdout.(*bytes.Buffer).String()
	if !strings.Contains(out, "--all") {
		t.Errorf("apply --help must document --all:\n%s", out)
	}
}

func TestApplyRejectsUnknownArg(t *testing.T) {
	env := Env{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, WorkDir: t.TempDir()}
	if code := Run(context.Background(), env, []string{"apply", "--bogus"}); code != ExitError {
		t.Fatalf("expected error exit, got %d", code)
	}
	if !strings.Contains(env.Stderr.(*bytes.Buffer).String(), "unknown apply argument") {
		t.Errorf("expected unknown-argument error, got %q", env.Stderr.(*bytes.Buffer).String())
	}
}

func TestApplyBundleRequiresPath(t *testing.T) {
	env := Env{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, WorkDir: t.TempDir()}
	if code := Run(context.Background(), env, []string{"apply", "--bundle"}); code != ExitError {
		t.Fatalf("expected error exit, got %d", code)
	}
	if !strings.Contains(env.Stderr.(*bytes.Buffer).String(), "--bundle requires a path") {
		t.Errorf("expected missing-path error, got %q", env.Stderr.(*bytes.Buffer).String())
	}
}
