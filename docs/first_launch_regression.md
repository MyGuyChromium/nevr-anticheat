# First-launch regression (0.11.1)

## Cause

A fresh Windows installation wrote `installed.toml` pointing at
`%LOCALAPPDATA%\NEVR-Anticheat\nevr-anticheat.db`, but did not create the
`NEVR-Anticheat` data directory. SQLite cannot create a missing parent directory.
Desktop startup therefore exited during schema initialization, before opening
the app window, with an `unable to open database file` error.

The installer smoke test checked that files existed but never launched the
installed desktop. It then created the evidence directory for preservation
checks, hiding the missing first-install step. The private-candidate smoke also
placed its database directly in a directory prepared by the harness.

## Fix and regression coverage

- Setup creates the default per-user data directory and preserves it on uninstall.
- Desktop startup creates missing parent directories for normal filesystem
  database paths before opening SQLite, including older installed configurations.
  Existing directories, databases, and configuration files are retained. SQLite
  special connection strings retain their existing behavior.
- Runtime tests cover nested missing paths, quoted/spaced names, existing data,
  and clear failure when a parent path is a file. An isolated process exercises
  startup, the local page/health endpoint, and shutdown.
- The Windows workflow requires the installed data directory to exist, launches
  the installed executable using the actual installed configuration, checks its
  page and migrated database, and requests a clean shutdown before upgrade and
  uninstall preservation tests. It does not manually create the missing folder.
- Private-candidate readiness now starts with an absent nested data directory;
  settings fixtures are written only after first-launch creation succeeds.

The existing CI/security publication dependencies remain mandatory. Local tests
and hosted results are recorded on the PR; passing them is not a claim about
detector accuracy, publisher signing, or SmartScreen reputation. Installer tests
run on the disposable Windows runner, not against the maintainer's installation.

## Existing affected installations

The same issue can be repaired without reinstalling by creating the directory
named as the parent of `general.db_path` in the installed configuration, then
launching the NEVR shortcut again. Do not replace a database or reset settings.
For a custom path, verify the configured target and permissions first. The fixed
desktop performs the normal-path directory creation automatically on startup.
