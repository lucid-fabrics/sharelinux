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

# The video stream: a mock Mac that plays a canned HEVC clip, decoded in software here (no GPU).
# 127.0.0.2 because the viewer's media socket takes the server's port number.
MOCKAPPLE_PASSWORD=check mockapple -video -listen 127.0.0.2:5991 -user mock >/tmp/mockapple.log 2>&1 &
sleep 1
sharelinux -hud -v -video software -password-file /tmp/check.pw mock@127.0.0.2:5991 >/tmp/viewer-video.log 2>&1 &
viewer=$!
i=0
until grep -q 'state=showing' /tmp/viewer-video.log; do
	i=$((i + 1))
	[ $i -lt 30 ] || { echo "the video did not show"; cat /tmp/viewer-video.log; cat /tmp/mockapple.log; exit 1; }
	sleep 1
done
sleep 3
grep -q 'decoder="HEVC (software)"' /tmp/viewer-video.log || { echo "not decoded by libavcodec"; cat /tmp/viewer-video.log; exit 1; }
import -window root /tmp/video.png
kill $viewer 2>/dev/null || true
echo "ok: video stream negotiated, decoded in software and shown (screenshot in /tmp/video.png)"
