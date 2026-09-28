#!/bin/sh
set -eu

dist=${1:-dist}
max_bytes=$((50 * 1024 * 1024))
count=0

for archive in "$dist"/assign_*_darwin_*.tar.gz "$dist"/assign_*_linux_*.tar.gz "$dist"/assign_*_windows_*.zip; do
  [ -f "$archive" ] || continue
  count=$((count + 1))
  bytes=$(wc -c < "$archive" | tr -d ' ')
  if [ "$bytes" -gt "$max_bytes" ]; then
    echo "$(basename "$archive") is $bytes bytes; the release ceiling is $max_bytes bytes" >&2
    exit 1
  fi
  case "$archive" in
    *.zip)
      contents=$(unzip -Z1 "$archive")
      printf '%s\n' "$contents" | grep -Eq '(^|/)assign\.exe$'
      printf '%s\n' "$contents" | grep -Eq '(^|/)assign-discuss\.exe$'
      ;;
    *)
      contents=$(tar -tzf "$archive")
      printf '%s\n' "$contents" | grep -Eq '(^|/)assign$'
      printf '%s\n' "$contents" | grep -Eq '(^|/)assign-discuss$'
      ;;
  esac
  test -s "$archive.sbom.json"
  printf '%s\t%s bytes\tSBOM verified\n' "$(basename "$archive")" "$bytes"
done

test "$count" -eq 6 || { echo "expected six release archives, found $count" >&2; exit 1; }

if [ "$(uname -s)" = Linux ] && { [ "$(uname -m)" = x86_64 ] || [ "$(uname -m)" = amd64 ]; }; then
  stage="$dist/.installed-package-smoke"
  trap 'rm -rf "$stage"' EXIT HUP INT TERM
  mkdir -p "$stage"
  native_archive=$(find "$dist" -maxdepth 1 -type f -name 'assign_*_linux_amd64.tar.gz' -print | head -n 1)
  test -n "$native_archive"
  tar -xzf "$native_archive" -C "$stage"
  test -x "$stage/assign"
  test -x "$stage/assign-discuss"
  "$stage/assign" version | grep -Eq '^assign [^[:space:]]+'
  if "$stage/assign-discuss" >"$stage/sidecar.out" 2>"$stage/sidecar.err"; then
    echo "the Discuss companion unexpectedly started without its parent session" >&2
    exit 1
  fi
  grep -q '^assign-discuss: Missing terminal Discuss broker$' "$stage/sidecar.err"
  rm -rf "$stage"
  trap - EXIT HUP INT TERM
  echo "linux/amd64 installed-package launch verified"
fi
