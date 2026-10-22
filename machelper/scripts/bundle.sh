#!/bin/bash
# Builds "OSS Mouse Helper.app" into machelper/build/ and signs it.
# Installs nothing, launches nothing, requests no permission.
#
# Signed ad-hoc and nothing else (design 2.5). An ad-hoc build has no stable
# designated requirement, so macOS treats every rebuild as a different program:
# Accessibility and Keychain access are asked for again. Approve those prompts
# only right after you rebuilt the helper yourself, and never answer "Always
# Allow" for anything else. There is deliberately no local signing identity:
# any program of the same user could use it to sign a binary that passes the
# designated requirement. Never the Cendara team (8G389M2DZR), a personal
# Developer ID or any Apple certificate: the signed result is checked below.
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
# Ad-hoc signature (identity "-"), hardened runtime, stable identifier. The app
# needs no entitlements: posting CGEvents is a TCC grant, not an entitlement.
codesign --force --sign - --options runtime --identifier "$identifier" "$app"
codesign --verify --strict "$app"
info="$(codesign -dvv "$app" 2>&1)"
echo "$info" | grep -q '^Signature=adhoc' || { echo "bundle.sh: signature is not ad-hoc" >&2; exit 1; }
echo "$info" | grep -q '^TeamIdentifier=not set' || { echo "bundle.sh: a team identifier is set" >&2; exit 1; }
# No real signing identity may be in the result.
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
