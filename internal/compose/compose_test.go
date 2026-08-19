package compose

import (
	"strings"
	"testing"
)

func TestCommandStructure(t *testing.T) {
	c := New("database", "/ws/database/compose.yaml", "/ws/database/compose.prod.yaml")
	got := c.Up(180)
	for _, want := range []string{
		"docker compose --project-name database",
		"--file /ws/database/compose.yaml",
		"--file /ws/database/compose.prod.yaml",
		"up -d --remove-orphans --wait --wait-timeout 180",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Up missing %q in:\n%s", want, got)
		}
	}
}

func TestAllActionsQuoted(t *testing.T) {
	c := New("with space", "/ws/a b/compose.yaml")
	for _, got := range []string{
		c.ConfigQuiet(), c.Services(), c.Down(), c.Pull(), c.Build(), c.Up(30), c.PsJSON(), c.PsQ(),
	} {
		if !strings.Contains(got, "'with space'") {
			t.Errorf("project name not quoted in %q", got)
		}
	}
}

func TestPruneScripts(t *testing.T) {
	img := ImagePrune()
	for _, tok := range []string{"docker", "image", "prune", "--force"} {
		if !strings.Contains(img, tok) {
			t.Errorf("ImagePrune missing %q in %q", tok, img)
		}
	}
	sys := SystemPrune()
	for _, tok := range []string{"system", "prune", "--force", "--all"} {
		if !strings.Contains(sys, tok) {
			t.Errorf("SystemPrune missing %q in %q", tok, sys)
		}
	}
	if strings.Contains(sys, "--volumes") {
		t.Error("SystemPrune must never include --volumes")
	}
	np := NetworkPrune("caddy")
	for _, tok := range []string{"network", "prune", "--force", "--filter", "label=com.docker.compose.project=caddy"} {
		if !strings.Contains(np, tok) {
			t.Errorf("NetworkPrune missing %q in %q", tok, np)
		}
	}
	if !strings.Contains(np, "--force") {
		t.Error("NetworkPrune must never ask for confirmation")
	}
}

func TestHasBuild(t *testing.T) {
	c := New("app", "/ws/app/compose.yaml")
	got := c.HasBuild()
	for _, want := range []string{
		"docker compose --project-name app",
		"--file /ws/app/compose.yaml",
		"config --format json",
		"grep -q '\"build\"'", "echo yes", "echo no",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("HasBuild missing %q in:\n%s", want, got)
		}
	}
}

func TestWaitSeconds(t *testing.T) {
	if FmtWaitSeconds(0) != "1" || FmtWaitSeconds(45) != "45" {
		t.Fatal("WaitSeconds normalization failed")
	}
}
