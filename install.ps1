# Nnun installer for Windows PowerShell 5.1 and PowerShell 7.
param([string]$Version = $env:NNUN_VERSION)
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12

if ($env:OS -ne 'Windows_NT') { throw 'Use install.sh on macOS or Linux.' }
$architecture = $env:PROCESSOR_ARCHITEW6432
if (-not $architecture) { $architecture = $env:PROCESSOR_ARCHITECTURE }
switch ($architecture.ToUpperInvariant()) {
    'AMD64' { $arch = 'amd64' }
    'ARM64' { $arch = 'arm64' }
    default { throw 'Supported architectures: x64 and ARM64.' }
}
$releaseRoot = 'https://github.com/Savokr/nnun-cli/releases'
if (-not $Version) {
    $release = Invoke-RestMethod -Uri 'https://api.github.com/repos/Savokr/nnun-cli/releases/latest' -Headers @{ 'User-Agent' = 'nnun-installer' }
    $Version = $release.tag_name
}
$Version = $Version -replace '^v', ''
if ($Version -notmatch '^\d+\.\d+\.\d+(-[A-Za-z0-9]+([.-][A-Za-z0-9]+)*)?$') { throw 'Invalid version; expected 0.1.0 or v0.1.0.' }
$installDir = $env:NNUN_INSTALL_DIR
if (-not $installDir) { $installDir = Join-Path $env:LOCALAPPDATA 'Nnun\bin' }
if (-not [IO.Path]::IsPathRooted($installDir) -or $installDir -match "[`r`n]") { throw 'NNUN_INSTALL_DIR must be an absolute path without line breaks.' }
$null = New-Item -ItemType Directory -Path $installDir -Force
$tempDir = Join-Path $installDir ('.nnun-install-' + [Guid]::NewGuid().ToString('N'))
$null = New-Item -ItemType Directory -Path $tempDir
try {
    $asset = "nnun_${Version}_windows_${arch}.zip"
    $base = "$releaseRoot/download/v$Version"
    $archive = Join-Path $tempDir 'archive.zip'
    Write-Host "Installing nnun $Version for Windows/$arch..."
    Invoke-WebRequest -UseBasicParsing -Uri "$base/$asset" -OutFile $archive
    $checksums = (Invoke-WebRequest -UseBasicParsing -Uri "$base/checksums.txt").Content
    if ($checksums -is [byte[]]) { $checksums = [Text.Encoding]::UTF8.GetString($checksums) }
    $matchesForAsset = @($checksums -split '\r?\n' | Where-Object { $_ -match ('^[a-fA-F0-9]{64}\s+' + [regex]::Escape($asset) + '$') })
    if ($matchesForAsset.Count -ne 1) { throw 'Missing or invalid release checksum.' }
    $expected = ($matchesForAsset[0] -split '\s+')[0]
    $actual = (Get-FileHash -LiteralPath $archive -Algorithm SHA256).Hash
    if ($actual -ne $expected) { throw 'Checksum mismatch; installation stopped.' }
    Expand-Archive -LiteralPath $archive -DestinationPath $tempDir
    $binary = Join-Path $tempDir 'nnun.exe'
    if (-not (Test-Path -LiteralPath $binary -PathType Leaf)) { throw 'Archive did not contain nnun.exe.' }
    $destination = Join-Path $installDir 'nnun.exe'
    # Same-volume replacement; no partially downloaded binary reaches PATH.
    if (Test-Path -LiteralPath $destination) { [IO.File]::Replace($binary, $destination, [NullString]::Value) }
    else { [IO.File]::Move($binary, $destination) }
    $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
    $parts = @($userPath -split ';' | Where-Object { $_ })
    if (-not ($parts | Where-Object { $_.TrimEnd('\') -ieq $installDir.TrimEnd('\') })) {
        [Environment]::SetEnvironmentVariable('Path', (($parts + $installDir) -join ';'), 'User')
    }
    if (-not (($env:Path -split ';') | Where-Object { $_.TrimEnd('\') -ieq $installDir.TrimEnd('\') })) { $env:Path = "$installDir;$env:Path" }
    & $destination --version
    if ($LASTEXITCODE -ne 0) { throw 'Installed CLI could not run.' }
    Write-Host 'Open a new terminal, then run: nnun login'
}
finally {
    # This exact directory was created for this installation.
    Remove-Item -LiteralPath $tempDir -Recurse -Force -ErrorAction SilentlyContinue
}
