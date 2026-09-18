@echo off
setlocal
rem Arguments are passed on, so "Uninstall-NEVR.cmd -RemoveEvidence" also
rem deletes the local evidence folder. Without an argument evidence is kept.
powershell.exe -NoProfile -ExecutionPolicy Bypass -File "%~dp0Uninstall-NEVR.ps1" %*
if errorlevel 1 pause
