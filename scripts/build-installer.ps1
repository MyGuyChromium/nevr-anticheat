[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$PackageZip,
    [string]$OutputDirectory = "",
    [string]$Version = "",
    [string]$SignToolCommand = ""
)

$ErrorActionPreference = "Stop"
$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$zipPath = (Resolve-Path -LiteralPath $PackageZip).Path
if ([string]::IsNullOrWhiteSpace($OutputDirectory)) {
    $OutputDirectory = Join-Path $repoRoot "dist"
}
$outputRoot = [System.IO.Path]::GetFullPath($OutputDirectory)
[System.IO.Directory]::CreateDirectory($outputRoot) | Out-Null

if ([string]::IsNullOrWhiteSpace($Version)) {
    $main = Get-Content -Raw -LiteralPath (Join-Path $repoRoot "cmd\desktop\main.go")
    $match = [regex]::Match($main, 'const appVersion = "([0-9]+\.[0-9]+\.[0-9]+)"')
    if (-not $match.Success) { throw "Could not read appVersion from cmd/desktop/main.go" }
    $Version = $match.Groups[1].Value
}
if ($Version -notmatch '^\d+\.\d+\.\d+$') { throw "Installer version must be MAJOR.MINOR.PATCH" }

$isccCandidates = @(@(
    (Get-Command ISCC.exe -ErrorAction SilentlyContinue | Select-Object -ExpandProperty Source -ErrorAction SilentlyContinue),
    (Join-Path $env:LOCALAPPDATA "Programs\Inno Setup 6\ISCC.exe"),
    (Join-Path ${env:ProgramFiles(x86)} "Inno Setup 7\ISCC.exe"),
    (Join-Path ${env:ProgramFiles(x86)} "Inno Setup 6\ISCC.exe")
) | Where-Object { $_ -and (Test-Path -LiteralPath $_) })
if ($isccCandidates.Count -eq 0) {
    throw "Inno Setup 6 or newer is required to build NEVR-Anticheat-Setup.exe"
}
$iscc = $isccCandidates[0]

$stage = Join-Path $outputRoot ("installer-stage-" + [System.IO.Path]::GetRandomFileName())
$stage = [System.IO.Path]::GetFullPath($stage)
if (-not $stage.StartsWith($outputRoot + [System.IO.Path]::DirectorySeparatorChar, [System.StringComparison]::OrdinalIgnoreCase)) {
    throw "Refusing to create installer staging outside $outputRoot"
}
[System.IO.Directory]::CreateDirectory($stage) | Out-Null

try {
    Expand-Archive -LiteralPath $zipPath -DestinationPath $stage -Force
    $desktopExecutables = @(Get-ChildItem -LiteralPath $stage -Filter "nevr-desktop.exe" -File -Recurse)
    if ($desktopExecutables.Count -ne 1) {
        throw "Package must contain exactly one nevr-desktop.exe"
    }
    $sourceRoot = $desktopExecutables[0].Directory.FullName
    foreach ($required in @(
        "nevr-desktop.exe", "nevr-ac.exe", "nevr-server.exe", "nevr-bridge.exe", "nevr-compat.exe",
        "configs\default.toml", "configs\shadow_deploy.toml", "README.md", "README-WINDOWS.txt",
        "installer\Rollback-NEVR.cmd", "installer\Rollback-NEVR.ps1"
    )) {
        if (-not (Test-Path -LiteralPath (Join-Path $sourceRoot $required))) {
            throw "Package is incomplete: missing $required"
        }
    }

    $scriptPath = Join-Path $repoRoot "packaging\NEVR-Anticheat.iss"
    $compilerArgs = @(
        "/Qp",
        "/O$outputRoot",
        "/DSourceDir=$sourceRoot",
        "/DMyAppVersion=$Version"
    )
    if (-not [string]::IsNullOrWhiteSpace($SignToolCommand)) {
        $compilerArgs += "/DEnableSigning=1"
        $compilerArgs += "/Snevr=$SignToolCommand"
    }
    $compilerArgs += $scriptPath
    & $iscc @compilerArgs
    if ($LASTEXITCODE -ne 0) { throw "Inno Setup compilation failed with exit code $LASTEXITCODE" }

    $setupPath = Join-Path $outputRoot "NEVR-Anticheat-Setup.exe"
    if (-not (Test-Path -LiteralPath $setupPath)) { throw "Installer output was not created" }
    if (-not [string]::IsNullOrWhiteSpace($SignToolCommand)) {
        $signature = Get-AuthenticodeSignature -LiteralPath $setupPath
        if ($signature.Status -ne "Valid") { throw "Installer signature is $($signature.Status), expected Valid" }
    }
    $hash = (Get-FileHash -Algorithm SHA256 -LiteralPath $setupPath).Hash.ToLowerInvariant()
    Set-Content -LiteralPath ($setupPath + ".sha256") -Value ($hash + "  NEVR-Anticheat-Setup.exe") -Encoding ascii
    Write-Host "Created $setupPath"
    Write-Host "SHA256 $hash"
}
finally {
    if (Test-Path -LiteralPath $stage) {
        Remove-Item -LiteralPath $stage -Recurse -Force
    }
}
