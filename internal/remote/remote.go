// Package remote shells out to the local OpenSSH client. There is no embedded
// SSH implementation, and the client's Docker context is never modified.
package remote

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"al.essio.dev/pkg/shellescape"
)

// Quote single-quote escapes each argument and joins them with spaces. It is the
// only helper used to interpolate untrusted strings into remote scripts.
func Quote(args ...string) string {
	return shellescape.QuoteCommand(args)
}

// Script wraps inner in sh -c so every command runs under /bin/sh regardless of
// the remote user's default shell. Single-quoting the entire script keeps its
// contents verbatim on the far side.
func Script(inner string) string {
	return "sh -c " + Quote(inner)
}

// ValidatePath rejects paths that are unsafe to build commands around.
func ValidatePath(p string) error {
	if p == "" {
		return errors.New("remote: empty path")
	}
	if strings.ContainsRune(p, '\x00') {
		return errors.New("remote: path contains NUL byte")
	}
	if !filepath.IsAbs(p) {
		return fmt.Errorf("remote: path must be absolute: %q", p)
	}
	if p == filepath.Clean("/") {
		return errors.New("remote: refusing root path /")
	}
	return nil
}

// Session executes remote commands through the local ssh binary.
type Session struct {
	sshBin   string
	target   string
	extraEnv []string
	home     string
	homeErr  error
}

// New returns a Session using the real "ssh" executable.
func New(target string) *Session {
	return &Session{sshBin: "ssh", target: target}
}

// NewWithSSH returns a Session using a custom ssh binary and extra environment,
// primarily for tests that inject a fake ssh into PATH.
func NewWithSSH(target, sshBin string, extraEnv []string) *Session {
	return &Session{sshBin: sshBin, target: target, extraEnv: extraEnv}
}

// Target returns the SSH destination in use.
func (s *Session) Target() string { return s.target }

func (s *Session) cmd(ctx context.Context, inner string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, s.sshBin, s.target, Script(inner))
	cmd.Env = append(os.Environ(), s.extraEnv...)
	return cmd
}

// Exec runs inner and returns captured stdout and stderr.
func (s *Session) Exec(ctx context.Context, inner string) (string, string, error) {
	cmd := s.cmd(ctx, inner)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	if err != nil {
		return out.String(), errb.String(), fmt.Errorf("remote %s: %s: %w%s", s.target, Script(inner), err, stderrNote(errb.String()))
	}
	return out.String(), errb.String(), nil
}

// Run executes inner and returns an error if the remote command fails.
func (s *Session) Run(ctx context.Context, inner string) error {
	_, _, err := s.Exec(ctx, inner)
	return err
}

// Stream runs inner, streaming output to out and appending it to the mirrored
// log path on the remote host (logs/<bundle>/<stack>.log).
func (s *Session) Stream(ctx context.Context, inner, logPath string, out io.Writer) error {
	script := inner + " 2>&1 | tee -a " + Quote(logPath)
	cmd := s.cmd(ctx, script)
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("remote %s (%s): %w", s.target, Script(script), err)
	}
	return nil
}

// Home returns and caches the remote user's home directory.
func (s *Session) Home(ctx context.Context) (string, error) {
	if s.home != "" || s.homeErr != nil {
		return s.home, s.homeErr
	}
	out, errb, err := s.Exec(ctx, `printf "%s\n" "$HOME"`)
	if err != nil {
		s.homeErr = fmt.Errorf("remote: resolve $HOME: %w%s", err, errb)
		return "", s.homeErr
	}
	s.home = strings.TrimSpace(out)
	if s.home == "" {
		s.homeErr = errors.New("remote: could not determine $HOME")
		return "", s.homeErr
	}
	s.homeErr = ValidatePath(s.home)
	if s.homeErr != nil {
		return "", s.homeErr
	}
	return s.home, nil
}

// ExpandHome turns a leading ~ into the remote home directory. Absolute paths
// pass through unchanged.
func (s *Session) ExpandHome(ctx context.Context, p string) (string, error) {
	switch {
	case p == "~":
		return s.Home(ctx)
	case strings.HasPrefix(p, "~/"):
		home, err := s.Home(ctx)
		if err != nil {
			return "", err
		}
		return filepath.Join(home, strings.TrimPrefix(p, "~/")), nil
	default:
		return p, nil
	}
}

