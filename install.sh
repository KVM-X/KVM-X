#!/usr/bin/env bash
# KVM-X installer.
#   curl -fsSL https://raw.githubusercontent.com/KVM-X/KVM-X/main/install.sh | bash
#
# Environment:
#   KVMX_VERSION  release tag to install (default: latest)
#   KVMX_PREFIX   install directory (default: /usr/local/bin, or ~/.local/bin without root/sudo)
#   KVMX_YES=1    do not ask before using sudo
set -euo pipefail

REPO="KVM-X/KVM-X"
VERSION="${KVMX_VERSION:-latest}"

say() { printf 'kvmx-install: %s\n' "$*"; }
die() { printf 'kvmx-install: error: %s\n' "$*" >&2; exit 1; }

[ "$(uname -s)" = "Linux" ] || die "KVM-X supports Linux only (this is $(uname -s))"
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) die "unsupported CPU architecture $(uname -m) (release binaries: amd64, arm64)" ;;
esac

if command -v curl >/dev/null 2>&1; then
  fetch() { curl -fsSL --retry 3 -o "$2" "$1"; }
elif command -v wget >/dev/null 2>&1; then
  fetch() { wget -q -O "$2" "$1"; }
else
  die "need curl or wget"
fi

if [ "$VERSION" = "latest" ]; then
  base="https://github.com/$REPO/releases/latest/download"
else
  base="https://github.com/$REPO/releases/download/$VERSION"
fi
bin_name="kvmx-linux-$arch"

# Choose the destination and whether it needs sudo.
dest="${KVMX_PREFIX:-}"
use_sudo=0
if [ -z "$dest" ]; then
  if [ "$(id -u)" -eq 0 ]; then
    dest=/usr/local/bin
  elif command -v sudo >/dev/null 2>&1; then
    dest=/usr/local/bin
    use_sudo=1
  else
    dest="$HOME/.local/bin"
  fi
elif [ "$(id -u)" -ne 0 ]; then
  # writable if the nearest existing ancestor is (the directory itself may not exist yet)
  probe="$dest"
  while [ ! -e "$probe" ] && [ "$probe" != "/" ]; do probe="$(dirname "$probe")"; done
  [ -w "$probe" ] || use_sudo=1
fi

sudo_note=""
[ "$use_sudo" = 1 ] && sudo_note="  (using sudo)"
say "will download  $base/$bin_name"
say "      verify    $base/SHA256SUMS"
say "      install   $dest/kvmx$sudo_note"
have_tty() { ( : </dev/tty ) 2>/dev/null; }
if [ "$use_sudo" = 1 ] && [ "${KVMX_YES:-0}" != 1 ]; then
  if have_tty; then
    printf 'kvmx-install: continue? [Y/n] ' >/dev/tty
    read -r ans </dev/tty || ans=n
    case "$ans" in "" | y | Y | yes | YES) ;; *) die "aborted" ;; esac
  else
    say "no terminal to confirm on; continuing (set KVMX_PREFIX to avoid sudo)"
  fi
fi

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
fetch "$base/$bin_name" "$tmp/$bin_name" || die "download failed (is there a release for $VERSION?)"
fetch "$base/SHA256SUMS" "$tmp/SHA256SUMS" || die "could not download SHA256SUMS"

expected="$(awk -v f="$bin_name" '$2 == f || $2 == "*" f {print $1}' "$tmp/SHA256SUMS")"
[ -n "$expected" ] || die "$bin_name not listed in SHA256SUMS"
if command -v sha256sum >/dev/null 2>&1; then
  actual="$(sha256sum "$tmp/$bin_name" | awk '{print $1}')"
elif command -v shasum >/dev/null 2>&1; then
  actual="$(shasum -a 256 "$tmp/$bin_name" | awk '{print $1}')"
else
  die "need sha256sum or shasum to verify the download"
fi
[ "$expected" = "$actual" ] || die "checksum mismatch (expected $expected, got $actual)"
say "checksum OK ($actual)"

chmod 0755 "$tmp/$bin_name"
if [ "$use_sudo" = 1 ]; then
  sudo install -D -m 0755 "$tmp/$bin_name" "$dest/kvmx"
else
  mkdir -p "$dest"
  install -m 0755 "$tmp/$bin_name" "$dest/kvmx"
fi
say "installed $("$dest/kvmx" version)"

case ":$PATH:" in
  *":$dest:"*) ;;
  *) say "note: $dest is not on your PATH" ;;
esac
cat <<MSG

Next steps:
  kvmx doctor          what this machine can do, and what to fix
  kvmx enable          show the plan, then apply reversible changes
  kvmx run --demo      boot a tiny test guest in the best available mode
Remove everything with:  kvmx uninstall --self
MSG
