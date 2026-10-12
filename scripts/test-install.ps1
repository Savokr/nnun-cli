$ErrorActionPreference = 'Stop'
$fixtureDir = Join-Path ([IO.Path]::GetTempPath()) ('nnun-installer-test-' + [Guid]::NewGuid().ToString('N'))
$null = New-Item -ItemType Directory -Path $fixtureDir
$oldInstallDir = $env:NNUN_INSTALL_DIR
$oldPath = $env:Path
$oldUserPath = [Environment]::GetEnvironmentVariable('Path', 'User')
try {
    $binary = Join-Path $fixtureDir 'nnun.exe'
    & go build -o $binary .
    if ($LASTEXITCODE -ne 0) { throw 'Fixture build failed.' }
    $fixtureArchive = Join-Path $fixtureDir 'fixture.zip'
    Compress-Archive -LiteralPath $binary -DestinationPath $fixtureArchive
    $script:fixtureArchive = $fixtureArchive
    $script:checksum = (Get-FileHash -LiteralPath $fixtureArchive -Algorithm SHA256).Hash
    $script:corruptChecksum = $false
    function Invoke-WebRequest {
        param([switch]$UseBasicParsing, [string]$Uri, [string]$OutFile)
        if ($Uri -match '/download/v0\.1\.0/nnun_0\.1\.0_windows_(amd64|arm64)\.zip$') {
            Copy-Item -LiteralPath $script:fixtureArchive -Destination $OutFile
        }
        elseif ($Uri -match '/download/v0\.1\.0/checksums\.txt$') {
            $sum = $script:checksum
            if ($script:corruptChecksum) { $sum = '0' * 64 }
            return @{ Content = "$sum  nnun_0.1.0_windows_amd64.zip`n$sum  nnun_0.1.0_windows_arm64.zip`n" }
        }
        else { throw "Unexpected installer request: $Uri" }
    }
    function Invoke-RestMethod {
        param([string]$Uri, [hashtable]$Headers)
        if ($Uri -ne 'https://api.github.com/repos/Savokr/nnun-cli/releases/latest') { throw 'Unexpected latest URL.' }
        return @{ tag_name = 'v0.1.0' }
    }
    $env:NNUN_INSTALL_DIR = Join-Path $fixtureDir 'user space\bin'
    & ./install.ps1
    & ./install.ps1 -Version 'v0.1.0'
    if (-not (Test-Path -LiteralPath (Join-Path $env:NNUN_INSTALL_DIR 'nnun.exe'))) { throw 'CLI was not installed.' }
    $entries = @([Environment]::GetEnvironmentVariable('Path', 'User') -split ';' | Where-Object { $_ -eq $env:NNUN_INSTALL_DIR })
    if ($entries.Count -ne 1) { throw 'PATH update was not idempotent.' }
    $env:NNUN_INSTALL_DIR = Join-Path $fixtureDir 'corrupt\bin'
    $script:corruptChecksum = $true
    $rejected = $false
    try { & ./install.ps1 -Version '0.1.0' }
    catch { if ($_.Exception.Message -notmatch 'Checksum mismatch') { throw }; $rejected = $true }
    if (-not $rejected -or (Test-Path -LiteralPath (Join-Path $env:NNUN_INSTALL_DIR 'nnun.exe'))) { throw 'Corrupted archive was installed.' }
    Write-Host 'Windows installer version pinning, latest resolution, replacement, checksum, and PATH tests passed.'
}
finally {
    $env:NNUN_INSTALL_DIR = $oldInstallDir
    $env:Path = $oldPath
    [Environment]::SetEnvironmentVariable('Path', $oldUserPath, 'User')
    Remove-Item -LiteralPath $fixtureDir -Recurse -Force -ErrorAction SilentlyContinue
}
