# Operator interface polish

This change preserves the embedded Go/HTML desktop app and NEVR's navy/blue
visual direction. The header now uses an original inline-vector interpretation
of the Echo Arena disc, not the previous abstract slash mark.

## Delivered

- Shared dark/light/system color tokens, consistent controls and focus states,
  compact spacing, larger text, reduced motion, and a keyboard skip link.
- An overview separating local engine connectivity, the age of its health
  response, replay activity, loaded scored cases, and review-only enforcement.
  Local engine health is explicitly **not** live telemetry freshness.
- Section navigation that opens advanced disclosures when needed. Setup is
  always accessible; storage readiness and Spark discovery are separate checks.
- Explicit queue search/review-state filters, bounded history pagination,
  browser-local filter/selection preferences, and an explicit refresh policy
  that does not silently replace a reviewer's current list.
- A replay review workspace with incident selection, recorded evidence,
  explanation/limitations, Spark clip actions, scrollable throws and clip
  anchors, notes, and consistent previous/next controls.
- Nullable reported player/disc speeds in the investigation presentation API.
  Missing values leave chart gaps; measured zero remains zero. Connecting
  lines are described as visual interpolation, not recorded motion. The marker
  represents the selected incident, not Spark's playback position.
- Browser-local draft recovery and explicit database-confirmed note saving.
  Duplicate clicks, delayed confirmations, failed saves, and navigation while
  saving cannot silently clear the next draft or report an unconfirmed save.
- Bounded request times, permission/network/conflict messages, actionable
  empty states, and session-local notification history. Upload transfer
  progress is measured; analysis progress is indeterminate. A cancellation
  request is not reported as completed cancellation. A 30-minute upload wait
  timeout leaves backend completion explicitly unknown and stops later files.
- Scoped keyboard navigation: arrows in focused incident/clip queues; J/K
  opt-in. Typing notes and native Enter/Space actions are not intercepted.

## Verification

Automated checks passed locally with Go 1.26.6:

```text
go test -count=1 ./...
go vet ./...
go test -race -count=1 ./cmd/desktop
go run honnef.co/go/tools/cmd/staticcheck@v0.8.1 ./...
go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12 -color=false
go run github.com/securego/gosec/v2/cmd/gosec@v2.28.0 -severity medium -exclude G201,G202,G204,G304 -nosec-require-justification -nosec-require-rules ./...
node --test scripts/desktop-review.test.cjs scripts/autopocket-review.test.cjs scripts/operator-ui.test.cjs scripts/release-gate.test.cjs
```

The Node suite includes 57 operator workflow tests (93 total across those four
files). The two obsolete title/cancellation assertions were updated; detector
behavior assertions were not relaxed.

Rendered inspection used the actual embedded application, not a mock frontend:

- A temporary synthetic evidence store with 30 history entries, eight players
  including a long name containing markup characters, 160 observations, 100
  throws, 260 clip anchors, notes, and a timeline with absent measurements.
- Desktop layouts at 1280 pixels and narrow layouts at 390 pixels; light/dark,
  larger text, compact spacing, reduced motion, setup, long incident lists,
  search with no results, history paging, and keyboard incident navigation.
- Successful note saving, an injected 503 save failure, retained/reopened
  drafts, denied setup access, and switching to a draft while a delayed setup
  request finishes.
- Confirmed shutdown with a neutral header and disabled actions, without
  browser-console JavaScript errors. Late health responses cannot restore the
  connected indicator after shutdown is accepted.
- A separate isolated production desktop binary importing the checked-in
  synthetic replay through the real file chooser and analysis endpoint,
  accepting that stored replay again, and rejecting deliberately malformed
  JSON with a finished error state. Raw file diagnostics are collapsible.
- Before/after screenshots saved under ignored `dist/ui-polish/screenshots/`.
  These artifacts and temporary databases/binaries are not part of the PR.

## Reproduce the rendered fixture

`TestOperatorUIFixture` is opt-in test code, never included in the distributed
desktop binary. It creates a temporary SQLite store and marks the page as
synthetic. External launches, installation, file watching and uploads are
blocked in this fixture. Use a separate isolated desktop binary for upload QA.

```powershell
$env:NEVR_UI_FIXTURE = '1'
go test ./cmd/desktop -run '^TestOperatorUIFixture$' -count=1 -v -timeout=30m
```

The test prints its loopback address. `NEVR_UI_FIXTURE_EMPTY=1` selects an empty
store. The header-authenticated loopback fault control and shutdown endpoint
are documented in `cmd/desktop/operator_ui_fixture_test.go`.
Unset these variables before running normal Go tests.

## Boundaries and unverified states

- No detector mathematics, scoring thresholds, promotion decisions, or
  enforcement policy changed. Synthetic UI checks do not establish accuracy.
- This desktop remains an offline replay app. It does not implement a live
  connection picker, live freshness, or Live/Test session switching. Existing
  detector configuration profiles are not connection profiles.
- Spark was not installed/discoverable in this environment. Discovery/error
  presentation is covered, but actual external playback and synchronization
  were not verified. There is no embedded player or claimed playback sync.
- Local drafts/preferences are scoped to this browser origin; changing ports,
  profiles or PCs can make them unavailable. Click Save note for durable
  evidence storage. There is no automatic server autosave, reviewer account
  authorization, or cross-user edit lock.
- History searches only the latest 200 returned matches. Notification history
  is bounded and session-local, not an OS notification service.
- All legacy advanced workflows, OS display scaling combinations, actual
  multi-hour uploads, installation/update dialogs, and screen-reader behavior
  were not exhaustively exercised. Full Windows release/installer checks remain
  the responsibility of CI. Signing/SmartScreen reputation is unchanged.
