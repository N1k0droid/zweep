#!/bin/sh
# Builds the app in a pinned Linux container (reproducible environment).
# Usage (from the android/ directory): ./build.sh [gradle tasks...]   default: assembleDebug testDebugUnitTest
# The Linux SDK lives in the Docker volume zw-sdk. It is seeded once from the local Android SDK
# (ANDROID_SDK): platform 37 and the license files its owner accepted; the Android Gradle plugin
# then downloads the missing Linux build tools under those licenses. The debug signing key is kept
# in the volume zw-android, so that debug builds update each other on a test device.
set -e
SDK="${ANDROID_SDK:-$HOME/AppData/Local/Android/Sdk}"
IMAGE="eclipse-temurin:17-jdk@sha256:b64592d40959b4d13b218f6b06b9ab219ff8aa3dad61efd3b5f519ba4d72ef92"
[ $# -eq 0 ] && set -- assembleDebug testDebugUnitTest
export MSYS_NO_PATHCONV=1
docker volume create zw-sdk >/dev/null
docker run --rm -v "$SDK:/seed:ro" -v zw-sdk:/sdk "$IMAGE" sh -c '
  [ -d /sdk/licenses ] || cp -r /seed/licenses /sdk/
  mkdir -p /sdk/platforms && for p in /seed/platforms/android-37*; do [ -d "/sdk/platforms/$(basename $p)" ] || cp -r "$p" /sdk/platforms/; done'
docker run --rm \
  -v "$(pwd -W 2>/dev/null || pwd):/w" -w /w \
  -v zw-sdk:/sdk -e ANDROID_HOME=/sdk \
  -v zw-gradle:/root/.gradle \
  -v zw-android:/root/.android \
  "$IMAGE" ./gradlew --no-daemon -q "$@"
