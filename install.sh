#!/bin/sh
# dabs installer — https://dabs.dev
# Downloads a prebuilt release binary; nothing is compiled on your machine.
set -eu

REPO="jjmerino/dabs"
INSTALL_DIR="${DABS_INSTALL_DIR:-/usr/local/bin}"

os="$(uname -s 2>/dev/null || echo unknown)"
arch="$(uname -m 2>/dev/null || echo unknown)"

case "$os" in
  Linux)  os="linux" ;;
  Darwin) os="darwin" ;;
  CYGWIN*|MINGW*|MSYS*|Windows_NT)
    echo "Sorry — dabs does not run on Windows yet."
    echo "We are accepting Windows driver contributions: https://github.com/$REPO"
    exit 1
    ;;
  *)
    echo "Sorry — unsupported platform: $os (dabs supports Linux and macOS)."
    exit 1
    ;;
esac

case "$arch" in
  x86_64|amd64)  arch="amd64" ;;
  aarch64|arm64) arch="arm64" ;;
  *)
    echo "Sorry — unsupported architecture: $arch (dabs ships amd64 and arm64)."
    exit 1
    ;;
esac

asset="dabs_${os}_${arch}"
url="https://github.com/$REPO/releases/latest/download/$asset"

echo "dabs → $os/$arch"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

if command -v curl >/dev/null 2>&1; then
  curl -fsSL -o "$tmp/dabs" "$url"
elif command -v wget >/dev/null 2>&1; then
  wget -qO "$tmp/dabs" "$url"
else
  echo "Need curl or wget to download dabs." >&2
  exit 1
fi
chmod +x "$tmp/dabs"

if [ -w "$INSTALL_DIR" ]; then
  mv "$tmp/dabs" "$INSTALL_DIR/dabs"
else
  echo "Installing to $INSTALL_DIR (needs sudo)…"
  sudo mv "$tmp/dabs" "$INSTALL_DIR/dabs"
fi

echo "Installed: $("$INSTALL_DIR/dabs" --help 2>&1 | head -1)"
echo "Start with: dabs recipes"
