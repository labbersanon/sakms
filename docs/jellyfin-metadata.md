# Jellyfin metadata — sakms as source

sakms writes Jellyfin/Kodi-compatible sidecars next to media so Jellyfin can
prefer local art and NFO instead of scraping TMDB/TVDB itself.

## Files sakms writes

| File | Location | Purpose |
|---|---|---|
| `folder.jpg` | movie folder / series root | Poster |
| `backdrop.jpg` | movie folder / series root | Fanart |
| `movie.nfo` | movie folder | Title, year, plot, TMDB/TVDB/IMDB ids |
| `tvshow.nfo` | series root | Same fields for TV |

No `<lockdata>` is written. Existing non-empty images are left alone unless a
forced rewrite is requested (admin backfill currently uses `Force=false`).

## When sakms writes

1. **Import** — after a grab lands in the library (`importGrabMovies` /
   `importGrabSeries`).
2. **Poster resolve** — after `/api/.../poster` successfully resolves art
   (lazy path for titles that never imported through sakms).
3. **Backfill** — `POST /api/admin/mediafolder/backfill` (202 Accepted) and a
   one-shot boot sweep that skips existing images.

Series rows with `tmdb_id=0` are repaired from existing `tvshow.nfo` (or via
TVDB→TMDB find) before art is fetched, then `poster_url` / `poster_source` are
updated on the library row.

## Jellyfin library settings (required)

For each Movies/TV library that shares disk with sakms:

1. **Metadata downloaders** — prefer **NFO** (or set NFO first in the order).
2. **Image fetchers** — prefer **local images** / disable remote download if
   you want sakms-only art (Jellyfin will still use `folder.jpg` /
   `backdrop.jpg` when present).
3. Do **not** rely on lockdata; sakms does not set it.
4. After the first sakms backfill, run a Jellyfin library scan (or wait for
   the next scheduled scan) so NFO ids and local art are picked up.

Exact UI labels vary by Jellyfin version; the goal is: local NFO + local
images win over online scrapers.

## Admin trigger

```http
POST /api/admin/mediafolder/backfill
```

Requires a normal authenticated sakms session (same gate as other
`/api/admin/*` triggers). Returns `202` immediately; progress is logged as
`mediafolder backfill: movies_ok=… series_ok=… fail=…`.

## Troubleshooting

- **Letter tile in sakms, poster in Jellyfin** — disk already had
  `folder.jpg` from Jellyfin scrape, but the sakms row had no `tmdbId` /
  `poster_url`. Run backfill (or open the title so `/poster` resolves); sakms
  will repair ids from NFO and persist `poster_url`.
- **Jellyfin re-scrapes and overwrites** — NFO/local image preference is not
  set; adjust library metadata/image settings as above.
- **Backfill no-ops** — title has no `file_path` / episode paths and no
  resolvable series folder under `root_folder_path`.
