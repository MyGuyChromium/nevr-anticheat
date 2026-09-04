# Windows release trust and code signing

The normal installation artifact is `NEVR-Anticheat-Setup.exe`. It is an offline,
per-user Inno Setup package: it does not download or execute scripts during
installation, does not request administrator access, and keeps evidence under
`%LOCALAPPDATA%\NEVR-Anticheat` outside the replaceable program directory.

Every release publishes SHA-256 checksums and GitHub build-provenance
attestations. The Windows release workflow also performs a real silent
install/uninstall smoke test and scans the final artifacts with Microsoft
Defender when Defender is available on the hosted runner.

## Authenticode signing

Windows publisher identity and SmartScreen reputation require a trusted
Authenticode code-signing certificate. Checksums and GitHub attestations protect
integrity but cannot replace that certificate.

The workflow automatically signs all five executables, Setup, and its embedded
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
as an unknown publisher. No project setting can honestly guarantee SmartScreen
reputation for an unsigned binary.
