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
| Per-mode package slider | Ceiling (default max/all relevant): crawl/search only groups with grade ≤ step; advanced customize → Custom |
| Passive grades | Start at 1 (most reliable); +1 per Usenet article failure (sticky at 5); grade > ceiling → stop crawl + delete that group's index |
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

Group picking: curated **5-step packages** (`seeds/packages.json`). The
Settings slider is a **ceiling** (default 5 = all relevant). Every group
starts grade 1; Usenet article failures demote one tier (5 sticky). When
grade exceeds the ceiling, crawl stops for that group and its index rows
are deleted. Advanced customize → Custom.


## PreDB provider (name recovery)

Claude 2026-10-15: obfuscated sets are renamed from a local PreDB table
(`usenet_nntp_predb`) fed by an on-demand HTTP lookup (default on) and an
optional IRC announce feed (default off).

**Default HTTP provider: `https://api.predb.net/`** (setting
`usenet_nntp_predb_http_url`). Verified live on 2026-10-16:

- JSON API: `GET /?type=pre&q=<release>` returns
  `{"status":"success","data":[{"release","size"(MB),"files","section","pretime","status"}]}`.
  The query is a loose match, so the client keeps only the exact
  (case-insensitive) release name. A release with no match returns
  `status:"success"` with zero results (a cacheable miss).
- No API key is required, so there is no secret to store for HTTP.
- Published limit: 30 requests/minute. The client spaces requests at
  least 2 seconds apart, caches hits locally, and caches confirmed misses for
  30 days. Errors, non-200 responses, and malformed JSON are *not* cached and
  never rename anything.
- `predb.ovh` was a candidate but did not resolve (DNS) during verification.
- If the provider changes its ToS or response shape, set
  `usenet_nntp_predb_http_enabled=false` (or an empty URL: the adapter goes
  inert and logs once); name recovery then relies on PAR2/RAR/yEnc names that
  pass the title + (year|episode) + (resolution|source) check.

The IRC feed (`usenet_nntp_predb_irc_enabled`) is the only way to reverse
md5/sha1(title) subjects, because a name-lookup API cannot reverse a hash. It is
forward-only (no history), so it is off by default. The NickServ password is
stored encrypted (`secrets.Store`) and is never logged.

## Spike numbers

See `docs/usenet-nntp-native-spike.md`.
