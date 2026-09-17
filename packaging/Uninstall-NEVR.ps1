[CmdletBinding()]
param(
    [switch]$RemoveEvidence,
    # Internal: set when this script relaunched itself from a temporary folder.
    [switch]$Relocated,
    # Do not wait for a key press at the end (automation and tests).
    [switch]$NoPause,
    # Where the desktop shortcut lives. Tests pass a scratch folder so that the
    # real Desktop of the machine running them is never touched.
    [string]$DesktopDirectory = ''
)

$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'Program-Snapshot.ps1')
$roots = Resolve-NEVRInstallRoots $env:LOCALAPPDATA
$installRoot, $dataRoot = $roots.InstallRoot, $roots.DataRoot
if (-not $DesktopDirectory) { $DesktopDirectory = [Environment]::GetFolderPath('Desktop') }
$startShortcut = Join-Path $env:APPDATA 'Microsoft\Windows\Start Menu\Programs\NEVR-Anticheat.lnk'
$desktopShortcut = Join-Path $DesktopDirectory 'NEVR-Anticheat.lnk'

Assert-NEVRProgramsClosed $installRoot

function Test-NEVRPathInside([string]$Path, [string]$Root) {
    $prefix = [IO.Path]::GetFullPath($Root).TrimEnd('\', '/') + [IO.Path]::DirectorySeparatorChar
    return ([IO.Path]::GetFullPath($Path).TrimEnd('\', '/') + [IO.Path]::DirectorySeparatorChar).StartsWith($prefix, [StringComparison]::OrdinalIgnoreCase)
}

# Install-NEVR.ps1 copies this uninstaller INTO the program folder, which is the
# only place a user can start it from. A folder cannot be deleted while it is
# the current directory of the cmd.exe wrapper and of this PowerShell, and
# cmd.exe re-reads Uninstall-NEVR.cmd from it line by line. The old script
# therefore deleted the shortcuts, failed with "in use", and left every program
# file behind. Continue from a temporary copy instead, outside the folder.
if (-not $Relocated -and (Test-NEVRPathInside $PSScriptRoot $installRoot)) {
    $stage = Join-Path ([IO.Path]::GetTempPath()) ('nevr-uninstall-' + [Guid]::NewGuid().ToString('N'))
    [IO.Directory]::CreateDirectory($stage) | Out-Null
    foreach ($name in @('Uninstall-NEVR.ps1', 'Program-Snapshot.ps1')) {
        Copy-Item -LiteralPath (Join-Path $PSScriptRoot $name) -Destination $stage
    }
    $quoted = { param($value) '"' + $value.TrimEnd('\') + '"' }
    $arguments = @('-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', (& $quoted (Join-Path $stage 'Uninstall-NEVR.ps1')),
        '-Relocated', '-DesktopDirectory', (& $quoted $DesktopDirectory))
    if ($RemoveEvidence) { $arguments += '-RemoveEvidence' }
    if ($NoPause) { $arguments += '-NoPause' }
    $window = if ($NoPause) { 'Hidden' } else { 'Normal' }
    Start-Process -FilePath 'powershell.exe' -ArgumentList $arguments -WorkingDirectory ([IO.Path]::GetTempPath()) -WindowStyle $window
    Write-Host 'Uninstall continues in a separate window so that the program folder is no longer in use.'
    exit 0
}

$failure = $null
try {
    # Never sit inside a folder that is about to be deleted. Set-Location moves
    # only PowerShell's own location; the process directory is separate.
    Set-Location -LiteralPath ([IO.Path]::GetTempPath())
    [Environment]::CurrentDirectory = [IO.Path]::GetTempPath()
    if (Test-Path -LiteralPath $installRoot) {
        # The wrapper that started us may need a moment to exit and release the
        # folder. Retry the folder itself; never loop forever.
        $deadline = [DateTime]::UtcNow.AddSeconds(20)
        while ($true) {
            try {
                Remove-Item -LiteralPath $installRoot -Recurse -Force
                break
            } catch {
                if ([DateTime]::UtcNow -ge $deadline) {
                    throw "The program folder could not be removed: $($_.Exception.Message) Close any window or terminal that is open in $installRoot and run the uninstaller again. The shortcuts were kept so NEVR can still be started."
                }
                Start-Sleep -Milliseconds 500
            }
        }
    }
    # Only now: a failed uninstall must not leave a working program with no way to start it.
    Remove-Item -LiteralPath $startShortcut, $desktopShortcut -Force -ErrorAction SilentlyContinue
    if ($RemoveEvidence -and (Test-Path -LiteralPath $dataRoot)) {
        Remove-Item -LiteralPath $dataRoot -Recurse -Force
        Write-Host 'NEVR-Anticheat and its local evidence were removed.'
    } else {
        Write-Host "NEVR-Anticheat was removed. Evidence was preserved at $dataRoot"
    }
} catch {
    $failure = $_.Exception.Message
    Write-Host "Uninstall did not finish: $failure" -ForegroundColor Red
}

if ($Relocated) {
    # Best effort: this script is already loaded, so its temporary copy can go.
    $stagePrefix = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\', '/') + [IO.Path]::DirectorySeparatorChar + 'nevr-uninstall-'
    if ([IO.Path]::GetFullPath($PSScriptRoot).StartsWith($stagePrefix, [StringComparison]::OrdinalIgnoreCase)) {
        Remove-Item -LiteralPath $PSScriptRoot -Recurse -Force -ErrorAction SilentlyContinue
    }
    if (-not $NoPause) { $null = Read-Host 'Press Enter to close this window' }
}
if ($failure) { exit 1 }
