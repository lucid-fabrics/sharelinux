#!/bin/sh
# Builds ~/oss-probe/DeltaLab.app only. Installs nothing else.
set -e
here=$(cd "$(dirname "$0")" && pwd)
app="$HOME/oss-probe/DeltaLab.app"
mkdir -p "$app/Contents/MacOS"
swiftc -O -o "$app/Contents/MacOS/DeltaLab" "$here/deltalab.swift" -framework AppKit -framework CoreGraphics -framework GameController
cp "$here/Info.plist" "$app/Contents/Info.plist"
codesign --force --sign - "$app"
echo "built $app"
