#!/bin/sh
# Builds dist/sharelinux.flatpak. Needs flatpak, flatpak-builder, Go and the
# Flathub remote with org.gnome.Sdk//50 and org.freedesktop.Sdk.Extension.golang//25.08
# (flatpak-builder installs missing ones with --install-deps-from=flathub).
set -eu
here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../.." && pwd)
app=io.github.sharelinux.ScreenSharing
work=${WORK:-$root/build/flatpak}

mkdir -p "$work" "$root/dist"
# The Flatpak build has no network: vendor the Go modules into a tarball first.
(cd "$root" && go mod vendor && tar -C vendor -cJf "$here/vendor.tar.xz" . && rm -rf vendor)

flatpak-builder --force-clean --user --install-deps-from=flathub \
	--repo="$work/repo" --state-dir="$work/state" "$work/build" "$here/$app.yml"
flatpak build-bundle "$work/repo" "$root/dist/sharelinux.flatpak" "$app"
rm -f "$here/vendor.tar.xz"
echo "$root/dist/sharelinux.flatpak"
