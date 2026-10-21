#!/bin/bash
# Builds "OSS Mouse Helper.app" into machelper/build/ and signs it.
# Installs nothing, launches nothing, requests no permission.
#
# Two ways to sign, nothing else:
#
#  * Default: ad-hoc. No stable designated requirement, so macOS treats every
#    rebuild as a different program: the Keychain asks again for the helper's
#    items and the Accessibility grant is lost (design 2.5). Do NOT answer
#    "Always Allow": that would hand the item to whatever binary asked. After
#    a rebuild, remove the helper's Keychain items and pair again instead.
#  * OSS_LOCAL_SIGNING_IDENTITY="OSS Mouse Helper Local Signing": a self-signed
#    identity the owner made once with make-local-signing-identity.sh. The
#    designated requirement is pinned to that certificate's hash, so rebuilds
#    keep their Keychain items and grant without any prompt.
#
# Never the Cendara team (8G389M2DZR), a personal Developer ID or any Apple
# certificate: check-signing-identity.sh refuses every name that is not an
# "OSS Mouse Helper Local ..." self-signed certificate, and the signed result
# is checked below.
set -euo pipefail

here="$(cd "$(dirname "$0")/.." && pwd)"
out="$here/build"
app="$out/OSS Mouse Helper.app"
config="${CONFIGURATION:-release}"

cd "$here"
swift build -c "$config" --product MouseHelper
bin="$(swift build -c "$config" --show-bin-path)/MouseHelper"
[ -x "$bin" ] || { echo "bundle.sh: no executable at $bin" >&2; exit 1; }

rm -rf "$app"
mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources"
cp "$bin" "$app/Contents/MacOS/MouseHelper"
cp "$here/Resources/Info.plist" "$app/Contents/Info.plist"
plutil -lint "$app/Contents/Info.plist" >/dev/null

identifier=io.github.sharelinux.mousehelper
identity="${OSS_LOCAL_SIGNING_IDENTITY:-}"
if [ -n "$identity" ]; then
	sha="$("$here/scripts/check-signing-identity.sh" "$identity")"
	# Hardened runtime, stable identifier, requirement pinned to this one certificate.
	codesign --force --sign "$sha" --options runtime --identifier "$identifier" \
		-r="designated => identifier \"$identifier\" and certificate leaf = H\"$sha\"" "$app"
	codesign --verify --strict "$app"
	info="$(codesign -dvv "$app" 2>&1)"
	echo "$info" | grep -q '^Signature=adhoc' && { echo "bundle.sh: signature is ad-hoc, not the local identity" >&2; exit 1; }
	echo "$info" | grep -q '^TeamIdentifier=not set' || { echo "bundle.sh: a team identifier is set" >&2; exit 1; }
	echo "$info" | grep -q "^Authority=$identity\$" || { echo "bundle.sh: not signed by '$identity'" >&2; exit 1; }
	req="$(codesign -d -r- "$app" 2>&1)"
	echo "$req" | grep -q "certificate leaf = H\"$sha\"" || { echo "bundle.sh: designated requirement is not pinned to the certificate" >&2; exit 1; }
	echo "$req" | grep -q 'anchor apple' && { echo "bundle.sh: requirement names Apple" >&2; exit 1; }
else
	# Ad-hoc signature (identity "-"), hardened runtime, stable identifier. The app
	# needs no entitlements: posting CGEvents is a TCC grant, not an entitlement.
	codesign --force --sign - --options runtime --identifier "$identifier" "$app"
	codesign --verify --strict "$app"
	info="$(codesign -dvv "$app" 2>&1)"
	echo "$info" | grep -q '^Signature=adhoc' || { echo "bundle.sh: signature is not ad-hoc" >&2; exit 1; }
	echo "$info" | grep -q '^TeamIdentifier=not set' || { echo "bundle.sh: a team identifier is set" >&2; exit 1; }
fi
# Whatever the way, no real signing identity may be in the result.
if echo "$info" | grep -qE '8G389M2DZR|KRPUAN3FFA|Developer ID|Apple Development'; then
	echo "bundle.sh: a real signing identity leaked into the build" >&2
	exit 1
fi

# The LaunchAgent plist, filled in for this build, NOT installed.
"$here/scripts/fill-launchagent.sh" "$here/Resources/io.github.sharelinux.mousehelper.plist" \
	"$HOME/Applications/OSS Mouse Helper.app/Contents/MacOS/MouseHelper" "$out/io.github.sharelinux.mousehelper.plist"
plutil -lint "$out/io.github.sharelinux.mousehelper.plist" >/dev/null

echo "built: $app"
echo "LaunchAgent template (not installed): $out/io.github.sharelinux.mousehelper.plist"
echo "Copy the app to ~/Applications and the plist to ~/Library/LaunchAgents yourself when you are ready."
