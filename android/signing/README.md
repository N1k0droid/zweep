# Signing the Zweep app

Android installs an update only if it is signed with **the same key** as the installed app. The
release key is therefore created **once** and used for every release, for the whole life of the app.

⚠ If the key or its password is lost, no update can be installed any more: every phone must uninstall
the app (losing its local history) and install it again. If the key is stolen, someone could build an
"update" that phones would accept. Treat it like the master key of the server.

Each script exists in two versions: `.ps1` for Windows PowerShell, `.sh` for Git Bash, Linux and
macOS. Docker must be running (keytool and the build run in a pinned container).

## 1. Create the key (once)

Windows PowerShell, from the `zweep` folder:

```powershell
powershell -ExecutionPolicy Bypass -File .\android\signing\new-release-key.ps1
powershell -ExecutionPolicy Bypass -File .\android\signing\new-release-key.ps1 -Dir D:\safe\place -Org "Example S.r.l."
```

`-ExecutionPolicy Bypass` applies to that command only; it does not change the settings of the
computer. The default directory is `zweep\secrets\android`. Git Bash, Linux, macOS:

```bash
cd android/signing
./new-release-key.sh                         # → ../../secrets/android/zweep-release.jks
./new-release-key.sh /safe/place "Example S.r.l."
```

- It asks the password twice (at least 12 characters); the password is never written to disk. For
  unattended use it reads it from the `ZWEEP_KEYSTORE_PASSWORD` environment variable instead.
- RSA 4096, validity about 27 years, alias `zweep`, PKCS#12 keystore.
- It prints the SHA-256 fingerprint of the certificate: the Download page of the dashboard shows the
  same fingerprint for the APK it offers, so you can check that it is yours.
- It refuses to overwrite an existing keystore.

`secrets/` is ignored by git. Keep **two offline copies** of `zweep-release.jks` (for example an
encrypted USB key in the safe and the company password manager), and the password in the password
manager.

## 2. Build a release

```powershell
powershell -ExecutionPolicy Bypass -File .\android\signing\release.ps1 -Version 1.0.0
powershell -ExecutionPolicy Bypass -File .\android\signing\release.ps1 -Version 1.0.1 -Code 26101512
```

```bash
cd android/signing
./release.sh 1.0.0                 # version code: yyMMddHH of now (UTC)
./release.sh 1.0.1 26101512        # explicit version code
```

- The version code must be higher than the one of the installed app, or Android refuses the update:
  pass it explicitly when the default (`yyMMddHH`) would not be.
- The password is read from `ZWEEP_KEYSTORE_PASSWORD` or asked.
- The build runs in the same pinned container as the debug builds; the keystore is mounted read-only.
- The signed APK is copied to `apk/zweep-<version>.apk`: the next `docker build` puts it into the
  server image, or copy it into the APK volume of a running server (dashboard → Download → "Check the
  directory again").

The **version code must grow** at every release: phones are offered an update only when the server has a
higher version code than theirs. The default (date and hour) grows by itself and stays above the codes
of the development builds.

## 3. From development builds to release builds

Development builds are signed with the debug key of the build container (`zw-android` volume). A phone
with a development build cannot be updated to a release build (different key): uninstall the app once,
install the release APK, and activate the phone again with a QR code. From then on, updates arrive from
the server.
