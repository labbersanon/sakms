# Parked plan: Series episode in-app playback (Jellyfin-style series page)

**Status:** parked — do not implement until the operator says to start.
**Saved:** 2026-09-24. **Revised:** 2026-09-24 (same day) after the
Discover/Library merge, unified search, and owned Replace/Rematch
decisions. Those changes are **not built yet**; this file is written
against the **post-merge** world.
**Base when first written:** `main` after Settings multi-screen IA
(`baddf904`). Re-verify line numbers before building.

This is the git-tracked copy. A local pointer also lives at
`.omc/plans/series-episode-playback.md` (`.omc/` is gitignored).

This is **not** the transcoding / native-TV player item in `docs/ROADMAP.md`
("Streaming media player" / "Native TV-app player enabling real transcoding").
That work needs a legal/licensing gate and HLS encode. This plan only
extends the **already-shipped HTML5 Movies player** to Series episodes.

---

## Prerequisite: Discover/Library merge (lands first)

Do **not** implement series play against today’s two pages
(`/library/mainstream` + `/discover/mainstream`). The operator paused this
plan to merge those surfaces. Series play mounts on the **merged Discover
owned detail**, after that merge (or in the same effort only if the merge
is already in the tree).

Post-merge (locked 2026-09-24, not yet coded):

- One nav item: Discover. Library sidebar group and `/library*` **pages**
  are gone; `/library*` **redirects** to `/discover/{section}?view=library`.
- **In library** chip mounts `LibraryView` (owned grid). Catalog rows hide.
- Preview **In your library** row stays; **View all** only turns the chip
  on. `/discover/row/library` View All is deleted.
- **Two cards, two jobs** — not one card for everything:
  - Catalog: Discover `PosterCard` + grab `DetailPopup`
  - Owned: Library `PosterCard` + owned `DetailPopup` (play, tags, files)
- Discover `LibraryCard` is **deleted**. Preview and owned grid use the
  owned card.
- **One search box**, always owned **and** catalog. Owned identity wins
  (no duplicate catalog card). Adult search is the same rule.
- Owned detail is **not** `allowGrab={false}` forever. It has **Replace**
  (same-title release pick, existing Discover grid + upgrade-on-import)
  and **Rematch** (`SearchTakeover`, then Replace if a new file is needed).
  Catalog cards stay grab-as-today.
- Kids stays Mainstream owned titles (kids root + tags). No Kids chip.
- `library` section-lock PIN follows Discover.

Series play **only** on owned series detail. Catalog-only series still
have nothing to stream.

---

## Operator decisions

### Playback (locked 2026-09-24, morning)

| Question | Answer |
|---|---|
| Where does play start? | In-app player — same `TrackedPlayback` / `PlayFullscreenLink` Movies uses |
| How close to Jellyfin? | Full: series Play/Resume, season picker, episode list with play + progress |
| Surfaces (original) | Library Series detail **and** Discover Series `DetailPopup` |
| Out of scope | Poster-card play, transcoding, Jellyfin/Emby/Plex deep-link, Adult series |

Kids is not a separate page. Adult has no series.

### Surfaces rewritten (locked 2026-09-24, afternoon)

The original “Library + Discover popups” pair is **stale**. After the merge
there is one Discover page and two **detail modes**:

| Mode | How you get there | Series page |
|---|---|---|
| **Owned** | In library chip, owned search hit, preview-row owned card | Jellyfin-style: Play/Resume, season picker, episode rows + play/progress, monitor panel, **Replace**, **Rematch** |
| **Catalog-only** | TMDB/search catalog card (not tracked) | Grab + `SeasonEpisodePicker` only. No Play Show, no episode stream |

Search owned-wins: a catalog hit you already own opens **owned** detail, so
Replace/Rematch/play are available. That is why a bad download is not a
dead end just because search hid the catalog card.

Original line “Discover play when `LibraryCard` / tmdb seasons resolve” is
replaced by: play iff the popup has a `TrackedItem` (owned). No
`LibraryCard`.

---

## Why this exists

Movies owned detail can play a film. Series cannot.

- `playableLibrarySrc` in `frontend/src/screens/Library.tsx` returns `""` for series.
- `GET /api/modes/{mode}/tracked/{id}/video` returns **400** for series
  (`trackedVideoHandler`: "only supported for adult scenes and movies").
- Episode files already exist: `library_episodes.file_path` +
  `library_episode_files`. The UI never sees `episodeId` or a `videoUrl`.
- There is **no** watch-progress table or API. Resume and episode progress
  bars cannot be honest without adding one.

Comments already flag the hole (`Review if: Series episode playback lands`)
in `Library.tsx`, `DetailPopup.tsx`, `TrackedPlayback.tsx`, `tracked.go`.
`Library.test.tsx` asserts **no** "Play Show →" for series — after the
merge those tests move to Discover owned URLs.

