NEVR-Anticheat for Windows (x64)
================================

START HERE

NORMAL INSTALL (recommended)

1. Download NEVR-Anticheat-Setup.exe.
2. Double-click it and choose Install.
3. NEVR opens automatically. Drop one or more .echoreplay files onto the app.

Setup needs no ZIP extraction, command prompt, administrator access, folder
selection, or manual configuration. It creates the Windows shortcuts and the
Installed apps entry automatically.

AUTOMATIC ASSESSMENT

NEVR runs its detectors locally when you import a replay. The automatic
assessment lists players with signals to review, explains which detectors fired,
and offers Review in Spark. This works for every player without a name list,
an admission, a cloud upload, or an assistant examining the file.

Review needed includes observation-only (shadow) signals that deliberately add
zero score. No signals means no detector fired on the available telemetry, not
verified fair play. Detector signals are not confirmed cheating or automatic bans.

PORTABLE / ADVANCED

Extract the entire NEVR-Anticheat-Windows-x64.zip, then run nevr-desktop.exe.
Install-NEVR.cmd remains in that ZIP for compatibility with older installs.

Installed mode keeps the database under LocalAppData\NEVR-Anticheat. Portable
mode stores it beside nevr-desktop.exe. Windows Firewall does not need to expose
it: the desktop app listens only on 127.0.0.1 (this computer).

UPDATES AND ROLLBACK

Use Windows updates > Check for updates > Install update in the app. Installed
copies verify and run the update package, preserve local evidence and restart.
Portable copies use the manual-download fallback.

The current release feed is in a private GitHub repository. Downloads require
repository access; in-app updates additionally need a read-only token through
NEVR_GITHUB_TOKEN. Signing into a browser does not authenticate the app. The
installer itself runs offline if shared directly, but public distribution and
token-free updates require a publicly readable release channel. Never share a
maintainer token with recipients.

Installing a newer package snapshots the previous program files without
copying or changing the evidence database. If an update has a problem, run
Rollback-NEVR.cmd from LocalAppData\Programs\NEVR-Anticheat to restore the
newest program snapshot. Database backup/restore is a separate, verified action
inside the desktop app's Investigation & reliability studio.

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
not contain compiled EXEs. Download the one-click installer here:

  https://github.com/MyGuyChromium/nevr-anticheat/releases/download/windows-latest/NEVR-Anticheat-Setup.exe

The advanced portable package remains available here:

  https://github.com/MyGuyChromium/nevr-anticheat/releases/download/windows-latest/NEVR-Anticheat-Windows-x64.zip

Successful releases include SHA-256 checksums. GitHub build provenance is added
when the repository's visibility supports attestations. Publisher signing also
depends on the maintainer's signing configuration; checksums do not remove
Windows SmartScreen warnings.
