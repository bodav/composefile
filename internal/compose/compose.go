// Package compose builds Docker Compose command lines as quoted remote scripts.
//
// Compose projects use the manifest stack name as the stable --project-name.
// Every argument is quoted through remote.Quote before being handed to the
// remote shell.
package compose

import (
	"fmt"
	"strconv"

	"composefile/internal/remote"
)

// Compose describes one stack's project invocation.
type Compose struct {
	projectName string
	files       []string
}

// New returns a Compose for the given stable project name and absolute remote
// compose file paths.
func New(projectName string, files ...string) *Compose {
	return &Compose{projectName: projectName, files: files}
}

// cmd joins a docker compose invocation, quoting every token.
func (c *Compose) cmd(action string, args ...string) string {
	tokens := []string{"docker", "compose", "--project-name", c.projectName}
	for _, f := range c.files {
		tokens = append(tokens, "--file", f)
	}
	tokens = append(tokens, action)
	tokens = append(tokens, args...)
	return remote.Quote(tokens...)
}

// ConfigQuiet validates the merged compose configuration.
func (c *Compose) ConfigQuiet() string { return c.cmd("config", "--quiet") }

// Services returns the expected service names.
func (c *Compose) Services() string { return c.cmd("config", "--services") }

// Down stops the current project.
func (c *Compose) Down() string { return c.cmd("down") }

// Pull downloads registry-only images.
func (c *Compose) Pull() string { return c.cmd("pull", "--ignore-buildable") }

// Build builds Dockerfile-based services with refreshed base images.
func (c *Compose) Build() string { return c.cmd("build", "--pull") }

// HasBuild returns a script that prints "yes" when the merged config contains
// a build step for any service, else "no". It always exits 0 so callers can
// branch on the output without tripping over grep's exit code.
func (c *Compose) HasBuild() string {
	return c.cmd("config", "--format", "json") + ` | grep -q '"build"' && echo yes || echo no`
}

// Up starts, reconciles, and waits for the project.
func (c *Compose) Up(waitSeconds int) string {
	return c.cmd("up", "-d", "--remove-orphans", "--wait", "--wait-timeout", strconv.Itoa(waitSeconds))
}

// PsJSON returns JSON container state for the project.
func (c *Compose) PsJSON() string { return c.cmd("ps", "--format", "json") }

// PsQ returns the project's container ids (empty when none run).
func (c *Compose) PsQ() string { return c.cmd("ps", "-q") }

// ImagePrune returns the exactly-once image prune command.
func ImagePrune() string { return remote.Quote("docker", "image", "prune", "--force") }

// SystemPrune returns the exactly-once system prune command (never --volumes).
func SystemPrune() string {
	return remote.Quote("docker", "system", "prune", "--force", "--all")
}

// NetworkPrune removes unused networks created by the given compose project.
// It is scoped by the project label so networks owned by other projects are
// never touched.
func NetworkPrune(projectName string) string {
	return remote.Quote("docker", "network", "prune", "--force", "--filter", "label=com.docker.compose.project="+projectName)
}

// WaitSeconds converts a duration into the integer seconds --wait-timeout needs.
func WaitSeconds(seconds int) int {
	if seconds < 1 {
		return 1
	}
	return seconds
}

// FmtWaitSeconds builds a --wait-timeout value; kept for tests.
func FmtWaitSeconds(seconds int) string { return fmt.Sprintf("%d", WaitSeconds(seconds)) }
