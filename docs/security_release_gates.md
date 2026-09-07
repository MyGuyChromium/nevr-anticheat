# Security release gates

Every push and pull request runs CI: build, vet, tests, race checks, at least 70% total statement coverage, Staticcheck, reachable Go vulnerabilities, and `gosec`. A failing step stops later steps; a red run does not establish that the remaining checks executed. The toolchain is pinned by `go.mod`; statement counts may differ between toolchains, and the actual coverage profile is retained as a build artifact. The security workflow additionally fuzzes replay/manifest boundaries and emits a CycloneDX SBOM, including on its scheduled run. Dependabot watches both Go modules and workflow actions.

`gosec` fails on medium-or-higher findings. Four broad syntactic rules are excluded: dynamic SQL construction (`G201`/`G202`) is limited to internal table/column constants and parameter-placeholder counts; command execution (`G204`) is the desktop's explicit browser/Spark launcher; and user-selected paths (`G304`) are fundamental to a replay file processor. High-confidence taint/path findings remain enabled. Any line suppression must name its rule and carry a justification, which CI enforces.

CodeQL and GitHub dependency review are configured to run only for a public repository. They are unavailable/skipped under the current private-repository policy, not successful security checks. `govulncheck` and `gosec` remain mandatory either way. This change does not alter repository visibility, entitlements, or access permissions.

## Enforced publication dependencies

The Windows workflow calls the existing CI and security definitions as
unconditional `quality_ci` and `quality_security` jobs with `contents: read`
and no inherited signing secrets. Local reusable workflow references resolve
to [the same commit as the caller](https://docs.github.com/en/actions/how-tos/reuse-automations/reuse-workflows).
This does not query the latest green badge or reuse a different revision's
successful run.

The release-writing `publish` job needs **both quality jobs and the Windows
package job to succeed**, and accepts only a push or manual dispatch on
`master` or a `v*` tag. Failure, cancellation, skipped or missing quality results
cannot authorize publication. It retains GitHub's normal
[failed/skipped dependency handling](https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax#jobsjob_idneeds);
there is no `always()` or `continue-on-error` escape. Pull requests exercise
the full dependency graph but never publish. Separate CI/security triggers
remain, so some checks run more than once; this trades runner time for a direct,
same-run release dependency without polling or privileged `workflow_run` code.

Build/package jobs run in parallel with quality checks. Private CI artifacts may
therefore exist even when publication is blocked: a downloadable artifact is
not release approval. Existing package signing behavior is unchanged. Review
the SBOM, Defender availability/result, installer preservation tests, hashes,
and signing verification. Never waive a finding because a detector is shadow-only.

This gate takes effect only after its PR is merged. It cannot undo an earlier
merge, revoke a previously published binary, or prevent the owner from manually
merging red PRs. Branch-protection settings are unchanged; continue to wait for
all required PR checks before merging.

## Preventing the test-helper scan gap

PR #29's three red runs reported the same `G703` finding in
`scripts/testdata/soak-helper.go`. The prior local scan targeted only `cmd`,
`internal`, and `tests`, whereas CI's `gosec ./...` also discovers Go sources
under `scripts/testdata`. The helper now confines config reads and marker
writes through `os.OpenRoot`; no finding is globally excluded or suppressed.

CI explicitly runs `go test -race ./scripts/testdata`, because normal Go `./...`
package discovery skips `testdata`. These tests reject parent/absolute escapes
and symlink escapes. Windows may skip symlink creation without the required
privileges; the Linux run is needed for those cases. The PowerShell soak harness
also verifies rejected config escapes alongside its success/failure/resource
cases, without touching installed data.

For maintainer verification, scan a **clean committed source checkout** with
the exact pinned CI command and full `./...` scope. Do not omit helper folders
to avoid scanning generated files in a dirty workspace. `scripts/release-gate.test.cjs`
checks the actual job dependencies, expression outcomes and shared-workflow
references; actionlint validates YAML/Actions syntax. These static tests do not
replace the hosted workflow's real outcomes or prove a release was published.
