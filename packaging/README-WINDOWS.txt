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

UPDATES

Use Windows updates > Check for updates > Install update in the app. Installed
copies download the update package, check it against the size, SHA-256 and
release tag in the release manifest, run it, preserve local evidence and
restart. The package is integrity-checked only: that detects a damaged or
mismatched download, it does not prove who published it. Portable copies use
the manual-download fallback.

NEVR installs a release only when it is newer than the build already installed.
Every rolling release states when its source was committed; a release that is
older, the same, or does not state it is refused with an explanation. To go
back to an older release on purpose, either run that release's
NEVR-Anticheat-Setup.exe by hand, or start NEVR once with

  nevr-desktop.exe --allow-update-downgrade

The flag applies to that one run only, cannot be switched on from inside the
app, and skips only the "must be newer" rule; every integrity check still runs.

One-click update also refuses, before downloading anything, while
nevr-server.exe, nevr-bridge.exe, nevr-ac.exe, nevr-compat.exe or a second
desktop copy runs from the installation folder (quit them first; nothing is
closed for you), and on an installation whose database was moved out of
LocalAppData\NEVR-Anticheat (download and run Setup instead).

When the release feed is publicly readable, updates need no sign-in. If the
repository is private, downloads require repository access and in-app updates
additionally need a read-only token in NEVR_GITHUB_TOKEN; signing into a
browser does not authenticate the app. The installer itself runs offline if
shared directly. Never share a maintainer token with recipients.

ROLLBACK

Installing a newer package snapshots the previous program files without
copying or changing the evidence database. If an update has a problem, run
Rollback-NEVR.cmd from LocalAppData\Programs\NEVR-Anticheat to restore the
newest program snapshot that passes checksum verification. A snapshot that
fails verification (for example one that was interrupted half-way) is skipped
and reported, never restored. Database backup/restore is a separate, verified
action inside the desktop app's Investigation & reliability studio.

Snapshots live in LocalAppData\NEVR-Anticheat\program-rollbacks. Folder names
start with the UTC date and time the snapshot was taken; Setup writes names
such as 20260917-184502Z-1, where the Z means UTC. "Newest" therefore means
the same thing whichever tool wrote the folder. Older folders named in local
time are still understood. Only the newest 3 verified snapshots are kept
(about 40 MB each); older verified ones are removed automatically. A folder
that does not verify is never restored, never counted and never deleted
automatically: remove it by hand if you need the space.

UNINSTALL

Installed with Setup: use Windows Settings > Apps > Installed apps.
Installed from the ZIP with Install-NEVR.cmd: quit NEVR, then run
Uninstall-NEVR.cmd from LocalAppData\Programs\NEVR-Anticheat. The uninstaller
copies itself to a temporary folder and continues from there in a second
window, because Windows cannot delete a folder that a running script sits in
(earlier copies failed with "in use" for that reason and left the program
behind). The shortcuts are removed only after the program folder is gone.

Both ways keep your evidence (database, recordings, settings) in
LocalAppData\NEVR-Anticheat. To delete that as well with the ZIP uninstaller,
open a Command Prompt in the program folder and run

  Uninstall-NEVR.cmd -RemoveEvidence

This cannot be undone. Make a database backup first if in doubt.

WINDOW, LOGS AND QUITTING

nevr-desktop.exe is a windowed program: starting it opens only the NEVR app
window, never a console window. The command-line tools (nevr-ac.exe,
nevr-server.exe, nevr-bridge.exe, nevr-compat.exe) remain console programs.

Closing the app window quits NEVR about 8 seconds later; reloading the page
within that time cancels the exit. If the window goes away without saying so
(a browser crash, for example), NEVR quits after 90 seconds of silence. A
minimized window still reports in often enough to keep NEVR running. NEVR
never quits while an analysis, recovery, watch-folder import, backup, restore,
update or any other request is still running; the countdown restarts when that
work ends. Auto-exit only begins once an app window has connected, so a copy
started with --no-browser and never opened keeps running. Start
nevr-desktop.exe with --no-auto-exit to keep it running until the in-app Quit
button is used.

Without a console, everything NEVR would have printed is written to

  <data folder>\logs\nevr-desktop.log      (rotated at 5 MB, 3 older files kept)
  <data folder>\logs\nevr-desktop-crash.log (only written if NEVR crashes)

The data folder is the one that holds the database: LocalAppData\NEVR-Anticheat
for an installed copy, the folder beside nevr-desktop.exe for a portable one
(Health & maintenance > Open data folder). The log never contains the app's
per-run access address. If NEVR cannot start, it shows the reason in a dialog
and in that log. From a terminal, nevr-desktop.exe --console prints there
instead of to the log file.

A database that a newer NEVR has already upgraded is refused by an older copy
with "needs an update" rather than opened: install the latest version. The
database is not modified by the refusal.

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
