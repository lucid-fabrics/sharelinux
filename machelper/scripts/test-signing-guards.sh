#!/bin/bash
# Checks that check-signing-identity.sh refuses every name it must refuse, without
# touching any keychain: the name rules only (--name-only).
set -uo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
bad=0
refuse() {
	if "$here/check-signing-identity.sh" "$1" --name-only >/dev/null 2>&1; then
		echo "FAIL: accepted '$1'" >&2
		bad=1
	fi
}
accept() {
	"$here/check-signing-identity.sh" "$1" --name-only >/dev/null 2>&1 || { echo "FAIL: refused '$1'" >&2; bad=1; }
}
accept "OSS Mouse Helper Local Signing"
accept "OSS Mouse Helper Local 2"
refuse ""
refuse "-"
refuse "Developer ID Application: Wassim Mehanna (KRPUAN3FFA)"
refuse "Apple Development: Created via API (9W74KCHBGG)"
refuse "Cendara Systems LLC (8G389M2DZR)"
refuse "OSS Mouse Helper Local Developer ID"
refuse "OSS Mouse Helper Local (8G389M2DZR)"
refuse "OSS Mouse Helper Local; rm -rf"
refuse "OSS Mouse Helper Local\$(id)"
refuse "oss mouse helper local"
[ "$bad" = 0 ] && echo "signing guards ok"
exit "$bad"
