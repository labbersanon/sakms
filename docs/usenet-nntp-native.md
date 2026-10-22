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
- New obfuscated sets stage on namefix yield groups (packages.json step ≤4
  plus `alt.binaries.anime`) and on groups that already have a recovered
  name, so firehose hashes cannot fill `unnamed_max_pct`. Already-staged
  keys still receive continuation rows.

## PreDB provider (name recovery)

Claude 2026-10-15: obfuscated sets are renamed from a local PreDB table
(`usenet_nntp_predb`) fed by an on-demand HTTP lookup (default on) and an
IRC announce feed (default on: synIRC `#PreNNTmux`).

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

A name-lookup HTTP API cannot reverse a hash. Reverse lookups use the local
table: IRC announces (forward-only, including NOTICE hashed `[FN:]` filenames
from TmuxPREBot on `#PreNNTmux` and `[RQ:]` request ids) plus an
operator-supplied nZEDb dump (`usenet_nntp_predb_dump_path`, local TSV/gz or
an http(s) dump *file* URL). Do not scrape `api.predb.net` to populate that
table. nZEDbPre_Dumps stopped publishing new daily files in
2024-05; historical `dumps/*/*_predb_dump.csv.gz` are still on GitHub and
import as quoted MySQL TSV (`\N` nulls). There is no default dump URL.
Default IRC is on: synIRC `#PreNNTmux`, TLS 6697, a random unregistered
nick, client PING heartbeat, plus a second client on Corrupt-net `#pre`
(`irc.corrupt-net.org:6697` TLS). NickServ is optional and unused by default
(synIRC confirm mail does not arrive). The NickServ password, if set, is
stored encrypted (`secrets.Store`) and is never logged. `#nZEDbPRE` is dead;
do not point the scraper there. Extra feeds: KV
`usenet_nntp_predb_irc_extra` JSON `[{server,port,tls,channels}]`. Empty =
Corrupt-net default. `[]` or `none` turns extras off (primary synIRC stays).

## Gap fill (completeness, repair, name recovery, backfill)

Claude 2026-10-15: four cooperating features make the index complete and
searchable by true release name. All run on the single crawl goroutine, borrow
connections `tryOnly`, yield on `ErrBusy`, and reuse `chunkPause` / `busyBackoff`
/ `catchUpPause`, so downloads are never starved. Loop order per iteration:
collection-key rewrite (once) → forward crawl → repair → recompute → name recovery → backfill → sweeps → liveness → idle collection-drift.

- **Collection grouping.** `headers.release_name` is the upload, not the file.
  `ParseSubject` strips `[k/N]`, `(n/m)`, and `N of M` from the stem (NNTmux
  collection hash input) and stores the file counter on `file_n`/`file_m`.
  `[48/73] Show…` and `[01/73] Show…` share one key. A quoted filename that is
  already a title (resolution, including underscore `1080p`; a spacey or
  underscore-separated name such as `Metropolis_Magazine_-_September_2026`;
  or `[Group] fansub`) is the release key instead of the pack stem or `obf:`.
  Completeness then ignores collection `file_m` on ident-shaped keys so one
  mkv is not scored as 12 missing files. Hash.par2 quotes stay staged. Leading `[#a.b.teevee]` /
  `[Group]` tags are stripped. EFNet `[id]-[FULL]-[#a.b…@EFNet]-[ Title ]` and
  `www.Bin-Req.net Presents: #id - Title` wrappers are stripped to the title;
  a request-id-only leftover (`23051509 ]`) is staged, not searchable, so dump
  `requestid` / IRC `[RQ:]` can still name it. Obfuscated sets hash `(group, poster, skeleton)`;
  a 16+ character token before `[k/N]` is the skeleton (NNTmux hash-token
  capture) so per-file random quoted names still cluster. Skeletons shorter
  than 8 characters are not staged. Existing headers are rewritten in place
  (`usenet_nntp_collection_rewrite_v3`); watermarks are not rewound. v3
  rekeys ident-quoted episode filenames without wiping recovered names.
  Extra RE2 `strip_prefix` / `lead_token` patterns (KV
  `usenet_nntp_collection_extra_patterns`) are applied at parse time. An idle
  crawl-loop pass samples live split clusters and unseen NNTmux seeder regexes,
  asks the configured AI (`ChatJSON`) for one pattern, and publishes it only if
  golden stem fixtures still hold. Applied extras do not rewrite stored
  headers. The pass no-ops without an AI client (`ai_fallback_enabled`).
