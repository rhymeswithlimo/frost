#!/bin/sh
# Installs the latest frost release.
#
#   curl -fsSL https://raw.githubusercontent.com/rhymeswithlimo/frost/main/install/install.sh | sh
#
# Works on macOS, Linux (including WSL) and Windows through Git Bash, MSYS2
# or Cygwin. Downloads the prebuilt binary for your platform, checks that the
# release's checksums.txt was signed by the frost release key, checks the
# download against it, and puts it on your PATH.
#
# Environment:
#   FROST_VERSION      install this tag instead of the latest, e.g. v0.1.0
#   FROST_INSTALL_DIR  install here instead of picking a directory
#   FROST_BASE_URL     download from a mirror (expects the same file names)
set -eu

REPO="rhymeswithlimo/frost"

# Public key that signs every release's checksums.txt. Same as
# install/release-signing.pub. Written by scripts/release.sh --setup-key.
RELEASE_KEY="ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHO/g64dTbRW3poi7pdiPKgljYdHXG+TZeZQg4cfzAsV"

say() { printf '%s\n' "$*"; }
die() { printf 'install.sh: %s\n' "$*" >&2; exit 1; }
has() { command -v "$1" >/dev/null 2>&1; }

fetch() { # url dest
  if has curl; then
    curl -fsSL --retry 3 -o "$2" "$1"
  elif has wget; then
    wget -q -O "$2" "$1"
  else
    die "need curl or wget"
  fi
}

latest_version() {
  url="https://github.com/$REPO/releases/latest"
  if has curl; then
    final=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "$url")
  else
    final=$(wget -S --spider "$url" 2>&1 | sed -n 's/^ *Location: *//p' | tail -n 1)
  fi
  v="${final##*/}"
  case "$v" in
    v*) printf '%s' "$v" ;;
    *) die "couldn't find the latest release (set FROST_VERSION to pick one)" ;;
  esac
}

sha256() {
  if has sha256sum; then
    sha256sum "$1" | cut -d ' ' -f 1
  elif has shasum; then
    shasum -a 256 "$1" | cut -d ' ' -f 1
  else
    die "need sha256sum or shasum to verify the download"
  fi
}

# Platform.
case "$(uname -s)" in
  Darwin) os=darwin ;;
  Linux) os=linux ;; # includes WSL
  MINGW* | MSYS* | CYGWIN*) os=windows ;;
  *) die "unsupported OS: $(uname -s)" ;;
esac

case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  armv7*) arch=armv7 ;; # 32-bit ARM, e.g. older Raspberry Pi OS
  *) die "unsupported CPU architecture: $(uname -m)" ;;
esac

# On Apple Silicon, uname -m says x86_64 under Rosetta. Prefer native.
if [ "$os" = darwin ] && [ "$arch" = amd64 ] && [ "$(sysctl -n hw.optional.arm64 2>/dev/null || echo 0)" = 1 ]; then
  arch=arm64
fi

version="${FROST_VERSION:-}"
[ -n "$version" ] || version=$(latest_version)
num="${version#v}"

ext=tar.gz
bin=frost
if [ "$os" = windows ]; then
  ext=zip
  bin=frost.exe
fi
archive="frost_${num}_${os}_${arch}.${ext}"
base="${FROST_BASE_URL:-https://github.com/$REPO/releases/download/$version}"

# Download and verify.
tmp=$(mktemp -d 2>/dev/null || mktemp -d -t frost)
trap 'rm -rf "$tmp"' EXIT INT TERM

say "Downloading frost $version for $os/$arch"
fetch "$base/$archive" "$tmp/$archive" || die "download failed: $base/$archive"
fetch "$base/checksums.txt" "$tmp/checksums.txt" || die "download failed: $base/checksums.txt"

# The signature proves checksums.txt came from the frost release key, not
# just from whoever controls the download.
if [ -n "$RELEASE_KEY" ]; then
  fetch "$base/checksums.txt.sig" "$tmp/checksums.txt.sig" || die "download failed: $base/checksums.txt.sig"
  if has ssh-keygen; then
    printf 'frost-release %s\n' "$RELEASE_KEY" >"$tmp/allowed_signers"
    if out=$(ssh-keygen -Y verify -f "$tmp/allowed_signers" -I frost-release -n file \
      -s "$tmp/checksums.txt.sig" <"$tmp/checksums.txt" 2>&1); then
      say "Signature ok"
    else
      case "$out" in
        *"illegal option"* | *"unknown option"* | *"usage:"*)
          say "warning: this ssh-keygen is too old to check signatures (needs OpenSSH 8.1+), checking the checksum only"
          ;;
        *) die "checksums.txt isn't signed by the frost release key. Don't install this." ;;
      esac
    fi
  else
    say "warning: ssh-keygen not found, skipping the signature check"
  fi
fi

want=$(awk -v f="$archive" '$2 == f || $2 == "*"f { print $1 }' "$tmp/checksums.txt")
[ -n "$want" ] || die "$archive isn't listed in checksums.txt"
got=$(sha256 "$tmp/$archive")
[ "$want" = "$got" ] || die "checksum mismatch for $archive (expected $want, got $got)"
say "Checksum ok"

mkdir "$tmp/x"
if [ "$ext" = zip ]; then
  if has unzip; then
    unzip -q "$tmp/$archive" -d "$tmp/x"
  elif has powershell.exe; then
    powershell.exe -NoProfile -Command "Expand-Archive -Path '$(cygpath -w "$tmp/$archive" 2>/dev/null || echo "$tmp/$archive")' -DestinationPath '$(cygpath -w "$tmp/x" 2>/dev/null || echo "$tmp/x")'"
  else
    die "need unzip to extract $archive"
  fi
else
  tar -xzf "$tmp/$archive" -C "$tmp/x"
fi
[ -f "$tmp/x/$bin" ] || die "$bin not found in $archive"

# Pick where to install.
on_path() { case ":$PATH:" in *":$1:"*) return 0 ;; *) return 1 ;; esac; }

dir="${FROST_INSTALL_DIR:-}"
if [ -z "$dir" ]; then
  if [ "$os" != windows ] && [ -d /usr/local/bin ] && [ -w /usr/local/bin ]; then
    dir=/usr/local/bin
  elif [ "$os" = windows ]; then
    dir="$HOME/bin"
  else
    dir="$HOME/.local/bin"
  fi
fi

mkdir -p "$dir"
cp "$tmp/x/$bin" "$dir/$bin.tmp"
chmod 755 "$dir/$bin.tmp"
mv -f "$dir/$bin.tmp" "$dir/$bin"
say "Installed $dir/$bin"

if ! on_path "$dir"; then
  say ""
  say "$dir isn't on your PATH yet. Add it with:"
  case "${SHELL:-}" in
    */zsh) say "  echo 'export PATH=\"$dir:\$PATH\"' >> ~/.zshrc && exec zsh" ;;
    */fish) say "  fish_add_path $dir" ;;
    *) say "  echo 'export PATH=\"$dir:\$PATH\"' >> ~/.bashrc && exec bash" ;;
  esac
fi

say ""
say "Next: run 'frost init' to set up your first backup."
