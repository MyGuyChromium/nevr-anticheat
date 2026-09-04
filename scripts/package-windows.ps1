[CmdletBinding()]
param(
    [string]$OutputDirectory = "",
    [switch]$BuildInstaller
)

$ErrorActionPreference = "Stop"
$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
if ([string]::IsNullOrWhiteSpace($OutputDirectory)) {
    $OutputDirectory = Join-Path $repoRoot "dist"
}
$outputRoot = [System.IO.Path]::GetFullPath($OutputDirectory)
[System.IO.Directory]::CreateDirectory($outputRoot) | Out-Null

$resourcePath = Join-Path $repoRoot "cmd\desktop\resource_windows_amd64.syso"

$stage = Join-Path $outputRoot ("NEVR-Anticheat-Windows-x64-" + [System.IO.Path]::GetRandomFileName())
$stage = [System.IO.Path]::GetFullPath($stage)
if (-not $stage.StartsWith($outputRoot + [System.IO.Path]::DirectorySeparatorChar, [System.StringComparison]::OrdinalIgnoreCase)) {
    throw "Refusing to create a staging directory outside $outputRoot"
}
[System.IO.Directory]::CreateDirectory($stage) | Out-Null

try {
    $mainSource = Get-Content -Raw -LiteralPath (Join-Path $repoRoot "cmd\desktop\main.go")
    $versionMatch = [regex]::Match($mainSource, 'const appVersion = "([0-9]+)\.([0-9]+)\.([0-9]+)"')
    if (-not $versionMatch.Success) { throw "Could not read appVersion from cmd/desktop/main.go" }
    $windres = Get-Command windres.exe -ErrorAction SilentlyContinue | Select-Object -ExpandProperty Source -ErrorAction SilentlyContinue
    if ([string]::IsNullOrWhiteSpace($windres)) { throw "windres.exe is required to add the NEVR icon and version metadata" }
    $programs = @(
        @{ Name = "nevr-desktop.exe"; Package = "./cmd/desktop" },
        @{ Name = "nevr-ac.exe";      Package = "./cmd/anticheat" },
        @{ Name = "nevr-server.exe";  Package = "./cmd/server" },
        @{ Name = "nevr-bridge.exe";  Package = "./cmd/bridge" },
        @{ Name = "nevr-compat.exe";  Package = "./cmd/compat" }
    )
    Push-Location $repoRoot
    try {
        & $windres -I . `
            "-DNEVR_VERSION_MAJOR=$($versionMatch.Groups[1].Value)" `
            "-DNEVR_VERSION_MINOR=$($versionMatch.Groups[2].Value)" `
            "-DNEVR_VERSION_PATCH=$($versionMatch.Groups[3].Value)" `
            "packaging\nevr-version.rc" -O coff -o "cmd\desktop\resource_windows_amd64.syso"
        if ($LASTEXITCODE -ne 0) { throw "windres failed while creating desktop version metadata" }

        $revision = (& git rev-parse HEAD).Trim()
        if ($LASTEXITCODE -ne 0) { throw "git rev-parse failed" }
        $buildTime = [DateTime]::UtcNow.ToString("yyyy-MM-ddTHH:mm:ssZ")
        foreach ($program in $programs) {
            $target = Join-Path $stage $program.Name
            $linkerFlags = "-s -w"
            if ($program.Name -eq "nevr-desktop.exe") {
                $linkerFlags += " -X main.buildCommit=$revision -X main.buildTime=$buildTime"
            }
            & go build -trimpath -ldflags $linkerFlags -o $target $program.Package
            if ($LASTEXITCODE -ne 0) {
                throw "go build failed for $($program.Package)"
            }
        }
    }
    finally {
        Pop-Location
    }

    Copy-Item -LiteralPath (Join-Path $repoRoot "README.md") -Destination $stage
    Copy-Item -LiteralPath (Join-Path $repoRoot "packaging\README-WINDOWS.txt") -Destination $stage
    $configTarget = Join-Path $stage "configs"
    [System.IO.Directory]::CreateDirectory($configTarget) | Out-Null
    Copy-Item -LiteralPath (Join-Path $repoRoot "configs\default.toml") -Destination $configTarget
    Copy-Item -LiteralPath (Join-Path $repoRoot "configs\shadow_deploy.toml") -Destination $configTarget
    $installerTarget = Join-Path $stage "installer"
    [System.IO.Directory]::CreateDirectory($installerTarget) | Out-Null
    Copy-Item -LiteralPath (Join-Path $repoRoot "packaging\Install-NEVR.cmd") -Destination $stage
    Copy-Item -LiteralPath (Join-Path $repoRoot "packaging\Install-NEVR.ps1") -Destination $installerTarget
    Copy-Item -LiteralPath (Join-Path $repoRoot "packaging\Uninstall-NEVR.cmd") -Destination $installerTarget
    Copy-Item -LiteralPath (Join-Path $repoRoot "packaging\Uninstall-NEVR.ps1") -Destination $installerTarget
    Copy-Item -LiteralPath (Join-Path $repoRoot "packaging\Rollback-NEVR.cmd") -Destination $installerTarget
    Copy-Item -LiteralPath (Join-Path $repoRoot "packaging\Rollback-NEVR.ps1") -Destination $installerTarget

    $zipPath = Join-Path $outputRoot "NEVR-Anticheat-Windows-x64.zip"
    Compress-Archive -Path (Join-Path $stage "*") -DestinationPath $zipPath -Force
    Write-Host "Created $zipPath"
    if ($BuildInstaller) {
        & (Join-Path $repoRoot "scripts\build-installer.ps1") -PackageZip $zipPath -OutputDirectory $outputRoot
        if ($LASTEXITCODE -ne 0) { throw "installer build failed" }
    }
}
finally {
    if (Test-Path -LiteralPath $stage) {
        Remove-Item -LiteralPath $stage -Recurse -Force
    }
    if (Test-Path -LiteralPath $resourcePath) {
        Remove-Item -LiteralPath $resourcePath -Force
    }
}
