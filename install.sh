#!/bin/sh
# dabs installer — https://dabs.dev
# Downloads a prebuilt release binary; nothing is compiled on your machine.
# The source of this script lives at https://github.com/jjmerino/dabs/blob/main/install.sh
set -eu

REPO="jjmerino/dabs"
# A user-owned directory: dabs needs no root. It drives docker, bwrap or
# apple's container as the invoking user and keeps its state in ~/.dabs.
if [ -n "${DABS_INSTALL_DIR:-}" ]; then
  INSTALL_DIR="$DABS_INSTALL_DIR"
elif [ -n "${HOME:-}" ]; then
  INSTALL_DIR="$HOME/.local/bin"
else
  echo "HOME is not set, so there is no default install directory." >&2
  echo "Set HOME, or name the directory: DABS_INSTALL_DIR=/path/to/bin" >&2
  exit 1
fi

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

# Settle the destination before anything is downloaded, so an unusable one
# fails in a second. ~/.local/bin frequently does not exist yet.
if ! mkdir -p "$INSTALL_DIR" 2>/dev/null; then
  echo "Could not create the install directory $INSTALL_DIR." >&2
  echo "Point DABS_INSTALL_DIR at a directory you can write, e.g. DABS_INSTALL_DIR=\$HOME/bin" >&2
  exit 1
fi
if [ ! -w "$INSTALL_DIR" ]; then
  echo "The install directory $INSTALL_DIR is not writable by $(id -un 2>/dev/null || echo "this user")." >&2
  echo "Point DABS_INSTALL_DIR at a directory you can write, e.g. DABS_INSTALL_DIR=\$HOME/bin" >&2
  exit 1
fi
# Absolute and without a trailing slash, so every path printed from here reads
# cleanly and the PATH line offered at the end is one a shell can use. An empty
# CDPATH keeps cd from resolving a relative name against some other tree, and
# from echoing where it landed into this substitution; -- keeps a leading-dash
# name an operand. A directory can pass the two checks above and still not be
# enterable: mode 600 is writable but not searchable.
if ! resolved="$(CDPATH='' cd -- "$INSTALL_DIR" 2>/dev/null && pwd)"; then
  echo "Could not enter the install directory $INSTALL_DIR." >&2
  echo "A directory needs execute permission to be entered; check its mode, or point" >&2
  echo "DABS_INSTALL_DIR at another directory, e.g. DABS_INSTALL_DIR=\$HOME/bin" >&2
  exit 1
fi
INSTALL_DIR="$resolved"

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
    rm -f "$staged" || true
  fi
  return 0
}
trap cleanup EXIT
# dash and ash run the EXIT trap only on exit, not on a signal, so an
# interrupted install would leave the staging file behind. Turn each signal
# into an exit.
trap 'exit 130' INT
trap 'exit 143' TERM
trap 'exit 129' HUP

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

# Copy into the destination directory first, then rename within it. A rename
# inside one directory is atomic, so an interrupted install leaves the previous
# dabs whole instead of half-overwritten by a cross-device copy.
staged="$INSTALL_DIR/dabs.tmp.$$"
# rm -f first: cp opens an existing path through a symlink, so without it a
# link left at the staging path would send these bytes, and this mode, wherever
# it points. Deleting the path first means cp always creates a fresh regular
# file. The mv is a rename within one directory, which never follows a link.
rm -f "$staged"
cp "$tmp/dabs" "$staged"
# An explicit mode, not chmod +x, which is umask-relative and under umask 077
# would install a binary only its owner can read or run.
chmod 755 "$staged"
mv "$staged" "$INSTALL_DIR/dabs"
staged=""

# Check the file that is now on disk, not merely that some dabs answers: a
# failed move would otherwise leave an older binary reporting success.
if [ ! -f "$INSTALL_DIR/dabs" ] || [ "$(checksum "$INSTALL_DIR/dabs")" != "$want" ]; then
  echo "$INSTALL_DIR/dabs is not the binary just downloaded — the install did not land." >&2
  exit 1
fi

echo "Installed $tag to $INSTALL_DIR/dabs"

# Where the user's shell keeps its PATH, for the line they need to add.
# SHELL is unset in a container build, where there is no login shell at all.
user_shell="${SHELL:-}"
case "${user_shell##*/}" in
  zsh)  profile="~/.zshrc" ;;
  bash) profile="~/.bashrc" ;;
  fish) profile="" ;;
  *)    profile="your shell profile" ;;
esac

path_line() {
  if [ -z "$profile" ]; then
    echo "    fish_add_path $INSTALL_DIR"
  else
    echo "    export PATH=\"$INSTALL_DIR:\$PATH\""
    echo "  Add that line to $profile to keep it."
  fi
}

# What the user's shell will actually run may be a different dabs, or none.
# Compared by content, so a symlinked or trailing-slash INSTALL_DIR that
# resolves to the same binary does not read as a mismatch.
found="$(command -v dabs 2>/dev/null || true)"
if [ -z "$found" ]; then
  echo
  echo "$INSTALL_DIR is not on your PATH, so typing dabs will not find it yet."
  echo "  Fix it for this shell:"
  path_line
elif [ "$(checksum "$found" 2>/dev/null || true)" != "$want" ]; then
  echo
  echo "Careful: typing dabs does NOT run what was just installed."
  echo "  typing dabs runs:  $found  (an older or different dabs, earlier in PATH)"
  echo "  just installed:    $INSTALL_DIR/dabs  ($tag)"
  echo "  Remove $found, or put $INSTALL_DIR ahead of it:"
  path_line
  echo "  Then run: hash -r   (shells cache the path of a command they have run)"
fi

echo "Start with: dabs recipes"
