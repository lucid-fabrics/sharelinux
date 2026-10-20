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
* GTK **4.14** or newer and libadwaita **1.7** or newer, with their development files, plus `libgirepository1.0-dev` (gotk4 needs it), `libx11-dev`, `libxfixes-dev` and `pkg-config`. Ubuntu 26.04 and Debian 14: `apt install libgtk-4-dev libadwaita-1-dev libgirepository1.0-dev libx11-dev libxfixes-dev`
* A Secret Service (GNOME Keyring, KWallet) if you want saved passwords; without one connections still save, the password is typed each time
* Avahi or any mDNS-capable network for the "On this network" list (the viewer browses `_rfb._tcp` itself, it does not need the Avahi daemon)

```
go build -o sharelinux ./cmd/sharelinux
./sharelinux user@mac.local            # or: ./sharelinux for the list of Macs
make install-linux                        # icon and desktop entry under ~/.local/share
```

Open **Preferences** (Ctrl+comma) for quality, the Command key (Super or Alt), natural scrolling, the clipboard and the download folder. In a session, **Exclusive** mode gives the keyboard (including Super and Alt+Tab) to the Mac and, on X11, keeps the pointer inside the window; press Ctrl+Alt+Esc to get them back. On Wayland GTK 4 cannot confine the pointer, so Exclusive grabs the keyboard shortcuts only. Hold Ctrl+Alt+Shift and press M (view, control, exclusive), H (stats), F (fullscreen), U (quality), D (display), V (virtual display at window size), X (HDR), R (refresh), T (type the local clipboard on the Mac), G (fetch files the Mac is dragging), S (screenshot) or Q (quit). Run `sharelinux -h` for all flags.

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
