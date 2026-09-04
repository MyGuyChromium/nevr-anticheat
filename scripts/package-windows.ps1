[CmdletBinding()]
param(
    [string]$OutputDirectory = ""
)

$ErrorActionPreference = "Stop"
$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
if ([string]::IsNullOrWhiteSpace($OutputDirectory)) {
    $OutputDirectory = Join-Path $repoRoot "dist"
}
$outputRoot = [System.IO.Path]::GetFullPath($OutputDirectory)
[System.IO.Directory]::CreateDirectory($outputRoot) | Out-Null

$stage = Join-Path $outputRoot ("NEVR-Anticheat-Windows-x64-" + [System.IO.Path]::GetRandomFileName())
$stage = [System.IO.Path]::GetFullPath($stage)
if (-not $stage.StartsWith($outputRoot + [System.IO.Path]::DirectorySeparatorChar, [System.StringComparison]::OrdinalIgnoreCase)) {
    throw "Refusing to create a staging directory outside $outputRoot"
}
[System.IO.Directory]::CreateDirectory($stage) | Out-Null

try {
    $programs = @(
        @{ Name = "nevr-desktop.exe"; Package = "./cmd/desktop" },
        @{ Name = "nevr-ac.exe";      Package = "./cmd/anticheat" },
        @{ Name = "nevr-server.exe";  Package = "./cmd/server" },
        @{ Name = "nevr-bridge.exe";  Package = "./cmd/bridge" },
        @{ Name = "nevr-compat.exe";  Package = "./cmd/compat" }
    )
    Push-Location $repoRoot
    try {
        foreach ($program in $programs) {
            $target = Join-Path $stage $program.Name
            & go build -trimpath -ldflags "-s -w" -o $target $program.Package
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

    $zipPath = Join-Path $outputRoot "NEVR-Anticheat-Windows-x64.zip"
    Compress-Archive -Path (Join-Path $stage "*") -DestinationPath $zipPath -Force
    Write-Host "Created $zipPath"
}
finally {
    if (Test-Path -LiteralPath $stage) {
        Remove-Item -LiteralPath $stage -Recurse -Force
    }
}
