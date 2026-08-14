[CmdletBinding()]
param(
    [switch]$Force,
    [string]$OutputDirectory = ""
)

$ErrorActionPreference = "Stop"

if ([string]::IsNullOrWhiteSpace($OutputDirectory)) {
    $OutputDirectory = Join-Path (Split-Path -Parent $MyInvocation.MyCommand.Path) "..\deploy\secrets"
}

if (-not (Get-Command openssl -ErrorAction SilentlyContinue)) {
    $gitOpenSSL = "C:\Program Files\Git\usr\bin\openssl.exe"
    if (Test-Path -LiteralPath $gitOpenSSL) {
        Set-Alias -Name openssl -Value $gitOpenSSL -Scope Script
    } else {
        throw "openssl is required; run scripts/bootstrap-local-toolchain.ps1 first"
    }
}

$directory = [IO.Path]::GetFullPath($OutputDirectory)
New-Item -ItemType Directory -Force -Path $directory | Out-Null

$encryptionKey = Join-Path $directory "encryption.key"
$bootstrapToken = Join-Path $directory "bootstrap-token"
$caKey = Join-Path $directory "agent-ca.key"
$caCertificate = Join-Path $directory "agent-ca.crt"

$targets = @($encryptionKey, $bootstrapToken, $caKey, $caCertificate)
$existing = @($targets | Where-Object { Test-Path -LiteralPath $_ })
if ($existing.Count -gt 0 -and -not $Force) {
    throw "Local secrets already exist. Use -Force only when rotating the entire local environment."
}

openssl rand -base64 -out $encryptionKey 48
openssl rand -base64 -out $bootstrapToken 48
openssl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:P-256 -out $caKey
openssl req -x509 -new -sha256 -key $caKey -out $caCertificate -days 3650 -subj "/CN=GuGuManager Local Agent CA"

if ($LASTEXITCODE -ne 0) {
    throw "openssl failed to generate local secrets"
}

$fingerprint = openssl x509 -in $caCertificate -noout -fingerprint -sha256
Write-Host "Generated local secrets in $directory"
Write-Host $fingerprint
