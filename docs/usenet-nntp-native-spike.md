# Native NNTP discovery spike (2026-09-17)

Measurement-only. Product surface still off. See `.omc/plans/usenet-nntp-native-backend.md`.

## Owner locks reflected here

- Groups are **manual** (`usenet_nntp_groups`); search indexes only those.
- Index storage uses dedicated **Postgres** tables (not a sidecar SQLite file —
  `cmd/sakms` cannot import `modernc.org/sqlite`). Soft `usenet_nntp_index_max_gb`
  (default 20) still applies.
- Eweka **account** connection limit is **50**. Live `service_connections.max_conns` for `news.eweka.nl` remains `0` (engine default 4) until a separate soaked raise.

## Tool

```text
NNTP_USER=… NNTP_PASS=… go run ./cmd/nntpprobe \
  -host news.eweka.nl -port 563 -tls \
  -group alt.binaries.movies \
  -range 100000 -conns 2 -sample 50000
```

Default `-conns 2` (live-budget world). Use `-conns 12` for the MaxConns=50 crawl-share world. Does not read or write `sakms.db`.

## Results — `news.eweka.nl:563`, group `alt.binaries.movies`

| Measurement | Result |
|---|---|
| CAPABILITIES | `XOVER`/`XHDR` present; **no `SEARCH`** |
| GROUP span | ≈ 12.1 billion articles |
| OVER 100k @ 2 conns | 7.2 s ≈ 13.9k arts/sec; 0 broken-pipe |
| OVER 100k @ 12 conns | 2.5 s ≈ 39.6k arts/sec; 0 broken-pipe |
| Index row size | ≈ 186 B/row → 20 GiB ≈ 115 M rows |
| Subject sample (n=50k) | usable 5.7% / obfuscated 23.6% / other 70.7% |

## Gate read

Crawl speed is not the blocker. Binding constraints are obfuscation/unparseable subjects and articles/day × groups × window vs the 20 GiB budget. Confirm articles/day with a 24 h watermark delta before locking the window. Do not raise live MaxConns on OVER evidence alone.
