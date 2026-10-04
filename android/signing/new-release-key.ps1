# Creates the release signing key of the Zweep app (once, for the whole life of the app).
# Usage (PowerShell, from any directory):
#   powershell -ExecutionPolicy Bypass -File <repo>\android\signing\new-release-key.ps1 [-Dir <directory>] [-Org "Example S.r.l."]
#   -Dir  where the keystore is written (default: <repo>\secrets\android, ignored by git)
#   -Org  organization shown in the certificate (default: Zweep)
# The password is asked twice and never written to disk or shown. keytool runs in the same pinned
# container as the build, so no JDK is needed on this machine (Docker must be running).
param(
    [string]$Dir = "",
    [string]$Org = "Zweep"
)
$ErrorActionPreference = "Stop"
# Windows PowerShell 5.1 leaves $PSScriptRoot empty in parameter defaults: paths are resolved here
$Here = Split-Path -Parent $MyInvocation.MyCommand.Definition
if (-not $Dir) { $Dir = Join-Path $Here "..\..\secrets\android" }
$Image = "eclipse-temurin:17-jdk@sha256:b64592d40959b4d13b218f6b06b9ab219ff8aa3dad61efd3b5f519ba4d72ef92"

New-Item -ItemType Directory -Force -Path $Dir | Out-Null
$Dir = (Resolve-Path $Dir).Path
$Keystore = Join-Path $Dir "zweep-release.jks"
if (Test-Path $Keystore) {
    Write-Error "$Keystore already exists: never replace a release key (phones would refuse the updates)."
}

function Read-Plain([string]$Prompt) {
    $s = Read-Host -Prompt $Prompt -AsSecureString
    $b = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($s)
    try { return [Runtime.InteropServices.Marshal]::PtrToStringBSTR($b) } finally { [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($b) }
}
if ($env:ZWEEP_KEYSTORE_PASSWORD) {
    $pw1 = $env:ZWEEP_KEYSTORE_PASSWORD # unattended use
} else {
    $pw1 = Read-Plain "Password of the new key (at least 12 characters)"
    $pw2 = Read-Plain "Repeat the password"
    if ($pw1 -ne $pw2) { Write-Error "The passwords differ." }
}
if ($pw1.Length -lt 12) { Write-Error "Too short." }

# Passed to the container through the environment, never on the command line
$env:ZWEEP_KEYSTORE_PASSWORD = $pw1
try {
    # keytool is called directly (no shell): Windows PowerShell would mangle quotes inside a shell script
    docker run --rm -e ZWEEP_KEYSTORE_PASSWORD -v "${Dir}:/keys" $Image keytool -genkeypair `
        -keystore /keys/zweep-release.jks -storetype PKCS12 -alias zweep -keyalg RSA -keysize 4096 -validity 10000 `
        -dname "CN=Zweep, O=$Org" -storepass:env ZWEEP_KEYSTORE_PASSWORD -keypass:env ZWEEP_KEYSTORE_PASSWORD -noprompt
    if ($LASTEXITCODE -ne 0 -or -not (Test-Path $Keystore)) { Write-Error "keytool failed (is Docker running?)" }
    $list = docker run --rm -e ZWEEP_KEYSTORE_PASSWORD -v "${Dir}:/keys" $Image keytool -list -v `
        -keystore /keys/zweep-release.jks -storepass:env ZWEEP_KEYSTORE_PASSWORD
    $list | Select-String -Pattern "SHA256:|Valid" | ForEach-Object { Write-Host $_.Line.Trim() }
} finally {
    Remove-Item Env:ZWEEP_KEYSTORE_PASSWORD -ErrorAction SilentlyContinue
}
Write-Host ""
Write-Host "Created $Keystore (alias zweep)."
Write-Host "Now keep a copy of the keystore AND its password in a safe place (see README.md)."