- **Strict completeness gate.** Autograb and `ToNZB` require at least
  `min_completeness_pct` of payload segments (default 95, clamped 80–100).
  Computed per release into `usenet_nntp_releases` (`searchable`,
  `completeness_bp`, `gate_reason`). Identification is separate: a PreDB,
  PAR2/RAR/yEnc/NFO, or a title with resolution plus year, season, or a
  multi-word name is searchable at ≥80%; `Query.GrabReady` and `ToNZB` still
  refuse it until the set meets the gate. Picture-date buckets, hash
  tokens, and junk mega-buckets (`(Els1212)`, description-GB, single tokens)
  stay off search even at 100%. Dashboard **Identified** is binary-group
  (non-`pictures.*`) headers on those names, excluding `(????)` /
  single-token poster IDs from the denominator.   Below-80% named
  releases are reaped after HIGH has passed (`SweepIncomplete`);
  never-recomputed scraps (`last_msg=0`) that cannot reach 80% from current
  headers are skip-stubbed first (`SweepUncomputedIncomplete`) so they do
  not occupy the per-key recompute budget. `incomplete_ttl_days` is unused
  on that path.
- **Gap repair.** OVER chunks record missing article numbers in
  `usenet_nntp_missed_ranges`; a bounded repair pass re-OVERs them (3 attempts
  with backoff, then the range is dropped). The forward watermark never waits
  on a gap.
