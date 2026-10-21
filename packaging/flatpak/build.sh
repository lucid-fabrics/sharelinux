#!/bin/sh
# Builds dist/sharelinux.flatpak. Needs flatpak, flatpak-builder and Go. The GNOME 50
# SDK and the Go extension are used from whichever installation has them; when they are
# missing they are installed from Flathub into the user installation first.
set -eu
here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../.." && pwd)
app=io.github.sharelinux.ScreenSharing
work=${WORK:-$root/build/flatpak}

mkdir -p "$work" "$root/dist"
# The Flatpak build has no network: vendor the Go modules into a tarball first.
(cd "$root" && go mod vendor && tar -C vendor -cJf "$here/vendor.tar.xz" . && rm -rf vendor)

deps=
if ! flatpak info org.gnome.Sdk//50 >/dev/null 2>&1 || ! flatpak info org.freedesktop.Sdk.Extension.golang//25.08 >/dev/null 2>&1; then
	flatpak remote-add --user --if-not-exists flathub https://dl.flathub.org/repo/flathub.flatpakrepo
	deps="--user --install-deps-from=flathub"
fi

# shellcheck disable=SC2086
flatpak-builder --force-clean $deps \
	--repo="$work/repo" --state-dir="$work/state" "$work/build" "$here/$app.yml"
flatpak build-bundle "$work/repo" "$root/dist/sharelinux.flatpak" "$app"
rm -f "$here/vendor.tar.xz"
echo "$root/dist/sharelinux.flatpak"
