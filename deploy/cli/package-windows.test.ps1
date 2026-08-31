param(
    [string] $TernalctlBin = $env:TERNALCTL_TEST_BIN,
    [string] $PigeonsBin = $env:TERNAL_PIGEONS_TEST_BIN
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

function Get-NativeWindowsPlatform {
    if (-not [System.Runtime.InteropServices.RuntimeInformation]::IsOSPlatform(
            [System.Runtime.InteropServices.OSPlatform]::Windows)) {
        throw 'Ternal CLI Windows package test requires Windows'
    }

    $osArchitecture = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture
    $processArchitecture = [System.Runtime.InteropServices.RuntimeInformation]::ProcessArchitecture
    if ($osArchitecture -ne $processArchitecture) {
        throw "Ternal CLI Windows package test requires a native process (OS: $osArchitecture, process: $processArchitecture)"
    }

    switch ($processArchitecture.ToString()) {
        'X64' { return 'amd64' }
        'Arm64' { return 'arm64' }
        default { throw "unsupported native Windows architecture: $processArchitecture" }
    }
}

function Get-AbsolutePath([string] $Path) {
    if ([string]::IsNullOrWhiteSpace($Path)) {
        throw 'binary path must not be empty'
    }
    if ([System.IO.Path]::IsPathRooted($Path)) {
        return [System.IO.Path]::GetFullPath($Path)
    }
    return [System.IO.Path]::GetFullPath((Join-Path (Get-Location).ProviderPath $Path))
}

function Set-EnvironmentValue([string] $Name, [AllowNull()] $Value) {
    if ($null -eq $Value) {
        Remove-Item "Env:$Name" -ErrorAction SilentlyContinue
    } else {
        Set-Item "Env:$Name" $Value
    }
}

function Set-PeMachine([string] $Path, [uint16] $Machine) {
    $stream = [System.IO.File]::Open(
        $Path,
        [System.IO.FileMode]::Open,
        [System.IO.FileAccess]::ReadWrite,
        [System.IO.FileShare]::None
    )
    $reader = [System.IO.BinaryReader]::new($stream, [System.Text.Encoding]::UTF8, $true)
    $writer = [System.IO.BinaryWriter]::new($stream, [System.Text.Encoding]::UTF8, $true)
    try {
        if ($stream.Length -lt 64 -or $reader.ReadUInt16() -ne 0x5A4D) {
            throw "test input is not a valid PE executable: $Path"
        }
        $stream.Position = 0x3C
        $peOffset = $reader.ReadUInt32()
        if ([uint64] $peOffset + 6 -gt [uint64] $stream.Length) {
            throw "test input has an invalid PE header offset: $Path"
        }
        $stream.Position = $peOffset
        if ($reader.ReadUInt32() -ne 0x00004550) {
            throw "test input is missing the PE signature: $Path"
        }
        $writer.Write($Machine)
        $writer.Flush()
    } finally {
        $writer.Dispose()
        $reader.Dispose()
        $stream.Dispose()
    }
}

$platform = Get-NativeWindowsPlatform
$expectedPackageName = "ternalctl-windows-$platform"
$scriptDirectory = $PSScriptRoot

if ([string]::IsNullOrWhiteSpace($TernalctlBin)) {
    throw 'pass -TernalctlBin or set TERNALCTL_TEST_BIN to a real native ternalctl.exe build'
}
if ([string]::IsNullOrWhiteSpace($PigeonsBin)) {
    if (-not [string]::IsNullOrWhiteSpace($env:TERNAL_PIGEONS_BIN)) {
        $PigeonsBin = $env:TERNAL_PIGEONS_BIN
    } else {
        throw 'pass -PigeonsBin or set TERNAL_PIGEONS_TEST_BIN to a real patched pigeons.exe build'
    }
}
$TernalctlBin = Get-AbsolutePath $TernalctlBin
$PigeonsBin = Get-AbsolutePath $PigeonsBin
if (-not (Test-Path -LiteralPath $TernalctlBin -PathType Leaf)) {
    throw "missing real ternalctl.exe test binary: $TernalctlBin"
}
if (-not (Test-Path -LiteralPath $PigeonsBin -PathType Leaf)) {
    throw "missing real patched pigeons.exe test binary: $PigeonsBin"
}

$work = Join-Path ([System.IO.Path]::GetTempPath()) ("ternal windows package test " + [guid]::NewGuid().ToString('N'))
$packageDirectory = Join-Path $work "dist/$expectedPackageName"
$archive = Join-Path $work "dist/$expectedPackageName.zip"
$extractDirectory = Join-Path $work 'fresh extraction with spaces'
$launchDirectory = Join-Path $work 'launch cwd without binaries'
$isolatedHome = Join-Path $work 'isolated user profile'
$isolatedSession = Join-Path $work 'isolated session/ternal-session.json'
$isolatedXdgConfig = Join-Path $work 'isolated xdg config'
$isolatedLocalAppData = Join-Path $work 'isolated local app data'
$isolatedAppData = Join-Path $work 'isolated roaming app data'
$emptyPath = Join-Path $work 'empty path'

$environmentNames = @(
    'TERNALCTL_BIN',
    'TERNAL_PIGEONS_BIN',
    'TERNALCTL_PACKAGE_DIR',
    'TERNALCTL_ARCHIVE',
    'TERNAL_API_URL',
    'TERNAL_CLAIMS',
    'TERNAL_CSRF_TOKEN',
    'TERNAL_USER',
    'TERNAL_GROUPS',
    'TERNAL_SESSION_COOKIE',
    'TERNAL_SESSION_PATH',
    'XDG_CONFIG_HOME',
    'LOCALAPPDATA',
    'APPDATA',
    'HOME',
    'USERPROFILE',
    'HOMEDRIVE',
    'HOMEPATH',
    'PATH'
)
$savedEnvironment = @{}
foreach ($name in $environmentNames) {
    $savedEnvironment[$name] = if (Test-Path "Env:$name") { (Get-Item "Env:$name").Value } else { $null }
}

New-Item -ItemType Directory -Path $work | Out-Null
try {
    $env:TERNALCTL_BIN = $TernalctlBin
    $env:TERNALCTL_PACKAGE_DIR = $packageDirectory
    $env:TERNALCTL_ARCHIVE = $archive

    $mismatchedPigeons = Join-Path $work 'mismatched pigeons.exe'
    Copy-Item -LiteralPath $PigeonsBin -Destination $mismatchedPigeons
    $wrongMachine = if ($platform -eq 'amd64') { [uint16] 0xAA64 } else { [uint16] 0x8664 }
    Set-PeMachine $mismatchedPigeons $wrongMachine
    $env:TERNAL_PIGEONS_BIN = $mismatchedPigeons
    $mismatchRejected = $false
    try {
        & (Join-Path $scriptDirectory 'package-windows.ps1') | Out-Null
    } catch {
        if ($_.Exception.Message -notmatch 'pigeons\.exe PE machine .* does not match Windows') {
            throw
        }
        $mismatchRejected = $true
    }
    if (-not $mismatchRejected) {
        throw 'package accepted a pigeons.exe for the wrong Windows architecture'
    }

    $env:TERNAL_PIGEONS_BIN = $PigeonsBin
    & (Join-Path $scriptDirectory 'package-windows.ps1')

    foreach ($name in @('ternalctl.exe', 'pigeons.exe', 'LICENSE.pigeons', 'README.txt')) {
        $path = Join-Path $packageDirectory $name
        if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
            throw "package is missing $name"
        }
    }
    if (-not (Test-Path -LiteralPath $archive -PathType Leaf)) {
        throw "package archive was not created: $archive"
    }
    $readme = [System.IO.File]::ReadAllText((Join-Path $packageDirectory 'README.txt'))
    foreach ($text in @('OpenSSH Client', 'ssh.exe', 'ssh-keygen.exe', 'TERNAL_PIGEONS_BIN')) {
        if (-not $readme.Contains($text)) {
            throw "README is missing required text: $text"
        }
    }

    Add-Type -AssemblyName System.IO.Compression
    Add-Type -AssemblyName System.IO.Compression.FileSystem
    $zip = [System.IO.Compression.ZipFile]::OpenRead($archive)
    try {
        $actualEntries = @(
            $zip.Entries |
                ForEach-Object { $_.FullName.Replace('\', '/') } |
                Sort-Object
        )
    } finally {
        $zip.Dispose()
    }
    $expectedEntries = @(
        "$expectedPackageName/README.txt",
        "$expectedPackageName/pigeons.exe",
        "$expectedPackageName/LICENSE.pigeons",
        "$expectedPackageName/ternalctl.exe"
    ) | Sort-Object
    $layoutDifference = Compare-Object -ReferenceObject $expectedEntries -DifferenceObject $actualEntries
    if ($null -ne $layoutDifference) {
        throw "unexpected ZIP layout:`n$($layoutDifference | Out-String)"
    }

    New-Item -ItemType Directory -Path $extractDirectory | Out-Null
    [System.IO.Compression.ZipFile]::ExtractToDirectory($archive, $extractDirectory)
    $bundleDirectory = Join-Path $extractDirectory $expectedPackageName
    $bundleCli = Join-Path $bundleDirectory 'ternalctl.exe'

    New-Item -ItemType Directory -Path $launchDirectory | Out-Null
    New-Item -ItemType Directory -Path $isolatedHome | Out-Null
    New-Item -ItemType Directory -Path $isolatedXdgConfig | Out-Null
    New-Item -ItemType Directory -Path $isolatedLocalAppData | Out-Null
    New-Item -ItemType Directory -Path $isolatedAppData | Out-Null
    New-Item -ItemType Directory -Path $emptyPath | Out-Null
    Remove-Item Env:TERNAL_PIGEONS_BIN -ErrorAction SilentlyContinue
    Remove-Item Env:TERNAL_CLAIMS -ErrorAction SilentlyContinue
    Remove-Item Env:TERNAL_CSRF_TOKEN -ErrorAction SilentlyContinue
    Remove-Item Env:TERNAL_SESSION_COOKIE -ErrorAction SilentlyContinue
    $env:TERNAL_API_URL = 'http://127.0.0.1:1'
    $env:TERNAL_USER = 'package-test'
    $env:TERNAL_GROUPS = 'package-test'
    $env:TERNAL_SESSION_PATH = $isolatedSession
    $env:XDG_CONFIG_HOME = $isolatedXdgConfig
    $env:LOCALAPPDATA = $isolatedLocalAppData
    $env:APPDATA = $isolatedAppData
    $env:HOME = $isolatedHome
    $env:USERPROFILE = $isolatedHome
    $env:HOMEDRIVE = [System.IO.Path]::GetPathRoot($isolatedHome).TrimEnd('\')
    $env:HOMEPATH = $isolatedHome.Substring([System.IO.Path]::GetPathRoot($isolatedHome).Length - 1)
    $env:PATH = $emptyPath

    $standardOutput = Join-Path $work 'ternalctl stdout.txt'
    $standardError = Join-Path $work 'ternalctl stderr.txt'
    $process = Start-Process `
        -FilePath $bundleCli `
        -ArgumentList @(
            'proxy',
            'host-1',
            'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa:22'
        ) `
        -WorkingDirectory $launchDirectory `
        -RedirectStandardOutput $standardOutput `
        -RedirectStandardError $standardError `
        -NoNewWindow `
        -Wait `
        -PassThru
    if ($process.ExitCode -eq 0) {
        throw 'expected loopback API request to fail'
    }
    $runtimeOutput = [System.IO.File]::ReadAllText($standardOutput) +
        [System.IO.File]::ReadAllText($standardError)
    $expectedRefusal = 'Cannot connect to Ternal at http://127.0.0.1:1.'
    if (-not $runtimeOutput.Contains($expectedRefusal)) {
        throw "bundle did not reach the loopback API after sibling endpoint-id succeeded:`n$runtimeOutput"
    }
    if ($runtimeOutput.Contains('run pigeons endpoint-id') -or
            $runtimeOutput.Contains('pigeons endpoint-id failed')) {
        throw "bundled sibling pigeons endpoint-id failed:`n$runtimeOutput"
    }

    Write-Output "cli windows $platform package test passed"
} finally {
    foreach ($name in $environmentNames) {
        Set-EnvironmentValue $name $savedEnvironment[$name]
    }
    Remove-Item -LiteralPath $work -Recurse -Force -ErrorAction SilentlyContinue
}
