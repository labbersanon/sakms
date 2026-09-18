# Native NNTP discovery (feature-flagged)

Built-in Usenet search via an incremental `OVER` crawler over **manually listed**
newsgroups, a local SQLite header index under a **settings-specified absolute
directory**, and in-memory NZB synthesis for the existing download engine.

Default **off**. NZB + Prowlarr remain the fallback on miss.

## Settings (Download → Usenet → Native NNTP search)

| Setting | Notes |
|---|---|
| Master + per-mode toggles | Movies / Series / Adult AND-ed with master |
| Newsgroups | One per line; no auto-discovery |
| Index directory | Absolute path required when enabled (`nntp-index.sqlite` inside) |
| Max GiB / window days / crawl interval | Soft size budget, retention, 0 = crawler off |
| Probe state/detail | Read-only |

## Honest limits

- NNTP has no `SEARCH`. Discovery is local index search only.
- Obfuscated subjects are not recoverable from headers alone.
- Crawl shares the subscription connection pool and yields under download load.
- Eweka account ceiling may be 50; live `max_conns` is independent — do not raise it without a soaked step-up.

## Spike numbers

See `docs/usenet-nntp-native-spike.md`.
