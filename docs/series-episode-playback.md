# Parked plan: Series episode in-app playback (Jellyfin-style series page)

**Status:** parked — do not implement until the operator says to start.
**Saved:** 2026-09-24 (conversation; operator asked to save after answering
scope questions, then paused to do a different change first).
**Base when written:** `main` after Settings multi-screen IA (`baddf904`).
Re-verify line numbers before building — this file is the design, not a
live index.

This is the git-tracked copy. A local pointer also lives at
`.omc/plans/series-episode-playback.md` (`.omc/` is gitignored).

This is **not** the transcoding / native-TV player item in `docs/ROADMAP.md`
("Streaming media player" / "Native TV-app player enabling real transcoding").
That work needs a legal/licensing gate and HLS encode. This plan only
extends the **already-shipped HTML5 Movies player** to Series episodes.

---

## Operator decisions (locked 2026-09-24)

| Question | Answer |
|---|---|
| Where does play start? | In-app player — same `TrackedPlayback` / `PlayFullscreenLink` Movies uses |
| How close to Jellyfin? | Full: series Play/Resume, season picker, episode list with play + progress |
| Surfaces | Library Series detail **and** Discover Series `DetailPopup` |
| Out of scope | Poster-card play, transcoding, Jellyfin/Emby/Plex deep-link, Adult series |

Kids is not a separate page — same Library Series surface. Adult has no series.

---

## Why this exists

Movies Library detail can play a film. Series cannot.

- `playableLibrarySrc` in `frontend/src/screens/Library.tsx` returns `""` for series.
- `GET /api/modes/{mode}/tracked/{id}/video` returns **400** for series
  (`trackedVideoHandler`: "only supported for adult scenes and movies").
- Episode files already exist: `library_episodes.file_path` +
  `library_episode_files`. The UI never sees `episodeId` or a `videoUrl`.
- There is **no** watch-progress table or API. Resume and episode progress
  bars cannot be honest without adding one.

Comments already flag the hole (`Review if: Series episode playback lands`)
in `Library.tsx`, `DetailPopup.tsx`, `TrackedPlayback.tsx`, `tracked.go`.
`Library.test.tsx` asserts **no** "Play Show →" for series.

---

## Current surfaces (do not confuse them)

### Library Series detail

`LibraryView mode="series"` → card click → `DetailPopup` (`allowGrab={false}`)
with `DetailPanel` children: quality prefs, **`SeasonsPanel`** (monitor +
collapsed `E01 · Title · missing` text), tags. No Files block. No play.

`GET /api/modes/series/tracked` has no `files` / `videoUrl`.
`GET /api/modes/series/library/{seriesID}/seasons` returns `SeasonState` +
`SeasonEpisode{episodeNumber,title,hasFile}` — no id, path, or stream URL.

### Discover Series detail

Same `DetailPopup` (`allowGrab` default true). `SeasonEpisodePicker` is a
**TMDB grab gate** (poster/still grids, whole-season tile). `SeasonsPanel`
is keyed by `tmdbId` (same `library_season_monitored` rows). No `playSrc`.

A Discover title that is **not** in the library has nothing to stream.
Play UI on Discover is only for a tracked series (resolve via existing
`.../library/tmdb/{tmdbId}/seasons` alias, or `LibraryCard` which already
has a `TrackedItem`).

### What Jellyfin does, and the modal adaptation

Stable Jellyfin web is two hops: series → season cards → episode list.
Header **Play / Resume** uses Next Up / in-progress. Episode rows are a
list (still, title, overview, play, progress). Player has next/prev.

Swiftfin and jellyfin-web#8364 collapse season picker + episode list onto
the series page.

SAK detail is already a **modal**. Extra hops inside that modal are worse
than Jellyfin. Copy the **collapsed** page:

- Header Play Show / Resume (next playable episode)
- Season selector on the same screen
- Episode rows with still, title, play, progress
- Leave `SeasonEpisodePicker` as the Discover **grab** block — do not
  reuse it for playback

Do **not** add a `/play` route or a second modal. `TrackedPlayback.tsx`
already forbids nesting dialogs inside Library detail.

---

## Architecture

### 1. Stream route (mirror Movies)

A show is not one file. Do **not** put a series-level `videoUrl` on
`GET /tracked`.

```
GET /api/modes/series/tracked/{seriesID}/video?episodeId=&fileId=
```

`{id}` stays the **series** id. Resolve like `movieTrackedVideoPath`:

1. `GetEpisodeByID` — episode must belong to `{seriesID}`
2. If `fileId` > 0: that row in `ListEpisodeFiles`
3. Else primary / first file / denormalized `episode.FilePath`

Reuse `browserPlayableVideo` (`.mp4`, `.m4v`, `.webm`, `.mov`) and
`serveLocalVideoFile` (`http.ServeContent`, Range). Same "cannot play in
the browser" copy for mkv/avi. No transcoding.

Helpers already in store: `GetEpisode`, `GetEpisodeByID`, `ListEpisodes`,
`ListEpisodeFiles`.

Replace `TestTrackedVideoHandler_SeriesStillUnsupported` with: 400 without
`episodeId`, 404 wrong series, 200 + Range for a playable episode.

### 2. Enriched seasons payload

Extend the existing seasons GETs (Library id + TMDB alias) rather than
dumping episodes onto `GET /tracked`.

`SeasonEpisode` should grow:

- `id` (library episode row)
- `airDate`
- `overview` if we already have it; otherwise omit
- `stillPath` — optional TMDB merge from `discover/detail` (lazy if a
  20-season show makes the GET heavy)
