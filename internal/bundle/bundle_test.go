package bundle

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"composefile/internal/manifest"
)

func buildFixture(t *testing.T, srcs map[string]map[string]mode) *manifest.Manifest {
	t.Helper()
	dir := t.TempDir()
	stacks := make([]manifest.Stack, 0, len(srcs))
	for name, files := range srcs {
		root := filepath.Join(dir, name)
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
		for rel, m := range files {
			p := filepath.Join(root, rel)
			if m.dir {
				if err := os.MkdirAll(p, 0o755); err != nil {
					t.Fatal(err)
				}
				continue
			}
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if m.symlink != "" {
				if err := os.Symlink(m.symlink, p); err != nil {
					t.Fatal(err)
				}
				continue
			}
			if err := os.WriteFile(p, []byte(m.data), m.mode); err != nil {
				t.Fatal(err)
			}
		}
		stacks = append(stacks, manifest.Stack{Name: name, SourceAbs: root})
	}
	return &manifest.Manifest{Name: "prod", Stacks: stacks}
}

type mode struct {
	mode    os.FileMode
	dir     bool
	symlink string
	data    string
}

func readBundle(t *testing.T, path string) map[string]tar.Header {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	out := map[string]tar.Header{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		h := *hdr
		out[strings.TrimSuffix(hdr.Name, "/")] = h
	}
}

func TestBuildStructureAndModes(t *testing.T) {
	m := buildFixture(t, map[string]map[string]mode{
		"database": {
			"compose.yaml":   {mode: 0o600},
			"run.sh":         {mode: 0o755},
			".env":           {mode: 0o644},
			"sub/thing.txt":  {mode: 0o644},
			".git/refs/head": {mode: 0o644},
			".composefile/x": {mode: 0o644},
			".bundle/bundle": {mode: 0o644},
		},
	})
	path, err := Build(m, filepath.Join(m.Stacks[0].SourceAbs, "..", "..", "..", ".bundle"))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	entries := readBundle(t, path)

	checks := map[string]int64{
		"stacks/database/compose.yaml":  0o644,
		"stacks/database/run.sh":        0o755,
		"stacks/database/.env":          0o644,
		"stacks/database/sub/thing.txt": 0o644,
	}
	for name, want := range checks {
		hdr, ok := entries[name]
		if !ok {
			t.Errorf("missing %s", name)
			continue
		}
		if got := hdr.Mode & 0o777; got != want {
			t.Errorf("%s mode = %o, want %o", name, got, want)
		}
	}
	for _, name := range []string{"stacks/database/.git/refs/head", "stacks/database/.composefile/x", "stacks/database/.bundle/bundle"} {
		if _, ok := entries[name]; ok {
			t.Errorf("%s should be excluded", name)
		}
	}
	if _, ok := entries["stacks/database/.git"]; ok {
		t.Error(".git dir should be excluded")
	}
}

func TestBuildSymlink(t *testing.T) {
	m := buildFixture(t, map[string]map[string]mode{
		"app": {
			"compose.yaml": {mode: 0o644},
			"link":         {mode: 0o644, symlink: "compose.yaml"},
		},
	})
	path, err := Build(m, t.TempDir())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	hdr := readBundle(t, path)["stacks/app/link"]
	if hdr.Typeflag != tar.TypeSymlink || hdr.Linkname != "compose.yaml" {
		t.Fatalf("symlink header = %+v", hdr)
	}
}

func TestBuildRejectsEscapingSymlink(t *testing.T) {
	outside := t.TempDir()
	m := buildFixture(t, map[string]map[string]mode{
		"app": {
			"compose.yaml": {mode: 0o644},
			"evil":         {mode: 0o644, symlink: outside},
		},
	})
	_, err := Build(m, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "escapes source root") {
		t.Fatalf("want escape error, got %v", err)
	}
}

func TestBuildRejectsSpecialFile(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "app")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "compose.yaml"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := mkfifo(filepath.Join(root, "pipe")); err != nil {
		t.Skipf("cannot create fifo: %v", err)
	}
	m := &manifest.Manifest{Name: "prod", Stacks: []manifest.Stack{{Name: "app", SourceAbs: root}}}
	_, err := Build(m, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "unsupported file type") {
		t.Fatalf("want unsupported file type error, got %v", err)
	}
}

