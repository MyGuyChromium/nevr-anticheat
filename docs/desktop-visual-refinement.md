# Desktop visual refinement — 2026-09-18

This is a presentation-only follow-up to `operator-ui-polish.md`, based on
master after PR #60. It does not require the separate detector-evidence PR #61.
No detector mathematics, scoring, configuration, enforcement, storage schema,
or installer behavior is changed.

## Changes

- Rebalanced overview/import headings and spacing; the resume-review action
  now has its own readable container, including long match identifiers.
- Consistent, keyboard-operable disclosure headers, with one chevron whether
  the markup uses the old indicator span or a plain summary.
- Clear selected-incident cards and visually separated finding explanations.
- Removed the second sticky toolbar that could slide beneath the modal title.
  The title remains pinned; a Save review note action is now next to the editor
  and shares the existing duplicate-write/pending-state protections.
- Independently scrolling review throw logs, with atomic clock/speed values.
- Consistent appearance/settings cards, labels, explanatory text and spacing.

Existing navy/blue branding, light/system themes, larger text, reduced motion,
navigation, evidence limitations and all established actions are retained.

## Repeatable rendered check

```powershell
node scripts/ui-render-check.cjs --shots dist/visual-polish-after --label after
```

Requires Go, a C compiler, Node 22+, and installed Edge/Chrome. The script starts
the existing opt-in synthetic Go fixture on loopback, uses the actual embedded
page and handlers, then stops its own fixture. Original recordings and installed
databases are not used. Screenshots and logs belong in ignored `dist/`.

Seven passes cover 1280/1440px dark and light, 1024/640px dark, and 390px light.
Checks include table geometry, pinned columns, long player names, larger-text
timeline layout, setup, keyboard focus, Escape/focus return, empty-search
recovery, 100-throw scrolling, modal toolbar overlap, and physics inspection.
The fixture-owned path also tests setup failure/retry and an exactly-once note
save through the new editor action while all save controls are disabled.

Native dialogs may send reverse-Tab to browser chrome (a BODY/HTML fallback
during the focus transition). The test permits that browser behavior, requires Tab
to return to the modal, and verifies background page controls cannot take focus.
It does not replace native focus behavior with an application-specific trap.

Fault injection and note writes are only performed against the fixture started
by this script, never against an arbitrary `--url` server. Existing screenshot
baselines are not replaced; assertions check geometry and behavior, not pixels.

## Verification boundaries and follow-up

Executed locally: build and vet passed; the standalone desktop Go suite passed;
140 Node tests passed; all seven rendered passes passed, including delayed
exactly-once note saving, clock wrapping and setup failure/retry. Dark/light
dashboard, settings, review, saved-note, narrow-layout and error screenshots
were inspected. A spot-check of 16 shared text/background token pairs had
contrast ratios of at least 5.17:1; this is not an exhaustive accessibility audit.

This pass verifies browser-rendered desktop content with synthetic evidence.
It does not establish detector accuracy or independent calibration, and does
not certify the packaged native window, installation/update flow, real Spark
playback, screen readers, or all OS display-scaling combinations.

One concurrent full Go run exposed an existing recovery-test timing race:
`TestDesktopRegressionComparisonSandboxHistoryRuntimeAndSupport` expected one
pending file to be discarded but got zero. The runtime's delayed startup pass
can remove the deliberately invalid file first. The standalone desktop suite
and 20 isolated repetitions passed. Backend/test files were not changed here;
a follow-up should synchronize startup before staging the discard fixture,
preserving the exact `removed == 1` assertion rather than accepting zero.
After the rendered fixture was stopped, `go test ./... -count=1` passed in full.
That successful rerun does not erase the earlier intermittent-test finding.
