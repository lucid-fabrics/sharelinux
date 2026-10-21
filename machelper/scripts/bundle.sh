#!/bin/bash
# Builds "OSS Mouse Helper.app" into machelper/build/ and signs it AD-HOC.
# Installs nothing, launches nothing, requests no permission.
#
# Signing is ad-hoc on purpose and has no knob: no identity argument, no
# environment override. It is never signed with the Cendara team (8G389M2DZR)
# or a personal Developer ID. Ad-hoc code has no stable designated requirement,
# so macOS forgets an Accessibility grant on every rebuild (design 2.5); a
# self-signed certificate is the later, owner-made upgrade for a personal Mac.
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

# Ad-hoc signature (identity "-"), hardened runtime, stable identifier. The app
# needs no entitlements: posting CGEvents is a TCC grant, not an entitlement.
codesign --force --sign - --options runtime \
	--identifier io.github.sharelinux.mousehelper "$app"
codesign --verify --strict "$app"

# Refuse to hand over anything that is not ad-hoc and team-less.
info="$(codesign -dvv "$app" 2>&1)"
echo "$info" | grep -q '^Signature=adhoc' || { echo "bundle.sh: signature is not ad-hoc" >&2; exit 1; }
echo "$info" | grep -q '^TeamIdentifier=not set' || { echo "bundle.sh: a team identifier is set" >&2; exit 1; }
if echo "$info" | grep -qE '8G389M2DZR|KRPUAN3FFA|Developer ID'; then
	echo "bundle.sh: a real signing identity leaked into the build" >&2
	exit 1
fi

# The LaunchAgent plist, filled in for this build, NOT installed.
sed "s|__APP_EXECUTABLE__|$HOME/Applications/OSS Mouse Helper.app/Contents/MacOS/MouseHelper|" \
	"$here/Resources/io.github.sharelinux.mousehelper.plist" >"$out/io.github.sharelinux.mousehelper.plist"
plutil -lint "$out/io.github.sharelinux.mousehelper.plist" >/dev/null

echo "built: $app"
echo "LaunchAgent template (not installed): $out/io.github.sharelinux.mousehelper.plist"
echo "Copy the app to ~/Applications and the plist to ~/Library/LaunchAgents yourself when you are ready."