func TestValidateMissingStack(t *testing.T) {
	m := buildFixture(t, map[string]map[string]mode{
		"database": {"compose.yaml": {mode: 0o644}},
	})
	path, err := Build(m, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	missing := &manifest.Manifest{Name: "prod", Stacks: []manifest.Stack{{Name: "other"}}}
	if err := Validate(path, missing); err == nil || !strings.Contains(err.Error(), "missing stack") {
		t.Fatalf("want missing stack error, got %v", err)
	}
}

func TestValidateOK(t *testing.T) {
	m := buildFixture(t, map[string]map[string]mode{
		"database": {"compose.yaml": {mode: 0o644}},
		"app":      {"compose.yaml": {mode: 0o644}},
	})
	path, err := Build(m, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(path, m); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestExtractStack(t *testing.T) {
	m := buildFixture(t, map[string]map[string]mode{
		"app": {
			"compose.yaml": {mode: 0o600},
			"run.sh":       {mode: 0o755},
			"link":         {mode: 0o644, symlink: "compose.yaml"},
		},
	})
	path, err := Build(m, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "ws")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ExtractStack(path, "app", dest); err != nil {
		t.Fatalf("ExtractStack: %v", err)
	}
	info, err := os.Stat(filepath.Join(dest, "run.sh"))
	if err != nil {
		t.Fatalf("stat run.sh: %v", err)
	}
	if got := info.Mode().Perm() & 0o111; got == 0 {
		t.Error("run.sh not executable")
	}
	info, err = os.Stat(filepath.Join(dest, "compose.yaml"))
	if err != nil {
		t.Fatalf("stat compose.yaml: %v", err)
	}
	if got := info.Mode().Perm() & 0o111; got != 0 {
		t.Error("compose.yaml should not be executable")
	}
	if _, err := os.Lstat(filepath.Join(dest, "link")); err != nil {
		t.Errorf("symlink not extracted: %v", err)
	}
}

func TestBuildRefusesOverwrite(t *testing.T) {
	m := buildFixture(t, map[string]map[string]mode{
		"app": {"compose.yaml": {mode: 0o644}},
	})
	out := t.TempDir()
	if _, err := Build(m, out); err != nil {
		t.Fatal(err)
	}
	if _, err := Build(m, out); err == nil || !strings.Contains(err.Error(), "refusing to overwrite") {
		t.Fatalf("want overwrite error, got %v", err)
	}
}

func TestBundleNameFormat(t *testing.T) {
	m := &manifest.Manifest{Name: "production"}
	got := BundleName(m, time.Now())
	if !strings.HasPrefix(got, "2026") || !strings.HasSuffix(got, "-production.tar.gz") {
		t.Fatalf("BundleName = %q", got)
	}
}

// kindOf returns the ChangeKind for a (stack, rel) pair, or a sentinel.
func kindOf(t *testing.T, changes []Change, stack, rel string) ChangeKind {
	t.Helper()
	for _, c := range changes {
		if c.Stack == stack && c.Rel == rel {
			return c.Kind
		}
	}
	return -100
}

func TestCompareReportsAddModifyDelete(t *testing.T) {
	oldM := buildFixture(t, map[string]map[string]mode{
		"app": {
			"compose.yaml":  {mode: 0o644},
			"run.sh":        {mode: 0o755},
			"to_delete.txt": {mode: 0o644},
			"so_change.txt": {mode: 0o600},
			"link":          {mode: 0o644, symlink: "compose.yaml"},
		},
	})
	oldPath, err := Build(oldM, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	newM := buildFixture(t, map[string]map[string]mode{
		"app": {
			"compose.yaml":  {mode: 0o600},            // content same "x", mode exec-bit same -> no diff
			"run.sh":        {mode: 0o644},            // now not executable -> Modify
			"so_change.txt": {mode: 0o644, data: "y"}, // content differs -> Modify
			"link":          {mode: 0o644, symlink: "run.sh"},
			"new_file.txt":  {mode: 0o644},
		},
	})
	newPath, err := Build(newM, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	changes, err := Compare(newPath, oldPath)
	if err != nil {
		t.Fatal(err)
	}
	if !changesHave(changes, ChangeAdd, "app", "new_file.txt") {
		t.Errorf("missing added new_file.txt: %+v", changes)
	}
	if !changesHave(changes, ChangeDelete, "app", "to_delete.txt") {
		t.Errorf("missing delete to_delete.txt: %+v", changes)
	}
	if !changesHave(changes, ChangeModify, "app", "run.sh") {
		t.Errorf("missing modify run.sh (mode): %+v", changes)
	}
	if !changesHave(changes, ChangeModify, "app", "so_change.txt") {
		t.Errorf("missing modify so_change.txt (content): %+v", changes)
	}
	// content is the same ("x") and exec bit unchanged (0644 vs 0600 both
	// non-executable) -> no diff expected for compose.yaml.
	if got := kindOf(t, changes, "app", "compose.yaml"); got != -100 {
		t.Errorf("compose.yaml unexpected change kind %d", got)
	}
}

func TestCompareIgnoresTimestamps(t *testing.T) {
	m := buildFixture(t, map[string]map[string]mode{
		"app": {"compose.yaml": {mode: 0o644}},
	})
	oldPath, err := Build(m, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Re-build from the same sources; only file mtime ordering may shift, but
	// content and normalized modes are identical.
	newPath, err := Build(m, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	changes, err := Compare(newPath, oldPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 0 {
		t.Fatalf("expected no differences, got %+v", changes)
	}
}

func TestCompareIdenticalBundles(t *testing.T) {
	m := buildFixture(t, map[string]map[string]mode{
		"app": {"compose.yaml": {mode: 0o644}, "run.sh": {mode: 0o755}},
	})
	one, err := Build(m, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	two, err := Build(m, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	changes, err := Compare(two, one)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 0 {
		t.Fatalf("expected no changes, got %+v", changes)
	}
}

func TestCompareEmptyOldBaseline(t *testing.T) {
	m := buildFixture(t, map[string]map[string]mode{
		"app": {"compose.yaml": {mode: 0o644}},
	})
	newPath, err := Build(m, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Rewrite the manifest to remove the stack, yielding an "empty" old bundle.
	emptyM := &manifest.Manifest{Name: "prod"}
	oldPath, err := Build(emptyM, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	changes, err := Compare(newPath, oldPath)
	if err != nil {
		t.Fatal(err)
	}
	if !changesHave(changes, ChangeAdd, "app", "compose.yaml") {
		t.Fatalf("expected add compose.yaml, got %+v", changes)
	}
}

func TestCompareStackScopedToOneStack(t *testing.T) {
	oldM := buildFixture(t, map[string]map[string]mode{
		"app": {"compose.yaml": {mode: 0o644}, "to_delete.txt": {mode: 0o644}},
		"db":  {"compose.yaml": {mode: 0o644}},
	})
	oldPath, err := Build(oldM, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	newM := buildFixture(t, map[string]map[string]mode{
		"app": {"compose.yaml": {mode: 0o644}, "added.txt": {mode: 0o644}},
		"db":  {"compose.yaml": {mode: 0o644, data: "changed"}},
	})
	newPath, err := Build(newM, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	appChanges, err := CompareStack(newPath, oldPath, "app")
	if err != nil {
		t.Fatal(err)
	}
	if !changesHave(appChanges, ChangeAdd, "app", "added.txt") {
		t.Errorf("missing add added.txt: %+v", appChanges)
	}
	if !changesHave(appChanges, ChangeDelete, "app", "to_delete.txt") {
		t.Errorf("missing delete to_delete.txt: %+v", appChanges)
	}
	for _, c := range appChanges {
		if c.Stack != "app" {
			t.Errorf("CompareStack(app) leaked stack %q: %+v", c.Stack, appChanges)
		}
	}

	dbChanges, err := CompareStack(newPath, oldPath, "db")
	if err != nil {
		t.Fatal(err)
	}
	if !changesHave(dbChanges, ChangeModify, "db", "compose.yaml") {
		t.Errorf("missing modify db compose.yaml: %+v", dbChanges)
	}
}

func TestCompareStackIdentical(t *testing.T) {
	m := buildFixture(t, map[string]map[string]mode{
		"app": {"compose.yaml": {mode: 0o644}},
		"db":  {"compose.yaml": {mode: 0o644, data: "same"}},
	})
	one, err := Build(m, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	two, err := Build(m, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"app", "db"} {
		changes, err := CompareStack(two, one, name)
		if err != nil {
			t.Fatal(err)
		}
		if len(changes) != 0 {
			t.Fatalf("stack %s: expected no changes, got %+v", name, changes)
		}
	}
}

// changesHave reports whether a change with the given stack/rel is present.
func changesHave(changes []Change, kind ChangeKind, stack, rel string) bool {
	for _, c := range changes {
		if c.Stack == stack && c.Rel == rel && c.Kind == kind {
			return true
		}
	}
	return false
}

func mkfifo(path string) error {
	return syscall.Mkfifo(path, 0o644)
}
