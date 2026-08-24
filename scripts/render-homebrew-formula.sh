#!/usr/bin/env bash

set -euo pipefail

if [ "$#" -ne 3 ]; then
  echo "usage: $0 <tag> <checksums-file> <output-file>" >&2
  exit 1
fi

tag="$1"
checksums_file="$2"
output_file="$3"
version="${tag#v}"

if [[ "$tag" != v* || ! "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$ ]]; then
  echo "invalid release tag: $tag" >&2
  exit 1
fi
if [ ! -f "$checksums_file" ]; then
  echo "checksums file not found: $checksums_file" >&2
  exit 1
fi

checksum() {
  local artifact="$1"
  local value
  value="$(awk -v artifact="$artifact" '$2 == artifact { print $1 }' "$checksums_file")"
  if [[ ! "$value" =~ ^[0-9a-f]{64}$ ]]; then
    echo "missing or invalid checksum for $artifact" >&2
    exit 1
  fi
  printf '%s' "$value"
}

darwin_amd64="$(checksum "romty_${version}_darwin_amd64.tar.gz")"
darwin_arm64="$(checksum "romty_${version}_darwin_arm64.tar.gz")"
linux_amd64="$(checksum "romty_${version}_linux_amd64.tar.gz")"
linux_arm64="$(checksum "romty_${version}_linux_arm64.tar.gz")"

mkdir -p "$(dirname "$output_file")"
cat >"$output_file" <<EOF
class Romty < Formula
  desc "Persistent terminal workspace manager"
  homepage "https://github.com/nalbam/romty"
  version "$version"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/nalbam/romty/releases/download/v#{version}/romty_#{version}_darwin_arm64.tar.gz"
      sha256 "$darwin_arm64"
    else
      url "https://github.com/nalbam/romty/releases/download/v#{version}/romty_#{version}_darwin_amd64.tar.gz"
      sha256 "$darwin_amd64"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      url "https://github.com/nalbam/romty/releases/download/v#{version}/romty_#{version}_linux_arm64.tar.gz"
      sha256 "$linux_arm64"
    else
      url "https://github.com/nalbam/romty/releases/download/v#{version}/romty_#{version}_linux_amd64.tar.gz"
      sha256 "$linux_amd64"
    end
  end

  def install
    bin.install "romty"
  end
end
EOF
