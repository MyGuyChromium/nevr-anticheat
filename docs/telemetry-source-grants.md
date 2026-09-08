# Telemetry source grants and migration

Remote ingestion requires operator-configured source grants. A legacy global
`NEVR_AC_AUTH_TOKEN`, or `allow_unauthenticated`, no longer authorizes a listener
bound to a wildcard or non-loopback address. Missing or invalid grants fail
before the server opens its database or listeners. No credentials are migrated
or deployed automatically.

Each principal owns exact `server_ids`, and its secret is loaded from the named
environment variable. Never put the secret in TOML or commit it. Server IDs must
be unique between principals. Match and player identifiers are exact strings;
`*` patterns are not supported.

```toml
[server]
listen = "127.0.0.1:8080" # example: behind an operator-managed TLS proxy

[[server.source_grants]]
principal = "bridge-east"
token_env = "NEVR_INGEST_EAST_TOKEN"
server_ids = ["203.0.113.10:6721"] # replace with the actual authorized source ID
match_ids = ["replace-with-an-exact-match-id"]
player_ids = ["echovr:replace-with-an-exact-player-id"]
```

The example address is documentation-only. Set the environment variable using
your deployment's secret mechanism, then give that principal's ingest secret to
its bridge using the existing anticheat-token setting. The WebSocket payload is
unchanged. A global legacy token is not accepted as a fallback when grants exist.
Use a different credential from Nakama discovery or moderator/enforcement
services; ingestion does not expose ban or kick credentials or actions.

For a bridge that discovers dynamic matches or rosters, the operator may replace
`match_ids` with `allow_any_match = true` and/or replace `player_ids` with
`allow_any_player = true`. These are explicit broad authorizations, not default
behavior. Do not combine an allow-any flag with a nonempty list. Even broad
grants remain restricted to their configured server IDs and existing match
source associations.

When starting from `configs/default.toml`, replace its `source_grants = []` line
with the array-of-tables policy above; do not define the array twice. Changing
only `--listen` does not create a grant. All command-line overrides are validated
again before opening the database.

Every batch and match-start/end control is checked against the authenticated
principal's source, match, and player scope. Unauthorized batches are rejected
before match/player bookkeeping and storage. The first accepted source binds the
match; another source or an omitted source ID cannot change or end it. The
association uses the bounded live-match registry and persists in match context,
so ending a match or restarting the server does not permit another source to
take over its stored identity. Changing the source for an existing match requires
an explicit future migration workflow; do not rewrite existing evidence to make
it fit. Old stored matches without a source are not silently adopted by a new
named source.

Literal `127.0.0.1`/IPv6 loopback listeners may retain legacy shared-token or
explicit unauthenticated development behavior, with a startup warning.
`localhost` is deliberately not treated as a proven literal loopback address.
Never expose this development path through an external proxy or port forward.

Grants authenticate an authorized submitter and constrain what it may assert.
They do **not** independently verify player identity, broadcaster responses,
engine builds, capture timing, or game mechanics. Broadcaster `/session` polling
is still plaintext HTTP; configure an explicit broadcaster allowlist and use a
trusted network. Probe, once, and continuous polling reject redirects, even to a
different path/port on an allowed address. Explicitly configured private or
loopback broadcaster addresses remain supported.

Nakama discovery and authentication also reject redirects, so session tokens or
refresh bodies are not forwarded to a redirected destination. The bridge bounds
the complete WebSocket upgrade, not only the TCP connect. Connection admission,
control-roster growth and ingest bookkeeping keys are bounded. At 10,000 active
rate keys, new identities are denied until stale keys expire; existing identities
retain their budgets. `/health` exposes `rate_key_capacity_rejected` and
`warning_keys_coalesced` counters without identifiers or secrets, and reports
`analysis_incomplete_matches` with `status = "degraded"` when live derived
analysis could not be persisted completely.

This change does not make synthetic detector tests accuracy validation, enable
automatic enforcement, or alter installed application data or private recordings.
