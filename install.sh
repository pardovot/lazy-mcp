#!/bin/sh
set -e

OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)

case "$ARCH" in
    x86_64)  ARCH="amd64" ;;
    aarch64) ARCH="arm64" ;;
esac

URL="https://github.com/tkukushkin/lazy-mcp/releases/latest/download/lazy-mcp-${OS}-${ARCH}"
DEST="$HOME/.local/bin/lazy-mcp"

mkdir -p "$HOME/.local/bin"
TMP=$(mktemp "$HOME/.local/bin/.lazy-mcp.XXXXXX")
trap 'rm -f "$TMP"' EXIT

echo "Downloading lazy-mcp for ${OS}/${ARCH}..."
curl -fsSL "$URL" -o "$TMP"
chmod 755 "$TMP"
mv "$TMP" "$DEST"
echo "Installed lazy-mcp to ${DEST}"

case ":$PATH:" in
    *":$HOME/.local/bin:"*) ;;
    *) echo "WARNING: $HOME/.local/bin is not in your PATH. Add it with:" ; echo "  export PATH=\"\$HOME/.local/bin:\$PATH\"" ;;
esac