- `hasFile`
- `videoUrl` + `files[]` (same shape as movie `TrackedItemFile`)
- After phase 3: `positionSeconds`, `durationSeconds`, `watched`

### 3. Header Play Show

`playSrc` is the **next playable episode URL**, never "the show":

1. In-progress, unwatched, playable file → **Resume Show**
2. Else first unwatched playable file (lowest S/E; skip Specials unless
   that is all that exists) → **Play Show**
3. Else first playable file → **Play Show**
4. Else hide the button (today's behavior)

`PlayFullscreenLink` already uses noun `"Show"` when `mode === "series"`.

### 4. UI split

Keep **`SeasonsPanel` monitor-only** (Monitor all + collapsed switches).
Do not hang play controls off those rows.

Add **`SeriesEpisodesPanel`**:

- Library: `{ seriesID }`
- Discover: `{ tmdbId }` — render only when seasons GET resolves a
  tracked series with episode rows
- Season chips / select (season 0 = Specials, same as `SeasonsPanel`)
- Rows: still (TMDB still or placeholder), `SxxExx`, title, air date,
  `TrackedPlayback` or "missing" / "cannot play in the browser"
- Progress bar when position exists

`LibraryCard` (`Mainstream.tsx`) can pass `playSrc` the same way Library
does once next-up is known. Catalog-only Discover cards stay grab-only.

### 5. Watch progress (required for honest Resume)

No `watch_progress` / playback-position table exists (`internal/db`).
Without one, Resume and progress bars are fake.

Minimal store (Series only this pass):

```
library_episode_progress (
  episode_id PK → library_episodes,
  position_seconds,
  duration_seconds,
  watched,
  updated_at
)
```

- `PUT` on throttled `timeupdate` / `pause` / `ended`
- Mark watched at ~90% or `ended`
- Movies/Adult stay untouched

`TrackedPlayback` stays the `<video>` primitive. Series wraps it with
episode identity + optional queue (current + next/prev in season, then
next season). Movies keep single-file behavior.

### 6. Explicit non-goals

- Transcode `.mkv` / `.avi` / `.wmv`
- Change `SeasonEpisodePicker` grab semantics or count rules
- New `/play` route or nested `Modal`
- Sync progress with Jellyfin/Emby/Plex (those connections are
  **rescan notify only** — `internal/jellyfin` is not a stream client)
- Kids-specific route
- Play buttons on poster cards
- The transcoding / native-TV roadmap item

---

## Implementation sequence (when authorized)

### Phase 1 — Stream + list + Play (no Resume yet)

Backend episode video + enriched `SeasonEpisode`. `SeriesEpisodesPanel`
on Library. Per-row `TrackedPlayback`. Header Play Show = first playable
episode. Tests: tracked video series cases; Library "Play Show →" when
a playable episode exists (today's null assertion must flip).

### Phase 2 — Discover

Same panel + `playSrc` when the series is tracked (`LibraryCard` and a
TMDB card whose `.../tmdb/{id}/seasons` resolves). Hide play when not
tracked.

### Phase 3 — Progress + Resume

Migration + PUT + bars + Resume vs Play + Next Up rule in §3.

### Phase 4 — Player queue (optional after 1–3)

Next/prev episode in the open `<video>` (Jellyfin OSD). Play from here.
Not required to ship 1–3.

---

## Files that will change

| Layer | Paths |
|---|---|
| API | `internal/api/tracked.go`, `internal/api/airdatemonitor.go` (`writeSeasonStates`), `internal/apidto/dto.go`, new progress handler + SQL migration |
| Store | `internal/library/library_series.go`, `library_episode_files.go` (read path exists) |
| Frontend | `Library.tsx` (`playableLibrarySrc`), new `SeriesEpisodesPanel`, `SeasonsPanel` stays monitor-only, `DetailPopup.tsx`, `Mainstream.tsx` (`LibraryCard`), `TrackedPlayback.tsx` (progress + optional queue) |
| Tests | `tracked_test.go`, `airdatemonitor_test.go`, `Library.test.tsx`, `DetailPopup.test.tsx`, new panel tests |

Re-run `go run ./cmd/gendto` after DTO changes.

---

## Risks / re-verify before coding

- Line numbers in this file will drift. Re-read `playableLibrarySrc`,
  `trackedVideoHandler`, `SeasonEpisode` in `apidto`, and `SeasonsPanel`.
- Enriching every episode on `GET .../seasons` is fine for normal shows;
  stills may need lazy `discover/detail` for long-running series.
- Discover TMDB cards that are **also** in the library already resolve
  via the tmdb seasons alias (`SeasonsPanel`). Reuse that, do not add a
  second lookup path.
- Header `playSrc` is a single string today. Resume + next-episode means
  "URL of the next episode" (or a small object), not a series-level URL.
- Section lock: Movies tracked video deliberately skips Adult lock.
  Series should match Movies unless a new constraint appears.

---

## Verify when built (not now)

- `CGO_ENABLED=0 go build ./...` and the series tracked-video tests
- Frontend: Library series play, Discover tracked vs catalog-only, no
  grab-picker regressions (`SeasonEpisodePicker` tests stay green)
- Browser: Library series with a playable mp4 — header Play Show and a
  row Play both start the same player Movies uses; a catalog-only
  Discover series still has no Play

---

## Related

- Movies play: `frontend/src/components/TrackedPlayback.tsx`,
  `internal/api/tracked.go` (`serveMoviesTrackedVideo`)
- Seasons API: `frontend/src/api/seasons.ts`, `internal/api/airdatemonitor.go`
- Roadmap pointer: `docs/ROADMAP.md` → "Series episode in-app playback"
- Distinct from: `.omc/specs/deep-interview-streaming-media-player.md`
