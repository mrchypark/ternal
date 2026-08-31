$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

function Get-NativeWindowsPlatform {
    if (-not [System.Runtime.InteropServices.RuntimeInformation]::IsOSPlatform(
            [System.Runtime.InteropServices.OSPlatform]::Windows)) {
        throw 'Ternal CLI Windows packaging requires Windows'
    }

    $osArchitecture = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture
    $processArchitecture = [System.Runtime.InteropServices.RuntimeInformation]::ProcessArchitecture
    if ($osArchitecture -ne $processArchitecture) {
        throw "Ternal CLI Windows packaging requires a native process (OS: $osArchitecture, process: $processArchitecture)"
    }

    switch ($processArchitecture.ToString()) {
        'X64' { return 'amd64' }
        'Arm64' { return 'arm64' }
        default { throw "unsupported native Windows architecture: $processArchitecture" }
    }
}

function Read-PigeonsBuildConfig([string] $Path) {
    $values = @{}
    foreach ($line in [System.IO.File]::ReadAllLines($Path)) {
        $trimmed = $line.Trim()
        if ($trimmed.Length -eq 0 -or $trimmed.StartsWith('#')) {
            continue
        }
        if ($trimmed -notmatch '^(?<name>[A-Z][A-Z0-9_]*)=(?<value>[^\r\n]*)$') {
            throw "invalid build config line: $line"
        }
        if ($values.ContainsKey($Matches.name)) {
            throw "duplicate build config key: $($Matches.name)"
        }
        $values[$Matches.name] = $Matches.value
    }
    foreach ($name in @('PIGEONS_VERSION', 'PIGEONS_COMMIT')) {
        if (-not $values.ContainsKey($name) -or [string]::IsNullOrWhiteSpace($values[$name])) {
            throw "missing build config key: $name"
        }
    }
    return $values
}

function Get-AbsolutePath([string] $Path) {
    if ([string]::IsNullOrWhiteSpace($Path)) {
        throw 'output path must not be empty'
    }
    if ([System.IO.Path]::IsPathRooted($Path)) {
        return [System.IO.Path]::GetFullPath($Path)
    }
    return [System.IO.Path]::GetFullPath((Join-Path (Get-Location).ProviderPath $Path))
}

function Test-PathWithin([string] $Candidate, [string] $Directory) {
    $separator = [System.IO.Path]::DirectorySeparatorChar
    $prefix = $Directory.TrimEnd($separator) + $separator
    return $Candidate.StartsWith($prefix, [System.StringComparison]::OrdinalIgnoreCase)
}

