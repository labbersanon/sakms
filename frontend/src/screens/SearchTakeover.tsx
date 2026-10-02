// SearchTakeover — the ONE full-page search surface shared by all three
// re-target entry points: Rename's Re-pick, Rename's Move-to-another-section,
// and Dedup's Move-to-another-section. It replaces RepickPanel (Rename.tsx)
// and MoveModePanel.tsx, whose search / results / error / season-episode logic
// was already duplicated line-for-line between them.
//
// It is NOT a modal. The root is a plain <section aria-label="Search">; there
// is no role="dialog", no `fixed inset-0`, and `Modal` is never imported. The
// caller hides its own list and renders this in its place (see
// .omc/plans/autopilot-impl.md §5.0).
//
// FIVE THINGS A FUTURE SESSION MUST NOT "SIMPLIFY" AWAY.
//
// 1. NEVER SEND `episodeNumber: 0`. Both commit endpoints reject it outright:
//    `POST /api/proposals/{id}/repick`    -> internal/api/proposals.go:944
//    `POST /api/proposals/{id}/move-mode` -> internal/api/proposals_movemode.go:154
//    both validate `*req.SeasonNumber < 0 || *req.EpisodeNumber < 1` and 400.
//    SeasonEpisodePicker emits `episode: 0` from TWO places — its "Whole
//    season" tile, and its degraded FreeTextPicker when the Episode box is left
//    blank. A repick/move proposal is ONE FILE OCCUPYING ONE EPISODE SLOT, so
//    "whole season" has no meaning here; this component maps `episode === 0` to
//    a SHOW-LEVEL COMMIT WITH BOTH SLOT FIELDS OMITTED (documented deviation
//    D-1). That payload is byte-identical to today's "leave both blank" repick,
//    so nothing is lost. Restoring pass-through ships a silent 400.
//    Season 0 is NOT collapsed: {season: 0, episode: 3} (Specials E3) still
//    ships a literal `seasonNumber: 0` on the wire. Only `episode === 0` maps.
//
// 2. "Use show-level match only" IS NOT A REDUNDANT DUPLICATE of the
//    whole-season tile (D-5). Leaving season+episode blank is the DEFAULT and
//    most common Series repick path today, and SeasonEpisodePicker structurally
//    cannot express it — it always requires drilling into a season first. The
//    tile is two clicks deeper and is unreachable entirely when the seasons
//    fetch soft-fails to a zero-length list. Removing this button removes the
//    workflow, which is a regression on shipped behaviour, not a UI change.
//
// 3. THE COMMIT IS A CALLBACK, NOT A `mode: "repick" | "move"` UNION. The two
//    use cases share 100% of the search, grid, error handling and season/episode
//    UI, and 0% of the commit: different endpoint, different DTO
//    (RepickRequest vs MoveModeRequest), different required fields. Branching a
//    shared component on a mode union would be exactly the premature
//    abstraction this repo's CLAUDE.md names as its failure mode. Each caller
//    supplies a short sibling adapter that builds its own DTO instead.
//
// 4. THE RESULTS BLOCK IS WRAPPED IN `<Show when={!results.error}>` ON PURPOSE.
//    A Solid resource RE-THROWS on read once its fetcher has errored (by
//    design, for ErrorBoundary integration), so a SIBLING <Show> whose body
//    calls catalogItems()/adultItems()/adultSoftErrors() would throw mid-render
//    while .error is set. That is the shipped GrabDialog bug class CLAUDE.md
//    documents, and it is latent in RepickPanel right now (Rename.tsx:376-383).
//    The outer guard is what fixes it. Do not flatten it into siblings.
//
// 5. THE FREE-TEXT-FALLBACK NOTICE IS UNCONDITIONAL IN SERIES STEP 2, and that
//    is a deliberate deviation from the plan's "render it only in the degraded
//    state" wording. The picker's degraded state is NOT OBSERVABLE from here:
//    it self-fetches, exposes only onSubmit, and scoping the notice would
//    require either modifying SeasonEpisodePicker (spec Non-Goal 3) or
//    duplicating its fetchTitleDetail(...,"seasons") request (rejected in the
//    plan's §4.2). The notice exists because FreeTextPicker coerces a blank
//    Episode to 0, which rule 1 maps to a show-level commit — silently
//    discarding a season the operator explicitly typed. Surfacing that is worth
//    one always-on line. If SeasonEpisodePicker ever gains an `onDegraded`
//    callback, this can be scoped; until then do not add absence assertions.
//    APPENDED 2026-08-09: Series step 2 now mounts SeasonEpisodeAccordion, not
//    SeasonEpisodePicker (a new sibling component built precisely so that
//    Non-Goal-3 file stays untouched — see SeasonEpisodeAccordion.tsx's header).
//    Rules 1, 2 and 5 all carry over to it UNCHANGED: it emits `episode: 0`
//    from the same two places, it still cannot express "leave both blank", and
//    its degraded state is still not observable from here. The accordion COULD
//    now grow an `onDegraded` callback without touching a protected file — but
//    adding one is out of scope (spec Non-Goal: "no changes to selection/confirm
//    logic"), so the notice stays unconditional and absence assertions stay
//    forbidden.
//
// APPENDED 2026-10-01: a Series result tile click DRILLS INTO EPISODES
// (step 2). Show-level commit is the "Use show-level match only" control.
// Movie tiles in a Series merge still commit on click. TVDB episode hits
// with presetSlot still one-click commit the slot.

import {
  type Component,
  type JSX,
  createResource,
  createSignal,
  createUniqueId,
  For,
  Show,
} from "solid-js";
import type {
  AdultSceneCandidate,
  DiscoverItem,
  SeriesSearchItem,
} from "@dto";
import { type Mode, proxyImage, tmdbPoster } from "../api/discover";
import { adultSceneSearch, adultSceneResolve, tmdbSearch, tvdbSearch, type AdultSearchOpts, type CatalogSearchOpts } from "../api/rename";
import { SectionLockedError } from "../api/client";
import { ADULT_CONTENT_SECTION, sectionLabel } from "../api/sectionLock";
import { Button, ErrorText, Muted, yearOf, inputClass, labelClass } from "../components/ui";
import { MediaFallbackTile } from "../components/media";
import { SeasonEpisodeAccordion } from "./discover/SeasonEpisodeAccordion";

