#!/bin/sh
# Builds the signed release APK of the Zweep app and copies it to ../apk (offered by the server image).
# Usage (from android/signing): ./release.sh VERSION [VERSION_CODE] [KEYSTORE]
#   VERSION       X.Y.Z, e.g. 1.0.1
#   VERSION_CODE  must grow at every release (default: yyMMddHH of now, like the development builds)
#   KEYSTORE      default ../../secrets/android/zweep-release.jks
# The keystore password is read from ZWEEP_KEYSTORE_PASSWORD or asked (never on the command line).
set -e
VERSION="$1"
[ -n "$VERSION" ] || { echo "usage: ./release.sh VERSION [VERSION_CODE] [KEYSTORE]" >&2; exit 2; }
CODE="${2:-$(date -u +%y%m%d%H)}"
KS="${3:-../../secrets/android/zweep-release.jks}"
[ -f "$KS" ] || { echo "Keystore not found: $KS (create it with ./new-release-key.sh)" >&2; exit 1; }
if [ -z "$ZWEEP_KEYSTORE_PASSWORD" ]; then
  printf 'Keystore password: '
  stty -echo 2>/dev/null || true; read -r ZWEEP_KEYSTORE_PASSWORD; stty echo 2>/dev/null || true; echo
fi
export ZWEEP_KEYSTORE_PASSWORD
SDK="${ANDROID_SDK:-$HOME/AppData/Local/Android/Sdk}"
IMAGE="eclipse-temurin:17-jdk@sha256:b64592d40959b4d13b218f6b06b9ab219ff8aa3dad61efd3b5f519ba4d72ef92"
export MSYS_NO_PATHCONV=1
KSDIR="$(cd "$(dirname "$KS")" && (pwd -W 2>/dev/null || pwd))"
# Check the password at once, not after minutes of build
if ! docker run --rm -e ZWEEP_KEYSTORE_PASSWORD -v "$KSDIR:/keys:ro" "$IMAGE" keytool -list -keystore "/keys/$(basename "$KS")" -storepass:env ZWEEP_KEYSTORE_PASSWORD -alias zweep >/dev/null 2>&1; then
  echo "Wrong keystore password (or the keystore has no key zweep). Nothing was built." >&2
  exit 1
fi
cd ..
docker volume create zw-sdk >/dev/null
docker run --rm \
  -v "$(pwd -W 2>/dev/null || pwd):/w" -w /w \
  -v zw-sdk:/sdk -e ANDROID_HOME=/sdk \
  -v zw-gradle:/root/.gradle \
  -v "$KSDIR:/keys:ro" -e ZWEEP_KEYSTORE="/keys/$(basename "$KS")" -e ZWEEP_KEYSTORE_PASSWORD \
  "$IMAGE" ./gradlew --no-daemon -q assembleRelease -Pzweep.version="$VERSION" -Pzweep.versionCode="$CODE"
OUT=app/build/outputs/apk/release/app-release.apk
[ -f "$OUT" ] || { echo "No signed APK produced" >&2; exit 1; }
mkdir -p ../apk
cp "$OUT" "../apk/zweep-$VERSION.apk"
echo "Signed APK: apk/zweep-$VERSION.apk (version $VERSION, code $CODE)."
echo "It goes into the server image at the next docker build, or copy it into the mounted APK volume."
