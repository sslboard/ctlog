#!/bin/sh
# Install the latest ctlog release for Linux or macOS.
#
#   curl -fsSL https://raw.githubusercontent.com/sslboard/ctlog/main/install.sh | sh
#
# Options (environment variables):
#   CTLOG_VERSION       tag to install, for example v0.2.0 (default: latest)
#   CTLOG_INSTALL_DIR   target directory (default: /usr/local/bin when
#                       writable, otherwise ~/.local/bin)
#
# The archive is checked against the release's checksums.txt before anything
# is installed.

set -eu

REPO="sslboard/ctlog"

fail() {
	echo "ctlog install: $*" >&2
	exit 1
}

need() {
	command -v "$1" >/dev/null 2>&1 || fail "$1 is required"
}

need curl
need tar
need uname

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in
	linux | darwin) ;;
	*) fail "unsupported OS $os. Download a release from https://github.com/$REPO/releases" ;;
esac

arch=$(uname -m)
case "$arch" in
	x86_64 | amd64) arch=amd64 ;;
	arm64 | aarch64) arch=arm64 ;;
	*) fail "unsupported architecture $arch" ;;
esac

tag="${CTLOG_VERSION:-}"
if [ -z "$tag" ]; then
	# /releases/latest redirects to /releases/tag/<tag>.
	url=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/$REPO/releases/latest") ||
		fail "cannot reach GitHub"
	tag=${url##*/}
fi
case "$tag" in
	v[0-9]*) ;;
	*) fail "unexpected release tag '$tag'" ;;
esac
version=${tag#v}

archive="ctlog_${version}_${os}_${arch}.tar.gz"
base="https://github.com/$REPO/releases/download/$tag"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

echo "Downloading ctlog $tag for $os/$arch..." >&2
curl -fsSL -o "$tmp/$archive" "$base/$archive" || fail "download failed: $base/$archive"
curl -fsSL -o "$tmp/checksums.txt" "$base/checksums.txt" || fail "download failed: $base/checksums.txt"

expected=$(awk -v f="$archive" '$2 == f { print $1 }' "$tmp/checksums.txt")
[ -n "$expected" ] || fail "$archive is not listed in checksums.txt"
if command -v sha256sum >/dev/null 2>&1; then
	actual=$(sha256sum "$tmp/$archive" | awk '{ print $1 }')
elif command -v shasum >/dev/null 2>&1; then
	actual=$(shasum -a 256 "$tmp/$archive" | awk '{ print $1 }')
else
	fail "sha256sum or shasum is required to verify the download"
fi
[ "$expected" = "$actual" ] || fail "checksum mismatch for $archive"

tar -xzf "$tmp/$archive" -C "$tmp" ctlog

dir="${CTLOG_INSTALL_DIR:-}"
if [ -z "$dir" ]; then
	if [ -w /usr/local/bin ]; then
		dir=/usr/local/bin
	else
		dir="$HOME/.local/bin"
	fi
fi
mkdir -p "$dir"
install -m 0755 "$tmp/ctlog" "$dir/ctlog" 2>/dev/null || {
	cp "$tmp/ctlog" "$dir/ctlog"
	chmod 0755 "$dir/ctlog"
}

echo "Installed $("$dir/ctlog" version) to $dir/ctlog" >&2
case ":$PATH:" in
	*":$dir:"*) ;;
	*) echo "Add $dir to your PATH to run ctlog from anywhere." >&2 ;;
esac
