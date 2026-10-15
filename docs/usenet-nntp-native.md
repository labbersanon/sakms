# Native NNTP discovery (feature-flagged)

Built-in Usenet search via an incremental `OVER` crawler over operator-selected
newsgroups, a local header index in **dedicated Postgres tables**
(`usenet_nntp_headers` / `usenet_nntp_watermarks`), and in-memory NZB synthesis
for the existing download engine.

Default **off**. NZB + Prowlarr remain the fallback on miss.

## Why Postgres (not a sidecar SQLite file)

`cmd/sakms` must not import `modernc.org/sqlite` after the Postgres cutover
(`TestCmdSakms_DoesNotImportModerncSQLite`). The index still has its own tables
and prune passes (window days + max GiB).

## Settings (Download → Usenet → Native NNTP search)

| Setting | Notes |
|---|---|
| Master + per-mode toggles | Movies / Series / Adult AND-ed with master |
| Per-mode newsgroups | Searchable dual-list + Apply recommended (curated seeds filtered to provider `LIST ACTIVE`); first-run pre-fills recommended as unsaved draft |
| Max GiB / soft age days / idle backoff | Soft size budget; soft age display; idle backoff after catch-up (0 = 60s default). Crawl is continuous when enabled |
| Probe state/detail | Read-only |

## Honest limits

- NNTP has no `SEARCH`. Discovery is local index search only.
- Obfuscated subjects are not recoverable from headers alone.
- Crawl shares the subscription connection pool and yields under download load.
- Eweka account ceiling may be 50; live `max_conns` is independent.
- Full `LIST ACTIVE` without wildmats is impractical on large providers — the picker uses mode-scoped wildmats.

## Continuous index

Claude 2026-10-15: **rate-limited continuous crawl** + **liveness monitor**
(STAT → delete only when the article is gone). No nightly/monthly schedules.
Soft `window_days` / `index_max_gb` never delete still-available articles for
age alone. Soft size budget uses row-count × ~200 B/row (`ApproxBytes`), not
`pg_total_relation_size` (which does not shrink after `DELETE` and previously
could wipe the index).

Group picking: curated **5-step packages** (`seeds/packages.json`) from
most reliable → largest selection (cumulative). Settings slider lands in a
follow-up; GET groups already exposes `steps` / `packages`.

## Spike numbers

See `docs/usenet-nntp-native-spike.md`.
