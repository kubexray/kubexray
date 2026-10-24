#!/usr/bin/env bash
# Renders the Homebrew cask for a published release from its checksums.txt.
#   render.sh <version> <checksums.txt>
set -euo pipefail
version="$1"
sums="$2"
sha() {
  local s
  s="$(awk -v f="kubexray_${version}_$1.tar.gz" '$2 == f { print $1 }' "$sums")"
  [[ "$s" =~ ^[0-9a-f]{64}$ ]] || { echo "no checksum for $1 in $sums" >&2; exit 1; }
  echo "$s"
}
sed -e "s/@VERSION@/$version/" \
  -e "s/@SHA_DARWIN_AMD64@/$(sha darwin_amd64)/" -e "s/@SHA_DARWIN_ARM64@/$(sha darwin_arm64)/" \
  -e "s/@SHA_LINUX_AMD64@/$(sha linux_amd64)/" -e "s/@SHA_LINUX_ARM64@/$(sha linux_arm64)/" \
  "$(dirname "$0")/kubexray.rb.in"