// Duplicated from SeasonEpisodePicker.tsx:69/71-72 rather than imported.
// Exporting them from that file would be a modification of a Non-Goal-3
// component for a two-string win; a duplicated Tailwind class literal is the
// cheaper and more honest call under this repo's no-premature-abstraction rule.
const GRID_CLASS = "grid grid-cols-3 gap-2 sm:grid-cols-4";
const TILE_CLASS =
  "cursor-pointer overflow-hidden rounded border border-border text-left transition hover:border-accent";

function isAdultResolveURL(q: string): boolean {
  const t = q.trim();
  if (/^https?:\/\//i.test(t)) {
    return true;
  }
  return /(?:^|\/\/)(?:www\.)?(stashdb\.org|fansdb\.cc|theporndb\.net)\//i.test(t);
}

// searchLockMessage is the SECTION-AGNOSTIC copy for a search 403. It is one
// shared handler for both the Movies/Series branch (tmdb-search, classified
// under Discover) and the Adult branch (scene-search) — a Discover-locked
// install can hit this on a Movies/Series target search too, so the copy must
// never hardcode "Adult". The section name comes off the thrown
// SectionLockedError (api()/client.ts already parses the {code,section} body
// into it), falling back to a neutral message when the name is absent.
function searchLockMessage(err: SectionLockedError): string {
  const label = err.section ? sectionLabel(err.section) : "";
  return label
    ? `${label} is PIN-locked — unlock it to search`
    : "This section is PIN-locked";
}

// CatalogHit tags a search result with WHICH CATALOG IT CAME OUT OF, mirroring
// Mainstream.tsx:95's `ModedTitle = { mode, item }` conceptually (duplicated,
// not imported — this is a screen, and it has no other reason to depend on
// Discover's Mainstream page).
//
// DiscoverItem DOES carry its own `mediaType` ("movie"/"tv", set server-side by
// tmdb.normalizeAll), so this wrapper looks redundant. It is not, for two
// reasons, and Mainstream carries the same wrapper against the same field:
//   - Vocabulary. `mediaType` is TMDB's classification; `mode` is SAK's, and
//     every routing decision in this app keys off SAK's ("movies"/"series",
//     the `Mode` values that select an endpoint). Reading the DTO field here
//     would mean translating two vocabularies at the one place that must not
//     get it wrong.
//   - Provenance, not classification. What the badge and the merge need to
//     record is which of the two `tmdb-search` calls produced this row. That
//     is a fact about the request, which no field on the response can be
//     trusted to restate.
type CatalogHit = {
  mode: "movies" | "series";
  item: DiscoverItem;
  // seriesTitle is the parent show when item.title is an episode name (TVDB
  // episode search). presetSlot enables one-click commit without step 2.
  seriesTitle?: string;
  presetSlot?: { season: number; episode: number };
  // tvdbId is the real TheTVDB series id on TVDB-backed hits. Anthology
  // shows use a negative synthetic tmdbId that cannot be reversed.
  tvdbId?: number;
};

type SearchResult =
  | { kind: "catalog"; items: CatalogHit[] }
  | { kind: "adult"; items: AdultSceneCandidate[]; errors?: string[] };

type CommitError = { adultLocked: boolean; message: string };

// TakeoverPick is what the operator chose. It is DELIBERATELY not a DTO: the
// takeover never knows whether it is feeding /repick or /move-mode. Season and
// episode are OPTIONAL and always travel as a PAIR or not at all (see rule 1
// above) — `episodeNumber` is never 0 here, because the whole-season tile maps
// to the no-slot shape before this type is constructed.
export type TakeoverPick =
  | {
      kind: "catalog";
      tmdbId: number;
      tvdbId?: number;
      title: string;
      year?: number;
      seasonNumber?: number; // present iff episodeNumber is present
      episodeNumber?: number; // always >= 1 when present
      episodeTitle?: string;
    }
  | {
      kind: "adult";
      title: string;
      box: string;
      sceneId: string;
      studio?: string;
      date?: string;
    };

// PickedShow is Series step 2's local state: the show whose season/episode grid
// is open. It deliberately does NOT touch the search resource, so "Change show"
// is free and issues no request.
// origin is informational only — NEVER branched on for commit/routing logic
// (see useCatalogItem's own comment for why). It exists solely to drive the
// step-2 advisory below for a movie-origin pick: the id is looked up against
// TMDB's TV catalog by SeasonEpisodeAccordion exactly like any other Series
// pick, and if it happens to collide with a real TV show's id (the two
// catalogs are independently numbered and can share an integer), the season
// list shown may belong to an unrelated show. See CLAUDE.md's note on this.
type PickedShow = {
  tmdbId: number;
  tvdbId?: number;
  title: string;
  year?: number;
  origin: "movies" | "series";
};

type SeriesDatabase = "tmdb" | "tvdb";

type AdvancedFields = {
  on: boolean;
  title: string;
  series: string;
  year: string;
  id: string;
  performer: string;
  studio: string;
};

function emptyAdvanced(): AdvancedFields {
  return {
    on: false,
    title: "",
    series: "",
    year: "",
    id: "",
    performer: "",
    studio: "",
  };
}

const EMPTY_ADVANCED: AdvancedFields = emptyAdvanced();

function parsePositiveInt(raw: string): number {
  const n = Number.parseInt(raw.trim(), 10);
  if (!Number.isFinite(n) || n <= 0) {
    return 0;
  }
  return n;
}

function catalogOpts(adv: AdvancedFields): CatalogSearchOpts | undefined {
  if (!adv.on) {
    return undefined;
  }
  const year = parsePositiveInt(adv.year);
  const id = parsePositiveInt(adv.id);
  const series = adv.series.trim();
  if (!year && !id && !series) {
    return undefined;
  }
  return {
    year: year || undefined,
    id: id || undefined,
    series: series || undefined,
  };
}

function adultOpts(adv: AdvancedFields): AdultSearchOpts | undefined {
  if (!adv.on) {
    return undefined;
  }
  const year = parsePositiveInt(adv.year);
  const performer = adv.performer.trim();
  const studio = adv.studio.trim();
  if (!year && !performer && !studio) {
    return undefined;
  }
  return {
    year: year || undefined,
    performer: performer || undefined,
    studio: studio || undefined,
  };
}

