$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

function Get-NativeWindowsPlatform {
    if (-not [System.Runtime.InteropServices.RuntimeInformation]::IsOSPlatform([System.Runtime.InteropServices.OSPlatform]::Windows)) { throw 'pigeons Windows build requires Windows' }
    $osArchitecture = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture
    $processArchitecture = [System.Runtime.InteropServices.RuntimeInformation]::ProcessArchitecture
    if ($osArchitecture -ne $processArchitecture) { throw "pigeons Windows build requires a native process (OS: $osArchitecture, process: $processArchitecture)" }
    switch ($processArchitecture.ToString()) { 'X64' { 'amd64' } 'Arm64' { 'arm64' } default { throw "unsupported native Windows architecture: $processArchitecture" } }
}
function Read-PigeonsBuildConfig([string] $Path) {
    $values = @{}
    foreach ($line in [System.IO.File]::ReadAllLines($Path)) {
        $trimmed = $line.Trim(); if ($trimmed.Length -eq 0 -or $trimmed.StartsWith('#')) { continue }
        if ($trimmed -notmatch '^(?<name>[A-Z][A-Z0-9_]*)=(?<value>[^\r\n]*)$') { throw "invalid build config line: $line" }
        if ($values.ContainsKey($Matches.name)) { throw "duplicate build config key: $($Matches.name)" }; $values[$Matches.name] = $Matches.value
    }
    foreach ($name in @('PIGEONS_VERSION', 'PIGEONS_COMMIT', 'PIGEONS_SOURCE_SHA256', 'PIGEONS_CARGO_LOCK_SHA256')) { if (-not $values.ContainsKey($name) -or [string]::IsNullOrWhiteSpace($values[$name])) { throw "missing build config key: $name" } }
    if ($values.PIGEONS_COMMIT -notmatch '^[0-9a-f]{40}$') { throw 'PIGEONS_COMMIT must be a full lowercase Git commit' }
    foreach ($name in @('PIGEONS_SOURCE_SHA256', 'PIGEONS_CARGO_LOCK_SHA256')) { if ($values[$name] -notmatch '^[0-9a-f]{64}$') { throw "$name must be a lowercase SHA-256 digest" } }
    $values
}
function Require-Command([string] $Name) { if (-not (Get-Command $Name -CommandType Application -ErrorAction SilentlyContinue)) { throw "missing required command: $Name" } }
function Assert-Sha256([string] $Path, [string] $Expected) { $actual = (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant(); if ($actual -cne $Expected) { throw "SHA-256 mismatch for ${Path}: expected $Expected, got $actual" } }
function AbsolutePath([string] $Path) { if ([string]::IsNullOrWhiteSpace($Path)) { throw 'path must not be empty' }; if ([System.IO.Path]::IsPathRooted($Path)) { return [System.IO.Path]::GetFullPath($Path) }; [System.IO.Path]::GetFullPath((Join-Path (Get-Location).ProviderPath $Path)) }

$platform = Get-NativeWindowsPlatform
$config = Read-PigeonsBuildConfig (Join-Path $PSScriptRoot 'pigeons-build.env')
$patchFile = Join-Path $PSScriptRoot "pigeons-$($config.PIGEONS_VERSION)-ternal.patch"
if (-not (Test-Path -LiteralPath $patchFile -PathType Leaf)) { throw "missing Ternal pigeons patch: $patchFile" }
Require-Command cargo; Require-Command git; Require-Command rustc; Require-Command tar
$output = AbsolutePath $(if ([string]::IsNullOrWhiteSpace($env:TERNAL_PIGEONS_OUTPUT)) { "dist/pigeons-windows-$platform.exe" } else { $env:TERNAL_PIGEONS_OUTPUT })
$targetDirectory = AbsolutePath $(if ([string]::IsNullOrWhiteSpace($env:TERNAL_PIGEONS_TARGET_DIR)) { "target/pigeons-windows-$platform" } else { $env:TERNAL_PIGEONS_TARGET_DIR })
if ([System.IO.Path]::GetExtension($output) -cne '.exe') { throw "pigeons Windows output must use .exe: $output" }
$work = Join-Path ([System.IO.Path]::GetTempPath()) ('ternal pigeons build ' + [guid]::NewGuid().ToString('N'))
$archive = Join-Path $work 'pigeons.tar.gz'; $sourceDirectory = Join-Path $work "pigeons-$($config.PIGEONS_COMMIT)"
New-Item -ItemType Directory -Path $work | Out-Null
try {
    $ProgressPreference = 'SilentlyContinue'
    Invoke-WebRequest -Uri "https://codeload.github.com/n0-computer/pigeons/tar.gz/$($config.PIGEONS_COMMIT)" -OutFile $archive -MaximumRedirection 5
    Assert-Sha256 $archive $config.PIGEONS_SOURCE_SHA256
    & tar -xzf $archive -C $work; if ($LASTEXITCODE -ne 0) { throw "tar failed to extract pinned pigeons source (exit $LASTEXITCODE)" }
    Assert-Sha256 (Join-Path $sourceDirectory 'Cargo.lock') $config.PIGEONS_CARGO_LOCK_SHA256
    Push-Location $sourceDirectory; try { & git apply --check --whitespace=nowarn $patchFile; if ($LASTEXITCODE -ne 0) { throw 'Ternal pigeons patch check failed' }; & git apply --whitespace=nowarn $patchFile; if ($LASTEXITCODE -ne 0) { throw 'Ternal pigeons patch failed' } } finally { Pop-Location }
    New-Item -ItemType Directory -Path $targetDirectory -Force | Out-Null
    $previousTarget = $env:CARGO_TARGET_DIR; $env:CARGO_TARGET_DIR = $targetDirectory
    try {
        & cargo test --locked --manifest-path (Join-Path $sourceDirectory 'Cargo.toml'); if ($LASTEXITCODE -ne 0) { throw 'patched pigeons tests failed' }
        & cargo build --locked --release --manifest-path (Join-Path $sourceDirectory 'Cargo.toml'); if ($LASTEXITCODE -ne 0) { throw 'cargo failed to build patched pigeons' }
    } finally { $env:CARGO_TARGET_DIR = $previousTarget }
    $builtBinary = Join-Path $targetDirectory 'release/pigeons.exe'; if (-not (Test-Path -LiteralPath $builtBinary -PathType Leaf)) { throw "cargo did not produce expected binary: $builtBinary" }
    New-Item -ItemType Directory -Path (Split-Path -Parent $output) -Force | Out-Null; Copy-Item -LiteralPath $builtBinary -Destination $output -Force
    Write-Output ("built`t{0}`tpigeons={1}`tcommit={2}" -f $output, $config.PIGEONS_VERSION, $config.PIGEONS_COMMIT)
} finally { Remove-Item -LiteralPath $work -Recurse -Force -ErrorAction SilentlyContinue }