// MkdirAll creates a remote directory and its parents.
func (s *Session) MkdirAll(ctx context.Context, p string) error {
	return s.Run(ctx, "mkdir -p "+Quote(p))
}

// RemoveAll removes a validated remote directory recursively.
func (s *Session) RemoveAll(ctx context.Context, p string) error {
	if err := ValidatePath(p); err != nil {
		return err
	}
	return s.Run(ctx, "rm -rf -- "+Quote(p))
}

// Exists reports whether a remote path exists.
func (s *Session) Exists(ctx context.Context, p string) (bool, error) {
	_, _, err := s.Exec(ctx, "test -e "+Quote(p))
	if err == nil {
		return true, nil
	}
	if isRemoteStatus(err) {
		return false, nil
	}
	return false, err
}

// Upload streams a local file's bytes to remotePath via cat > path.
func (s *Session) Upload(ctx context.Context, localPath, remotePath string) error {
	f, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("upload: open %s: %w", localPath, err)
	}
	defer f.Close()

	cmd := s.cmd(ctx, "cat > "+Quote(remotePath))
	cmd.Stdin = f
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("upload %s -> %s: %w %s", localPath, remotePath, err, errb.String())
	}
	return nil
}

// Extract creates destDir and unpacks the gzip tar archive at archivePath into it.
func (s *Session) Extract(ctx context.Context, archivePath, destDir string) error {
	inner := "mkdir -p " + Quote(destDir) +
		" && tar -xzf " + Quote(archivePath) + " -C " + Quote(destDir)
	return s.Run(ctx, inner)
}

// Prerequisites verifies Linux, required tools, and Docker access without sudo.
func (s *Session) Prereq(ctx context.Context) error {
	out, errb, err := s.Exec(ctx, "uname -s")
	if err != nil {
		return fmt.Errorf("remote: uname: %w%s", err, errb)
	}
	if !strings.Contains(out, "Linux") {
		return fmt.Errorf("remote: host is not Linux (uname: %q)", strings.TrimSpace(out))
	}

	for _, tool := range []string{"sh", "tar", "gzip", "docker"} {
		if _, _, err := s.Exec(ctx, "command -v "+Quote(tool)+" >/dev/null"); err != nil {
			return fmt.Errorf("remote: missing prerequisite %q: %w", tool, err)
		}
	}

	ver, errb, err := s.Exec(ctx, "docker compose version")
	if err != nil {
		return fmt.Errorf("remote: docker compose unavailable: %w%s", err, errb)
	}
	if !isComposeVersionOK(ver) {
		return fmt.Errorf("remote: docker compose v2+ required, got %q", strings.TrimSpace(ver))
	}

	if _, errb, err := s.Exec(ctx, "docker info"); err != nil {
		return fmt.Errorf("remote: docker info failed (is docker usable without sudo?): %w%s", err, errb)
	}
	return nil
}

// isRemoteStatus reports whether err indicates a non-zero remote exit (as
// opposed to a transport/ssh failure).
// isComposeVersionOK requires the docker compose plugin (v2+ standalone
// versioning). Compose was rebranded rather than only "v2.x", so accept any
// major >= 2 because the legacy v1 binary predates the `docker compose`
// subcommand entirely.
func isComposeVersionOK(out string) bool {
	fields := strings.Fields(strings.TrimSpace(out))
	if len(fields) == 0 {
		return false
	}
	tok := strings.TrimPrefix(fields[len(fields)-1], "v")
	major, err := strconv.Atoi(strings.SplitN(tok, ".", 2)[0])
	if err != nil {
		return false
	}
	return major >= 2
}

func isRemoteStatus(err error) bool {
	var ee *exec.ExitError
	return errors.As(err, &ee)
}

func stderrNote(s string) string {
	if strings.TrimSpace(s) == "" {
		return ""
	}
	return fmt.Sprintf(": stderr\n%s", strings.TrimSpace(s))
}
