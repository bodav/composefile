package remote

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var (
	fakeSSHPath string
	fakeEnvVars []string
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "fakessh")
	if err != nil {
		panic(err)
	}
	fakeSSHPath = filepath.Join(dir, "ssh")
	build := exec.Command("go", "build", "-o", fakeSSHPath, "./internal/remote/testhelper")
	build.Dir = filepath.Join("..", "..")
	if out, err := build.CombinedOutput(); err != nil {
		os.RemoveAll(dir)
		panic(fmt.Sprintf("build fakessh: %v\n%s", err, out))
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

type rule struct {
	match string
	out   string
	code  int
}

func newFake(t *testing.T, rules ...rule) (*Session, string, string) {
	t.Helper()
	dir := t.TempDir()
	plan := filepath.Join(dir, "plan")
	log := filepath.Join(dir, "log")

	var b strings.Builder
	for _, r := range rules {
		fmt.Fprintf(&b, "%s\t%s\t%d\n", r.match, base64.StdEncoding.EncodeToString([]byte(r.out)), r.code)
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
	return NewWithSSH("deploy@host", fakeSSHPath, env), plan, log
}

func TestQuote(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"docker"}, "docker"},
		{[]string{"a", "b c"}, "a 'b c'"},
		{[]string{"it's"}, `'it'"'"'s'`},
		{[]string{""}, "''"},
		{[]string{"$HOME"}, "'$HOME'"},
		{[]string{"a;rm -rf /"}, "'a;rm -rf /'"},
	}
	for _, c := range cases {
		if got := Quote(c.args...); got != c.want {
			t.Errorf("Quote(%q) = %q, want %q", c.args, got, c.want)
		}
	}
}

func TestScript(t *testing.T) {
	got := Script("echo hi")
	want := "sh -c 'echo hi'"
	if got != want {
		t.Fatalf("Script = %q, want %q", got, want)
	}
}

func TestValidatePath(t *testing.T) {
	valid := []string{"/home/u/app", "/var/lib/x"}
	for _, p := range valid {
		if err := ValidatePath(p); err != nil {
			t.Errorf("ValidatePath(%q): %v", p, err)
		}
	}
	invalid := []string{"", "rel/path", "~", "/", "/x\x00y"}
	for _, p := range invalid {
		if err := ValidatePath(p); err == nil {
			t.Errorf("ValidatePath(%q): expected error", p)
		}
	}
}

func TestExecOutput(t *testing.T) {
	s, _, _ := newFake(t, rule{match: "printf", out: "/home/u\n"})
	out, _, err := s.Exec(context.Background(), `printf "%s\n" "$HOME"`)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if out != "/home/u\n" {
		t.Fatalf("out = %q", out)
	}
}

func TestRunFailsOnRemoteError(t *testing.T) {
	s, _, _ := newFake(t, rule{match: "boom", out: "boom stdout", code: 3})
	if err := s.Run(context.Background(), "boom"); err == nil {
		t.Fatal("expected error for exit code 3")
	}
}

func TestHome(t *testing.T) {
	s, _, _ := newFake(t, rule{match: "printf", out: "/home/deploy\n"})
	home, err := s.Home(context.Background())
	if err != nil {
		t.Fatalf("Home: %v", err)
	}
	if home != "/home/deploy" {
		t.Fatalf("home = %q", home)
	}
	if home, _ := s.Home(context.Background()); home != "/home/deploy" {
		t.Fatal("home not cached")
	}
}

func TestExpandHome(t *testing.T) {
	s, _, _ := newFake(t, rule{match: "printf", out: "/home/deploy\n"})
	p, err := s.ExpandHome(context.Background(), "~/x/y")
	if err != nil {
		t.Fatal(err)
	}
	if p != "/home/deploy/x/y" {
		t.Fatalf("ExpandHome = %q", p)
	}
}

func TestExists(t *testing.T) {
	s, _, _ := newFake(t, rule{match: "/a", code: 0})
	ok, err := s.Exists(context.Background(), "/a")
	if err != nil || !ok {
		t.Fatalf("Exists(/a) = %v, %v", ok, err)
	}

	s2, _, _ := newFake(t, rule{match: "/b", code: 1})
	ok, err = s2.Exists(context.Background(), "/b")
	if err != nil || ok {
		t.Fatalf("Exists(/b) = %v, %v", ok, err)
	}
}

