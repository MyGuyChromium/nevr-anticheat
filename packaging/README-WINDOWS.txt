NEVR-Anticheat for Windows (x64)
================================

START HERE

1. Extract the entire ZIP. Do not run an EXE from inside the ZIP.
2. Recommended: double-click Install-NEVR.cmd. It installs for your Windows
   account, creates shortcuts, and keeps evidence outside the program folder.
3. Or run nevr-desktop.exe directly for portable mode.
4. Drop one or more .echoreplay files onto the local app window.

Installed mode keeps the database under LocalAppData\NEVR-Anticheat. Portable
mode stores it beside nevr-desktop.exe. Windows Firewall does not need to expose
it: the desktop app listens only on 127.0.0.1 (this computer).

SPARK REPLAY VIEWER

Detection rows have an Open clip button and every throw-log row has an Open
throw button. They create a short .echoreplay around the exact frame and launch
Spark Replay Viewer. The desktop header can also open the viewer without a
clip. In Spark, open its Replay Viewer once first so Spark installs:

  Documents\Replay Viewer\Replay Viewer.exe

If your viewer is somewhere else, set the NEVR_REPLAY_VIEWER environment
variable to its full Replay Viewer.exe path. NEVR never opens the clip in a web
browser. Generated clips are kept in your local NEVR-Anticheat cache directory.

INCLUDED PROGRAMS

  nevr-desktop.exe  Drag-and-drop replay analysis and match review
  nevr-ac.exe       Command-line replay analysis and moderator tools
  nevr-server.exe   Live telemetry ingestion server
  nevr-bridge.exe   Echo broadcaster to NEVR live bridge
  nevr-compat.exe   /session payload compatibility checker

The configs folder contains the normal default and an all-shadow deployment
configuration. See README.md for command-line and live-server instructions.

IMPORTANT

GitHub's green Code > Download ZIP button downloads source code only. It does
not contain compiled EXEs. Download the ready-to-run package here:

  https://github.com/MyGuyChromium/nevr-anticheat/releases/download/windows-latest/NEVR-Anticheat-Windows-x64.zip

The Windows package artifact from a successful Actions run contains the same
ZIP and a SHA-256 checksum.
