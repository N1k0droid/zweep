#!/bin/sh
# Creates the release signing key of the Zweep app (once, for the whole life of the app).
# Usage (from android/signing): ./new-release-key.sh [directory] [organization]
#   directory     where the keystore is written (default: ../../secrets/android, outside git)
#   organization  shown in the certificate, e.g. "Example S.r.l." (default: Zweep)
# The password is asked twice and never written to disk or shown. keytool runs in the same pinned
# container as the build, so no JDK is needed on this machine.
set -e
DIR="${1:-../../secrets/android}"
ORG="${2:-Zweep}"
IMAGE="eclipse-temurin:17-jdk@sha256:b64592d40959b4d13b218f6b06b9ab219ff8aa3dad61efd3b5f519ba4d72ef92"
mkdir -p "$DIR"
if [ -e "$DIR/zweep-release.jks" ]; then
  echo "$DIR/zweep-release.jks already exists: never replace a release key (phones would refuse the updates)." >&2
  exit 1
fi
printf 'Password of the new key (at least 12 characters): '
stty -echo 2>/dev/null || true; read -r PW1; stty echo 2>/dev/null || true; echo
printf 'Repeat the password: '
stty -echo 2>/dev/null || true; read -r PW2; stty echo 2>/dev/null || true; echo
[ "$PW1" = "$PW2" ] || { echo "The passwords differ." >&2; exit 1; }
[ ${#PW1} -ge 12 ] || { echo "Too short." >&2; exit 1; }
export ZWEEP_KEYSTORE_PASSWORD="$PW1"
export MSYS_NO_PATHCONV=1
ABS="$(cd "$DIR" && (pwd -W 2>/dev/null || pwd))"
export ZWEEP_ORG="$ORG"
docker run --rm -e ZWEEP_KEYSTORE_PASSWORD -e ZWEEP_ORG -v "$ABS:/keys" "$IMAGE" sh -c '
  keytool -genkeypair -keystore /keys/zweep-release.jks -storetype PKCS12 -alias zweep \
    -keyalg RSA -keysize 4096 -validity 10000 -dname "CN=Zweep, O=$ZWEEP_ORG" \
    -storepass:env ZWEEP_KEYSTORE_PASSWORD -keypass:env ZWEEP_KEYSTORE_PASSWORD &&
  keytool -list -v -keystore /keys/zweep-release.jks -storepass:env ZWEEP_KEYSTORE_PASSWORD | grep -E "SHA256:|Valid"'
chmod 600 "$DIR/zweep-release.jks" 2>/dev/null || true
echo
echo "Created $DIR/zweep-release.jks (alias zweep)."
echo "Now: keep a copy of the keystore AND its password in a safe place (see README.md)."
