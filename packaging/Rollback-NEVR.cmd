@echo off
setlocal
powershell.exe -NoProfile -ExecutionPolicy Bypass -File "%~dp0Rollback-NEVR.ps1"
if errorlevel 1 pause