---

## Current surfaces (today, pre-merge — do not implement against these)

### Library Series detail (going away as a route)

`LibraryView mode="series"` → card click → `DetailPopup` (`allowGrab={false}`)
with `DetailPanel` children: quality prefs, **`SeasonsPanel`** (monitor +
collapsed `E01 · Title · missing` text), tags. No Files block. No play.

`GET /api/modes/series/tracked` has no `files` / `videoUrl`.
`GET /api/modes/series/library/{seriesID}/seasons` returns `SeasonState` +
`SeasonEpisode{episodeNumber,title,hasFile}` — no id, path, or stream URL.

After merge: same `LibraryView` + owned popup, mounted from Discover
`?view=library`. `allowGrab={false}` as a permanent owned rule is
**withdrawn** (Replace needs the pick grid).

### Discover Series detail (catalog path stays)

Same `DetailPopup` (`allowGrab` default true). `SeasonEpisodePicker` is a
**TMDB grab gate**. `SeasonsPanel` keyed by `tmdbId`. No `playSrc`.

Catalog-only: still no stream. Owned: do not add a second play path on
the catalog card — open owned detail instead.

### What Jellyfin does, and the modal adaptation

Stable Jellyfin web is two hops: series → season cards → episode list.
Header **Play / Resume** uses Next Up / in-progress. Episode rows are a
list (still, title, overview, play, progress). Player has next/prev.

Swiftfin and jellyfin-web#8364 collapse season picker + episode list onto
the series page.

SAK detail is already a **modal**. Extra hops inside that modal are worse
than Jellyfin. Copy the **collapsed** page **on owned detail only**:

- Header Play Show / Resume (next playable episode)
- Season selector on the same screen
- Episode rows with still, title, play, progress
- Monitor switches stay in `SeasonsPanel` (not mixed into play rows)
- **Replace** / **Rematch** on the same popup (merge decision)
- Catalog grab keeps `SeasonEpisodePicker` — do **not** reuse it as the
  play list

Do **not** add a `/play` route or a second modal. `TrackedPlayback.tsx`
already forbids nesting dialogs inside the detail dialog.

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

Extend the existing seasons GETs (library series id + TMDB alias) rather
than dumping episodes onto `GET /tracked`. Owned detail keys by
`seriesID`; catalog `SeasonsPanel` may still use `tmdbId` for monitoring.

`SeasonEpisode` should grow:

- `id` (library episode row)
- `airDate`
- `overview` if we already have it; otherwise omit
- `stillPath` — optional TMDB merge from `discover/detail` (lazy if a
  20-season show makes the GET heavy)
- `hasFile`
- `videoUrl` + `files[]` (same shape as movie `TrackedItemFile`)
- After progress phase: `positionSeconds`, `durationSeconds`, `watched`

### 3. Header Play Show (owned detail only)

`playSrc` is the **next playable episode URL**, never "the show":

1. In-progress, unwatched, playable file → **Resume Show**
2. Else first unwatched playable file (lowest S/E; skip Specials unless
   that is all that exists) → **Play Show**