// Claude 2026-10-01: carry tvdbId on TVDB hits (anthology tmdbId is negative).
// Reason: AnthologyTMDBID is a hash; repick must persist the real TVDB id.
// Troubleshooting: Organize Search 400 "tmdbId and title are both required".
// Review if: TVDB search no longer emits synthetic tmdb ids.
function showPrefixAgrees(prefix: string, title: string): boolean {
  const p = prefix.trim().toLowerCase();
  const t = title.trim().toLowerCase();
  return t === p || t.startsWith(`${p} `) || p.startsWith(`${t} `);
}

// Claude 2026-10-01: peel an agreeing show prefix when TMDB gets show+episode.
// Reason: "Phineas and Ferb interview with a platypus" returns [] from
//   show-name search. Traefik 200 + 3-byte bodies; dest stayed S00E00.
//   Handy Manny also returned multiple series (737 bytes); requiring
//   length === 1 skipped the peel.
// Troubleshooting: Organize Search "No results" after typing the episode.
// Review if: TMDB grows an episode-title search used on this path.
async function showPrefixEpisodeHits(
  q: string,
  opts?: CatalogSearchOpts,
): Promise<CatalogHit[] | null> {
  const words = q.trim().split(/\s+/).filter(Boolean);
  if (words.length < 3) {
    return null;
  }
  for (let n = words.length - 1; n >= 2; n--) {
    const prefix = words.slice(0, n).join(" ");
    const residual = words.slice(n).join(" ");
    if (residual.length < 3) {
      continue;
    }
    const series = await tmdbSearch("series", prefix, opts);
    const show = series.find((item) => showPrefixAgrees(prefix, item.title));
    if (!show) {
      continue;
    }
    // Claude 2026-10-01: peel returns the show only — no TVDB episode tiles.
    // Reason: episode pick is step 2 + title/SxxExx filter, not a step-1 hit.
    // Troubleshooting: Advanced/peel episode tiles skipped the show filter.
    // Review if: Series Search lists episodes in step 1 again.
    // const episodes = await tvdbSearch(residual, "episode", {
    //   ...opts,
    //   series: show.title,
    // });
    // return [
    //   { mode: "series", item: show },
    //   ...episodes.map((item) => tvdbItemToHit(item)),
    // ];
    return [{ mode: "series", item: show }];
  }
  return null;
}

// Claude 2026-10-01: Advanced Series/Title queries retired.
// Reason: that path searched TMDB movies for episode titles and skipped
//   the show. Series Search is show-name only; filter after pick.
// Troubleshooting: Advanced Title "Ice cream team" hit a movie.
// Review if: Series Search grows a real episode-title catalog query.
// function advancedSeriesQueries(
//   q: string,
//   adv: AdvancedFields,
// ): { showQ: string; episodeQ: string } | null {
//   if (!adv.on) {
//     return null;
//   }
//   const title = adv.title.trim();
//   const series = adv.series.trim();
//   const box = q.trim();
//   if (!title && !series) {
//     return null;
//   }
//   if (series && title) {
//     return { showQ: series, episodeQ: title };
//   }
//   if (series) {
//     const episodeQ =
//       box && box.toLowerCase() !== series.toLowerCase() ? box : "";
//     return { showQ: series, episodeQ };
//   }
//   return { showQ: title, episodeQ: title };
// }

function looksLikeEpisodeFilter(raw: string): boolean {
  const s = raw.trim();
  if (!s) {
    return false;
  }
  const compact = s.replace(/\s+/g, "");
  if (/^S\d{1,2}E\d{1,3}$/i.test(compact) || /^\d{1,2}x\d{1,3}$/i.test(compact)) {
    return true;
  }
  const hexish = s.replace(/[\s\-_.]/g, "");
  if (/^[a-f0-9]{8,}$/i.test(hexish)) {
    return false;
  }
  return /[a-z]/i.test(s);
}

function residualEpisodeFilter(query: string, showTitle: string): string {
  const q = query.trim();
  const show = showTitle.trim();
  if (!q) {
    return "";
  }
  const stripExt = (s: string) => s.replace(/\.[a-z0-9]{2,4}$/i, "").trim();
  let leftover = "";
  if (!show) {
    leftover = stripExt(q);
  } else {
    const ql = q.toLowerCase();
    const sl = show.toLowerCase();
    if (ql === sl) {
      return "";
    }
    if (ql.startsWith(sl)) {
      leftover = stripExt(q.slice(show.length).replace(/^[\s\-_]+/, ""));
    } else {
      const idx = ql.indexOf(sl);
      leftover =
        idx >= 0
          ? stripExt(q.slice(idx + show.length).replace(/^[\s\-_]+/, ""))
          : stripExt(q);
    }
  }
  return looksLikeEpisodeFilter(leftover) ? leftover : "";
}

function tvdbItemToHit(item: SeriesSearchItem): CatalogHit {
  const presetSlot =
    item.seasonNumber != null && item.episodeNumber != null
      ? { season: item.seasonNumber, episode: item.episodeNumber }
      : undefined;
  return {
    mode: "series",
    item: {
      id: item.tmdbId,
      title: item.title,
      posterPath: "",
      overview: "",
      releaseDate: item.releaseDate ?? "",
      voteAverage: 0,
      mediaType: "tv",
    },
    seriesTitle: item.seriesTitle,
    presetSlot,
    tvdbId: item.tvdbId || undefined,
  };
}

