#!/usr/bin/env bash
# Generate the macOS Homebrew formula from a GoReleaser checksum file.
# The formula selects the prebuilt archive for the user's CPU architecture.
set -euo pipefail

if [ "$#" -ne 3 ]; then
  printf 'usage: %s VERSION SHA256SUMS OUTPUT\n' "$0" >&2
  exit 2
fi

version=${1#v}
sums=$2
output=$3
repo="yottadynamics/yottacode"
base="https://github.com/${repo}/releases/download/v${version}"

sha_for() {
  local archive=$1
  awk -v want="$archive" '$2 == want { print $1; found = 1 } END { if (!found) exit 1 }' "$sums"
}

arm_archive="yottacode_${version}_darwin_arm64.tar.gz"
intel_archive="yottacode_${version}_darwin_amd64.tar.gz"
arm_sha=$(sha_for "$arm_archive") || {
  printf 'missing checksum for %s\n' "$arm_archive" >&2
  exit 1
}
intel_sha=$(sha_for "$intel_archive") || {
  printf 'missing checksum for %s\n' "$intel_archive" >&2
  exit 1
}

mkdir -p "$(dirname "$output")"
cat >"$output" <<'FORMULA'
class Yottacode < Formula
  desc "Sovereign AI coding agent for your terminal"
  homepage "https://yottacode.ai"
  version "__VERSION__"

  on_macos do
    on_arm do
      url "__BASE__/__ARM_ARCHIVE__"
      sha256 "__ARM_SHA__"
    end
    on_intel do
      url "__BASE__/__INTEL_ARCHIVE__"
      sha256 "__INTEL_SHA__"
    end
  end

  def install
    bin.install "yottacode"
  end

  def caveats
    <<~EOS
      Run `yottacode setup` once to choose a model provider.
    EOS
  end
end
FORMULA

sed -i \
  -e "s|__VERSION__|$version|g" \
  -e "s|__BASE__|$base|g" \
  -e "s|__ARM_ARCHIVE__|$arm_archive|g" \
  -e "s|__INTEL_ARCHIVE__|$intel_archive|g" \
  -e "s|__ARM_SHA__|$arm_sha|g" \
  -e "s|__INTEL_SHA__|$intel_sha|g" \
  "$output"