3. Else first playable file → **Play Show**
4. Else hide the button (today's behavior)

`PlayFullscreenLink` already uses noun `"Show"` when `mode === "series"`.
Catalog-only popups never pass `playSrc`.

### 4. UI split on owned detail

Keep **`SeasonsPanel` monitor-only** (Monitor all + collapsed switches).
Do not hang play or Replace off those rows.

Add **`SeriesEpisodesPanel`** only when the popup has a tracked series
(`seriesID`):

- Season chips / select (season 0 = Specials)
- Rows: still, `SxxExx`, title, air date, `TrackedPlayback` or "missing"
  / "cannot play in the browser"
- Progress bar when position exists
- Per-episode **Replace** can reuse the same row (search releases for
  that S/E) so a bad episode file does not require rematching the show

Do **not** mount this panel on catalog-only detail.

### 5. Replace and Rematch (owned series)

Same rules as the merge, specialized for episodes:

| Action | Series behavior |
|---|---|
| **Replace** | Identity stays. Pick grid is episode-scoped (season/episode gate, then `/search/grab`). Upgrade-on-import swaps that episode’s primary file (`import.go` already does this). |
| **Rematch** | Wrong show. `SearchTakeover` (fourth caller, owned `TrackedItem`, not a Rename proposal). Updates library series catalog ids/title, then Replace if files are still wrong. |

Do not invent a third picker. Do not keep the bad file as an alternate.
Do not put these actions on the poster card.

`SeasonEpisodePicker` remains the **catalog grab** gate and can gate
**Replace** for “which episode.” It is not the play list
(`SeriesEpisodesPanel` is).

### 6. Watch progress (required for honest Resume)

No `watch_progress` / playback-position table exists (`internal/db`).
Without one, Resume and episode progress bars are fake.

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

### 7. Unified search (does not change the stream API)

Discover search always queries owned + catalog. Dedup key for series is
`tmdbId` (owned wins). Owned-only hits (title match, `tmdbId === 0` or
TMDB miss) still open owned detail with play/Replace.

No series-specific search endpoint.

### 8. Explicit non-goals

- Transcode `.mkv` / `.avi` / `.wmv`
- Change catalog `SeasonEpisodePicker` grab count rules
- New `/play` route or nested `Modal`
- Sync progress with Jellyfin/Emby/Plex (rescan notify only)
- Kids-specific route or chip
- Play buttons on poster cards
- Rebuilding `LibraryCard` or `/library` pages
- Auto “watch for a better release” (deferred movie monitored flag)
- The transcoding / native-TV roadmap item

---

## Implementation sequence (when authorized)

**0 — Discover/Library merge** (separate effort, first). Series play
assumes owned `DetailPopup` + `LibraryView` live under Discover, Library
nav/routes gone, `LibraryCard` gone, search owned-wins, Replace/Rematch
wired on owned detail (movies can ship Replace before series play).

### Phase 1 — Stream + owned list + Play (no Resume yet)

Backend episode video + enriched `SeasonEpisode`. `SeriesEpisodesPanel`
on **owned** series detail only. Per-row `TrackedPlayback`. Header Play
Show = first playable episode. Tests against Discover owned URLs / owned
popup, not `/library/mainstream`. Flip the “no Play Show” assertion.

### Phase 2 — (deleted)

There is no second Discover play surface. Catalog-only stays grab-only.
Owned search hits and the preview row already use the same owned popup
as phase 1.

### Phase 3 — Progress + Resume

Migration + PUT + bars + Resume vs Play + Next Up rule in §3.

### Phase 4 — Player queue (optional after 1 and 3)

Next/prev episode in the open `<video>` (Jellyfin OSD). Play from here.
Not required to ship 1+3.

### Phase 5 — Episode Replace (if merge shipped Replace for movies only)

Per-episode Search releases on the episode row / owned popup, upgrade-on-import.
Skip if Replace on owned series detail already landed with the merge.

---

## Files that will change (post-merge names)

| Layer | Paths |
|---|---|
| API | `internal/api/tracked.go`, `internal/api/airdatemonitor.go` (`writeSeasonStates`), `internal/apidto/dto.go`, new progress handler + SQL migration |
| Store | `internal/library/library_series.go`, `library_episode_files.go` (read path exists) |
| Frontend | `Library.tsx` / wherever `LibraryView` + `playableLibrarySrc` live after the merge; new `SeriesEpisodesPanel`; `SeasonsPanel` monitor-only; owned `DetailPopup` (play + Replace + Rematch); **not** `LibraryCard` |
| Tests | `tracked_test.go`, `airdatemonitor_test.go`, Discover owned-series tests (ex-`Library.test.tsx` series play cases), `DetailPopup.test.tsx`, new panel tests |

Re-run `go run ./cmd/gendto` after DTO changes.

---

## Risks / re-verify before coding

- Merge may have moved `playableLibrarySrc` and deleted `/library` tests.
  Re-read owned popup wiring, `trackedVideoHandler`, `SeasonEpisode`,
  `SeasonsPanel`.
- Enriching every episode on `GET .../seasons` is fine for normal shows;
  stills may need lazy `discover/detail` for long-running series.
- Header `playSrc` is a single string today. Resume + next-episode means
  "URL of the next episode" (or a small object), not a series-level URL.
- Section lock: after merge, owned series lives under Discover. Movies
  tracked video skips Adult lock; series should match Movies.
- Rematch updates `library_series` identity; play URLs stay on series id
  + `episodeId`, not TMDB id.
- Replace + play on the same popup: do not nest the grab grid in a second
  modal.

---

## Verify when built (not now)

- `CGO_ENABLED=0 go build ./...` and the series tracked-video tests
- Frontend: Discover In library + owned search hit show Play Show and
  episode Play; catalog-only series has no Play; `SeasonEpisodePicker`
  grab tests stay green; Replace on an episode does not break play
- Browser: owned series with a playable mp4 — header Play Show and a row
  Play use the Movies player; a catalog-only series still has no Play

---

## Related

- Movies play: `frontend/src/components/TrackedPlayback.tsx`,
  `internal/api/tracked.go` (`serveMoviesTrackedVideo`)
- Seasons API: `frontend/src/api/seasons.ts`, `internal/api/airdatemonitor.go`
- Upgrade-on-import: `internal/api/import.go`
- Rematch UI: `frontend/src/screens/SearchTakeover.tsx`
- Roadmap pointer: `docs/ROADMAP.md` → "Series episode in-app playback"
- Distinct from: `.omc/specs/deep-interview-streaming-media-player.md`
