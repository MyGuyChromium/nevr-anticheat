NEVR-Anticheat for Windows (x64)
================================

START HERE

1. Extract the entire ZIP. Do not run an EXE from inside the ZIP.
2. Double-click nevr-desktop.exe.
3. Drop one or more .echoreplay files onto the local app window.

The database is stored beside nevr-desktop.exe. Windows Firewall does not need
to expose it: the desktop app listens only on 127.0.0.1 (this computer).

SPARK REPLAY VIEWER

Detection rows have an Open clip button. It creates a short .echoreplay around
the exact detector frame and launches Spark Replay Viewer. In Spark, open its
Replay Viewer once first so Spark installs:

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
not contain compiled EXEs. Download NEVR-Anticheat-Windows-x64.zip from the
repository's Releases page (or the Windows package artifact from an Actions
run) when you want ready-to-run programs.
