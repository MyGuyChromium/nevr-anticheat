# Native NEVR Stream capture import

Native `.tape` import preserves source fields that a converted `.echoreplay`
can lose. It is an offline, review-only input path, **not a new source of
authoritative game decisions or detector calibration**. Playspacing remains
paused. This integration does not promote detectors, change physics limits,
enable punishment, connect to a stream server, or import credentials.

## Use

1. Keep the complete original capture in a private evidence folder. Do not
   upload another player's recording to a public issue or PR.
2. Drop the `.tape` into the desktop app, or run `nevr-ac analyze capture.tape`.
   Folder/batch import, watched folders, and interrupted-upload recovery accept
   the same extension. `.nevrcap` is not an accepted filename extension.
3. Open **Native capture provenance and limitations** in the match report.
   Record the capture ID, producer (self-reported), format, decoder schema,
   and container-integrity result along with the app's build/configuration
   identity. A valid checksum does not authenticate the recorder or gameplay.
4. Review mechanics explanations and missing evidence, not just scored cases.
   Inconclusive checks are neither proof of cheating nor proof of fair play.
5. Reimport the same complete recording to refresh derived analysis. Preserve
   the original file even after database import or backup.

Use separate disposable evidence databases for different captures of the
same session. The current store keys matches by session, not capture ID.
Imports which would mix native and legacy evidence, change a capture's
records, or replace an existing native capture with a shorter clip are
rejected before replacing derived results. An identical recording can be
reanalyzed; a matching interrupted prefix can be completed. This is not
support for multiple independent applications writing one SQLite database
concurrently. The desktop serializes analysis, and batch uses one writer.

## Exact integration contract

The initial decoder targets sparse Echo Arena format version 2 with
`github.com/echotools/tape/v4 v4.1.0` and the generated NEVR API schema
`638a4669f605.2`. The upstream investigation was pinned to NEVR Stream
commit `50572352c43a2fadd0351566eeda49b91d28aa26`; source inspection is not
an end-to-end test against a deployed producer.

- Complete protobuf headers and frames, including unknown fields, are stored
  in the reserved `_nevr_tape` wrapper alongside a derived session view.
  Raw-backed reanalysis decodes protobuf again rather than treating the
  derived JSON as the original Echo HTTP feed. Retained protobuf records are
  deterministically reserialized, not a byte-identical copy of compressed
  source encoding; keep the original `.tape` for that purpose.
- Capture offsets, original frame indices, quaternions, slot/account identity,
  and zero-player event ticks are retained. Storage uses separate contiguous
  ordinals; these are not the original native frame indices.
- Roster and grab state are reconstructed from available sparse events. Slot
  reuse and missing baselines do not inherit possession. Index gaps and time
  gaps over 500 ms reset carried attachment/throw context. This continuity
  ceiling is a conservative analysis guard, not an asserted game tick rate.
- Local throw parameters require an identified recording client, an observed
  held-to-free transition, continuous capture and fresh values. Remote throws,
  first-frame events, and stale values are not promoted to local measurements.
- Source authority remains `client_reported`. Capture time is an offset in
  milliseconds, not an exact physics-tick timestamp. Non-optional protobuf
  scalar zeros cannot establish that a measurement was observed.
- Original container integrity is recorded only after successful full-file
  validation. Reanalysis of retained records does not independently recheck
  the original compressed container. Generated clips never inherit original
  container verification merely by containing those records.
- Spark clips are **derived viewing copies** with retained native wrappers.
  Nanosecond-precision timestamp prefixes preserve evidence alignment. The
  clip-generation tests mock launching Spark; real viewer playback remains a
  separate acceptance test.

Missing/unknown game type, dense encoding, unsupported capture versions,
external compression dictionaries, oversized compression windows, malformed
protobuf, mixed-source clips, timestamp disagreement, truncation, and failed
integrity checks produce explicit import failures. Keep the source file; do
not repair it by deleting inconvenient records or rename arbitrary files to
force them through a different parser.

Limits are 8 MiB per retained record/compression window, 8 GiB decoded or
retained records, 1 million native ticks, 1 million total normalized player
frames, and 64 simultaneous player slots. The desktop additionally caps each
upload at 4 GiB. Reaching a limit rejects the import rather than silently
truncating it. These bounds are not a promise that every allowed capture fits
in a low-memory machine; test representative workloads before distribution.

## Replay compatibility and review coverage

Some exporters write integral score/stat measurements as `5.0`. The parser
accepts exact integral decimal/exponent spelling without floating-point
rounding; fractions and overflow are rejected. Player and account IDs keep
their existing strict integer wire format. Custom team names map to blue and
orange only when the exact three-team/spectator layout provides an unambiguous
mapping; a name merely containing "blue" or "orange" is not team authority.

An observed direct transfer from another holder without a sampled free disc
now produces an **inconclusive** grab review, not a range verdict. Legal steals
and handoffs are possible. The explanation distinguishes previous-held
distances from actual last-free distances and does not interpolate an unseen
contact. This closes review visibility, not the independent geometry or
false-positive validation gate.

## Repeatable verification

From the repository root with its supported Go toolchain and C compiler:

```powershell
go build ./...
go vet ./...
go test ./... -coverprofile=coverage.out
go test -race ./...
go test -race ./scripts/testdata
node --test scripts/desktop-review.test.cjs scripts/autopocket-review.test.cjs scripts/operator-ui.test.cjs scripts/release-gate.test.cjs
go test ./internal/adapter -run '^$' -fuzz '^FuzzNativeTapeRaw$' -fuzztime 20s
go test ./internal/adapter -run '^$' -fuzz '^FuzzNativeTapeContainer$' -fuzztime 20s
```

The existing CI/security workflows also run pinned staticcheck, actionlint,
govulncheck, gosec, legacy parser fuzzing, and the coverage floor. Do not weaken
the floor, assertions, or native-source failures to pass a candidate.

Native fixtures are generated with the real pinned codec in temporary test
directories; no original player's recordings are committed. Tests cover
import, preserved records, remap, duplicate refresh, corruption, source/range
collisions, persistence failure, database reopening, clips and export. These
are synthetic workflow/contract tests, **not independent detector accuracy
tests or live producer integration tests**. User-reported clips used during
development are investigative examples, not a sealed evaluation set.

For the actual packaged candidate use the existing
[controlled-release verification entry point](test-release-verification.md)
and [tester handoff](private_tester_handoff.md). Record each unexecuted gate as
NOT TESTED or BLOCKED. In particular, test real producer captures, actual
Spark playback, installation in a disposable Windows account, backup/restore,
and sustained representative replay workloads. Replacing an executable does
not undo a database migration. Older app builds do not understand the retained
native-source contract: for rollback, restore a verified pre-upgrade database
backup with the matching program snapshot in isolated storage; do not use an
older mapper to reprocess a database containing new native captures.

Bug reports should include app/build/config/schema identity, capture format
and decoder revision, reproduction steps, expected/actual behavior and a
redacted diagnostic bundle. Share originals only through an explicitly
authorized private channel. This document is not production enforcement
approval.
