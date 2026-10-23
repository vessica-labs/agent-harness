#!/bin/sh
set -eu

# Debian's packaged CLI lags upstream security fixes.
version=${1:-2.103.0}
arch=$(dpkg --print-architecture)
case "$version/$arch" in
  2.103.0/amd64) checksum=fdd768eb70f6409f80fcfa9f3a1c0adf0fc98d827ab3ce0bed2ac22762c5d786 ;;
  2.103.0/arm64) checksum=7f322df34e624d14f5ec0c4acb121fd6dfa14392fdbbd1066aa8e0a05ac3721e ;;
  *) echo 'GitHub CLI version/architecture requires a verified release checksum' >&2; exit 1 ;;
esac
archive=$(mktemp)
trap 'rm -f "$archive"' EXIT HUP INT TERM
curl --fail --location --silent --show-error \
  "https://github.com/cli/cli/releases/download/v${version}/gh_${version}_linux_${arch}.tar.gz" \
  --output "$archive"
printf '%s  %s\n' "$checksum" "$archive" | sha256sum --check
tar -xzf "$archive" -C /usr/local/bin --strip-components=2 "gh_${version}_linux_${arch}/bin/gh"
chmod 0755 /usr/local/bin/gh
gh --version
