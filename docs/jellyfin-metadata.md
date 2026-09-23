# Sakms posters (DB) — Jellyfin stays independent

sakms stores poster art as `poster_url` / `poster_source` on
`library_items` and `library_series`. Jellyfin may keep scraping and writing
its own `folder.jpg` / NFO on disk; sakms does **not** write sidecars.

## When posters are filled

1. **Import** — after a grab lands, `ensureImportPoster` runs TMDB → TVDB →
   AI+SearXNG and persists an absolute URL on the library row.
2. **Lazy `/poster`** — card open still resolves and caches the same way.
3. **Throttled backfill** — `POST /api/admin/posters/backfill` and a boot
   one-shot walk titles with empty `poster_url` (and series with `tmdb_id≤0`).
   Gap between titles: **2 seconds** (`posterBackfillGap`) so TMDB/TVDB are
   not stampeded.

Series with `tmdb_id≤0` are repaired from an **existing** `tvshow.nfo` when
present (read-only). If that file has no TMDB id, backfill uses GuessTitle
and then web search, and sends the grounded title to TMDB's TV search.

## TMDB rate limit

All live TMDB HTTP calls (Rename, Discover, `/poster`, backfill, etc.) share
one process-wide limiter: **40 requests / 10 seconds** (token bucket, burst
40). Callers **block/wait** until a token is available (or their context
cancels). Cache hits do not consume a token. The poster backfill’s 2s gap
remains as extra pacing for TVDB/AI, not a substitute for this limit.

## Admin trigger

```http
POST /api/admin/posters/backfill
```

Returns `202` immediately. Logs:

```text
poster backfill: starting (gap=2s)
poster backfill: done movies_ok=… movies_fail=… series_ok=… series_fail=… id_repaired=…
```

## Troubleshooting

- **Letter tile** — row has empty `poster_url` (and maybe `tmdb_id=0`). Wait
  for boot backfill, open the title (`/poster`), or POST the admin endpoint.
- **Series still at tmdb_id≤0** — NFO, GuessTitle, and web search all missed,
  or another row already owns the TMDB id.
- **Old `/api/admin/mediafolder/backfill`** — removed; use `/api/admin/posters/backfill`.

## Embedded file tags (Movies identity)

Rename/scan and poster backfill prefer **ffprobe format.tags** before NFO and
filename: TMDB/IMDb ids when present, otherwise title + year → TMDB search.
Tracked Movies rows with `tmdb_id ≤ 0` are repaired the same way when the
file still has usable tags. If tags, NFO, and GuessTitle still produce no
TMDB id, backfill searches the web (SearXNG when configured), asks the local
model to read the snippets, and sends that title and year to TMDB. Series
rows with `tmdb_id≤0` use that GuessTitle then web-search order on TMDB TV.

