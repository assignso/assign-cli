#!/bin/sh
set -eu

cd "$(dirname "$0")"
rm -rf build
mkdir -p build

build() {
  os="$1"
  arch="$2"
  target="$3"
  suffix="$4"
  directory="build/${os}_${arch}"
  mkdir -p "$directory"
  bun build ./src/index.tsx --compile --minify --target="$target" --outfile "$directory/assign-discuss$suffix"
}

build darwin amd64 bun-darwin-x64 ""
build darwin arm64 bun-darwin-arm64 ""
build linux amd64 bun-linux-x64-baseline ""
build linux arm64 bun-linux-arm64 ""
build windows amd64 bun-windows-x64-baseline .exe
build windows arm64 bun-windows-arm64 .exe
