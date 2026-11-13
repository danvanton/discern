#!/bin/sh
# Pins Discern to a MakeMKV release: writes internal/makemkv/install/pin.env
# and saves MakeMKV's signed checksum file next to it, after checking the
# signature with the pinned key (decision D14).
#
#   scripts/update-makemkv-pin.sh           the newest release on makemkv.com
#   scripts/update-makemkv-pin.sh 2.0.0     a given release
#
# Run it from the repository root. It needs curl and gpgv. The weekly
# workflow .github/workflows/makemkv-update.yml runs it and opens a pull
# request when the pin changes.
set -eu

dir=internal/makemkv/install
base=https://www.makemkv.com/download

version=${1:-}
if [ -z "$version" ]; then
	# The download page links the newest release's checksum file.
	version=$(curl -fsSL --proto '=https' "$base/" |
		grep -o 'makemkv-sha-[0-9]*\.[0-9]*\.[0-9]*\.txt' | head -n 1 |
		sed 's/^makemkv-sha-//; s/\.txt$//')
	if [ -z "$version" ]; then
		echo "no checksum file link found on $base/" >&2
		exit 1
	fi
fi
if ! echo "$version" | grep -Eqx '[0-9]+\.[0-9]+\.[0-9]+'; then
	echo "not a version like 2.0.0: $version" >&2
	exit 1
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
mkdir "$tmp/gnupg"
chmod 700 "$tmp/gnupg"

curl -fsSL --proto '=https' -o "$tmp/sha.txt" "$base/makemkv-sha-$version.txt"

# --output writes only the signed text: lines added outside the signed block
# would still give "Good signature", so the hashes are read from this output.
if ! GNUPGHOME="$tmp/gnupg" gpgv --status-fd 3 --keyring "./$dir/makemkv-signing-key.gpg" \
	--output "$tmp/signed.txt" "$tmp/sha.txt" 3>"$tmp/status" 2>"$tmp/gpgv.log"; then
	cat "$tmp/gpgv.log" >&2
	echo "makemkv-sha-$version.txt: the signature does not verify with the pinned key" >&2
	exit 1
fi
fpr=$(tr -d '[:space:]' <"$dir/makemkv-signing-key.fingerprint")
if ! awk -v fpr="$fpr" '$1 == "[GNUPG:]" && $2 == "VALIDSIG" && toupper($NF) == fpr { ok = 1 } END { exit !ok }' "$tmp/status"; then
	echo "makemkv-sha-$version.txt is not signed by the pinned key $fpr" >&2
	exit 1
fi

hash() {
	awk -v f="$1" '$2 == f { print $1 }' "$tmp/signed.txt"
}
bin=$(hash "makemkv-bin-$version.tar.gz")
oss=$(hash "makemkv-oss-$version.tar.gz")
for h in "$bin" "$oss"; do
	if ! echo "$h" | grep -Eqx '[0-9a-f]{64}'; then
		echo "makemkv-sha-$version.txt does not list both Linux tarballs" >&2
		exit 1
	fi
done

{
	grep '^#' "$dir/pin.env"
	echo "MAKEMKV_VERSION=$version"
	echo "MAKEMKV_BIN_SHA256=$bin"
	echo "MAKEMKV_OSS_SHA256=$oss"
} >"$tmp/pin.env"
cp "$tmp/pin.env" "$dir/pin.env"
cp "$tmp/sha.txt" "$dir/makemkv-sha.txt"
echo "pinned MakeMKV $version"