function Assert-SafePackageDirectory([string] $Path) {
    $root = [System.IO.Path]::GetPathRoot($Path)
    if ($Path.TrimEnd('\', '/') -eq $root.TrimEnd('\', '/')) {
        throw "refusing unsafe package directory: $Path"
    }

    $currentDirectory = [System.IO.Path]::GetFullPath((Get-Location).ProviderPath)
    if ($Path -eq $currentDirectory -or (Test-PathWithin $currentDirectory $Path)) {
        throw "refusing package directory that contains the working directory: $Path"
    }
}

function Assert-PeMachine([string] $Path, [string] $Platform, [string] $Label) {
    $expectedMachine = switch ($Platform) {
        'amd64' { [uint16] 0x8664 }
        'arm64' { [uint16] 0xAA64 }
        default { throw "unsupported Windows platform for PE validation: $Platform" }
    }

    $stream = [System.IO.File]::Open(
        $Path,
        [System.IO.FileMode]::Open,
        [System.IO.FileAccess]::Read,
        [System.IO.FileShare]::Read
    )
    $reader = [System.IO.BinaryReader]::new($stream)
    try {
        if ($stream.Length -lt 64 -or $reader.ReadUInt16() -ne 0x5A4D) {
            throw "$Label is not a valid PE executable: $Path"
        }
        $stream.Position = 0x3C
        $peOffset = $reader.ReadUInt32()
        if ([uint64] $peOffset + 6 -gt [uint64] $stream.Length) {
            throw "$Label has an invalid PE header offset: $Path"
        }
        $stream.Position = $peOffset
        if ($reader.ReadUInt32() -ne 0x00004550) {
            throw "$Label is missing the PE signature: $Path"
        }
        $machine = $reader.ReadUInt16()
    } finally {
        $reader.Dispose()
        $stream.Dispose()
    }

    if ($machine -ne $expectedMachine) {
        $actual = '0x{0:X4}' -f $machine
        $expected = '0x{0:X4}' -f $expectedMachine
        throw "$Label PE machine $actual does not match Windows $Platform ($expected): $Path"
    }
}

function Set-EnvironmentValue([string] $Name, [AllowNull()] $Value) {
    if ($null -eq $Value) {
        Remove-Item "Env:$Name" -ErrorAction SilentlyContinue
    } else {
        Set-Item "Env:$Name" $Value
    }
}

$platform = Get-NativeWindowsPlatform
$scriptDirectory = $PSScriptRoot
$agentScriptDirectory = Join-Path $scriptDirectory '../agent'
$config = Read-PigeonsBuildConfig (Join-Path $agentScriptDirectory 'pigeons-build.env')
$expectedPackageName = "ternalctl-windows-$platform"

$cliBinary = if ([string]::IsNullOrWhiteSpace($env:TERNALCTL_BIN)) {
    'dist/bin/ternalctl.exe'
} else {
    $env:TERNALCTL_BIN
}
$packageDirectory = if ([string]::IsNullOrWhiteSpace($env:TERNALCTL_PACKAGE_DIR)) {
    "dist/$expectedPackageName"
} else {
    $env:TERNALCTL_PACKAGE_DIR
}
$archive = if ([string]::IsNullOrWhiteSpace($env:TERNALCTL_ARCHIVE)) {
    "dist/$expectedPackageName.zip"
} else {
    $env:TERNALCTL_ARCHIVE
}
$pigeonsBinary = if ([string]::IsNullOrWhiteSpace($env:TERNAL_TRANSPORT_BIN)) {
    $null
} else {
    $env:TERNAL_TRANSPORT_BIN
}

$cliBinary = Get-AbsolutePath $cliBinary
$packageDirectory = Get-AbsolutePath $packageDirectory
$archive = Get-AbsolutePath $archive
if ($null -ne $pigeonsBinary) {
    $pigeonsBinary = Get-AbsolutePath $pigeonsBinary
}

Assert-SafePackageDirectory $packageDirectory
if ((Split-Path -Leaf $packageDirectory) -cne $expectedPackageName) {
    throw "package directory must be named ${expectedPackageName}: $packageDirectory"
}
if ([System.IO.Path]::GetExtension($archive) -cne '.zip') {
    throw "Ternal CLI Windows archive must use .zip: $archive"
}
if ($archive -eq $packageDirectory -or (Test-PathWithin $archive $packageDirectory)) {
    throw "archive must be outside the package directory: $archive"
}
if ((Test-PathWithin $cliBinary $packageDirectory) -or
        ($null -ne $pigeonsBinary -and (Test-PathWithin $pigeonsBinary $packageDirectory))) {
    throw 'input binaries must be outside the package directory'
}
if (-not (Test-Path -LiteralPath $cliBinary -PathType Leaf)) {
    throw "missing ternalctl.exe: $cliBinary"
}

if ($null -eq $pigeonsBinary) {
    $pigeonsBinary = Get-AbsolutePath "dist/pigeons-windows-$platform.exe"
    $hadBuildOutput = Test-Path Env:TERNAL_TRANSPORT_OUTPUT
    $previousBuildOutput = $env:TERNAL_TRANSPORT_OUTPUT
    try {
        $env:TERNAL_TRANSPORT_OUTPUT = $pigeonsBinary
        & (Join-Path $agentScriptDirectory 'build-pigeons-windows.ps1')
    } finally {
        Set-EnvironmentValue 'TERNAL_TRANSPORT_OUTPUT' $(if ($hadBuildOutput) { $previousBuildOutput } else { $null })
    }
}
if (-not (Test-Path -LiteralPath $pigeonsBinary -PathType Leaf)) {
    throw "missing patched pigeons.exe: $pigeonsBinary"
}
Assert-PeMachine $cliBinary $platform 'ternalctl.exe'
Assert-PeMachine $pigeonsBinary $platform 'pigeons.exe'
if ($archive -eq $cliBinary -or $archive -eq $pigeonsBinary) {
    throw 'archive must not overwrite an input binary'
}

Remove-Item -LiteralPath $packageDirectory -Recurse -Force -ErrorAction SilentlyContinue
Remove-Item -LiteralPath $archive -Force -ErrorAction SilentlyContinue
New-Item -ItemType Directory -Path $packageDirectory | Out-Null
New-Item -ItemType Directory -Path (Split-Path -Parent $archive) -Force | Out-Null

Copy-Item -LiteralPath $cliBinary -Destination (Join-Path $packageDirectory 'ternalctl.exe')
Copy-Item -LiteralPath $pigeonsBinary -Destination (Join-Path $packageDirectory 'pigeons.exe')
Copy-Item -LiteralPath (Join-Path $agentScriptDirectory 'pigeons-LICENSE') -Destination (Join-Path $packageDirectory 'LICENSE.pigeons')

$readme = @"
Ternal CLI Windows $platform bundle

Supported platform: native Windows on $platform.
Runtime requirement: the Windows OpenSSH Client must be installed, with ssh.exe and ssh-keygen.exe available on PATH.
Install it from an elevated PowerShell prompt when needed:
  Add-WindowsCapability -Online -Name OpenSSH.Client~~~~0.0.1.0

Files:
- ternalctl.exe
- pigeons.exe
- LICENSE.pigeons (upstream MIT license)

Bundled pigeons: upstream $($config.PIGEONS_VERSION) ($($config.PIGEONS_COMMIT)), Ternal transport diagnostics patch

Keep both executables in the same directory.

pigeons lookup order:
1. TERNAL_TRANSPORT_BIN
2. pigeons.exe beside the resolved ternalctl.exe executable target
3. pigeons.exe on PATH

Run:
  `$env:TERNAL_API_URL = 'https://<ternal-host>'
  .\ternalctl.exe login
"@
[System.IO.File]::WriteAllText(
    (Join-Path $packageDirectory 'README.txt'),
    $readme.Replace("`r`n", "`n"),
    [System.Text.UTF8Encoding]::new($false)
)

Add-Type -AssemblyName System.IO.Compression
Add-Type -AssemblyName System.IO.Compression.FileSystem
$zip = [System.IO.Compression.ZipFile]::Open(
    $archive,
    [System.IO.Compression.ZipArchiveMode]::Create
)
try {
    foreach ($name in @('ternalctl.exe', 'pigeons.exe', 'LICENSE.pigeons', 'README.txt')) {
        $entry = $zip.CreateEntry(
            "$expectedPackageName/$name",
            [System.IO.Compression.CompressionLevel]::Optimal
        )
        $entry.LastWriteTime = [DateTimeOffset]::new(2000, 1, 1, 0, 0, 0, [TimeSpan]::Zero)
        $input = [System.IO.File]::OpenRead((Join-Path $packageDirectory $name))
        $output = $entry.Open()
        try {
            $input.CopyTo($output)
        } finally {
            $output.Dispose()
            $input.Dispose()
        }
    }
} finally {
    $zip.Dispose()
}

Write-Output ("packaged`t{0}" -f $archive)
