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
| Per-mode group set | Always every provider `LIST ACTIVE` group matching the mode wildmats (not `packages.json`, not the slider) |
| Per-mode package slider | Grade tolerance ceiling only (default 5 = most lenient): crawl/search groups with grade ≤ step; advanced customize → Custom |
| Passive grades | Start at 1 (most reliable); +1 per Usenet article failure (sticky at 5); grade > ceiling → stop crawl + delete that group's index |
| Max GiB / soft age days / idle backoff | Soft size budget; soft age display; idle backoff after catch-up (0 = 60s default). Crawl is continuous when enabled |
| Probe state/detail | Read-only |
| Dashboard card | GET `/api/admin/nntp-crawler` — groups, headers, index size, searchable/pending/missed, last OVER chunk |
| Preferred grab languages | Same Global setting as autograb. When the NNTP service is wired, the index skips groups whose name encodes a non-preferred language (e.g. `dvd.german`) and does not admit titles `TitleLanguageAllowed` would reject. Empty list = english-assumed (unmarked + english tags). |

## Honest limits

- NNTP has no `SEARCH`. Discovery is local index search only.
- Obfuscated subjects are not recoverable from headers alone; see "Gap fill" for the
  bounded BODY/PreDB recovery (some sets stay unnamed and are deleted).
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

Group membership: every provider group from mode-scoped `LIST ACTIVE`
wildmats (`internal/usenetsearch/mediagroups.go`). `seeds/packages.json`
only annotates known cores (step/pinned badges) — it is not the crawl set.
The Settings slider is **grade tolerance only** (default 5 = most lenient).
Every group starts grade 1; Usenet article failures demote one tier (5
sticky). When grade exceeds the ceiling, crawl stops for that group and
its index rows are deleted. Advanced customize → Custom.

Forward crawl is bounded like backfill: at most 25 OVER chunks per loop
iteration and 8 per group, round-robin start. A firehose obfuscated bin
(`bloaf` / `ath`) is still a member, but it cannot hold the goroutine
until HIGH — cores get a turn the same pass, and namefix runs between
passes.

NNTmux-inspired ingest (membership still full LIST ACTIVE):
- Package cores (`packages.json`) get 2/3 of each forward pass; the LIST
  tail gets the rest. Cores and tail rotate independently.
- Backfill only walks package cores and groups that already have a named
  (non-`obf:`) index row — analog of NNTmux `usenet_groups.backfill=1`.
- New obfuscated sets are staged only on those same groups, so firehose
  hashes cannot fill `unnamed_max_pct`. Already-staged keys still receive
  continuation rows.


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

## Gap fill (completeness, repair, name recovery, backfill)

Claude 2026-10-15: four cooperating features make the index complete and
searchable by true release name. All run on the single crawl goroutine, borrow
connections `tryOnly`, yield on `ErrBusy`, and reuse `chunkPause` / `busyBackoff`
/ `catchUpPause`, so downloads are never starved. Loop order per iteration:
collection-key rewrite (once) → forward crawl → repair → name recovery → recompute → backfill → sweeps → liveness.

- **Collection grouping.** `headers.release_name` is the upload, not the file.
  `ParseSubject` strips `[k/N]`, `(n/m)`, and `N of M` from the stem (NNTmux
  collection hash input) and stores the file counter on `file_n`/`file_m`.
  `[48/73] Show…` and `[01/73] Show…` share one key. Leading `[#a.b.teevee]` /
  `[Group]` tags are stripped. Obfuscated sets hash `(group, poster, skeleton)`;
  a 16+ character token before `[k/N]` is the skeleton (NNTmux hash-token
  capture) so per-file random quoted names still cluster. Skeletons shorter
  than 8 characters are not staged. Existing headers are rewritten in place
  (`usenet_nntp_collection_rewrite_v2`); watermarks are not rewound.
- **Strict completeness gate.** A release is searchable only when at least
  `min_completeness_pct` of its payload segments are indexed (default 95,
  clamped 80–100). Computed per release into `usenet_nntp_releases`
  (`searchable`, `completeness_bp`, `gate_reason`); `ToNZB` re-checks it.
  Below-gate named releases are reaped after `incomplete_ttl_days`.
- **Gap repair.** OVER chunks record missing article numbers in
  `usenet_nntp_missed_ranges`; a bounded repair pass re-OVERs them (3 attempts
  with backoff, then the range is dropped). The forward watermark never waits
  on a gap.
