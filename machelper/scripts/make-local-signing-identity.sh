#!/bin/bash
# OWNER ONLY. Makes a self-signed code-signing identity for building OSS Mouse
# Helper on this Mac, so a rebuild keeps its Keychain items and its Accessibility
# grant (they are tied to the signing identity, design 2.5). Nothing in the
# repository or the build runs this; the owner runs it once, by hand.
#
# It writes to the login keychain (a private key, a certificate and a trust
# setting for code signing), which asks for the login password. It never uses
# the Cendara team, a Developer ID or any Apple certificate, and the identity
# is only good for this helper's bundle identifier.
#
# usage: make-local-signing-identity.sh [name]
#        default name: "OSS Mouse Helper Local Signing"
# then:  OSS_LOCAL_SIGNING_IDENTITY="OSS Mouse Helper Local Signing" machelper/scripts/bundle.sh
set -euo pipefail

name="${1:-OSS Mouse Helper Local Signing}"
"$(dirname "$0")/check-signing-identity.sh" "$name" >/dev/null 2>&1 && {
	echo "an identity named '$name' already exists and is valid; nothing to do"
	exit 0
} || true
case "$name" in
"OSS Mouse Helper Local"*) ;;
*) echo "the name must start with 'OSS Mouse Helper Local'" >&2; exit 1 ;;
esac

keychain="$HOME/Library/Keychains/login.keychain-db"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
pass="$(openssl rand -hex 16)"

cat >"$work/openssl.cnf" <<CNF
[req]
distinguished_name = dn
x509_extensions = ext
prompt = no
[dn]
CN = $name
[ext]
basicConstraints = critical,CA:false
keyUsage = critical,digitalSignature
extendedKeyUsage = critical,codeSigning
CNF
openssl req -x509 -newkey rsa:2048 -nodes -days 3650 -config "$work/openssl.cnf" \
	-keyout "$work/key.pem" -out "$work/cert.pem" 2>/dev/null
openssl pkcs12 -export -inkey "$work/key.pem" -in "$work/cert.pem" -name "$name" \
	-out "$work/identity.p12" -passout "pass:$pass"

# codesign may use the key without asking; no other program is named.
security import "$work/identity.p12" -k "$keychain" -P "$pass" -T /usr/bin/codesign
# Trust the certificate for code signing only (macOS asks for the login password).
security add-trusted-cert -r trustRoot -p codeSign -k "$keychain" "$work/cert.pem"
echo
echo "Made '$name' in the login keychain."
echo "If codesign still asks to use the key, choose Allow (this is the signing key, not the helper's items)."
echo "Next: OSS_LOCAL_SIGNING_IDENTITY=\"$name\" machelper/scripts/bundle.sh"
echo "The first build with this identity cannot read items an ad-hoc build made."
echo "Remove those first (see design 2.5): Keychain Access, search io.github.sharelinux.mousehelper,"
echo "delete the .identity and .paired items, then pair your Linux devices again."
