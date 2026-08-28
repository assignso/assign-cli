#!/bin/sh
# Installs a verified Assign CLI release into a user-owned directory.
set -eu

repo=assignso/assign-cli
install_dir=${ASSIGN_INSTALL_DIR:-"$HOME/.local/bin"}

case "$(uname -s)" in
  Darwin) os=darwin ;;
  Linux) os=linux ;;
  *) echo "Assign supports macOS and Linux through this installer." >&2; exit 1 ;;
esac
case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) echo "Unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac

version=${ASSIGN_VERSION:-}
if [ -z "$version" ]; then
  version=$(curl --proto '=https' --tlsv1.2 -fsSL "https://api.github.com/repos/$repo/releases/latest" | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1)
fi
case "$version" in
  ''|*[!0-9A-Za-z.-]*) echo "Could not determine a valid Assign release version." >&2; exit 1 ;;
esac

archive="assign_${version}_${os}_${arch}.tar.gz"
checksums="assign_${version}_SHA256SUMS"
base="https://github.com/$repo/releases/download/$version"
temp_dir=$(mktemp -d)
trap 'rm -rf "$temp_dir"' EXIT HUP INT TERM

curl --proto '=https' --tlsv1.2 -fsSL "$base/$archive" -o "$temp_dir/$archive"
curl --proto '=https' --tlsv1.2 -fsSL "$base/$checksums" -o "$temp_dir/$checksums"
expected=$(awk -v file="$archive" '$2 == file { print $1; exit }' "$temp_dir/$checksums")
case "$expected" in
  ??????*) ;;
  *) echo "Release checksum entry is missing for $archive." >&2; exit 1 ;;
esac
if command -v shasum >/dev/null 2>&1; then
  actual=$(shasum -a 256 "$temp_dir/$archive" | awk '{ print $1 }')
else
  actual=$(sha256sum "$temp_dir/$archive" | awk '{ print $1 }')
fi
test "$actual" = "$expected"
tar -xzf "$temp_dir/$archive" -C "$temp_dir"
test -f "$temp_dir/assign"
chmod 0755 "$temp_dir/assign"

if [ "$os" = darwin ]; then
  if ! spctl --assess --type execute "$temp_dir/assign" >/dev/null 2>&1; then
    echo "The Assign release is not accepted by macOS Gatekeeper. It must be Developer ID signed and notarized; installation stopped." >&2
    exit 1
  fi
fi

mkdir -p "$install_dir"
install -m 0755 "$temp_dir/assign" "$install_dir/assign"
echo "Installed assign $version to $install_dir/assign"
case ":$PATH:" in
  *":$install_dir:"*) ;;
  *) echo "Add $install_dir to your PATH, then run: assign login" ;;
esac
