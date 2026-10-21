# Open Screen Sharing

![Open Screen Sharing on iPad connected to macOS and vphone-cli](assets/readme/hero.png)

Open Screen Sharing aims to develop superior servers and clients based on the private protocol used by Apple VNC (macOS's Screen Sharing server and ScreenSharing.app).

## Installation

### iOS

[![Download on the App Store](assets/readme/app-store-badge.svg)](https://apps.apple.com/us/app/open-screen-sharing/id6818992574)

The App Store version is **\$1 through October 31, 2026**, then **\$2**. Your purchase supports my work.

The App Store version excludes the following features due to App Store restrictions:

* **Apple VNC server:** relies on background keep-alive mechanisms and a lockdown exploit.
* **Dragging files directly from a remote Mac into other iOS/iPadOS apps:** relies on a private API.

To use these features, sideload the app by adding the source to SideStore or AltStore:

```
https://github.com/nekohasekai/open-screen-sharing/releases/latest/download/altstore.json
```

Or download [OpenScreenSharing.ipa](https://github.com/nekohasekai/open-screen-sharing/releases/latest/download/OpenScreenSharing.ipa) and sideload it.

### macOS

Download and install [OpenScreenSharing.pkg](https://github.com/nekohasekai/open-screen-sharing/releases/latest/download/OpenScreenSharing.pkg).

### Nightly builds

You can download and install nightly builds from the [Nightly release](https://github.com/nekohasekai/open-screen-sharing/releases/tag/nightly). For AltStore or SideStore on iOS, the source URL is:

```
https://github.com/nekohasekai/open-screen-sharing/releases/download/nightly/altstore.json
```

You can also switch to the Nightly track in the App settings of the macOS app and update directly.

## Features

1. Compared to the original ScreenSharing.app:

* Optimized for local networks
* Optimized for public networks (runs smoothly with Tailscale P2P)
* Fixed language switching
* Exclusive mouse support
* Debug HUD support
* Interactions optimized for iOS and iPadOS

2. Apple VNC server implementation on iOS and iPadOS

* Requires iOS 27 (Xcode DeviceHub); no jailbreak or TrollStore required (though sideloading is obviously necessary)
* Based on the iOS developer debugging protocol (requires [LocalDevVPN](https://apps.apple.com/app/id6755608044), or [sing-box](https://sing-box.sagernet.org) with `loopback_address`; For reference, an example of a usable configuration: [LocalDevVPN.json](examples/sing-box-loopback-example/LocalDevVPN.json))
* It can be connected to using the native macOS ScreenSharing.app (note: due to an Apple bug, you must go to Settings > Quality and disable "Full Quality" before adding the configuration)
* Our client is optimized for it

### Linux

`cmd/sharelinux` is a native GNOME app (GTK 4 and libadwaita) for the built-in macOS Screen Sharing server. It lists your saved Macs and the Macs it finds on the network, signs in with an account on the Mac (or a VNC password), and shows the Mac's screen with keyboard, mouse, clipboard (text and images) and file transfer.

Build dependencies:

* Go 1.26.8 or newer and a C compiler (the GTK binding uses cgo)
* GTK **4.16** or newer (4.20 draws software-decoded video from planar YUV; older versions convert it with swscale) and libadwaita **1.7** or newer, with their development files, plus `libgirepository1.0-dev` (gotk4 needs it), `libx11-dev`, `libxfixes-dev`, `libxi-dev`, `libwayland-dev`, `libavcodec-dev`, `libswscale-dev`, `libva-dev`, `libepoxy-dev` and `pkg-config`. Ubuntu 26.04 and Debian 14: `apt install libgtk-4-dev libadwaita-1-dev libgirepository1.0-dev libx11-dev libxfixes-dev libxi-dev libwayland-dev libavcodec-dev libswscale-dev libva-dev libepoxy-dev`
* A Secret Service (GNOME Keyring, KWallet) if you want saved passwords; without one connections still save, the password is typed each time
* Avahi or any mDNS-capable network for the "On this network" list (the viewer browses `_rfb._tcp` itself, it does not need the Avahi daemon)

```
go build -o sharelinux ./cmd/sharelinux
./sharelinux user@mac.local            # or: ./sharelinux for the list of Macs
make install-linux                        # icon and desktop entry under ~/.local/share
```

#### Flatpak

To run it on any Linux machine without installing the build dependencies, build the Flatpak once and install the single-file bundle. This needs `flatpak`, `flatpak-builder` and Go on the build machine (`apt install flatpak flatpak-builder`, then `flatpak remote-add --user --if-not-exists flathub https://dl.flathub.org/repo/flathub.flatpakrepo`); the build itself runs offline from vendored Go modules and pulls the GNOME 50 runtime and Go extension from Flathub on first use.

```
packaging/flatpak/build.sh               # writes dist/sharelinux.flatpak
flatpak install --user dist/sharelinux.flatpak
flatpak run io.github.sharelinux.ScreenSharing [user@mac.local]
```

HEVC needs the runtime's `org.freedesktop.Platform.codecs-extra//25.08-extra` extension (the runtime's own libavcodec has no HEVC), and VA-API on an Intel GPU needs `org.freedesktop.Platform.VAAPI.Intel//25.08`; flatpak installs both along with the app when they apply. If one is missing, `flatpak install flathub org.freedesktop.Platform.codecs-extra//25.08-extra org.freedesktop.Platform.VAAPI.Intel//25.08`. The preferences say what the viewer found.

The sandbox gets the network (the Mac, and the mDNS browse), Wayland and X11, the GPU, the Secret Service for saved passwords, and the Downloads folder (the default for "Files from the Mac"). Files you send to the Mac come from the file chooser or a drag, and another download folder is picked through the file chooser, so no other folder is opened up. `flatpak info --show-permissions io.github.sharelinux.ScreenSharing` lists them.

#### Video stream

When the Mac offers its media stream (the one the Apple apps call High Performance), the viewer asks for HEVC and decodes it with libavcodec instead of drawing picture tiles: far less CPU and network when the screen moves. It picks the decoder by probing the machine: VA-API on the GPU (Intel with `intel-media-va-driver`, AMD with Mesa's radeonsi), software libavcodec otherwise, and the picture tiles when the Mac has no media stream or this machine cannot decode HEVC. The stats card (Ctrl+Alt+Shift+H) names the decoder (`HEVC (VA-API)`, `HEVC (software)`), how long a picture took to decode, how it reached the screen and how many were dropped.

A GPU picture is exported as a DMA-BUF and wrapped in a `GdkDmabufTexture`, drawn inside a `GtkGraphicsOffload` so a Wayland compositor can scan it out directly. That only happens when a GPU draws the window; under a software renderer (llvmpipe, Cairo) the picture is read back into a memory texture instead, and the stats card says `GPU copy`. The Mac's stream carries one display, so with several displays shown together (the default) the tiles stay in charge; choose one display in the Display menu, or `-display ID`, to use the video. `-video auto|always|software|off` (or `SHARELINUX_VIDEO`, or Preferences, Video) chooses what is used: `auto` takes the video unless the window is drawn by the CPU (llvmpipe, Cairo) and the picture is bigger than about 2.2 megapixels, where repainting the whole picture 60 times a second costs several cores and the tiles are cheaper; `always` forces the video; `software` keeps the GPU decoder out; `off` keeps the tiles. `SHARELINUX_VAAPI_DEVICE=/dev/dri/renderD129` names the VA-API node on a machine with several GPUs, `SHARELINUX_THREADS` sets the software decoder's threads, `SHARELINUX_DMABUF=0|1` forces the read back or the DMA-BUF path, and `SHARELINUX_OFFLOAD=0` turns the offload off. `mockapple -video` (in `cmd/sharelinux/internal/mockapple`) plays a canned HEVC clip as the Mac's stream, to try all of this without a Mac.

#### Using it

HDR: with **Preferences, Displays, HDR** on (or `-hdr`, or X in a session) the viewer offers the Mac HEVC 4:4:4 10-bit video and the Mac answers with PQ (P3 primaries, full range) from a virtual display; a physical display of the Mac stays standard range. The switch is off, with the reason under it, unless the GPU decodes 4:4:4 10-bit (VA-API `VAProfileHEVCMain444_10`; the probe decodes a tiny stream of the profile at start, and software decoding is not enough for a big picture), the session is Wayland, the compositor has `wp_color_manager_v1` and the screen the window is on really shows HDR (GNOME and KDE offer the color manager on standard range screens too, so the viewer reads the screen's image description: PQ or HLG, or a peak luminance well above the reference white; a screen it cannot read counts as standard range). GTK color management is opt-in, so when all of that holds the viewer starts with `GDK_DEBUG=color-mgmt` (and `GSK_RENDERER=gl` unless you set one) and takes both out of its environment again once the window is up, so nothing it launches inherits them; a switch turned on in a running window needs a restart. The screen is read again before each connection. Moving the window to another screen does not change a running session, because the Mac sends HDR or not from the start: the viewer says so and an HDR session needs a reconnect to begin on an HDR screen. The stats card then says `HEVC 4:4:4 10-bit HDR (PQ, VA-API)`. Pictures reach GTK as three 10-bit planes with the stream's color state, or, for a big picture (more than 2.2 megapixels) decoded on an Intel GPU, as a P010 DMA-BUF the GPU's video processor makes (4:2:0 chroma; `SHARELINUX_HDR_GPU=on|off` overrides). On a screen that cannot show HDR, GTK converts the picture to standard range.

Open **Preferences** (Ctrl+comma) for quality, the Command key (Super or Alt), natural scrolling, the clipboard and the download folder. In a session, **Exclusive** mode gives the keyboard (including Super and Alt+Tab) to the Mac and locks the pointer to the window, so the Mac's pointer keeps moving past the window edge (Wayland compositors with pointer-constraints and relative-pointer, and X11 with XInput 2); press Ctrl+Alt+Esc to get them back. Losing the focus or hiding the window releases the lock too. A compositor without those protocols gets the keyboard shortcuts only, and the button says so. Hold Ctrl+Alt+Shift and press M (view, control, exclusive), H (stats), F (fullscreen), U (quality), D (display), V (virtual display at window size), X (HDR), R (refresh), T (type the local clipboard on the Mac), G (fetch files the Mac is dragging), S (screenshot) or Q (quit). Run `sharelinux -h` for all flags.

`cmd/sharelinux/Dockerfile` builds the viewer on Debian 14 and runs a check under Xvfb; `cmd/sharelinux/internal/mockrfb` is a tiny RFB server (`-size WxH`, `-damage N`) for checking it without a Mac.

## Roadmap

* [x] Add support for macOS
* [x] Add support for RDP
* [x] Publish to iOS App Store
* [ ] Publish to Homebrew
* [ ] Add support for other operating systems, such as Android and Windows (Linux: see `cmd/sharelinux`)
* [ ] Add a jailbreak version to provide numerous privilege-based improvements
* [ ] Create a protocol extension to support high refresh rates on Mac, touch passthrough on Mac (similar to Sidecar), Smaller scaling at native quality on Mac, multi-touch support on iOS, and further protocol optimizations

Given the complexity of custom protocols, we are currently focusing on improving the existing experience and expanding support based entirely on the Apple VNC protocol

## License

```
Copyright (C) 2026 by nekohasekai <contact-sagernet@sekai.icu>

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU General Public License for more details.

You should have received a copy of the GNU General Public License
along with this program. If not, see <http://www.gnu.org/licenses/>.

In addition, no derivative work may use the name or imply association
with this application without prior consent.
```
