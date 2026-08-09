package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"composefile/internal/manifest"
)

// sanitizeName makes a filesystem name safe for a manifest/bundle name.
var unsafeNameRe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func sanitizeName(s string) string {
	s = unsafeNameRe.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-.")
	if s == "" {
		s = "deployment"
	}
	return s
}

// runInit writes a starter composefile.yaml in WorkDir, refusing to overwrite.
func runInit(ctx context.Context, env Env, args []string) int {
	if len(args) > 0 {
		fmt.Fprintln(env.Stderr, "composefile: init takes no arguments")
		return ExitError
	}

	if _, err := os.Stat(filepath.Join(env.WorkDir, "composefile.yaml")); err == nil {
		fmt.Fprintf(env.Stderr, "composefile: composefile.yaml already exists in %s; refusing to overwrite\n", env.WorkDir)
		return ExitError
	}

	name := sanitizeName(filepath.Base(env.WorkDir))
	compose := detectCompose(env.WorkDir)
	if compose == "" {
		compose = "compose.yaml"
		fmt.Fprintf(env.Stderr, "composefile: no common compose file detected; using %s\n", compose)
	}

	doc := fmt.Sprintf(starterTemplate, name, name, compose)
	if err := os.WriteFile(filepath.Join(env.WorkDir, "composefile.yaml"), []byte(doc), 0o644); err != nil {
		fmt.Fprintf(env.Stderr, "composefile: init: %v\n", err)
		return ExitError
	}
	fmt.Fprintf(env.Stdout, "Wrote composefile.yaml\nEdit target and review the stack before running 'composefile bundle'.\n")
	return ExitOK
}

// detectCompose returns the first common compose filename found in dir.
func detectCompose(dir string) string {
	for _, c := range manifest.ComposeFileCandidates {
		if info, err := os.Stat(filepath.Join(dir, c)); err == nil && !info.IsDir() {
			return c
		}
	}
	return ""
}

const starterTemplate = `name: %s
target: deploy@your-server

defaults:
  remote_root: ~/.local/share/composefile
  health_timeout: 120s
  prune: none

stacks:
  - name: %s
    source: .
    compose:
      - %s
`
