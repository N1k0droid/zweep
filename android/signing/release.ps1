# Builds the signed release APK of the Zweep app and copies it to <repo>\apk (offered by the server image).
# Usage (PowerShell, from any directory):
#   powershell -ExecutionPolicy Bypass -File <repo>\android\signing\release.ps1 -Version 1.0.1 [-Code 26101512] [-Keystore <file>]
#   -Version   X.Y.Z, e.g. 1.0.1
#   -Code      version code, must grow at every release (default: yyMMddHH of now, UTC)
#   -Keystore  default <repo>\secrets\android\zweep-release.jks
# The keystore password is read from ZWEEP_KEYSTORE_PASSWORD or asked (never on the command line).
param(
    [Parameter(Mandatory = $true)][string]$Version,
    [string]$Code = (Get-Date).ToUniversalTime().ToString("yyMMddHH"),
    [string]$Keystore = ""
)
$ErrorActionPreference = "Stop"
# Windows PowerShell 5.1 leaves $PSScriptRoot empty in parameter defaults: paths are resolved here
$Here = Split-Path -Parent $MyInvocation.MyCommand.Definition
if (-not $Keystore) { $Keystore = Join-Path $Here "..\..\secrets\android\zweep-release.jks" }
$Image = "eclipse-temurin:17-jdk@sha256:b64592d40959b4d13b218f6b06b9ab219ff8aa3dad61efd3b5f519ba4d72ef92"
if (-not (Test-Path $Keystore)) { Write-Error "Keystore not found: $Keystore (create it with new-release-key.ps1)" }
$Keystore = (Resolve-Path $Keystore).Path
$KsDir = Split-Path $Keystore
$KsName = Split-Path $Keystore -Leaf
$Android = (Resolve-Path (Join-Path $Here "..")).Path
$Repo = (Resolve-Path (Join-Path $Here "..\..")).Path

$set = $false
if (-not $env:ZWEEP_KEYSTORE_PASSWORD) {
    $s = Read-Host -Prompt "Keystore password" -AsSecureString
    $b = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($s)
    try { $env:ZWEEP_KEYSTORE_PASSWORD = [Runtime.InteropServices.Marshal]::PtrToStringBSTR($b) } finally { [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($b) }
    $set = $true
}
$Sdk = if ($env:ANDROID_SDK) { $env:ANDROID_SDK } else { Join-Path $env:LOCALAPPDATA "Android\Sdk" }
try {
    # Check the password at once, not after minutes of build
    docker run --rm -e ZWEEP_KEYSTORE_PASSWORD -v "${KsDir}:/keys:ro" $Image keytool -list -keystore "/keys/$KsName" -storepass:env ZWEEP_KEYSTORE_PASSWORD -alias zweep | Out-Null
    if ($LASTEXITCODE -ne 0) { Write-Error "Wrong keystore password (or the keystore has no key zweep). Nothing was built." }
    docker volume create zw-sdk | Out-Null
    # The Linux SDK volume is seeded once from the local SDK (platform 37 and the accepted licenses), as build.sh does
    # No double quotes inside the script: Windows PowerShell would strip them
    docker run --rm -v "${Sdk}:/seed:ro" -v zw-sdk:/sdk $Image sh -c '[ -d /sdk/licenses ] || cp -r /seed/licenses /sdk/; mkdir -p /sdk/platforms; for p in /seed/platforms/android-37*; do d=/sdk/platforms/$(basename $p); [ -d $d ] || cp -r $p /sdk/platforms/; done'
    if ($LASTEXITCODE -ne 0) { Write-Error "Cannot prepare the Android SDK volume (is Docker running? is the Android SDK at $Sdk?)" }
    docker run --rm -v "${Android}:/w" -w /w -v zw-sdk:/sdk -e ANDROID_HOME=/sdk -v zw-gradle:/root/.gradle `
        -v "${KsDir}:/keys:ro" -e "ZWEEP_KEYSTORE=/keys/$KsName" -e ZWEEP_KEYSTORE_PASSWORD `
        $Image ./gradlew --no-daemon -q assembleRelease "-Pzweep.version=$Version" "-Pzweep.versionCode=$Code"
    if ($LASTEXITCODE -ne 0) { Write-Error "Build failed" }
} finally {
    if ($set) { Remove-Item Env:ZWEEP_KEYSTORE_PASSWORD -ErrorAction SilentlyContinue }
}
$Out = Join-Path $Android "app\build\outputs\apk\release\app-release.apk"
if (-not (Test-Path $Out)) { Write-Error "No signed APK produced" }
New-Item -ItemType Directory -Force -Path (Join-Path $Repo "apk") | Out-Null
$Dest = Join-Path $Repo "apk\zweep-$Version.apk"
Copy-Item $Out $Dest -Force
Write-Host "Signed APK: $Dest (version $Version, code $Code)."
Write-Host "It goes into the server image at the next docker build, or copy it into the mounted APK volume."
