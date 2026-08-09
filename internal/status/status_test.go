package status

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

const home = "/home/u"

func makeManifest(t *testing.T, stacks ...string) *manifest.Manifest {
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
		m.Stacks = append(m.Stacks, manifest.Stack{
			Name:       name,
			SourceAbs:  root,
			ComposeAbs: []string{filepath.Join(root, "compose.yaml")},
		})
	}
	return m
}

const metaJSON = `{"stack":"app","bundle":"20260805T081500Z-prod.tar.gz","services":["web"]}
`

func runStatus(t *testing.T, m *manifest.Manifest, rules ...testutil.Rule) (string, error) {
	t.Helper()
	sess, _ := testutil.NewSessionWithPrereq(t, rules...)
	var out bytes.Buffer
	err := RunWithSession(context.Background(), m, sess, &out)
	return out.String(), err
}

func TestStatusHealthy(t *testing.T) {
	m := makeManifest(t, "app")
	out, err := runStatus(t, m,
		testutil.Rule{Match: "printf", Out: home + "\n"},
		testutil.Rule{Match: "/cf/prod/metadata/stacks/app.json", Out: metaJSON},
		testutil.Rule{Match: "--services", Out: "web\n"},
		testutil.Rule{Match: "--format", Out: `[{"Service":"web","State":"running","Health":"healthy"}]` + "\n"},
	)
	if err != nil {
		t.Fatalf("healthy status returned error: %v\n%s", err, out)
	}
	for _, want := range []string{"STACK", "app", "1/1", "healthy"} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q:\n%s", want, out)
		}
	}
}

func TestStatusDegradedReturnsError(t *testing.T) {
	m := makeManifest(t, "app")
	out, err := runStatus(t, m,
		testutil.Rule{Match: "printf", Out: home + "\n"},
		testutil.Rule{Match: "/cf/prod/metadata/stacks/app.json", Out: metaJSON},
		testutil.Rule{Match: "--services", Out: "web\n"},
		testutil.Rule{Match: "--format", Out: `[{"Service":"web","State":"running","Health":"unhealthy"}]` + "\n"},
	)
	if err == nil || !strings.Contains(err.Error(), "not healthy") {
		t.Fatalf("want not healthy error, got %v", err)
	}
	if !strings.Contains(out, "degraded") {
		t.Errorf("expected degraded table cell:\n%s", out)
	}
}

func TestStatusMissingContainerIsDown(t *testing.T) {
	m := makeManifest(t, "app")
	out, err := runStatus(t, m,
		testutil.Rule{Match: "printf", Out: home + "\n"},
		testutil.Rule{Match: "/cf/prod/metadata/stacks/app.json", Out: metaJSON},
		testutil.Rule{Match: "--services", Out: "web\n"},
		testutil.Rule{Match: "--format", Out: "[]\n"},
	)
	if err == nil {
		t.Fatal("want error for down stack")
	}
	if !strings.Contains(out, "down") {
		t.Errorf("expected down table cell:\n%s", out)
	}
}

func TestStatusNotDeployedIsUnknown(t *testing.T) {
	m := makeManifest(t, "app")
	out, err := runStatus(t, m,
		testutil.Rule{Match: "printf", Out: home + "\n"},
	)
	if err == nil {
		t.Fatal("want error for never-deployed stack")
	}
	if !strings.Contains(out, "unknown") {
		t.Errorf("expected unknown table cell:\n%s", out)
	}
}

func TestStatusOrphanWarning(t *testing.T) {
	m := makeManifest(t, "app")
	out, err := runStatus(t, m,
		testutil.Rule{Match: "printf", Out: home + "\n"},
		testutil.Rule{Match: "/cf/prod/metadata/stacks/app.json", Out: metaJSON},
		testutil.Rule{Match: "--services", Out: "web\n"},
		testutil.Rule{Match: "--format", Out: `[{"Service":"web","State":"running","Health":"healthy"}]` + "\n"},
		testutil.Rule{Match: "ls -1", Out: "app.json\nold-stack.json\n"},
	)
	if err != nil {
		t.Fatalf("healthy status returned error: %v\n%s", err, out)
	}
	if !strings.Contains(out, "old-stack") {
		t.Errorf("expected orphan warning:\n%s", out)
	}
}

func TestClassify(t *testing.T) {
	st := &StackStatus{Total: 2}
	classify(st, []string{"a", "b"}, []psEntry{
		{Service: "a", State: "running", Health: "healthy"},
		{Service: "b", State: "running", Health: "healthy"},
	})
	if st.Health != HealthHealthy || st.Running != 2 {
		t.Fatalf("classify healthy: %+v", st)
	}

	classify(st, []string{"a", "b"}, []psEntry{
		{Service: "a", State: "running", Health: "unhealthy"},
		{Service: "b", State: "running", Health: "healthy"},
	})
	if st.Health != HealthDegraded {
		t.Fatalf("classify degraded: %+v", st)
	}

	classify(st, []string{"a", "b"}, nil)
	if st.Health != HealthDown || st.Running != 0 {
		t.Fatalf("classify down: %+v", st)
	}
}
