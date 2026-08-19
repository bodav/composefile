# composefile

Deploy multiple Docker Compose projects to one remote Linux server over SSH.

`composefile` packages local project sources into a retained release bundle,
transfers that bundle to the server, and deploys every declared stack in
manifest order. Dockerfile-based services are built on the remote server.

- Single Go binary, Linux and macOS clients.
- One remote Linux target per manifest (via your `ssh` executable and config).
- Multiple Compose stacks in one manifest, deployed in manifest order.
- Release bundles retained under `./.bundle`.
- Fixed remote workspaces for stable Compose-relative paths.
- Bind mounts for host-backed files and persistent data.
- Preflight validation before any stack is changed.
- Health-aware deployment completion and status reporting.

## Requirements

**Local:** Go 1.26+ to build; an `ssh` client.

**Remote (Linux host):** `/bin/sh`, `tar`, `gzip`, Docker, and Docker Compose
v2. The SSH user must be able to run Docker without `sudo`.

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/bodav/composefile/main/install.sh | sh
```

Installs the latest release binary to `~/.local/bin`. Set `COMPOSEFILE_VERSION`
to pin a specific tag, or `COMPOSEFILE_INSTALL_DIR` to change the install
directory.

To build from source instead:

```sh
go build -o composefile ./cmd/composefile   # or: make build
```

## Quick start

```sh
# 1. Create a starter manifest in the project directory.
composefile init

# 2. Edit composefile.yaml: set target and stacks.

# 3. Create a retained release bundle.
composefile bundle

# 4. Deploy (preflights everything before changing anything).
composefile apply

# 5. Check health of the deployed stacks.
composefile status

# 6. See what the next apply would change.
composefile diff
```

## Commands

| Command | Description |
| --- | --- |
| `composefile init` | Write a starter `composefile.yaml`; refuses to overwrite an existing one. |
| `composefile bundle` | Validate the manifest and write `./.bundle/<timestamp>-<name>.tar.gz`. |
| `composefile apply` | Build a bundle, preflight, and deploy every stack in order. |
| `composefile apply --bundle ./.bundle/<bundle>.tar.gz` | Redeploy an existing retained bundle. |
| `composefile status` | Report per-stack bundle, service counts, and health. |
| `composefile diff` | Build a bundle and compare it with the deployed bundle; exits `1` when anything differs. |
| `composefile purge` | Delete all retained bundles in `./.bundle`. |
| `composefile destroy` | Stop all stacks and remove the remote deployment state. |

Exit codes: `0` on success, `1` on any usage, validation, SSH, remote, health,
or deployment failure.

## Manifest

`composefile.yaml` in the working directory. Relative local paths resolve from
the manifest directory, not the caller's working directory.

```yaml
name: production
target: deploy@prod-server

defaults:
  remote_root: ~/.local/share/composefile
  health_timeout: 120s
  prune: none

stacks:
  - name: database
    source: ./database
    compose:
      - compose.yaml

  - name: application
    source: ./application
    compose:
      - compose.yaml
      - compose.production.yaml
    health_timeout: 180s
```

- `name` (required): identifies the deployment set; used in bundle and remote
  paths. Must match `[A-Za-z0-9._-]+`.
- `target` (required): the only SSH target, passed to your `ssh` executable.
- `defaults.remote_root`: base remote directory; `~` is expanded on the server.
  Default `~/.local/share/composefile`.
- `defaults.health_timeout`: default `up --wait` timeout. Default `120s`.
- `defaults.prune`: `none` (default), `images`, or `system`. Runs exactly once,
  only after a fully successful deployment. `system` never uses `--volumes`.
- `stacks[].name` (required, unique): the stable Compose project name.
- `stacks[].source` (required): local source root bundled for the stack.
- `stacks[].compose` (required): Compose files, relative to `source` and
  resolving inside it. Merged in order.
- `stacks[].health_timeout`: per-stack override of `defaults.health_timeout`.

Validation also requires: deployment root is not `/`, empty, or the SSH user's
home; compose `volumes` (named/anonymous) and non-bind mounts are rejected in
the bundled compose files; symlinks in the source may not escape the source
root.

## Bundles

`bundle` writes one gzip tar per run to `./.bundle/`:

```text
./.bundle/20260805T081500Z-production.tar.gz
```

Contents:

```text
stacks/
  database/...
  application/...
