# sharelinux Mouse Helper (Mac)

A small menu bar app for the Mac that lets a paired Linux computer move the Mac's mouse with real, relative motion while you control the Mac from the Linux Screen Sharing viewer in Exclusive mode.

## What it does and why it exists

Apple's Screen Sharing only carries absolute pointer positions: "the pointer is at x, y". That is fine for a desktop, but games and 3D tools need to know how far the mouse moved, not where it is, and they want it without an edge. So when the Linux viewer locks the pointer (Exclusive mode), it sends the mouse movement, buttons and scroll to this helper instead, over an encrypted UDP link on your network, and the helper posts them on the Mac as ordinary relative mouse events. The keyboard always stays on Screen Sharing. The helper never posts a key.

It only acts when all of these are true, checked on every packet:

* "Allow raw mouse" is on in its menu (it is off every time the app starts, and nothing remote can switch it on),
* the sender is a Linux computer you paired at this Mac,
* that computer has a live Screen Sharing connection to this Mac, from the same address,
* the screen is not locked, you are the signed-in console user, and the app has Accessibility access.

When any of that stops being true, or the link goes quiet, every held button is released and the Linux viewer goes back to ordinary Screen Sharing.

## Install (once)

You need macOS 13 or later and the Xcode command line tools (`xcode-select --install`).

1. Build it. From this folder:

   ```
   scripts/bundle.sh
   ```

   This makes `build/sharelinux Mouse Helper.app` (signed ad hoc, no developer account) and a LaunchAgent file next to it. It installs nothing, launches nothing and asks for no permission.

2. Put the app where it will live: copy it to `~/Applications/sharelinux Mouse Helper.app`. That is the path the LaunchAgent file was filled in for.

   ```
   mkdir -p ~/Applications && cp -R "build/sharelinux Mouse Helper.app" ~/Applications/
   ```

3. Open it once (`open "$HOME/Applications/sharelinux Mouse Helper.app"`). A pointer icon appears in the menu bar. If your menu bar is crowded the icon can hide behind the notch; the always-on-top badge and the shortcut below work without it.

4. Give it Accessibility access. In its menu choose **Request Accessibility access...**, then in System Settings, Privacy & Security, Accessibility, switch **sharelinux Mouse Helper** on. Nothing asks for this by itself. Because the build is signed ad hoc, macOS treats every rebuild as a new program and asks again: approve it only right after you rebuilt the helper yourself.

   **Keychain prompts after a rebuild.** For the same reason macOS may ask whether the new build may use the helper's items in your login Keychain. Click **Allow**, not **Always Allow**, and only right after a rebuild you did yourself. If the prompt shows up at any other time, click **Deny** and treat it as an alarm: something other than your rebuild is asking for the helper's keys.

5. Start it at login, one of two ways (not both):

   * System Settings, General, Login Items: press **+** and choose the app. This is the simplest.
   * Or a LaunchAgent: copy the filled-in file and load it.

     ```
     cp build/io.github.sharelinux.mousehelper.plist ~/Library/LaunchAgents/
     launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/io.github.sharelinux.mousehelper.plist
     ```

     If you moved the app, make the file again with `scripts/fill-launchagent.sh Resources/io.github.sharelinux.mousehelper.plist "/full/path/to/sharelinux Mouse Helper.app/Contents/MacOS/MouseHelper" ~/Library/LaunchAgents/io.github.sharelinux.mousehelper.plist`.

6. Pair the Linux computer. At the Mac choose **Pair a Linux device...** (the window stays open for 2 minutes). On the Linux computer open Preferences, Keyboard, Mouse helper, and choose Pair with this Mac. Both screens show the same six digits. Check that they match, then click **Confirm** at the Mac. Nobody can confirm for you: there is no default button, a click in the first second is ignored, and a window covering the dialog switches Confirm off. If macOS asks whether the app may accept incoming connections, allow it (it listens on UDP port 5961).

## Daily use

