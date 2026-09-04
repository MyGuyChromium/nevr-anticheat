# Security release gates

Every push and pull request builds, vets, tests, race-tests, enforces at least 70% total statement coverage, runs Staticcheck, checks reachable Go vulnerabilities, and runs `gosec`. The pinned Go 1.26.6 toolchain reports 70.8% for the current suite (Go 1.27 reports 73.8% because its instrumentation counts statements differently). The coverage profile is retained as a build artifact. A separate scheduled workflow fuzzes all replay input boundaries and emits a CycloneDX SBOM. Dependabot watches both Go modules and workflow actions.

`gosec` fails on medium-or-higher findings. Four broad syntactic rules are excluded: dynamic SQL construction (`G201`/`G202`) is limited to internal table/column constants and parameter-placeholder counts; command execution (`G204`) is the desktop's explicit browser/Spark launcher; and user-selected paths (`G304`) are fundamental to a replay file processor. High-confidence taint/path findings remain enabled. Any line suppression must name its rule and carry a justification, which CI enforces.

CodeQL and GitHub dependency review are wired to activate for a public repository. GitHub does not allow their result upload for a personal private repository without the applicable GitHub Code Security entitlement, so those jobs fail closed by being skipped while the repository is private; `govulncheck` and `gosec` remain mandatory either way.

Before publishing a release, require green **CI**, **Security and parser fuzzing**, and **Windows package** checks. Review the SBOM artifact, Microsoft Defender result, installer upgrade/uninstall test, hashes, and signing verification. Never waive a vulnerability finding solely because a detector is in shadow mode: the parser and desktop still process untrusted files.