```

- All ordinary files are included, including `.env`.
- `.git/`, `.composefile/`, and `.bundle/` are always excluded.
- File modes are normalized: ordinary files to `0644`, executables to `0755`.
- Device files, sockets, and FIFOs are rejected.
- Retained bundles are never deleted or overwritten.

## Diffing

`composefile diff` builds a fresh bundle into `./.bundle/`, reads the currently
deployed bundle name from the remote `metadata/deployment.json`, and reports the
file-level differences (added, modified, deleted) between the two.

- Compares file contents and normalized mode (executable vs not); timestamps are
  ignored, so untouched sources produce no noise.
- The candidate bundle is retained in `./.bundle`, matching `bundle`/`apply`.
- Exits `0` when the bundles are identical and `1` when anything differs, so
  `composefile diff && composefile apply` deploys only when changes exist.
- If the deployment set has never been deployed, every file is reported as
  added and it exits `1`.
- If the deployed bundle is not retained locally, it errors with a clear message
  rather than guessing.

## Purging

`composefile purge` deletes everything in `./.bundle` (the local retained-bundle
folder) to reclaim disk space. It only ever touches the manifest's `.bundle`
directory and never the host.

- Always deletes all bundles; it does not protect the currently-deployed bundle.
- Since it removes the `diff` baseline, `composefile diff` will error with
  "deployed bundle ... not retained locally" until the next `composefile apply`
  rebuilds it. The command prints this reminder after a non-empty purge.
- `composefile purge` takes no arguments and requires a valid `composefile.yaml`
  so it knows where `.bundle` lives.

## Destroying

`composefile destroy` tears down the deployment set on the server: it stops every
managed stack and then deletes the whole remote deployment root
(`~/.local/share/composefile/<name>/`, including workspaces, metadata, staging,
and logs).

- Stops each stack declared in the manifest, plus any orphaned stacks still
  tracked in `metadata/stacks` (managed projects that were later removed from
  the manifest), using their workspace compose files.
- Uses `docker compose down`: containers and compose-created networks are
  removed. Named volumes, bind-mount targets outside the root, and Docker images
  are left untouched.
- After stopping every stack, removes leftover networks owned by each project
  (`docker network prune --filter label=com.docker.compose.project=<stack>`),
  scoped by project label so unrelated networks are never touched.
- If any stack fails to stop, `destroy` aborts and leaves the remote state
  intact so the operation is retryable.
- Only ever deletes paths validated by the deployment-root guards (never `/`,
  the home directory, or an empty path).
- Local `./.bundle` is not touched; use `composefile purge` for that.
- `composefile destroy` takes no arguments.

## Remote layout

Default root: `~/.local/share/composefile/<name>/`

```text
<name>/
  metadata/
    deployment.json          last successful bundle for the set
    stacks/<stack>.json      per-stack deployment metadata
  staging/<bundle>/          temporary upload and extraction area
  workspaces/<stack>         fixed workspace, replaced on each deploy
  logs/<bundle>/<stack>.log  retained deployment logs
```

Each stack always uses the same fixed workspace, preserving Compose-relative
path behavior between releases. Persistent application data should use bind
paths outside the managed workspace; `composefile` never deletes or modifies
those bind targets.

## Deployment

`apply` runs in four phases:

1. **Local validation and packaging** — parse args, load and validate the
   manifest, build or load the release bundle, validate the archive.
2. **Remote preflight** — verify Linux/tools/Docker/Compose v2, create the
   remote layout, refuse unmanaged pre-existing projects with matching names,
   upload and stage the bundle, and run `docker compose config` for every
   stack. If anything fails, staging is removed and nothing is changed.
3. **Deploy each stack, in manifest order** — stop the current project, replace
   the fixed workspace, copy the stack source, `pull --ignore-buildable`,
   `build --pull`, then `up -d --remove-orphans --wait --wait-timeout <s>`.
   Output streams to the terminal and the remote log. Stops on the first
   failure; earlier successful stacks are left running.
4. **Completion** — record the last successful deployment, run the configured
   prune exactly once, remove staging data.

On failure, the failed stack's workspace and remote log are retained, and the
failure report prints the stage, stack, log path, container state, and recovery
commands.

## Safety guarantees

- Never mutates your active Docker context or transfers Docker credentials.
- Never invokes `sudo` or installs remote packages.
- All remote arguments are shell-quoted through one audited escaping helper.
- Recursive deletions only ever target validated fixed workspace and staging
  paths — never `/`, empty paths, the home directory, or the deployment root.
- Bind-mount targets are never deleted.
- Pre-existing Compose projects without `composefile` metadata are refused
  rather than adopted.
- No automatic rollback and no broad Docker pruning; pruning is opt-in via
  `defaults.prune`.

## Development

```sh
make build          # build ./bin/composefile (override VERSION=v1.0.0)
make install        # install to default GOBIN (~/go/bin; override GOBIN=...)
make vet            # go vet ./...
make test           # go test ./...
make test-race      # go test -race ./...
make fmt            # go fmt ./...
make fmt-check      # ensure gofmt-clean
make clean          # remove ./bin and ./.bundle
```

Tests use a fake `ssh` binary (`internal/remote/testhelper`) driven by
content-matched rule plans (`internal/testutil`) to exercise preflight,
deployment ordering, fail-fast behavior, pruning, and status without a real
host.
