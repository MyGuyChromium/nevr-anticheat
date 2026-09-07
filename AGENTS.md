# Project delivery workflow

- For every completed code update, commit the scoped changes on a `codex/` branch, push the branch, and create or update a pull request targeting the repository's default branch.
- Return the verified pull request link and its current check status to the user. Local files or a local build alone are not a completed GitHub handoff.
- Leave merging to the user. Do not merge a pull request or enable automatic merging unless the user explicitly asks for that action.
- Keep the repository private. Do not change visibility or publish a release as part of routine PR creation.
- Exclude original replay recordings, installed databases, credentials, generated binaries, and private calibration reports from commits. Keep private test artifacts in ignored output directories.
- Report verification limits honestly: synthetic and regression passes are not independently validated detector accuracy, and an unsigned installer is not a trusted-publisher release.
