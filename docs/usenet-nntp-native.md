# Native NNTP discovery (feature-flagged)

Built-in Usenet search via an incremental `OVER` crawler over **manually listed**
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
| Newsgroups | One per line; no auto-discovery |
| Max GiB / window days / crawl interval | Soft size budget, retention, 0 = crawler off |
| Probe state/detail | Read-only |

## Honest limits

- NNTP has no `SEARCH`. Discovery is local index search only.
- Obfuscated subjects are not recoverable from headers alone.
- Crawl shares the subscription connection pool and yields under download load.
- Eweka account ceiling may be 50; live `max_conns` is independent.

## Spike numbers

See `docs/usenet-nntp-native-spike.md`.
