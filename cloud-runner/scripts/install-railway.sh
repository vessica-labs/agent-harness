#!/bin/sh
set -eu

# Keep the CLI contract unchanged while avoiding the npm installer's tar dependency.
version=${1:-5.41.2}
case "$version/$(dpkg --print-architecture)" in
  5.41.2/amd64)
    target=x86_64-unknown-linux-gnu
    checksum=c84a196f9c0f18e71fafdd3e3ee0a0530028f56076fed18458803ce8e7ea65e1
    ;;
  5.41.2/arm64)
    target=aarch64-unknown-linux-musl
    checksum=884d2d9bb739a21b8fdc62f1092a2a94f6fb814d262c46b64ac1227f2632a6c2
    ;;
  *)
    echo 'Railway CLI version/architecture requires a verified release checksum' >&2
    exit 1
    ;;
esac
archive=$(mktemp)
trap 'rm -f "$archive"' EXIT HUP INT TERM
curl --fail --location --silent --show-error \
  "https://github.com/railwayapp/cli/releases/download/v${version}/railway-v${version}-${target}.tar.gz" \
  --output "$archive"
printf '%s  %s\n' "$checksum" "$archive" | sha256sum --check
tar -xzf "$archive" -C /usr/local/bin railway
chmod 0755 /usr/local/bin/railway
railway --version
