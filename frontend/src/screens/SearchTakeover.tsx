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
import { adultSceneSearch, adultSceneResolve, tmdbSearch, tvdbSearch } from "../api/rename";
import { SectionLockedError } from "../api/client";
import { ADULT_CONTENT_SECTION, sectionLabel } from "../api/sectionLock";
import { Button, ErrorText, Muted, yearOf } from "../components/ui";
import { SeasonEpisodeAccordion } from "./discover/SeasonEpisodeAccordion";
import { TextPoster } from "./discover/shared";

// Duplicated from SeasonEpisodePicker.tsx:69/71-72 rather than imported.
// Exporting them from that file would be a modification of a Non-Goal-3
// component for a two-string win; a duplicated Tailwind class literal is the
// cheaper and more honest call under this repo's no-premature-abstraction rule.
const GRID_CLASS = "grid grid-cols-3 gap-2 sm:grid-cols-4";
const TILE_CLASS =
  "overflow-hidden rounded border border-border text-left transition hover:border-accent";

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
      title: string;
      year?: number;
      seasonNumber?: number; // present iff episodeNumber is present
      episodeNumber?: number; // always >= 1 when present
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
  title: string;
  year?: number;
  origin: "movies" | "series";
};

type SeriesDatabase = "tmdb" | "tvdb";

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
  // TMDB searches series names; TVDB searches episode titles — one field, hint
  // follows the database choice.
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

  const [results] = createResource(
    () => ({
      q: submitted(),
      seriesDatabase:
        props.searchMode === "series" ? submittedSeriesDatabase() : "tmdb",
    }),
    async ({ q, seriesDatabase }): Promise<SearchResult> => {
    // Solid only skips a fetcher for false/null/undefined — a key with an empty
    // query still RUNS the fetcher. This guard is what makes autoSearch={false}
    // issue zero network calls on mount while the resource still resolves.
    if (props.searchMode === "adult") {
      if (!q.trim()) {
        return { kind: "adult", items: [] };
      }
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
      const res = await adultSceneSearch(q);
      return { kind: "adult", items: res.items, errors: res.errors };
    }
    if (props.searchMode === "series" && seriesDatabase === "tvdb") {
      const tvdbQ = q.trim();
      if (!tvdbQ) {
        return { kind: "catalog", items: [] };
      }
      const items = await tvdbSearch(tvdbQ, "episode");
      return {
        kind: "catalog",
        items: items.map((item) => tvdbItemToHit(item)),
      };
    }
    const tmdbQ = q.trim();
    if (!tmdbQ) {
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
      const [movies, series] = await Promise.all([
        tmdbSearch("movies", tmdbQ),
        tmdbSearch("series", tmdbQ),
      ]);
      return {
        kind: "catalog",
        items: [
          ...movies.map((item) => ({ mode: "movies" as const, item })),
          ...series.map((item) => ({ mode: "series" as const, item })),
        ],
      };
    }
    // Movies mode is UNCHANGED behaviourally — still exactly one call, no merge
    // and (see the render) no badge. It is wrapped in the same CatalogHit shape
    // only so the grid has one item type to render. `"movies" as const` rather
    // than a cast of props.searchMode: adult and series have both returned by
    // this line, so the literal is honest and a cast would silently admit
    // "adult" if that narrowing ever broke.
    const items = await tmdbSearch(props.searchMode, tmdbQ);
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
      title: show.title,
      year: show.year,
    });

  // useCatalogItem: Movies/Adult commit on a single click (today's behaviour);
  // Series drills into step 2 instead.
  //
  // ROUTING BRANCHES ON props.searchMode, NOT ON hit.mode — deliberately, and
  // it needed no change when series search started merging in movie results.
  // A movie-origin pick in Series mode goes to step 2 exactly like a
  // series-origin one: the operator is placing a file into their SERIES
  // library, so it needs a season/episode slot regardless of which TMDB
  // catalog the id came from. Branching on `hit.mode` here would send a short
  // film straight to a show-level commit and silently skip the slot
  // assignment. `origin` IS still threaded through — onto `PickedShow`, not
  // into this routing decision — purely so step 2 can render the movie-origin
  // advisory below; see PickedShow's own doc comment.
  const useCatalogItem = (
    item: DiscoverItem,
    origin: "movies" | "series",
    opts?: { presetSlot?: { season: number; episode: number }; seriesTitle?: string },
  ) => {
    const showTitle = opts?.seriesTitle ?? item.title;
    const show: PickedShow = {
      tmdbId: item.id,
      title: showTitle,
      year: yearOf(item.releaseDate),
      origin,
    };
    if (props.searchMode === "series") {
      if (opts?.presetSlot) {
        commitSlot(show, opts.presetSlot.season, opts.presetSlot.episode);
        return;
      }
      setCommitError(null);
      setPicked(show);
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
  const commitSlot = (show: PickedShow, season: number, episode: number) => {
    if (episode === 0) {
      showLevelCommit(show);
      return;
    }
    void commit({
      kind: "catalog",
      tmdbId: show.tmdbId,
      title: show.title,
      year: show.year,
      seasonNumber: season,
      episodeNumber: episode,
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
                ? seriesDatabase() === "tmdb"
                  ? "Series name"
                  : "Episode name"
                : undefined
          }
        />
        <Button type="submit">Search</Button>
      </form>

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
                onSubmit={(s, e) => commitSlot(show(), s, e)}
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
                        // from it exactly as before. `hit.mode` also feeds the
                        // badge below (Series mode only) and its
                        // aria-describedby wiring — never used for routing,
                        // see useCatalogItem's own comment.
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
                              })
                            }
                          >
                            <div class="aspect-[2/3] w-full">
                              <Show
                                when={src()}
                                fallback={<TextPoster label={item.title} />}
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
                        // proxyImage("") returns "", which routes to TextPoster.
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
                                fallback={<TextPoster label={c.title} />}
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
