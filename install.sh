#!/bin/sh
set -eu

REPO="bodav/composefile"
BIN_DIR="${COMPOSEFILE_INSTALL_DIR:-$HOME/.local/bin}"
VERSION="${COMPOSEFILE_VERSION:-latest}"

case "$(uname -s)" in
  Linux) OS=linux ;;
  Darwin) OS=darwin ;;
  *) echo "error: unsupported OS: $(uname -s)" >&2; exit 1 ;;
esac

case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) echo "error: unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac

if [ "$VERSION" = "latest" ]; then
  URL="https://github.com/$REPO/releases/latest/download/composefile_${OS}_${ARCH}.tar.gz"
else
  URL="https://github.com/$REPO/releases/download/$VERSION/composefile_${OS}_${ARCH}.tar.gz"
fi

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

echo "Downloading $URL"
curl -fsSL "$URL" -o "$TMP/composefile.tar.gz"
tar -xzf "$TMP/composefile.tar.gz" -C "$TMP"

mkdir -p "$BIN_DIR"
install -m 0755 "$TMP/composefile" "$BIN_DIR/composefile"

echo "Installed composefile to $BIN_DIR/composefile"
"$BIN_DIR/composefile" --version