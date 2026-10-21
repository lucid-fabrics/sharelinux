#!/bin/bash
# Validates the name of a local signing identity and prints its certificate's
# SHA-1 on success. Used by bundle.sh when OSS_LOCAL_SIGNING_IDENTITY is set.
#
# usage: check-signing-identity.sh <certificate common name> [--name-only]
#        --name-only stops after the name rules (exit 0, prints "name ok")
#
# The name rules run first and touch no keychain, so they can be tested
# anywhere (test-signing-guards.sh). The identity must be one the owner made
# for this helper with make-local-signing-identity.sh: its name starts with
# "OSS Mouse Helper Local", which no Developer ID, Apple Development or team
# certificate has. After the name passes, the certificate must exist in the
# login keychain, be self-signed, carry no team identifier, and be a valid
# code-signing identity.
set -euo pipefail

name="${1:-}"
fail() { echo "check-signing-identity: $*" >&2; exit 1; }

[ -n "$name" ] || fail "no identity name given"
case "$name" in
"OSS Mouse Helper Local"*) ;;
*) fail "the identity must be named 'OSS Mouse Helper Local ...' (made by make-local-signing-identity.sh); real signing identities are never used" ;;
esac
if printf '%s' "$name" | grep -qE '8G389M2DZR|KRPUAN3FFA|Developer ID|Apple Development|Apple Distribution|Mac Developer|Cendara'; then
	fail "that name belongs to a real signing identity"
fi
# A name is matched as a plain string by `security`: no wildcards or shell characters.
printf '%s' "$name" | grep -qE '^[A-Za-z0-9 ._-]+$' || fail "the name may only hold letters, digits, space, dot, dash and underscore"

if [ "${2:-}" = "--name-only" ]; then echo "name ok"; exit 0; fi

keychain="${OSS_SIGNING_KEYCHAIN:-$HOME/Library/Keychains/login.keychain-db}"
pem="$(security find-certificate -c "$name" -p "$keychain" 2>/dev/null)" || fail "no certificate named '$name' in $keychain; run make-local-signing-identity.sh first"
[ "$(printf '%s\n' "$pem" | grep -c 'BEGIN CERTIFICATE')" = 1 ] || fail "more than one certificate matches '$name'"
subject="$(printf '%s\n' "$pem" | openssl x509 -noout -subject -nameopt RFC2253)"
issuer="$(printf '%s\n' "$pem" | openssl x509 -noout -issuer -nameopt RFC2253)"
[ "${subject#subject=}" = "${issuer#issuer=}" ] || fail "the certificate is not self-signed"
if printf '%s' "$subject" | grep -qE 'OU=|8G389M2DZR|KRPUAN3FFA'; then
	fail "the certificate carries a team or organisational unit"
fi
sha="$(printf '%s\n' "$pem" | openssl x509 -noout -fingerprint -sha1 | sed 's/.*=//; s/://g')"
[ "${#sha}" = 40 ] || fail "could not read the certificate fingerprint"
security find-identity -v -p codesigning "$keychain" | grep -q "$sha" ||
	fail "the certificate is not a trusted code-signing identity (run make-local-signing-identity.sh again)"
echo "$sha"
