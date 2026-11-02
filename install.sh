#!/bin/sh
# Install ego-bridge from GitHub releases.
#
#   curl -fsSL https://raw.githubusercontent.com/walm/ego-bridge/main/install.sh | sh
#
# Environment:
#   EGO_BRIDGE_VERSION   release tag to install (default: latest)
#   EGO_BRIDGE_BIN_DIR   install directory (default: ~/.local/bin)
set -eu

REPO="walm/ego-bridge"
VERSION="${EGO_BRIDGE_VERSION:-latest}"
BIN_DIR="${EGO_BRIDGE_BIN_DIR:-$HOME/.local/bin}"

say() { printf '%s\n' "$*" >&2; }
die() { say "ego-bridge install: $*"; exit 1; }

case "$(uname -s)" in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) die "unsupported OS $(uname -s); ego-bridge supports Linux and macOS" ;;
esac
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) die "unsupported architecture $(uname -m)" ;;
esac

asset="ego-bridge-$os-$arch"
if [ "$VERSION" = latest ]; then
  base="https://github.com/$REPO/releases/latest/download"
else
  base="https://github.com/$REPO/releases/download/$VERSION"
fi

fetch() { # url dest
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL --retry 3 -o "$2" "$1"
  elif command -v wget >/dev/null 2>&1; then
    wget -q -O "$2" "$1"
  else
    die "need curl or wget"
  fi
}

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT INT TERM

say "downloading $asset ($VERSION)"
fetch "$base/$asset" "$tmp/$asset" || die "download failed: $base/$asset"
fetch "$base/checksums.txt" "$tmp/checksums.txt" || die "download failed: $base/checksums.txt"

expected="$(grep " $asset\$" "$tmp/checksums.txt" | cut -d' ' -f1)"
[ -n "$expected" ] || die "no checksum for $asset"
if command -v sha256sum >/dev/null 2>&1; then
  actual="$(sha256sum "$tmp/$asset" | cut -d' ' -f1)"
elif command -v shasum >/dev/null 2>&1; then
  actual="$(shasum -a 256 "$tmp/$asset" | cut -d' ' -f1)"
else
  die "need sha256sum or shasum to verify the download"
fi
[ "$expected" = "$actual" ] || die "checksum mismatch for $asset"

mkdir -p "$BIN_DIR"
chmod 0755 "$tmp/$asset"
mv -f "$tmp/$asset" "$BIN_DIR/ego-bridge"
say "installed $BIN_DIR/ego-bridge ($("$BIN_DIR/ego-bridge" version))"

case ":$PATH:" in
  *":$BIN_DIR:"*) ;;
  *) say "note: $BIN_DIR is not on PATH; add: export PATH=\"$BIN_DIR:\$PATH\"" ;;
esac

if [ "$os" = darwin ]; then
  say "next (on your Mac): ego-bridge serve --listen local   or   ego-bridge ssh <host>"
else
  say "next (on the agent machine): ego-bridge install   (see https://github.com/$REPO#manual-setup)"
fi
