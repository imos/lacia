#!/bin/sh
# Installs lacia from the latest release:
#
#   curl --proto '=https' --tlsv1.2 -fsSL https://raw.githubusercontent.com/imos/lacia/main/install.sh | sh
#
# It fetches the binary for this machine and SHA256SUMS from the latest
# release, checks the one against the other, and runs lacia setup of the
# binary, which puts it in place and asks the rest. Arguments go to lacia
# setup (sh -s -- --repo <URL>), and so do LACIA_GOOGLE_CLIENT_ID and
# LACIA_GOOGLE_CLIENT_SECRET, being in the environment. It asks nothing
# itself, needs no administrator, and writes nothing but a temporary
# directory; lacia setup does the rest (docs/design/install.md).
#
# LACIA_DOWNLOAD_URL replaces the place of the release, for a test against
# a release on this machine.

set -eu

# Everything is in a function called on the last line, so that a download
# cut short runs nothing.
main() {
	url=${LACIA_DOWNLOAD_URL:-https://github.com/imos/lacia/releases/latest/download}
	case $url in
	https://*) proto='=https' ;;
	http://127.0.0.1[:/]* | http://localhost[:/]*) proto='=http' ;;
	*) fail "LACIA_DOWNLOAD_URL には、https:// で始まる URL を指定してください。" ;;
	esac

	platform="$(uname -s)/$(uname -m)"
	case $platform in
	Darwin/arm64) asset=lacia-darwin-arm64 ;;
	*) fail "この端末（${platform}）向けのリリースはありません。本体をクローンして、make install で入れてください。手順は https://github.com/imos/lacia/blob/main/docs/install-guide.md にあります。" ;;
	esac

	dir=$(mktemp -d)
	trap 'rm -rf "$dir"' EXIT
	trap 'exit 1' HUP INT TERM
	fetch "$url/SHA256SUMS" "$dir/SHA256SUMS"
	fetch "$url/$asset" "$dir/lacia"
	want=$(awk -v name="$asset" '$2 == name || $2 == "*" name { print $1 }' "$dir/SHA256SUMS")
	got=$(sha256 "$dir/lacia")
	if [ -z "$want" ] || [ "$want" != "$got" ]; then
		fail "取得した $asset が、チェックサムの一覧と合いません。取得の途中で壊れたおそれがあります。もう1度実行してください。"
	fi
	chmod +x "$dir/lacia"
	"$dir/lacia" setup "$@"
}

fetch() {
	curl --proto "$proto" --proto-redir "$proto" --tlsv1.2 -fsSL -o "$2" "$1" ||
		fail "$1 を取得できませんでした。"
}

sha256() {
	if command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$1" | awk '{ print $1 }'
	else
		sha256sum "$1" | awk '{ print $1 }'
	fi
}

fail() {
	echo "lacia の導入を止めました: $1" >&2
	exit 1
}

main "$@"