- **Name recovery.** Obfuscated binary sets are staged under
  `release_name='obf:<key>'` instead of dropped. Recovery order: local PreDB by
  md5 / md5(md5(title)) / sha1 tokens taken from every header subject and
  filename (suffix-stripped), then PreDB `filename` / IRC `[FN:]`, then dump/IRC
  request id (EFNet/Bin-Req wrappers), then a
  quoted or scene-shaped header filename (fansub `" [UQW] Title…1080p.par2 "`
  included; `reFileTok` cannot match spaces/`+`/`[]`), then at most 3
  first-segment BODY fetches per
  set (PAR2 `FileDesc` → RAR/ZIP member names → yEnc `=ybegin` name → NFO scene
  name). PreDB (no BODY) also runs on ≥80% pending sets in **any** group
  (including boneless), 200 per pass, even when OVER is busy (BODY waits).
  Each pass runs quoted ident-shaped header names first (no BODY; ignores
  `next_name_at`), then 95% BODY, then ident BODY, then a one-shot hash_16K
  seed of named/shelved PAR2, then PreDB-only keys. BODY has a 90s phase
  budget; header/parhash/predb stay 20s. Namefix runs before OVER. While
  BODY leftover>0, OVER/repair/backfill skip (cap 4 consecutive skips so
  HIGH cannot starve). ErrBusy from downloads still skips crawl.
  BODY fetches up to 4 sets in parallel (tryOnly borrow; ErrBusy yields the
  rest of the phase). Header/parhash/predb stay sequential. When BODY leftover
  is >0, parhash seed and PreDB-only are parked until leftover drains so the
  next 90s BODY is not delayed 40s.
  Each BODY article is capped at 8s (dial included) and does not inherit leftover
  phase crumbs, so setRows eating the phase still fetches one PAR2. A hung fetch
  logs `namefix body msgid=` and continues to the next PAR2/NFO pick unless the
  phase is already gone. PAR2 FileDesc / yEnc names that are md5/sha1 tokens
  are reverse-looked up in the local PreDB (they are not in the header rows).
  FileDesc `hash_16K` (MD5 of the first 16 KiB) is stored in
  `usenet_nntp_par_hashes` and matched to already-named releases within ±5%
  size (NNTmux `fixNamesWithParHash`). A named PAR2 set can copy its title onto
  a hash.par2 sibling whose FileDesc names are also hashes. Seed BODYs only the
  main (non-vol) `.par2` and reserves half of each seed pass for shelved
  pending so named donors cannot starve hash.par2. The due queue ranks SFV,
  then `has_par2` without rar/zip/7z (hash.mkv+PAR2), then RAR/ZIP, then other
  PAR2. Split 7z is not a prefer rank. ngPost hash.7z and UUID `.7z.enc`
  are dropped from BODY queues (password is only in the NZB); PreDB may
  hash-lookup then shelve.   Hex-hash filenames skip PreDB `ByFilename` LIKE
  (those are `ByHash`). Stem matches (`hash.mkv` → `hash.part1.rar`) use a
  btree range on `lower(filename)`, not `LIKE` (the equality index cannot
  serve LIKE without `text_pattern_ops`).
  BODY picks SFV, then the main `.par2` (FileDesc / hash_16K), then the
  first-volume RAR/7z (`part01` / `.7z.001` / bare `.rar`) for srrdb CRC
  after a hash FileDesc. Last-volume RAR is skipped when a main PAR2
  exists. 7z last segment of `.7z.NNN` still runs for the end header.
  Never `.vol*.par2` or `.7z.enc`. A `rar5-enc` sample shelves the set.
  95% complete sets run before 80% ident BODY. RAR4 `FILE_CRC` / RAR5 Data CRC32 / ZIP CRC /
  7z end-header CRC / SFV CRC32 is looked up on srrdb
  `archive-crc:` when that CRC is unique
  (`resultsCount==1`, size ±15%); srrdb ToS is use-don't-scrape (no catalog
  paging). Native namefix (PAR2 / PreDB / srrdb / SFV) does not query
  Newznab — NZB indexers do not recover ngPost passwords. The Settings
  NZB host (`connections` service `newznab`) is an alternate Usenet
  **grab** search after native NNTP and before Prowlarr (`t=search` title
  + category, enclosure/`t=get` download URL). sakms does not scrape
  indexer HTML and does not brute-force.
  An `.sfv` whose payload filename already parses as a scene title is
  accepted as `name_source=sfv` without HTTP.
  Yield-group BODY is 40/pass at 95% plus 20/pass at ≥80% when PAR2 is present.
  A BODY whose FileDesc is the same hash is not retried (PreDB-only from then on).
  Sets with PAR2 are BODY-sampled even under 100 MiB. Accepted: a
  PreDB-confirmed name (size within ±15% when PreDB reports one), or a
  PAR2/RAR/yEnc name that parses as title + (year|episode) +
  (resolution|source), or an ident-shaped name (resolution plus year, season,
  or a multi-word title such as fansub `Ep170` / spaces), even on a PreDB miss.
  Spaced HD ident-shaped header names (snapped ≥720) skip the quality gate so a
  2160p movies floor cannot delete a complete 1080p quoted set. SD ident (480p)
  still faces MinRes. NFO-only names need a PreDB hit.
  Sets under 100 MiB without PAR2 are never BODY-fetched; after 3 attempts or
  `unnamed_ttl_days` a set is finalized and deleted, except ≥80% yield-group
  sets which stay for PreDB-only retries so a later IRC line or dump
  import can still name them. Staged header occupancy (pending_name rows × ~200 B,
  same units as `index_max_gb`) is capped at `unnamed_max_pct` of `index_max_gb`
  **per group**; BODY volume is `namefix_body_mb_per_hour`.
  New obfuscated headers stage on yield groups (step ≤4 + anime) even with
  no recovered name yet; step-5 firehose still needs a searchable recovered
  name.
  Claude 2026-10-19: occupancy, not yEnc payload_bytes — that mismatch was
  skip_obf_cap on every yield core.
  Review if: ApproxBytes stops using ~200 B/row.
  Historical names come from an operator-supplied nZEDb TSV dump
  (`usenet_nntp_predb_dump_path`, local file or dump-file URL); sakms does not
  scrape `api.predb.net` to build that table.
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
| `unnamed_ttl_days` / `unnamed_max_pct` | 7 / 25 | Staging TTL / header-occupancy cap |
| `predb_http_enabled` / `predb_http_url` | true / `https://api.predb.net/` | HTTP lookup |
| `predb_irc_enabled` | true | All IRC feeds (primary + extras) |
| `predb_irc_server`, `_port`, `_tls`, `_nick`, `_channels` | `irc.synirc.net`, 6697, true, generated, `#PreNNTmux` | Primary IRC connection (channels comma-separated; empty nick is generated) |
| `predb_irc_extra` | Corrupt-net `#pre` | JSON extra feeds on other networks. Empty = default. `[]`/`none` = primary only |
| `predb_irc_password_enc` | — | NickServ password, `secrets.Store` ciphertext; set via `PUT /api/settings/usenet-nntp-native/predb-irc-password` `{"password":"…"}` (empty clears) |
| `predb_dump_path` | empty | nZEDb PreDB dump file, directory, or http(s) dump *file* URL (`\t\t` TSV, optional `.gz`; quoted MySQL fields and `\N` are stripped). PUT `predbDumpPath` reloads the importer; SQL still needs a restart. Imported on boot/reload and every 15 minutes when mtime/size (or URL ETag) changes. Search APIs (`api.predb.net`, `?type=pre`) are rejected. Live file: `/data/predb/predb.tsv.gz` (host `/mnt/iscsi/sakms/predb/predb.tsv.gz`, nZEDb dailies through 2024-05-02). |
| `srrdb_enabled` | true | CRC-only lookup at `https://api.srrdb.com/v1/search/archive-crc:XXXXXXXX` (2s spacing). Unique hit + size ±15% → `name_source=srrdb`. Kill switch; do not scrape the catalog. |
| `nzb_password_enabled` | true | Unused on the live path. Newznab is grab-search, not namefix. |
| `min_completeness_pct` / `incomplete_ttl_days` | 95 / 3 | Completeness gate / reap |
| `repair_enabled` | true | Gap capture + repair |
| `backfill_enabled` / `backfill_days` / `backfill_budget_pct` | true / 0 (retention) / 90 | Backfill |
| `collection_drift_enabled` | true | Idle ChatJSON pass that may add extra RE2 grouping patterns (no-op without AI) |

