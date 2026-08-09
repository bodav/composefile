package deploy

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"composefile/internal/remote"
)

// Remote layout under <remote-root>/<deployment-set>/:
//
//	metadata/deployment.json   last successful bundle for the set
//	metadata/stacks/<stack>.json  per-stack deployment metadata
//	staging/                   uploaded archives and extraction dirs
//	workspaces/<stack>         fixed per-stack workspace
//	logs/<bundle>/<stack>.log  retained deployment logs
const (
	metaSubdir    = "metadata"
	stackMetaSub  = "metadata/stacks"
	stagingSubdir = "staging"
	workspacesSub = "workspaces"
	logsSubdir    = "logs"

	stackMetaFile = "deployment.json"
)

// StackMeta records the last successful deployment of one stack.
type StackMeta struct {
	Stack      string    `json:"stack"`
	Bundle     string    `json:"bundle"`
	DeployedAt time.Time `json:"deployed_at"`
	Services   []string  `json:"services"`
}

// DeployMeta records the deployment set's last successful bundle.
type DeployMeta struct {
	Bundle     string    `json:"bundle"`
	DeployedAt time.Time `json:"deployed_at"`
}

// BundleDir strips the .tar.gz suffix from a bundle filename.
func BundleDir(name string) string { return strings.TrimSuffix(name, ".tar.gz") }

// Root returns the deployment root for a set under an expanded remote root.
func Root(base, name string) string { return filepath.Join(base, name) }

// StackMetaPath returns metadata/stacks/<stack>.json.
func StackMetaPath(root, stack string) string {
	return filepath.Join(root, metaSubdir, "stacks", stack+".json")
}

// StackMetaDir returns metadata/stacks.
func StackMetaDir(root string) string { return filepath.Join(root, metaSubdir, "stacks") }

// DeploymentMetaPath returns metadata/deployment.json.
func DeploymentMetaPath(root string) string {
	return filepath.Join(root, metaSubdir, stackMetaFile)
}

// StagingDir returns root/staging.
func StagingDir(root string) string { return filepath.Join(root, stagingSubdir) }

// WorkspacePath returns the fixed workspace for a stack.
func WorkspacePath(root, stack string) string { return filepath.Join(root, workspacesSub, stack) }

// LogsDir returns root/logs.
func LogsDir(root string) string { return filepath.Join(root, logsSubdir) }

// ExpandRoot resolves and validates the deployment root on the remote host.
func ExpandRoot(ctx context.Context, sess *remote.Session, remoteRoot, setName string) (string, error) {
	home, err := sess.Home(ctx)
	if err != nil {
		return "", err
	}
	base, err := sess.ExpandHome(ctx, remoteRoot)
	if err != nil {
		return "", err
	}
	root := Root(base, setName)
	if err := remote.ValidatePath(root); err != nil {
		return "", fmt.Errorf("deploy root: %w", err)
	}
	if root == home {
		return "", fmt.Errorf("deploy root %s must not be the SSH user's home directory", root)
	}
	return root, nil
}

// writeJSON writes v as JSON to remotePath using a temporary local file.
func writeJSON(ctx context.Context, sess *remote.Session, remotePath string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp("", "composefile-meta-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	defer os.Remove(tmpName)
	return sess.Upload(ctx, tmpName, remotePath)
}

// ReadJSON reads and decodes a JSON file from the remote host.
func ReadJSON(ctx context.Context, sess *remote.Session, remotePath string, v any) error {
	out, errb, err := sess.Exec(ctx, "cat "+remote.Quote(remotePath))
	if err != nil {
		return fmt.Errorf("read %s: %w%s", remotePath, err, errb)
	}
	if err := json.Unmarshal([]byte(out), v); err != nil {
		return fmt.Errorf("decode %s: %w", remotePath, err)
	}
	return nil
}