* **Allow raw mouse** in the menu is the switch. It is off after every start. Turn it on when you want to play or draw; turn it off when you are done.
* **The indicator.** The menu bar pointer is dimmed when the switch is off and shows click marks while the helper is posting events. While events are being posted a small badge, "Raw mouse active: <device>", stays on top of everything, even if the menu bar icon is hidden. The first time a device starts using the helper in a session you also get one notification.
* **Stop at once.** Press **Control-Option-Command-Escape** anywhere, or choose **Stop now** in the menu. Buttons come up, the session ends and the switch goes off until you turn it on again. Quit does the same before the app closes. If the shortcut cannot be registered (another app owns it) the switch refuses to turn on.
* **Switching off tells the Linux side.** When you turn the switch off or press Stop now, the helper tells the Linux viewer right away, so its mouse is back on ordinary Screen Sharing within a few milliseconds instead of waiting for silence.
* **Pairing and reset.** They are closed while a Linux computer could be moving your mouse (a live session and the switch on), because a click on the pairing window could be its doing. Turn **Allow raw mouse** off first (or press Stop now) and they open again, even if the Linux viewer is still sitting in Exclusive mode.
* **Keys changed while it was not running.** If the helper's keys are not as the last run left them, it asks you to review "was" and "now" for both fingerprints before it accepts anything. **Keep paired devices** lets sessions in (the switch is still off), **Reset helper keys...** starts again, and **Leave the helper off** keeps it stopped (the menu then has **Review changed keys...**).

## What protects the keys, and the part it cannot protect

The helper's own key and the list of paired devices live in your login Keychain. A program that already runs as you can delete those Keychain items and add itself as a paired device. macOS only stops that for apps signed with a real Apple developer team, and this helper is signed ad hoc on purpose. That is an accepted limit, the same trust model as the keys in `~/.ssh`: malware running as you is out of scope, and the helper does not pretend to stop it.

What it does instead is notice. It remembers how its Keychain items looked after its own last write. If they differ before a new session or after the Mac wakes, or at the next launch, it ends every session, switches Allow raw mouse off, and tells you. Treat that as an alarm bell, not a guarantee: a program running as you could edit the note it compares with as well. Restoring an old backup of your whole keychain brings back devices you removed since then, so look at the paired list afterwards.

## Remove it completely

Do these in order.

1. **Quit** the app: menu, Quit. This puts every button up first.

2. **Stop it starting at login.** Remove the app from System Settings, General, Login Items (select it, press **-**). If you used the LaunchAgent:

   ```
   launchctl bootout gui/$(id -u)/io.github.sharelinux.mousehelper
   rm ~/Library/LaunchAgents/io.github.sharelinux.mousehelper.plist
   ```

3. **Take back the Accessibility grant:**

   ```
   tccutil reset Accessibility io.github.sharelinux.mousehelper
   ```

4. **Delete its Keychain items.** The easy way is the app's own: open it again, choose **Reset helper keys...** and confirm. It deletes the identity, the paired devices and the removed-devices list, then quits (do not let it start at login again). Or do it by hand in Terminal, exactly these commands (macOS may ask you to allow each deletion; click Allow):

   ```
   security delete-generic-password -s io.github.sharelinux.mousehelper.identity
   security delete-generic-password -s io.github.sharelinux.mousehelper.revoked
   while security delete-generic-password -s io.github.sharelinux.mousehelper.paired; do :; done
   ```

   The last line removes one item per paired device and stops with "The specified item could not be found in the keychain". That message means it is done. This is the step people forget.

5. **Delete the app and what it kept on disk** (no secrets or trust in the files; they hold handshake timestamps, the pairing failure counter and two remembered fingerprints):

   ```
   rm -rf "$HOME/Applications/sharelinux Mouse Helper.app"
   rm -rf "$HOME/Library/Application Support/sharelinux Mouse Helper"
   defaults delete io.github.sharelinux.mousehelper
   rm -f "$HOME/Library/Preferences/io.github.sharelinux.mousehelper.plist"
   ```

   And the build folder in this repository: `rm -rf machelper/build`.

6. **On the Linux computer**, forget the Mac: Preferences, Keyboard, Mouse helper, Remove. To clear the keyring by hand: `secret-tool clear service sharelinux username mouse-helper/macs` and `secret-tool clear service sharelinux username mouse-helper/client-key`.

## For developers

* `swift test` runs the tests with fake event posting and a fake environment. Nothing posts a real event, asks for a permission or touches your Keychain (the Keychain tests use a throwaway keychain file).
* `swift build -c release --product runtimeharness` builds the runtime on a real UDP socket with a fake poster, driven by the Go tests in `internal/machelper` and usable from another computer (`--lan --peer <ip>`).
* The design, the threat model and what each audit changed are in the design document the work was done from.