`GET /api/settings/usenet-nntp-native` returns the knobs under `gapFill` and
counters under `gapFillStatus` (`releasesSearchable`, `releasesBelowGate`,
`pendingName`, `namedBySource`, `missedRanges`, per-group `backfill`
`{low,target,done,reason}`).

### Operating notes

- O2 queries: `o2cli grep "collection rewrite"`, `"collection drift"`, `"namefix pass"`, `"nzb-password"`, `"repair pass"`,
  `"backfill group="`, `"completeness recompute"`, `"predb"`.
  `backfill ... state=paused reason=budget` means the index is at the budget.
- Re-run a finished backfill (e.g. after raising `backfill_days`):
  `UPDATE usenet_nntp_watermarks SET backfill_done=FALSE, backfill_reason='' WHERE group_name='…';`
- Undo a rename so the set is retried:
  `UPDATE usenet_nntp_releases SET display_name='', state='pending_name', name_source='', name_attempts=0, next_name_at=0, dirty=TRUE WHERE group_name='…' AND release_key='obf:…';`
- Migration `0044` has a Down that drops the new tables/columns; the `release_name`
  grouping key on headers is unchanged, so rolling back loses only recovered names.
- **IRC format.** `#PreNNTmux` lines are tagged (`[TT:]` title, `[CT:]` category,
  `[SZ:]`/`[FL:]` size/files, `[FN:]`/`[FILE:]`/`[FILENAME:]` hashed inner name,
  nuke tags). A hashed `[FN:]` line without `[TT:]` updates the last title.
  Logs emit tag letters only (`predb irc tags keys=`), never titles. NNTmux-style
  `NEW:`/`NUKE:` lines still parse. Keepalive `INFO:` lines have no `TT` and are
  ignored. The feed is on by default.
- With `namefix_enabled=false`, legacy subjects like `"<hash>.part01.rar"` can
  still produce bogus per-filename release names (pre-existing behavior).
- **ngPost** (compress + gen_name + gen_pass + 7z): overview is
  `[001/136] "random.7z.001" (10/732)` — no `yEnc` token, default article
  716800 bytes, last article is a remainder. From is `N@ngPost.com`; msgid
  `uuid@ngPost`. ObfKey omits group so GROUP_POLICY ALL does not split the
  set. ngPost hash.7z is not BODY-fetched (password locked); PreDB miss
  shelves BODY attempts so hash.mkv+PAR2 sets get the BODY phase budget.
  Title and password are in the NZB, not on NNTP; native namefix does not
  query Newznab (passwords are not recoverable from NZB indexers). `-x` UUID subjects have no
  filename in OVERVIEW and stay skip_unparsed.

## Spike numbers

See `docs/usenet-nntp-native-spike.md`.
