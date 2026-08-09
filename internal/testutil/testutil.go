// Package testutil provides test helpers shared across packages, notably a
// fake ssh binary driven by a content-matched rule plan.
package testutil

import (
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"composefile/internal/remote"
)

var (
	fakeOnce sync.Once
	fakePath string
	fakeErr  error
)

// FakeSSH returns the path to a freshly built fake ssh binary, building it once
// per test binary.
func FakeSSH(t *testing.T) string {
	t.Helper()
	fakeOnce.Do(func() {
		dir, err := os.MkdirTemp("", "fakessh")
		if err != nil {
			fakeErr = err
			return
		}
		fakePath = filepath.Join(dir, "ssh")
		cmd := exec.Command("go", "build", "-o", fakePath, "./internal/remote/testhelper")
		cmd.Dir = moduleRoot()
		if out, err := cmd.CombinedOutput(); err != nil {
			fakeErr = fmt.Errorf("build fakessh: %v\n%s", err, out)
		}
	})
	if fakeErr != nil {
		t.Fatal(fakeErr)
	}
	return fakePath
}

func moduleRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "."
		}
		dir = parent
	}
}

// Rule matches a single remote invocation by substrings.
type Rule struct {
	// Match is a substring of the script, or ";;;"-separated substrings that
	// must all be present, or "CAT" for stdin-capturing uploads.
	Match string
	Out   string
	Code  int
}

// NewSession creates a remote.Session backed by the fake ssh and returns the
// plan path and invocation log path.
func NewSession(t *testing.T, rules ...Rule) (*remote.Session, string, string) {
	t.Helper()
	dir := t.TempDir()
	plan := filepath.Join(dir, "plan")
	log := filepath.Join(dir, "log")

	var b strings.Builder
	for _, r := range rules {
		fmt.Fprintf(&b, "%s\t%s\t%d\n", r.Match, base64.StdEncoding.EncodeToString([]byte(r.Out)), r.Code)
	}
	if err := os.WriteFile(plan, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	env := []string{
		"FAKE_SSH_PLAN=" + plan,
		"FAKE_SSH_LOG=" + log,
		"FAKE_SSH_CAT_OUT=" + filepath.Join(dir, "catout"),
		"PATH=" + os.Getenv("PATH"),
	}
	return remote.NewWithSSH("deploy@host", FakeSSH(t), env), plan, log
}

// NewSessionWithPrereq is NewSession with the mandatory remote prereq outputs
// (Linux uname, Compose v2) prepended, so tests need not declare them.
func NewSessionWithPrereq(t *testing.T, rules ...Rule) (*remote.Session, string) {
	t.Helper()
	base := []Rule{
		{Match: "uname -s", Out: "Linux\n"},
		{Match: "docker compose version", Out: "Docker Compose version v2.29.0\n"},
	}
	all := make([]Rule, 0, len(base)+len(rules))
	all = append(all, base...)
	all = append(all, rules...)
	sess, _, log := NewSession(t, all...)
	return sess, log
}

// CAT is the special match value for stdin-capturing uploads.
const CAT = "__CAT__"

// LogScripts returns the inner scripts from the fake ssh invocation log.
func LogScripts(t *testing.T, logPath string) []string {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, l := range strings.Split(string(data), "\n") {
		if l == "" {
			continue
		}
		if i := strings.IndexByte(l, '|'); i >= 0 {
			out = append(out, l[i+1:])
		} else {
			out = append(out, l)
		}
	}
	return out
}
