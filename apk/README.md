# APK offered by the server

`docker build` copies the `*.apk` files of this directory into the image, at
`/usr/share/zweep/apk` (`ZWEEP_APK_DIR`). The server offers the newest Zweep APK found there:
dashboard **Download** page, update offer in the app, optional link without login.

To distribute another APK without rebuilding the image, mount a volume on that path and copy the
APK into it (the dashboard button "Check the directory again" reads it at once).

APK files are not committed (`.gitignore`): the official signed APK is attached to every
[release](https://github.com/N1k0droid/zweep/releases) and is already in the published images. Only APKs signed with the release key update the
installed app: see `android/signing/README.md`.
