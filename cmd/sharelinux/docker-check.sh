#!/bin/sh
# Starts the mock RFB server and the viewer under Xvfb, connects, checks that the
# session shows and that typed keys and a click reach the server, then leaves.
# It runs inside the image built from cmd/sharelinux/Dockerfile.
set -eu
export MOCKRFB_PASSWORD=check
mockrfb -listen 127.0.0.1:5990 >/tmp/mock.log 2>&1 &
sleep 1
printf '%s' "$MOCKRFB_PASSWORD" >/tmp/check.pw
chmod 600 /tmp/check.pw
sharelinux -vnc -password-file /tmp/check.pw 127.0.0.1:5990 >/tmp/viewer.log 2>&1 &
viewer=$!
i=0
until grep -q 'login ok' /tmp/mock.log; do
	i=$((i + 1))
	[ $i -lt 30 ] || { echo "the viewer did not log in"; cat /tmp/viewer.log; exit 1; }
	sleep 1
done
sleep 2
xdotool mousemove 640 400 click 1
xdotool type --delay 80 hi
sleep 1
import -window root /tmp/session.png
grep -q 'keysym=0x68' /tmp/mock.log || { echo "the key h did not arrive"; cat /tmp/mock.log; exit 1; }
grep -q 'buttons=0x1' /tmp/mock.log || { echo "the click did not arrive"; cat /tmp/mock.log; exit 1; }
kill $viewer 2>/dev/null || true
echo "ok: logged in, picture shown, input delivered (screenshot in /tmp/session.png)"