export const SearchTakeover: Component<{
  // --- identity / copy -------------------------------------------------
  heading: string;
  // subheading renders under the heading: the repick caller's "Currently
  // matched: X (Y)" line, or nothing.
  subheading?: JSX.Element;
  // notes renders the caller's advisory block above the results — the move
  // callers' cross-device / dedup-scope / adult-phash copy, or nothing.
  notes?: JSX.Element;
  // preview renders the caller's OPTIONAL click-to-expand source-file preview,
  // between the subheading and the search form.
  //
  // A SLOT, not a proposal/URL prop, for one structural reason: Dedup's Move
  // entry point (Dedup.tsx) mounts this component for a proposal that has NO
  // SourcePath at all — only Candidates[]. A `previewSrc: string` prop would
  // invite that call site to pass one anyway; the backend then answers 400
  // ("candidateIndex is required for this proposal"), which surfaces as a
  // silent dead player — no console error, no failing test.
  // Dedup passes NOTHING here, deliberately — its card view already shows every
  // candidate's own always-visible tile (Dedup.tsx).
  preview?: JSX.Element;

  // --- search behaviour -------------------------------------------------
  // searchMode selects the ENDPOINT and the card shape:
  //   "movies" | "series" -> GET /api/modes/{m}/tmdb-search    -> poster cards
  //   "adult"             -> GET /api/modes/adult/scene-search -> still cards
  // For a move this is the TARGET mode; for a repick it is the proposal's own.
  searchMode: Mode;
  initialQuery: string;
  // initialSeriesDatabase seeds the Series-only database dropdown (TMDB vs TVDB).
  // Both search series names only. Episode title / SxxExx is a step-2 filter.
  initialSeriesDatabase?: SeriesDatabase;
  // autoSearch true seeds `submitted` from initialQuery, reproducing
  // RepickPanel's mount-time search; false starts empty, reproducing
  // MoveModePanel's deliberately-empty `submitted`. This difference is
  // LOAD-BEARING for the cancel-is-a-no-op guarantee and its tests: the two
  // move entry points MUST pass false, or mounting-and-cancelling fires a GET.
  autoSearch: boolean;

  // --- series slot ------------------------------------------------------
  // currentSlot is DISPLAY-ONLY context ("Currently: S2 E5"), replacing
  // RepickPanel's pre-filled number inputs. SeasonEpisodePicker in
  // selectionMode="single" has no pre-selection concept and adding one would
  // modify a Non-Goal-3 component, so the pre-fill's PURPOSE ("start from what
  // is already there") is preserved as read-only context instead (D-2).
  // APPENDED 2026-08-09: "DISPLAY-ONLY" is no longer literally true — it is
  // now ALSO handed to SeasonEpisodeAccordion, which expands the matching
  // season row on mount. D-2's actual constraint is intact: nothing is
  // pre-SELECTED, and no commit is pre-staged. The sentence about
  // SeasonEpisodePicker remains true of that (still untouched) component.
  currentSlot?: { season: number; episode: number } | null;

  // --- outcomes ---------------------------------------------------------
  // onCommit MUST reject on failure. The takeover catches, classifies, stays
  // mounted with the picked candidate still visible, and never calls onDone.
  // Resolve == committed.
  onCommit: (pick: TakeoverPick) => Promise<void>;
  // onDone fires only after onCommit resolves. onCancel is a STRUCTURAL no-op:
  // the takeover issues zero requests on that path.
  onDone: () => void;
  onCancel: () => void;
}> = (props) => {
  const [query, setQuery] = createSignal(props.initialQuery);
  const [seriesDatabase, setSeriesDatabase] = createSignal<SeriesDatabase>(
    props.initialSeriesDatabase ?? "tmdb",
  );
  // `submitted` / `submittedSeriesDatabase` start empty unless autoSearch asked
  // for a mount-time search. See the autoSearch prop doc: an eager fetch on a
  // move entry point would break the "Cancel issues zero requests" guarantee.
  const [submitted, setSubmitted] = createSignal(
    props.autoSearch ? props.initialQuery : "",
  );
  const [submittedSeriesDatabase, setSubmittedSeriesDatabase] = createSignal<
    SeriesDatabase
  >(props.autoSearch ? (props.initialSeriesDatabase ?? "tmdb") : "tmdb");
  const [advancedOpen, setAdvancedOpen] = createSignal(false);
  const [advTitle, setAdvTitle] = createSignal("");
  // Claude 2026-10-01: setAdvSeries retired with the Series Advanced field.
  // Reason: tsc TS6133 unused setter after Title/Series were hidden (#125).
  // Troubleshooting: #125 deploy rolled back at frontend `tsc --noEmit`.
  // Review if: Series Advanced grows a Series name field again.
  const [advSeries] = createSignal("");
  const [advYear, setAdvYear] = createSignal("");
  const [advId, setAdvId] = createSignal("");
  const [advPerformer, setAdvPerformer] = createSignal("");
  const [advStudio, setAdvStudio] = createSignal("");
  const [submittedAdv, setSubmittedAdv] = createSignal<AdvancedFields>(EMPTY_ADVANCED);

  const [results] = createResource(
    () => ({
      q: submitted(),
      seriesDatabase:
        props.searchMode === "series" ? submittedSeriesDatabase() : "tmdb",
      adv: submittedAdv(),
    }),
    async ({ q, seriesDatabase, adv }): Promise<SearchResult> => {
    // Solid only skips a fetcher for false/null/undefined — a key with an empty
    // query still RUNS the fetcher. This guard is what makes autoSearch={false}
    // issue zero network calls on mount while the resource still resolves.
    if (props.searchMode === "adult") {
      if (isAdultResolveURL(q)) {
        const res = await adultSceneResolve(q.trim());
        if (!res.item) {
          throw new Error(res.message || "Could not resolve that URL");
        }
        return {
          kind: "adult",
          items: [res.item],
        };
      }
      const title = adv.on && adv.title.trim() ? adv.title.trim() : q.trim();
      const opts = adultOpts(adv);
      if (!title && !opts?.performer && !opts?.studio) {
        return { kind: "adult", items: [] };
      }
      const res = await adultSceneSearch(title, opts);
      return { kind: "adult", items: res.items, errors: res.errors };
    }
    if (props.searchMode === "series" && seriesDatabase === "tvdb") {
      // Claude 2026-10-01: TVDB Search is show-name only (kind=series).
      // Reason: kind=episode from the box listed slots before the show pick;
      //   episode title / SxxExx is the step-2 filter now.
      // Troubleshooting: TVDB "Duck Soup" returned an episode tile, not the show.
      // Review if: Series Search lists episodes in step 1 again.
      const seriesQ = q.trim();
      const opts = catalogOpts(adv);
      if (!seriesQ && !opts?.id) {
        return { kind: "catalog", items: [] };
      }
      const seriesItems =
        seriesQ || opts?.id ? await tvdbSearch(seriesQ, "series", opts) : [];
      return {
        kind: "catalog",
        items: seriesItems.map((item) => tvdbItemToHit(item)),
      };
    }
    const tmdbQ =
      props.searchMode !== "series" && adv.on && adv.title.trim()
        ? adv.title.trim()
        : q.trim();
    const tmdbOpts = catalogOpts(adv);
    if (!tmdbQ && !tmdbOpts?.id) {
      return { kind: "catalog", items: [] };
    }
    // SERIES SEARCHES BOTH CATALOGS. The motivating case is a short film that
    // TMDB files under movies but the operator tracks in their Series library;
    // in Series mode it was previously unfindable here at all.
    //
    // THIS MERGE MUST STAY ON THE CLIENT — do not "simplify" it by teaching
    // GET /api/modes/series/tmdb-search to blend movies in server-side. That
    // endpoint is SHARED with Discover's Mainstream search bar
    // (api/discover.ts's fetchTmdbSearch), which ALREADY does its own
    // client-side dual-call merge (Mainstream.tsx:821-828). A series-mode
    // response that included movies would make Mainstream show every movie
    // TWICE — once from its own "movies" call, once from the now-blended
    // "series" one. The backend handler is deliberately left mode-gated.
    //
    // Movies-first, mirroring Mainstream.tsx:826-827's concatenation order, so
    // the two search UIs read the same way side by side. NO DE-DUPLICATION: a
    // title in both catalogs appears twice, once per badge — Mainstream's own
    // precedent, and the two rows are genuinely different tmdbIds.
    //
    // Deliberately NOT wrapped in try/catch. Mainstream catches only because it
    // feeds setSetupError to raise its setup modal; here a rejection is what
    // populates `results.error`, which the render already handles. The tradeoff
    // is real and accepted: a movies-catalog failure now fails a series search.
    if (props.searchMode === "series") {
      // Claude 2026-10-01: Series Search is show-name only.
      // Reason: Advanced Title searched TMDB movies and skipped the show;
      //   episode pick is step 2 + title/SxxExx filter.
      // Troubleshooting: Advanced Series+Title still lists a movie.
      // Review if: TMDB grows an episode-title search used on this path.
      if (tmdbOpts?.id) {
        const series = await tmdbSearch("series", tmdbQ, tmdbOpts);
        return {
          kind: "catalog",
          items: series.map((item) => ({ mode: "series" as const, item })),
        };
      }
      const [movies, series] = await Promise.all([
        tmdbSearch("movies", tmdbQ, tmdbOpts),
        tmdbSearch("series", tmdbQ, tmdbOpts),
      ]);
      const items = [
        ...movies.map((item) => ({ mode: "movies" as const, item })),
        ...series.map((item) => ({ mode: "series" as const, item })),
      ];
      // Claude 2026-10-01: TMDB empty → TVDB episode-title fallback.
      // Reason: TMDB search is show-name only. An episode title (typed, or
      //   left over from a dual-episode filename) returns []. TVDB kind=episode
      //   already scans tracked catalogs for that title.
      // Troubleshooting: Organize Search "No results" for Day of the Living
      //   Gelatin while Traefik shows 200 + 3-byte TMDB bodies.
      // Review if: TMDB grows an episode-title search used on this path.
      if (items.length === 0 && tmdbQ) {
        const peeled = await showPrefixEpisodeHits(tmdbQ, tmdbOpts);
        if (peeled && peeled.length > 0) {
          return { kind: "catalog", items: peeled };
        }
        // Claude 2026-10-01: do not TVDB-search the box as an episode title.
        // Reason: that listed slots before a show pick; filter is step 2.
        // Troubleshooting: "Day of the Living Gelatin" skipped the show.
        // Review if: Series Search lists episodes in step 1 again.
        // const episodeItems = await tvdbSearch(tmdbQ, "episode", tmdbOpts);
        // return {
        //   kind: "catalog",
        //   items: episodeItems.map((item) => tvdbItemToHit(item)),
        // };
      }
      return { kind: "catalog", items };
    }
    // Movies mode is UNCHANGED behaviourally — still exactly one call, no merge
    // and (see the render) no badge. It is wrapped in the same CatalogHit shape
    // only so the grid has one item type to render. `"movies" as const` rather
    // than a cast of props.searchMode: adult and series have both returned by
    // this line, so the literal is honest and a cast would silently admit
    // "adult" if that narrowing ever broke.
    const items = await tmdbSearch(props.searchMode, tmdbQ, tmdbOpts);
    return {
      kind: "catalog",
      items: items.map((item) => ({ mode: "movies" as const, item })),
    };
  },
  );

  const catalogItems = (): CatalogHit[] => {
    const r = results();
    return r && r.kind === "catalog" ? r.items : [];
  };
  const adultItems = (): AdultSceneCandidate[] => {
    const r = results();
    return r && r.kind === "adult" ? r.items : [];
  };
  const adultSoftErrors = (): string[] => {
    const r = results();
    return r && r.kind === "adult" ? (r.errors ?? []) : [];
  };

  const [picked, setPicked] = createSignal<PickedShow | null>(null);
  const [busy, setBusy] = createSignal(false);
  const [commitError, setCommitError] = createSignal<CommitError | null>(null);

  // commit is the single dispatch path for every tile, the show-level button
  // and the season/episode picker. It is a SEPARATE error branch from the
  // search-403 handler above — required, and distinct from it. A move from
  // Adult to Movies while Adult is PIN-locked has a SUCCEEDING search (Movies'
  // own TMDB catalog, which the Adult lock does not touch) and a FAILING commit
  // (the backend gates the commit itself for any Adult-touching move), so
  // reusing the search-403 "the search is blocked, not the move" copy here
  // would be actively wrong. On any error the takeover stays mounted and the
  // just-picked candidate stays visible/clickable — the results resource is
  // untouched by a commit failure, and `picked` is deliberately NOT cleared —
  // so unlock-and-retry is one click, and onDone() is never called, since
  // nothing was written.
  const commit = async (pick: TakeoverPick) => {
    setCommitError(null);
    setBusy(true);
    try {
      await props.onCommit(pick);
      props.onDone();
    } catch (e) {
      if (e instanceof SectionLockedError && e.section === ADULT_CONTENT_SECTION) {
        setCommitError({
          adultLocked: true,
          message: "Adult is PIN-locked — unlock to continue",
        });
      } else {
        setCommitError({ adultLocked: false, message: (e as Error).message });
      }
    } finally {
      setBusy(false);
    }
  };

  // showLevelCommit is the no-slot payload. It is the single producer for BOTH
  // the "Use show-level match only" button and the `episode === 0` mapping, so
  // the two provably cannot drift apart.
  const showLevelCommit = (show: PickedShow) =>
    void commit({
      kind: "catalog",
      tmdbId: show.tmdbId,
      tvdbId: show.tvdbId,
      title: show.title,
      year: show.year,
    });

  // Claude 2026-10-01: series tile click opens episode assignment.
  // Reason: Handy Manny / Phineas title clicks wrote S00E00 dest names.
  //   Show-level is the escape hatch in step 2, not the tile.
  // Troubleshooting: series Search applies a show with no episode.
  // Review if: dest preview can render a show-only Series name usefully.
  // Related files: SearchTakeover.test.tsx, Rename.test.tsx
  //
  // Slot assignment is openSeriesStep2. presetSlot still branches on
  // props.searchMode, not hit.mode: a TVDB episode hit one-click commits.
  const catalogShow = (
    item: DiscoverItem,
    origin: "movies" | "series",
    seriesTitle?: string,
    tvdbId?: number,
  ): PickedShow => ({
    tmdbId: item.id,
    tvdbId,
    // Claude 2026-10-01: empty seriesTitle must not win over item.title.
    // Reason: ?? keeps "" and dest becomes " SxxExx Episode.ext".
    // Troubleshooting: TVDB episode hits with blank seriesTitle dropped the show.
    // Review if: TVDB episode JSON always sends a non-empty seriesTitle.
    title: seriesTitle?.trim() || item.title,
    year: yearOf(item.releaseDate),
    origin,
  });

  const openSeriesStep2 = (
    item: DiscoverItem,
    origin: "movies" | "series",
    seriesTitle?: string,
    tvdbId?: number,
  ) => {
    setCommitError(null);
    setPicked(catalogShow(item, origin, seriesTitle, tvdbId));
  };

  const useCatalogItem = (
    item: DiscoverItem,
    origin: "movies" | "series",
    opts?: {
      presetSlot?: { season: number; episode: number };
      seriesTitle?: string;
      tvdbId?: number;
      episodeTitle?: string;
    },
  ) => {
    const show = catalogShow(item, origin, opts?.seriesTitle, opts?.tvdbId);
    if (props.searchMode === "series" && opts?.presetSlot) {
      commitSlot(
        show,
        opts.presetSlot.season,
        opts.presetSlot.episode,
        opts.episodeTitle,
      );
      return;
    }
    if (props.searchMode === "series" && origin === "series") {
      openSeriesStep2(item, origin, opts?.seriesTitle, opts?.tvdbId);
      return;
    }
    showLevelCommit(show);
  };

  const useAdultCandidate = (c: AdultSceneCandidate) =>
    void commit({
      kind: "adult",
      title: c.title,
      box: c.box,
      sceneId: c.sceneId,
      studio: c.studio,
      date: c.date,
    });

  // commitSlot IS D-1's mapping, and it is the only place it lives.
  //   episode === 0 -> show-level: NEITHER slot field is sent. Both endpoints
  //                    400 on episodeNumber < 1, and a whole-season concept
  //                    does not exist for a single-file repick/move.
  //   episode >= 1  -> the literal pair, `!= null` semantics, never truthiness.
  //                    season 0 (Specials) paired with a real episode ships a
  //                    literal 0 and is NOT collapsed by the rule above.
  const commitSlot = (
    show: PickedShow,
    season: number,
    episode: number,
    episodeTitle?: string,
  ) => {
    if (episode === 0) {
      showLevelCommit(show);
      return;
    }
    void commit({
      kind: "catalog",
      tmdbId: show.tmdbId,
      tvdbId: show.tvdbId,
      title: show.title,
      year: show.year,
      seasonNumber: season,
      episodeNumber: episode,
      episodeTitle,
    });
  };

  return (
    <section aria-label="Search" class="rounded-xl border border-border bg-surface p-4">
      <div class="flex items-center gap-3">
        <Button aria-label="Back to list" onClick={props.onCancel}>
          Back
        </Button>
        <h3 class="min-w-0 flex-1 truncate text-sm font-semibold text-fg">
          {props.heading}
        </h3>
      </div>
      <Show when={props.subheading}>
        <div class="mt-1">{props.subheading}</div>
      </Show>

      <Show when={props.preview}>
        <div class="mt-3">{props.preview}</div>
      </Show>

      <form
        class="mt-3 flex flex-col gap-2 sm:flex-row sm:flex-wrap sm:items-center"
        onSubmit={(e) => {
          e.preventDefault();
          setSubmitted(query());
          if (props.searchMode === "series") {
            setSubmittedSeriesDatabase(seriesDatabase());
          }
          setSubmittedAdv(
            advancedOpen()
              ? {
                  on: true,
                  title: advTitle(),
                  series: advSeries(),
                  year: advYear(),
                  id: advId(),
                  performer: advPerformer(),
                  studio: advStudio(),
                }
              : EMPTY_ADVANCED,
          );
        }}
      >
        <Show when={props.searchMode === "series"}>
          <select
            class="rounded-md border border-border bg-bg px-3 py-2 text-sm text-fg outline-none focus:border-accent"
            aria-label="Database"
            value={seriesDatabase()}
            onChange={(e) =>
              setSeriesDatabase(e.currentTarget.value as SeriesDatabase)
            }
          >
            <option value="tmdb">TMDB</option>
            <option value="tvdb">TVDB</option>
          </select>
        </Show>
        <input
          class="w-80 max-w-full rounded-md border border-border bg-bg px-3 py-2 text-sm text-fg outline-none focus:border-accent"
          value={query()}
          onInput={(e) => setQuery(e.currentTarget.value)}
          aria-label="Catalog search query"
          placeholder={
            props.searchMode === "adult"
              ? "Search text or paste URL"
              : props.searchMode === "series"
                ? "Series name"
                : undefined
          }
        />
        <Button type="submit">Search</Button>
        {/* Claude 2026-10-01: Advanced is a disclosure, not a second search page.
            Reason: title/series/year/id (and Adult performer/studio) are opt-in;
              autoSearch and Cancel-is-a-no-op still use only the main query
              until Search is clicked.
            Troubleshooting: TVDB shorts need a parent series or id; Adult
              scenes need actress/actor or studio when the title is generic.
            Review if: Movies/Series/Adult search grows a dedicated query DSL. */}
        <Button
          aria-expanded={advancedOpen()}
          aria-controls="rename-search-advanced"
          onClick={() => setAdvancedOpen((open) => !open)}
        >
          Advanced
        </Button>
      </form>
      <Show when={advancedOpen()}>
        <div
          id="rename-search-advanced"
          class="mt-2 grid grid-cols-1 gap-2 sm:grid-cols-2 lg:grid-cols-4"
        >
          <Show when={props.searchMode === "adult"}>
            <label class="block">
              <span class={labelClass}>Actress / actor</span>
              <input
                class={`${inputClass} mt-1`}
                value={advPerformer()}
                onInput={(e) => setAdvPerformer(e.currentTarget.value)}
                aria-label="Actress / actor"
              />
            </label>
          </Show>
          {/* Claude 2026-10-01: Series Advanced is Year + ID only.
              Reason: Title/Series searched movies and skipped the show;
                episode title / SxxExx is the step-2 filter after a show pick.
              Troubleshooting: Advanced Title "Ice cream team" hit a TMDB movie.
              Review if: Series Search grows a real episode-title catalog query. */}
          <Show when={props.searchMode !== "series"}>
          <label class="block">
            <span class={labelClass}>Title</span>
            <input
              class={`${inputClass} mt-1`}
              value={advTitle()}
              onInput={(e) => setAdvTitle(e.currentTarget.value)}
              aria-label="Title"
            />
          </label>
          </Show>
          {/* <Show when={props.searchMode === "series"}>
            <label class="block">
              <span class={labelClass}>Series</span>
              <input
                class={`${inputClass} mt-1`}
                value={advSeries()}
                onInput={(e) => setAdvSeries(e.currentTarget.value)}
                aria-label="Series"
              />
            </label>
          </Show> */}
          <Show when={props.searchMode === "adult"}>
            <label class="block">
              <span class={labelClass}>Studio</span>
              <input
                class={`${inputClass} mt-1`}
                value={advStudio()}
                onInput={(e) => setAdvStudio(e.currentTarget.value)}
                aria-label="Studio"
              />
            </label>
          </Show>
          <label class="block">
            <span class={labelClass}>Year</span>
            <input
              class={`${inputClass} mt-1`}
              value={advYear()}
              onInput={(e) => setAdvYear(e.currentTarget.value)}
              inputMode="numeric"
              aria-label="Year"
            />
          </label>
          <Show when={props.searchMode !== "adult"}>
            <label class="block">
              <span class={labelClass}>
                {props.searchMode === "series" && seriesDatabase() === "tvdb"
                  ? "TVDB ID"
                  : "TMDB ID"}
              </span>
              <input
                class={`${inputClass} mt-1`}
                value={advId()}
                onInput={(e) => setAdvId(e.currentTarget.value)}
                inputMode="numeric"
                aria-label={
                  props.searchMode === "series" && seriesDatabase() === "tvdb"
                    ? "TVDB ID"
                    : "TMDB ID"
                }
              />
            </label>
          </Show>
        </div>
      </Show>

      <Show when={props.notes}>
        <div class="mt-3 rounded-md border border-border bg-surface-2 p-3">
          {props.notes}
        </div>
      </Show>

      <Show when={commitError()}>
        <ErrorText>{commitError()!.message}</ErrorText>
      </Show>

      {/* Series step 2 — the show is chosen; assign a slot (or don't). */}
      <Show when={picked()}>
        {(show) => (
          <div class="mt-3 rounded-md border border-border bg-surface-2 p-3">
            <div class="flex items-center gap-3">
              <Button onClick={() => setPicked(null)}>Change show</Button>
              <span class="min-w-0 flex-1 truncate text-sm font-medium text-fg">
                {show().title}
                {show().year ? ` (${show().year})` : ""}
              </span>
            </div>
            {/* WARNING ONLY, NOT A GUARD — a deliberate, accepted-risk choice
                (see CLAUDE.md). TMDB's movie and TV catalogs are numbered
                independently and can share an id; the accordion below looks
                this id up against the TV catalog exactly like any other
                Series pick, so if it collides with a real show's id, the
                season/episode names shown belong to that unrelated show, not
                to this title. The chosen season/episode NUMBERS are still
                whatever the operator picks and are unaffected either way —
                this notice exists so a wrong-looking name is recognized as a
                warning sign rather than trusted. */}
            <Show when={show().origin === "movies"}>
              <ErrorText>
                This title came from TMDB's movie catalog — the season list
                below is looked up under the same id in the TV catalog and may
                belong to an unrelated show. If the episode names look wrong,
                prefer “Use show-level match only” below.
              </ErrorText>
            </Show>
            <Show when={props.currentSlot}>
              {(slot) => (
                <Muted class="mt-1">
                  Currently: S{slot().season} E{slot().episode}
                </Muted>
              )}
            </Show>
            {/* D-5. A SIBLING of the picker, above it — never merged into the
                whole-season tile. See rule 2 in the file header. */}
            <div class="mt-2">
              <Button
                variant="primary"
                disabled={busy()}
                onClick={() => showLevelCommit(show())}
              >
                Use show-level match only
              </Button>
            </div>
            {/* Unconditional by design — see rule 5 in the file header. */}
            <Muted class="mt-2">
              Enter a season <strong>and</strong> an episode to assign a specific
              slot. To re-point the match without changing the episode, use “Use
              show-level match only” above.
            </Muted>
            <div class="mt-2">
              {/* SELF-FETCH ONLY. The accordion has no caller-managed
                  `seasons`/`loading` pair at all — this is its one mount point
                  and it has always self-fetched here, so the prop pair would be
                  dead surface. `currentSlot` is passed IN ADDITION to the
                  read-only "Currently:" line above, not instead of it: here it
                  only expands the matching season row, which pre-selects
                  nothing (D-2's no-pre-fill reasoning is unaffected). */}
              <SeasonEpisodeAccordion
                tmdbId={show().tmdbId}
                currentSlot={props.currentSlot}
                initialEpisodeFilter={residualEpisodeFilter(
                  submitted(),
                  show().title,
                )}
                onSubmit={(s, e, episodeTitle) =>
                  commitSlot(show(), s, e, episodeTitle)
                }
              />
            </div>
          </div>
        )}
      </Show>

      {/* Step 1 — the search result grid. Hidden once a show is picked. */}
      <Show when={!picked()}>
        <div class="mt-3">
          <Show when={results.error}>
            <ErrorText>
              {results.error instanceof SectionLockedError
                ? searchLockMessage(results.error as SectionLockedError)
                : (results.error as Error)?.message}
            </ErrorText>
          </Show>
          {/* Guarded on `!results.error`, and every branch below it too: Solid
              resources RE-THROW on a `results()` read once the fetcher has
              errored (by design, for ErrorBoundary integration) — calling
              catalogItems()/adultItems()/adultSoftErrors() (which all invoke
              results()) while an error is set would throw mid-render. Same bug
              class as the GrabDialog incident documented in CLAUDE.md: sibling
              <Show> blocks let the success branch's resource read execute even
              while .error was set. */}
          <Show when={!results.error}>
            <Show when={adultSoftErrors().length > 0}>
              <Muted>
                Some sources could not be searched: {adultSoftErrors().join(", ")}
              </Muted>
            </Show>
            <Show when={!results.loading} fallback={<Muted>Searching…</Muted>}>
              <Show when={props.searchMode !== "adult"}>
                <Show
                  when={catalogItems().length > 0}
                  fallback={<Muted>No results.</Muted>}
                >
                  <div class={GRID_CLASS}>
                    <For each={catalogItems()}>
                      {(hit) => {
                        // `item` is the DiscoverItem; every line below reads
                        // from it exactly as before. `hit.mode` feeds the
                        // badge (Series mode only), aria-describedby, and
                        // useCatalogItem: series-origin drills into step 2;
                        // movie-origin still commits on title click.
                        const item = hit.item;
                        const src = () => tmdbPoster(item.posterPath);
                        const y = () => yearOf(item.releaseDate);
                        const badgeLabel = hit.mode === "movies" ? "Movie" : "Series";
                        const seriesLine = () => hit.seriesTitle;
                        // aria-describedby, NOT aria-label. An explicit
                        // aria-label overrides ALL descendant content for the
                        // accessible NAME, so folding the badge into the label
                        // would (a) still leave the visible badge unannounced
                        // and (b) change every tile's accessible name — this
                        // file's aria-label is `Use ${title}` and is queried
                        // by `getByLabelText` throughout this suite and its
                        // siblings (Rename/Dedup tests), so changing it would
                        // ripple across dozens of unrelated assertions for no
                        // reason: describedby adds the badge as supplementary
                        // description (announced after the name) without
                        // touching the name at all. Only wired in series mode,
                        // matching the badge's own visibility.
                        const badgeId = createUniqueId();
                        return (
                          <div class="flex flex-col">
                          <button
                            type="button"
                            class={TILE_CLASS}
                            aria-label={`Use ${item.title}`}
                            aria-describedby={
                              props.searchMode === "series" ? badgeId : undefined
                            }
                            disabled={busy()}
                            onClick={() =>
                              useCatalogItem(item, hit.mode, {
                                presetSlot: hit.presetSlot,
                                seriesTitle: hit.seriesTitle,
                                tvdbId: hit.tvdbId,
                                episodeTitle: hit.presetSlot
                                  ? item.title
                                  : undefined,
                              })
                            }
                          >
                            <div class="aspect-[2/3] w-full">
                              <Show
                                when={src()}
                                fallback={<MediaFallbackTile title={item.title} />}
                              >
                                <img
                                  src={src()}
                                  alt={item.title}
                                  class="h-full w-full object-cover"
                                  loading="lazy"
                                />
                              </Show>
                            </div>
                            <div class="p-1.5">
                              <div class="truncate text-xs font-medium text-fg">
                                {item.title}
                              </div>
                              <div class="text-[11px] text-muted">
                                <Show when={seriesLine()}>
                                  {(name) => (
                                    <>
                                      <span class="truncate">{name()}</span>
                                      {" "}
                                    </>
                                  )}
                                </Show>
                                <Show when={hit.presetSlot}>
                                  {(slot) => (
                                    <>
                                      S{slot().season} E{slot().episode}
                                      {" "}
                                    </>
                                  )}
                                </Show>
                                <Show when={y()}>{(yy) => <>{yy()}</>}</Show>
                                {/* SERIES-MODE ONLY. A movies-mode search
                                    returns one catalog, so every row would
                                    carry an identical "Movie" badge — pure
                                    clutter. Deliberately diverges from
                                    Mainstream, which merges without any badge:
                                    a wrong pick there costs a re-search, but
                                    here it renames or moves a real file. Same
                                    class literal as the Adult card's `box`
                                    badge below, not a new badge style. */}
                                <Show when={props.searchMode === "series"}>
                                  {" "}
                                  <span
                                    id={badgeId}
                                    class="rounded bg-surface px-1 py-0.5 text-xs uppercase text-muted"
                                  >
                                    {badgeLabel}
                                  </span>
                                </Show>
                              </div>
                            </div>
                          </button>
                          <Show when={props.searchMode === "series" && !hit.presetSlot}>
                            <button
                              type="button"
                              class="mt-1 rounded border border-border px-1.5 py-1 text-[11px] text-fg hover:border-accent disabled:opacity-50"
                              aria-label={`Assign episode for ${item.title}`}
                              disabled={busy()}
                              onClick={() =>
                                openSeriesStep2(
                                  item,
                                  hit.mode,
                                  hit.seriesTitle,
                                  hit.tvdbId,
                                )
                              }
                            >
                              Assign episode
                            </button>
                          </Show>
                          </div>
                        );
                      }}
                    </For>
                  </div>
                </Show>
              </Show>
              <Show when={props.searchMode === "adult"}>
                <Show
                  when={adultItems().length > 0}
                  fallback={<Muted>No results.</Muted>}
                >
                  <div class={GRID_CLASS}>
                    <For each={adultItems()}>
                      {(c) => {
                        // proxyImage, NOT tmdbPoster: imageUrl is a FULL URL,
                        // not a TMDB path. It is omitempty, so `?? ""` — and
                        // proxyImage("") returns "", which routes to MediaFallbackTile.
                        const src = () => proxyImage(c.imageUrl ?? "");
                        return (
                          <button
                            type="button"
                            class={TILE_CLASS}
                            aria-label={`Use ${c.title}`}
                            disabled={busy()}
                            onClick={() => useAdultCandidate(c)}
                          >
                            <div class="aspect-video w-full">
                              <Show
                                when={src()}
                                fallback={<MediaFallbackTile title={c.title} />}
                              >
                                <img
                                  src={src()}
                                  alt={c.title}
                                  class="h-full w-full object-cover"
                                  loading="lazy"
                                />
                              </Show>
                            </div>
                            <div class="p-1.5">
                              <div class="truncate text-xs font-medium text-fg">
                                {c.title}
                              </div>
                              <div class="text-[11px] text-muted">
                                {c.studio ?? "Unknown studio"}
                                {c.date ? ` — ${c.date}` : ""}
                                {" — "}
                                <span class="rounded bg-surface px-1 py-0.5 text-xs uppercase text-muted">
                                  {c.box}
                                </span>
                              </div>
                            </div>
                          </button>
                        );
                      }}
                    </For>
                  </div>
                </Show>
              </Show>
            </Show>
          </Show>
        </div>
      </Show>
    </section>
  );
};
