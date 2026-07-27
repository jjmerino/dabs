#!/bin/sh
# dabs installer — https://dabs.dev
# Downloads a prebuilt release binary; nothing is compiled on your machine.
# The source of this script lives at https://github.com/jjmerino/dabs/blob/main/install.sh
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

# One downloader, picked once. Both forms write the URL to a file.
if command -v curl >/dev/null 2>&1; then
  downloader="curl"
elif command -v wget >/dev/null 2>&1; then
  downloader="wget"
else
  echo "Need curl or wget to download dabs." >&2
  exit 1
fi

fetch() { # fetch <url> <dest>
  case "$downloader" in
    curl) curl -fsSL -o "$2" "$1" ;;
    wget) wget -qO "$2" "$1" ;;
  esac || {
    echo "Could not download $1" >&2
    return 1
  }
}

# One checksum tool, picked once. A release publishes SHA256SUMS beside the
# binaries, and this script refuses to install anything it cannot check against
# it — a verification step that quietly does nothing is worse than none.
if command -v sha256sum >/dev/null 2>&1; then
  checksum() { sha256sum "$1" | cut -d' ' -f1; }
elif command -v shasum >/dev/null 2>&1; then
  checksum() { shasum -a 256 "$1" | cut -d' ' -f1; }
else
  echo "Need sha256sum or shasum to verify the download; refusing to install unverified." >&2
  echo "Install one of them (coreutils or perl), or build from source: https://github.com/$REPO" >&2
  exit 1
fi

# Where the binary goes, and whether reaching it needs sudo — settled before
# anything is downloaded, so an unusable destination fails in a second.
if [ ! -d "$INSTALL_DIR" ]; then
  echo "Install directory $INSTALL_DIR does not exist." >&2
  echo "Create it, or point DABS_INSTALL_DIR at a directory that exists." >&2
  exit 1
fi
if [ -w "$INSTALL_DIR" ]; then
  sudo_cmd=""
elif command -v sudo >/dev/null 2>&1; then
  sudo_cmd="sudo"
else
  echo "$INSTALL_DIR is not writable and sudo is not installed." >&2
  echo "Set DABS_INSTALL_DIR to a directory you can write, e.g. DABS_INSTALL_DIR=\$HOME/.local/bin" >&2
  exit 1
fi

# Resolve /releases/latest to the tag it currently points at, so the binary and
# the SHA256SUMS come from ONE release even if a new one publishes mid-install,
# and so the version can be reported truthfully at the end.
latest_tag() {
  case "$downloader" in
    curl) curl -fsSL -o /dev/null -w '%{url_effective}' "https://github.com/$REPO/releases/latest" ;;
    wget) wget -q -S --spider "https://github.com/$REPO/releases/latest" 2>&1 |
            sed -n 's#^ *[Ll]ocation: *\([^ ]*\).*#\1#p' | tail -1 ;;
  esac
}

tag="$(latest_tag | sed -n 's#.*/releases/tag/##p')"
if [ -z "$tag" ]; then
  echo "Could not resolve the latest dabs release; aborting." >&2
  exit 1
fi

echo "dabs $tag → $os/$arch"
tmp="$(mktemp -d)"
staged=""
cleanup() {
  rm -rf "$tmp"
  if [ -n "$staged" ]; then
    $sudo_cmd rm -f "$staged" 2>/dev/null || true
  fi
  return 0
}
trap cleanup EXIT

base="https://github.com/$REPO/releases/download/$tag"
fetch "$base/$asset" "$tmp/dabs"
fetch "$base/SHA256SUMS" "$tmp/SHA256SUMS"

want="$(awk -v a="$asset" '$2 == a { print $1 }' "$tmp/SHA256SUMS")"
if [ -z "$want" ]; then
  echo "No checksum for $asset in the $tag SHA256SUMS; aborting." >&2
  exit 1
fi

got="$(checksum "$tmp/dabs")"
if [ "$got" != "$want" ]; then
  echo "Checksum mismatch for $asset — refusing to install." >&2
  echo "  expected $want" >&2
  echo "  got      $got" >&2
  exit 1
fi

if [ -n "$sudo_cmd" ]; then
  echo "Installing to $INSTALL_DIR (needs sudo)…"
fi

# Copy into the destination directory first, then rename within it. A rename
# inside one directory is atomic, so an interrupted install leaves the previous
# dabs whole instead of half-overwritten by a cross-device copy.
staged="$INSTALL_DIR/dabs.tmp.$$"
$sudo_cmd cp "$tmp/dabs" "$staged"
# An explicit mode, not chmod +x: +x is umask-relative and under umask 077
# would install a binary only its owner can read or run.
$sudo_cmd chmod 755 "$staged"
$sudo_cmd mv "$staged" "$INSTALL_DIR/dabs"
staged=""

# Check the file that is now on disk, not merely that some dabs answers: a
# failed move would otherwise leave an older binary reporting success.
if [ ! -f "$INSTALL_DIR/dabs" ] || [ "$(checksum "$INSTALL_DIR/dabs")" != "$want" ]; then
  echo "$INSTALL_DIR/dabs is not the binary just downloaded — the install did not land." >&2
  exit 1
fi

echo "Installed $tag to $INSTALL_DIR/dabs"

# What the user's shell will actually run may be a different dabs, or none.
# Compared by content, so a symlinked or trailing-slash INSTALL_DIR that
# resolves to the same binary does not read as a mismatch.
found="$(command -v dabs 2>/dev/null || true)"
if [ -z "$found" ]; then
  echo "Note: $INSTALL_DIR is not on your PATH — run $INSTALL_DIR/dabs, or add the directory to PATH."
  echo "      An open shell may also have cached an older path; run: hash -r"
elif [ "$(checksum "$found" 2>/dev/null || true)" != "$want" ]; then
  echo "Note: typing dabs runs $found, not the one just installed."
  echo "      An open shell may also have cached an older path; run: hash -r"
fi

echo "Start with: dabs recipes"
