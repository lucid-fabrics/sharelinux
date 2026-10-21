#!/bin/bash
# Prints the LaunchAgent template with the app executable filled in.
# usage: fill-launchagent.sh <template.plist> <absolute path of the executable>
#
# Only the <string> element of ProgramArguments is replaced, and the path is
# XML-escaped first, so characters such as & | \ < > $ or quotes in a home
# directory name cannot change the plist's structure or leak into the comment.
set -euo pipefail

template="${1:?template path}"
exe="${2:?executable path}"

case "$exe" in
/*) ;;
*) echo "fill-launchagent: the executable path must be absolute" >&2; exit 1 ;;
esac
case "$exe" in
*$'\n'* | *$'\r'*) echo "fill-launchagent: the path contains a line break" >&2; exit 1 ;;
esac

esc="${exe//&/&amp;}"
esc="${esc//</&lt;}"
esc="${esc//>/&gt;}"

placeholder='__APP_EXECUTABLE__'
found="$(grep -c "$placeholder" "$template" || true)"
if [ "$found" != "1" ]; then
	echo "fill-launchagent: expected the placeholder exactly once in $template, found $found" >&2
	exit 1
fi

# index/substr instead of sub(): the replacement is data, so "&" and "\" in it mean nothing.
EXE_ESCAPED="$esc" awk '
	/^[ \t]*<string>__APP_EXECUTABLE__<\/string>[ \t]*$/ {
		i = index($0, "__APP_EXECUTABLE__")
		$0 = substr($0, 1, i - 1) ENVIRON["EXE_ESCAPED"] substr($0, i + 18)
		done++
	}
	{ print }
	END { if (done != 1) { print "fill-launchagent: the placeholder is not a ProgramArguments string" > "/dev/stderr"; exit 1 } }
' "$template"