- **Name recovery.** Obfuscated binary sets are staged under
  `release_name='obf:<key>'` instead of dropped. Recovery order: local PreDB by
  md5/sha1 → at most 3 first-segment BODY fetches per set (PAR2 `FileDesc` →
  RAR/ZIP member names → yEnc `=ybegin` name → NFO scene name). Accepted: a
  PreDB-confirmed name (size within ±15% when the PreDB reports one), or a
  PAR2/RAR/yEnc name that parses as title + (year|episode) + (resolution|source)
  even on a PreDB miss. NFO-only names need a PreDB hit. Sets under 100 MiB are
  never BODY-fetched; after 3 attempts or `unnamed_ttl_days` a set is finalized
  and deleted. Staged bytes are capped at `unnamed_max_pct` of `index_max_gb`
  and BODY volume at `namefix_body_mb_per_hour`.
- **Retention backfill.** A per-group `low` cursor (`usenet_nntp_watermarks.low`)
  walks from the oldest indexed article down to provider retention in 2000-article
  chunks, through the same parse → admission → upsert path. It pauses at
  `backfill_budget_pct` of `index_max_gb` (resumes as space frees), stops early at
  `backfill_days`, and is skipped while admission is off. `PruneOldest` now
  evicts by posted time, so backfilled history is the first to go under pressure.

### Knobs and kill switches

All settings are KV rows (`settings` table), read on every settings GET/PUT and
at boot; no UI. The two PreDB/IRC groups are read when the PreDB runtime starts
(boot, or after the IRC-password PUT). With every switch below set to `false`
the crawl behaves as before this feature, except that staged/obfuscated subjects
are dropped again and completeness is still recorded.

| Key (`usenet_nntp_…`) | Default | Effect |
|---|---|---|
| `namefix_enabled` | true | Stage + recover obfuscated sets (off = drop them) |
| `namefix_body_mb_per_hour` | 512 | Hourly BODY budget |
| `unnamed_ttl_days` / `unnamed_max_pct` | 7 / 25 | Staging TTL / cap |
| `predb_http_enabled` / `predb_http_url` | true / `https://api.predb.net/` | HTTP lookup |
| `predb_irc_enabled` | false | Optional IRC feed |
| `predb_irc_server`, `_port`, `_tls`, `_nick`, `_channels` | —, 6697, true, —, — | IRC connection (channels comma-separated) |
| `predb_irc_password_enc` | — | NickServ password, `secrets.Store` ciphertext; set via `PUT /api/settings/usenet-nntp-native/predb-irc-password` `{"password":"…"}` (empty clears) |
| `min_completeness_pct` / `incomplete_ttl_days` | 95 / 3 | Completeness gate / reap |
| `repair_enabled` | true | Gap capture + repair |
| `backfill_enabled` / `backfill_days` / `backfill_budget_pct` | true / 0 (retention) / 90 | Backfill |

`GET /api/settings/usenet-nntp-native` returns the knobs under `gapFill` and
counters under `gapFillStatus` (`releasesSearchable`, `releasesBelowGate`,
`pendingName`, `namedBySource`, `missedRanges`, per-group `backfill`
`{low,target,done,reason}`).

### Operating notes

- O2 queries: `o2cli grep "collection rewrite"`, `"namefix pass"`, `"repair pass"`,
  `"backfill group="`, `"completeness recompute"`, `"predb"`.
  `backfill ... state=paused reason=budget` means the index is at the budget.
- Re-run a finished backfill (e.g. after raising `backfill_days`):
  `UPDATE usenet_nntp_watermarks SET backfill_done=FALSE, backfill_reason='' WHERE group_name='…';`
- Undo a rename so the set is retried:
  `UPDATE usenet_nntp_releases SET display_name='', state='pending_name', name_source='', name_attempts=0, next_name_at=0, dirty=TRUE WHERE group_name='…' AND release_key='obf:…';`
- Migration `0044` has a Down that drops the new tables/columns; the `release_name`
  grouping key on headers is unchanged, so rolling back loses only recovered names.
- **IRC format caveat.** The IRC parser targets NNTmux-style announce lines
  (`[PRE] [section] release` plus size/files/nuke variants). The exact lines of any
  given network/channel were not verified against a live feed; the parser
  ignores lines it cannot read, and the feed is off by default. Verify with a
  real channel before relying on md5/sha1 hash reversal.
- With `namefix_enabled=false`, legacy subjects like `"<hash>.part01.rar"` can
  still produce bogus per-filename release names (pre-existing behavior).

## Spike numbers

See `docs/usenet-nntp-native-spike.md`.
