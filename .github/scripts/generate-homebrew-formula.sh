#!/usr/bin/env bash
# Generate the macOS Homebrew formula from a GoReleaser checksum file.
# The formula selects the prebuilt archive for the user's CPU architecture.
set -euo pipefail

if [ "$#" -ne 3 ]; then
  printf 'usage: %s VERSION SHA256SUMS OUTPUT\n' "$0" >&2
  exit 2
fi

version=${1#v}
# Validate a stable release version before using it in URLs and Ruby source.
if [[ ! "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$ ]]; then
  printf 'invalid release version %q\n' "$version" >&2
  exit 1
fi
sums=$2
output=$3
repo="yottadynamics/yottacode"
base="https://github.com/${repo}/releases/download/v${version}"

sha_for() {
  local archive=$1
  local sha
  sha=$(awk -v want="$archive" '$2 == want { if (found++) exit 2; print $1 }' "$sums") || {
    printf 'missing or duplicate checksum for %s\n' "$archive" >&2
    return 1
  }
  if [[ ! "$sha" =~ ^[0-9a-f]{64}$ ]]; then
    printf 'invalid checksum for %s\n' "$archive" >&2
    return 1
  fi
  printf '%s\n' "$sha"
}

arm_archive="yottacode_${version}_darwin_arm64.tar.gz"
intel_archive="yottacode_${version}_darwin_amd64.tar.gz"
arm_sha=$(sha_for "$arm_archive") || exit 1
intel_sha=$(sha_for "$intel_archive") || exit 1

mkdir -p "$(dirname "$output")"
cat >"$output" <<FORMULA
class Yottacode < Formula
  desc "Sovereign AI coding agent for your terminal"
  homepage "https://yottacode.ai"
  version "$version"

  on_macos do
    on_arm do
      url "$base/$arm_archive"
      sha256 "$arm_sha"
    end
    on_intel do
      url "$base/$intel_archive"
      sha256 "$intel_sha"
    end
  end

  def install
    bin.install "yottacode"
  end

  def caveats
    <<~EOS
      Run yottacode setup once to choose a model provider.
    EOS
  end
end
FORMULA
