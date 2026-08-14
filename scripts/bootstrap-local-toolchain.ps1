[CmdletBinding()]
param()

$ErrorActionPreference = "Stop"

function Add-UserPathEntry {
    param([Parameter(Mandatory)][string]$Path)

    if (-not (Test-Path -LiteralPath $Path)) {
        return
    }
    $current = [Environment]::GetEnvironmentVariable("Path", "User")
    $entries = @($current -split ";" | Where-Object { $_ })
    if ($entries -notcontains $Path) {
        $entries += $Path
        [Environment]::SetEnvironmentVariable("Path", ($entries -join ";"), "User")
    }
    if (($env:Path -split ";") -notcontains $Path) {
        $env:Path = "$Path;$env:Path"
    }
}

function Require-Command {
    param([Parameter(Mandatory)][string]$Name)

    if (-not (Get-Command $Name -ErrorAction SilentlyContinue)) {
        throw "$Name is unavailable after toolchain bootstrap"
    }
}

$dockerBin = "C:\Program Files\Docker\Docker\resources\bin"
if (-not (Test-Path -LiteralPath (Join-Path $dockerBin "docker.exe"))) {
    winget install --id Docker.DockerDesktop --exact --accept-source-agreements --accept-package-agreements
}
Add-UserPathEntry $dockerBin

$gitOpenSSL = "C:\Program Files\Git\usr\bin"
if (-not (Test-Path -LiteralPath (Join-Path $gitOpenSSL "openssl.exe"))) {
    throw "OpenSSL was not found. Install Git for Windows or an OpenSSL 3 distribution."
}
Add-UserPathEntry $gitOpenSSL

$postgresBin = "C:\Program Files\PostgreSQL\17\bin"
if (-not (Test-Path -LiteralPath (Join-Path $postgresBin "psql.exe"))) {
    throw "PostgreSQL 17 client tools were not found at $postgresBin"
}
Add-UserPathEntry $postgresBin

$goBin = Join-Path $env:USERPROFILE "go\bin"
New-Item -ItemType Directory -Force -Path $goBin | Out-Null
Add-UserPathEntry $goBin
if (-not (Get-Command actionlint -ErrorAction SilentlyContinue)) {
    go install github.com/rhysd/actionlint/cmd/actionlint@v1.7.12
}

$protocRoot = Join-Path $env:USERPROFILE ".local\tools\protoc-31.1"
$protocBin = Join-Path $protocRoot "bin"
if (-not (Test-Path -LiteralPath (Join-Path $protocBin "protoc.exe"))) {
    $archive = Join-Path $env:TEMP "protoc-31.1-win64.zip"
    $download = "https://github.com/protocolbuffers/protobuf/releases/download/v31.1/protoc-31.1-win64.zip"
    New-Item -ItemType Directory -Force -Path (Split-Path $protocRoot -Parent) | Out-Null
    Invoke-WebRequest -Uri $download -OutFile $archive
    Expand-Archive -LiteralPath $archive -DestinationPath $protocRoot -Force
}
Add-UserPathEntry $protocBin

foreach ($command in @("docker", "openssl", "psql", "protoc", "actionlint", "go", "node", "npm", "buf")) {
    Require-Command $command
}

docker --version
docker compose version
openssl version
psql --version
protoc --version
actionlint -version
go version
node --version
npm --version
buf --version
