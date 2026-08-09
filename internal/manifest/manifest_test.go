package manifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeFixture creates a manifest dir with composefile.yaml plus stack sources.
func writeFixture(t *testing.T, yaml string, stacks ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, s := range stacks {
		if err := os.MkdirAll(filepath.Join(dir, s), 0o755); err != nil {
			t.Fatal(err)
		}
		for _, f := range []string{"compose.yaml", "compose.production.yaml"} {
			if err := os.WriteFile(filepath.Join(dir, s, f), []byte("services: {}\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "composefile.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func loadFixture(t *testing.T, yaml string, stacks ...string) (*Manifest, error) {
	t.Helper()
	return Load(writeFixture(t, yaml, stacks...))
}

func TestLoadValidWithDefaults(t *testing.T) {
	m, err := loadFixture(t, `
name: production
target: deploy@prod-server
stacks:
  - name: database
    source: ./database
    compose: [compose.yaml]
`, "database")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if m.Name != "production" || m.Target != "deploy@prod-server" {
		t.Fatalf("unexpected name/target: %q %q", m.Name, m.Target)
	}
	if m.Defaults.RemoteRoot != DefaultRemoteRoot {
		t.Errorf("remote_root default = %q", m.Defaults.RemoteRoot)
	}
	if m.Defaults.Prune != PruneNone {
		t.Errorf("prune default = %q, want none", m.Defaults.Prune)
	}
	if m.Stacks[0].HealthTimeoutD != DefaultHealthTimeout {
		t.Errorf("health timeout default = %v", m.Stacks[0].HealthTimeoutD)
	}
}

func TestLoadOverrides(t *testing.T) {
	m, err := loadFixture(t, `
name: production
target: deploy@prod-server
defaults:
  health_timeout: 30s
  prune: images
stacks:
  - name: database
    source: ./database
    compose: [compose.yaml]
  - name: app
    source: ./app
    compose: [compose.yaml, compose.production.yaml]
    health_timeout: 90s
`, "database", "app")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if m.Defaults.Prune != PruneImages {
		t.Errorf("prune = %q", m.Defaults.Prune)
	}
	if m.Stacks[0].HealthTimeoutD != 30*time.Second {
		t.Errorf("db timeout = %v", m.Stacks[0].HealthTimeoutD)
	}
	if m.Stacks[1].HealthTimeoutD != 90*time.Second {
		t.Errorf("app timeout = %v", m.Stacks[1].HealthTimeoutD)
	}
	if len(m.Stacks[1].ComposeAbs) != 2 {
		t.Fatalf("compose abs = %v", m.Stacks[1].ComposeAbs)
	}
	want := filepath.Join(m.Stacks[1].SourceAbs, "compose.production.yaml")
	if m.Stacks[1].ComposeAbs[1] != want {
		t.Errorf("compose abs[1] = %q, want %q", m.Stacks[1].ComposeAbs[1], want)
	}
}

func TestUnknownFieldRejected(t *testing.T) {
	_, err := loadFixture(t, `
name: production
target: t
stacks:
  - name: db
    source: ./database
    compose: [compose.yaml]
bogus: true
`, "database")
	if err == nil || !strings.Contains(err.Error(), "bogus") {
		t.Fatalf("want unknown-field error, got %v", err)
	}
}

func TestRequiredFields(t *testing.T) {
	cases := []struct {
		name   string
		yaml   string
		stacks []string
		want   string
	}{
		{"name", "target: t\nstacks: [{name: db, source: ./d, compose: [c.yaml]}]\n", []string{"d"}, "name is required"},
		{"target", "name: p\nstacks: [{name: db, source: ./d, compose: [c.yaml]}]\n", []string{"d"}, "target is required"},
		{"no stacks", "name: p\ntarget: t\n", nil, "at least one stack"},
		{"stack name", "name: p\ntarget: t\nstacks: [{source: ./d, compose: [c.yaml]}]\n", []string{"d"}, "requires a name"},
		{"dup stack", "name: p\ntarget: t\nstacks: [{name: db, source: ./d, compose: [c.yaml]}, {name: db, source: ./d, compose: [c.yaml]}]\n", []string{"d"}, "duplicate stack name"},
		{"no source", "name: p\ntarget: t\nstacks: [{name: db, compose: [c.yaml]}]\n", nil, "requires a source"},
		{"no compose", "name: p\ntarget: t\nstacks: [{name: db, source: ./d}]\n", []string{"d"}, "at least one compose"},
		{"bad name", "name: bad name!\ntarget: t\nstacks: [{name: db, source: ./d, compose: [c.yaml]}]\n", []string{"d"}, "must match"},
		{"bad prune", "name: p\ntarget: t\ndefaults: {prune: volumes}\nstacks: [{name: db, source: ./d, compose: [c.yaml]}]\n", []string{"d"}, "invalid prune scope"},
		{"bad default timeout", "name: p\ntarget: t\ndefaults: {health_timeout: nope}\nstacks: [{name: db, source: ./d, compose: [c.yaml]}]\n", []string{"d"}, "health_timeout"},
		{"bad stack timeout", "name: p\ntarget: t\nstacks: [{name: db, source: ./d, compose: [c.yaml], health_timeout: nope}]\n", []string{"d"}, "health_timeout"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadFixture(t, tc.yaml, tc.stacks...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestComposeEscapesSource(t *testing.T) {
	_, err := loadFixture(t, `
name: p
target: t
stacks:
  - name: db
    source: ./database
    compose: [../outside.yaml]
`, "database")
	if err == nil || !strings.Contains(err.Error(), "must resolve inside source") {
		t.Fatalf("want escape error, got %v", err)
	}
}

func TestMissingSource(t *testing.T) {
	_, err := loadFixture(t, `
name: p
target: t
stacks:
  - name: db
    source: ./missing
    compose: [compose.yaml]
`)
	if err == nil || !strings.Contains(err.Error(), "source") {
		t.Fatalf("want source error, got %v", err)
	}
}

func TestMissingComposeFile(t *testing.T) {
	_, err := loadFixture(t, `
name: p
target: t
stacks:
  - name: db
    source: ./database
    compose: [nope.yaml]
`, "database")
	if err == nil || !strings.Contains(err.Error(), "nope.yaml") {
		t.Fatalf("want compose error, got %v", err)
	}
}
