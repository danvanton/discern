#!/bin/sh
# Downloads the pinned makemkv-oss tarball into the current folder, checks its
# SHA-256 against pin.env, and prints the file name. The Dockerfile builds
# from it and the release workflow publishes it, so both use this one script
# and always get the same file.
#
#   scripts/fetch-makemkv-oss.sh internal/makemkv/install/pin.env
set -eu

pin=$1
case $pin in
*/*) ;;
*) pin=./$pin ;; # ". file" without a slash would search PATH
esac
. "$pin"

f="makemkv-oss-${MAKEMKV_VERSION}.tar.gz"
curl -fsSL --proto '=https' -o "$f" "https://www.makemkv.com/download/$f"
echo "${MAKEMKV_OSS_SHA256}  $f" | sha256sum -c - >&2
echo "$f"
