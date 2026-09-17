# Windows release trust and code signing

The normal installation artifact is `NEVR-Anticheat-Setup.exe`. It is an offline,
per-user Inno Setup package: it does not download or execute scripts during
installation, does not request administrator access, and keeps evidence under
`%LOCALAPPDATA%\NEVR-Anticheat` outside the replaceable program directory.

Every release publishes SHA-256 checksums. GitHub build-provenance attestations
are added when the repository visibility supports them; GitHub does not offer
that feature to user-owned private repositories. The Windows release workflow
also performs a real silent install/uninstall smoke test and scans the final
artifacts with Microsoft Defender when Defender is available on the hosted
runner.

## What the update manifest does and does not establish

`NEVR-Anticheat-Update.json` carries the release tag, commit, `commit_time`
(committer time of that commit, UTC), version, `desktop_subsystem` (measured
from the shipped `nevr-desktop.exe`; `windows` for a released build) and the
installer's name, size and SHA-256. The in-app updater uses it to check that
the download is the file the release describes and that the release is newer
than the installed build. That makes an update integrity-checked. It does not
identify the publisher: the manifest and the installer come from the same
release, so anyone who can replace one can replace the other. Only a publisher
signature, described below, adds identity. `--allow-update-downgrade` skips the
"newer" rule for one run and nothing else.

The publish job runs one at a time per release and moves the rolling
`windows-latest` tag only forwards (to the published commit or a descendant).
`scripts/release-gate.test.cjs` executes that guard against scratch
repositories and fails if a publish step escapes it.

## Publisher signing

Windows publisher identity and SmartScreen reputation require a trusted
Authenticode code-signing certificate. Checksums and GitHub attestations protect
integrity but cannot replace that certificate.

The workflow supports exactly one signing mode per run and rejects partial or
ambiguous configuration. Microsoft Artifact Signing with GitHub OIDC is the
preferred mode because no exportable private key is stored in GitHub. Configure
these repository secrets:

- `AZURE_CLIENT_ID`: Microsoft Entra application/client ID.
- `AZURE_TENANT_ID`: tenant ID.
- `AZURE_SUBSCRIPTION_ID`: subscription containing the signing account.

Configure these repository variables:

- `ARTIFACT_SIGNING_ENDPOINT`: regional endpoint, including `https://` and the trailing slash.
- `ARTIFACT_SIGNING_ACCOUNT`: Artifact Signing account name.
- `ARTIFACT_SIGNING_PROFILE`: validated certificate profile name.

The Entra identity needs **Artifact Signing Certificate Profile Signer** on the
profile and a federated credential restricted to this repository/workflow. The
workflow signs all five application executables before creating the portable
ZIP, then signs Setup, RFC 3161 timestamps everything, recomputes hashes, and
fails if Windows does not report every expected signature as valid. Inno's
generated uninstaller cannot call the GitHub Action during compilation and is
not publisher-signed in this mode; it is created locally by the signed Setup.

The alternative PFX mode signs all five executables, Setup, and Inno's embedded
uninstaller when both repository secrets are configured:

- `WINDOWS_SIGNING_CERTIFICATE`: the base64-encoded contents of a PFX file.
- `WINDOWS_SIGNING_PASSWORD`: the PFX password.

Create the first value locally without printing the certificate bytes:

```powershell
[Convert]::ToBase64String(
  [IO.File]::ReadAllBytes('C:\secure\nevr-code-signing.pfx')
) | Set-Clipboard
```

Add the copied value and password under **Repository settings → Secrets and
variables → Actions**. Never commit the PFX, password, or decoded temporary
certificate. Once configured, the workflow imports it only into the ephemeral
runner's current-user certificate store and removes the PFX in a guaranteed
cleanup step.

An unsigned development build remains installable but Windows may identify it
as an unknown publisher. A valid signature establishes publisher identity; it
does not guarantee that SmartScreen will immediately trust a new certificate or
new download. Reputation still accumulates from clean, consistently signed
downloads. No project setting can honestly guarantee SmartScreen reputation for
an unsigned binary.