func TestUploadCapturesStdin(t *testing.T) {
	s, _, log := newFake(t, rule{match: "__CAT__"})
	src := filepath.Join(t.TempDir(), "src")
	if err := os.WriteFile(src, []byte("hello-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.Upload(context.Background(), src, "/staging/x.tar.gz"); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(log), "catout"))
	if err != nil || string(data) != "hello-bytes" {
		t.Fatalf("cat output = %q, %v", data, err)
	}
}

func TestStreamWritesToOutAndTeesLog(t *testing.T) {
	s, _, _ := newFake(t, rule{match: "echo hello", out: "line1\nline2\n"})
	var buf bytes.Buffer
	if err := s.Stream(context.Background(), "echo hello", "/logs/b/x.log", &buf); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if buf.String() != "line1\nline2\n" {
		t.Fatalf("streamed = %q", buf.String())
	}
}

func TestPrereqSuccess(t *testing.T) {
	s, _, _ := newFake(t,
		rule{match: "uname -s", out: "Linux\n"},
		rule{match: "command -v sh", out: "/bin/sh\n"},
		rule{match: "command -v tar", out: "/bin/tar\n"},
		rule{match: "command -v gzip", out: "/bin/gzip\n"},
		rule{match: "command -v docker", out: "/usr/bin/docker\n"},
		rule{match: "docker compose version", out: "Docker Compose version v2.29.0\n"},
		rule{match: "docker info", out: "Server Version: 26.0\n"},
	)
	if err := s.Prereq(context.Background()); err != nil {
		t.Fatalf("Prereq: %v", err)
	}
}

func TestPrereqAcceptsNewerCompose(t *testing.T) {
	s, _, _ := newFake(t,
		rule{match: "uname -s", out: "Linux\n"},
		rule{match: "command -v sh", out: "/bin/sh\n"},
		rule{match: "command -v tar", out: "/bin/tar\n"},
		rule{match: "command -v gzip", out: "/bin/gzip\n"},
		rule{match: "command -v docker", out: "/usr/bin/docker\n"},
		rule{match: "docker compose version", out: "Docker Compose version v5.4.0\n"},
		rule{match: "docker info", out: "Server Version: 26.0\n"},
	)
	if err := s.Prereq(context.Background()); err != nil {
		t.Fatalf("Prereq: %v", err)
	}
}

func TestIsComposeVersionOK(t *testing.T) {
	ok := []string{
		"Docker Compose version v2.29.0\n",
		"Docker Compose version v5.4.0\n",
		"Docker Compose version v10.0.0\n",
	}
	for _, in := range ok {
		if !isComposeVersionOK(in) {
			t.Errorf("expected ok for %q", in)
		}
	}
	bad := []string{
		"Docker Compose version v1.29.2\n",
		"Docker Compose version\n",
		"",
		"Docker Compose version vX.4.0\n",
	}
	for _, in := range bad {
		if isComposeVersionOK(in) {
			t.Errorf("expected not ok for %q", in)
		}
	}
}

func TestPrereqRejectsNonLinux(t *testing.T) {
	s, _, _ := newFake(t, rule{match: "uname -s", out: "Darwin\n"})
	if err := s.Prereq(context.Background()); err == nil || !strings.Contains(err.Error(), "not Linux") {
		t.Fatalf("want not Linux error, got %v", err)
	}
}

func TestPrereqRejectsNoDockerAccess(t *testing.T) {
	s, _, _ := newFake(t,
		rule{match: "uname -s", out: "Linux\n"},
		rule{match: "command -v sh", out: "/bin/sh\n"},
		rule{match: "command -v tar", out: "/bin/tar\n"},
		rule{match: "command -v gzip", out: "/bin/gzip\n"},
		rule{match: "command -v docker", out: "/usr/bin/docker\n"},
		rule{match: "docker compose version", out: "Docker Compose version v2.29.0\n"},
		rule{match: "docker info", code: 1},
	)
	err := s.Prereq(context.Background())
	if err == nil || !strings.Contains(err.Error(), "docker info") {
		t.Fatalf("want docker info error, got %v", err)
	}
}

func readLog(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}
