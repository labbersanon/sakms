// SeasonEpisodePicker — the two-level poster grid that gates a Series grab: no
// release can be scored until a specific season (or a specific episode) is
// chosen. Replaces the two free-text S/E inputs that used to live in
// Mainstream.tsx, which survive here as state 4 (see below).
//
// GRID, NOT CAROUSEL — deliberate. Every other Discover row in this codebase is
// a horizontal arrow-navigated Carousel (components/Carousel.tsx); both levels
// of this picker are static CSS grids instead, because a picker is a bounded
// set the operator scans at once, not an endless browse rail. Do NOT "fix" this
// inconsistency by reaching for Carousel.
//
// FOUR STATES, in this precedence order — the ordering is load-bearing:
//   1. tmdbId <= 0    -> state 4 directly, WITHOUT ever attempting a lookup.
//                        Two call sites (LibraryCard, TraktWatchlistRow) can
//                        legitimately produce a 0 id, and a picker that waited
//                        for a doomed request would hang on a title that can
//                        never enumerate. "No enumeration possible" does not
//                        become possible by waiting, so this precedes loading.
//   2. loading         -> skeleton tiles.
//   3. seasons present -> the season grid, drilling into the episode grid.
//   4. otherwise       -> the degraded free-text S/E fallback.
//
// STATE 4 IS NOT OPTIONAL. Without it, a TMDB outage (or an unconfigured TMDB
// connection) would remove ALL Series grabbing capability at every call site
// simultaneously — a hard regression on shipped behavior. The markup is the
// previously-shipped free-text picker, aria-labels included, so the fallback IS
// today's behavior rather than a new one.
//
// SELECTION MODES. "single" submits one pick via onSubmit (episode 0 means the
// whole season); "multi" toggles picks against the caller's shared selection
// store via isSelected/onToggle. Navigation is IDENTICAL in both modes — a
// season tile drills in, and the episode grid's leading "Whole season" tile is
// what expresses episode 0. Multi mode needs the same drill-down because
// episode-level bulk selection is the whole point of the granularity; one
// component rather than two because the grids, drill-down, states and fallback
// are ~90% shared and only a tile click differs.
//
// TWO DATA MODES. A caller that already holds the detail bundle (DetailPopup)
// passes `seasons` and drives `loading` itself; a caller that holds only a
// tmdbId omits `seasons` entirely and the component fetches for itself, with
// ?sections=seasons so the server skips the credits/keywords/providers/
// recommendations fan-out. Omission — not the prop's VALUE — is what selects
// the mode; see selfFetching below for why that distinction is load-bearing.
//
// Season ordering is the API's decision (ascending by seasonNumber, Specials
// first) and is deliberately not re-sorted here in either mode.

import { type Component, For, Show, createResource, createSignal } from "solid-js";
import type { SeasonSummary } from "@dto";
import { fetchTitleDetail, tmdbPoster, tmdbStill } from "../../api/discover";
import { Button, Muted, yearOf } from "../../components/ui";
import { MediaFallbackTile } from "../../components/media";

// FE-1a declared local PickerEpisode/PickerSeason shapes mirroring the (then
// unbuilt) apidto types field-for-field; FE-1b swapped them for the generated
// ones. The swap needed no field edits — the generated SeasonSummary /
// EpisodeSummary match name-for-name, which was the point of mirroring them.
// The two documented nuances the local types carried live on in the generated
// DTO's own doc comments: posterPath/stillPath are "" when TMDB has no art (a
// normal per-item condition, not an error), and `episodes` is EMPTY — not
// absent — for a season whose per-season fetch soft-failed, so the season tile
// renders either way and drilling in shows the empty-season notice.

// EpisodePick identifies one selectable thing. episode === 0 means "the whole
// season", preserving the shipped grab semantic (S4 with no E suffix).
export type EpisodePick = { season: number; episode: number };

// GRID_CLASS is shared by both levels so a drill-down doesn't reflow the modal.
const GRID_CLASS = "grid grid-cols-3 gap-2 sm:grid-cols-4";

const TILE_CLASS =
  "overflow-hidden rounded border text-left transition hover:border-accent";

// seasonLabel prefers TMDB's own season name ("Specials", "Season 4") and falls
// back to a synthesized one when TMDB has none.
const seasonLabel = (s: SeasonSummary): string =>
  s.name || (s.seasonNumber === 0 ? "Specials" : `Season ${s.seasonNumber}`);

// FreeTextPicker is the degraded fallback (state 4) — the previously-shipped
// two-input picker, verbatim including its aria-labels and "Go" submit, so a
// TMDB-less install keeps exactly the grabbing capability it has today.
//
// SEASON IS REQUIRED; EPISODE IS NOT. The asymmetry is the whole point, so do
// not "tidy" it into one shared rule:
//   - A blank Episode legitimately means "the whole season" — the shipped
//     `S4`-with-no-`E`-suffix semantic. 0 is its real, correct value.
//   - A blank Season has no such meaning. It used to coerce to 0 via
//     `parseInt("", 10) || 0`, which is Season 0 — SPECIALS, a real,
//     dispatchable season that is a legitimate grid tile elsewhere in this
//     picker. So an empty-form submit staged a genuine Specials grab that was
//     indistinguishable from an operator deliberately choosing Specials.
//
// The pre-rewrite bulk-select code guarded this with `if (season <= 0) return`.
// That guard CANNOT be restored verbatim: it would also reject a deliberate
// Specials pick, which is exactly what the season grid now offers. Validating
// the INPUT (did they type a parseable number?) rather than the VALUE (is it
// > 0?) is what separates "typed nothing" from "chose Specials" — a distinction
// a bare number can never carry, the same reasoning behind grabs.Grab's
// SeasonSpecified bool.
//
// Claude 2026-08-02, Phase-4 review FIX 1 (architect F1 + code-reviewer).
// Troubleshooting: empty-form submit silently staged a Season 0 selection.
// Review if: the free-text fallback is ever removed (see the file header for
// why it must not be).
const FreeTextPicker: Component<{
  onSubmit: (season: number, episode: number) => void;
}> = (props) => {
  const [season, setSeason] = createSignal("");
  const [episode, setEpisode] = createSignal("");

  // parseNonNegativeInt returns null for anything that is not a bare
  // non-negative integer — "" and "  " included, which is the case that matters.
  // Deliberately NOT parseInt: that accepts "4abc" (→ 4) and "" (→ NaN, then
  // coerced to 0 by the `||` this replaces), both of which are how the bug got
  // in. A leading "+"/"-" or a decimal point is rejected outright rather than
  // truncated, so what is dispatched is always exactly what was typed.
  const parseNonNegativeInt = (raw: string): number | null => {
    const s = raw.trim();
    if (!/^\d+$/.test(s)) return null;
    return parseInt(s, 10);
  };

  const seasonValue = () => parseNonNegativeInt(season());
  const valid = () => seasonValue() !== null;

  return (
    <form
      class="mt-1 flex items-center gap-1"
      onSubmit={(e) => {
        e.preventDefault();
        // Guarded here as well as via the disabled button below, and the pair is
        // NOT redundant: a form submits on Enter from either input, which never
        // touches the button and would otherwise bypass a disabled-only guard.
        const s = seasonValue();
        if (s === null) return;
        props.onSubmit(s, parseNonNegativeInt(episode()) ?? 0);
      }}
    >
      <input
        class="w-12 rounded border border-border bg-bg px-1 py-0.5 text-xs text-fg outline-none focus:border-accent"
        placeholder="S"
        aria-label="Season"
        value={season()}
        onInput={(e) => setSeason(e.currentTarget.value)}
      />
      <input
        class="w-12 rounded border border-border bg-bg px-1 py-0.5 text-xs text-fg outline-none focus:border-accent"
        placeholder="E"
        aria-label="Episode"
        value={episode()}
        onInput={(e) => setEpisode(e.currentTarget.value)}
      />
      <button
        type="submit"
        disabled={!valid()}
        class="rounded bg-accent px-2 py-0.5 text-xs font-medium text-accent-fg disabled:cursor-not-allowed disabled:opacity-50"
      >
        Go
      </button>
    </form>
  );
};

export const SeasonEpisodePicker: Component<{
  tmdbId: number;
  selectionMode: "single" | "multi";
  // Pre-fetched season/episode data, for a caller that already has the detail
  // bundle (DetailPopup). Undefined means either "still loading" (see loading)
  // or "unavailable" — the two are indistinguishable from this prop alone,
  // which is exactly why loading exists.
  //
  // PASSING THIS PROP AT ALL — even as undefined — switches off self-fetching
  // (see selfFetching below).
  seasons?: SeasonSummary[];
  loading?: boolean;
  // single mode only. episode === 0 means "the whole season".
  onSubmit?: (season: number, episode: number) => void;
  // multi mode only.
  isSelected?: (p: EpisodePick) => boolean;
  onToggle?: (p: EpisodePick) => void;
}> = (props) => {
  // openSeason is the drill-down level: null = season grid, n = season n's
  // episode grid.
  const [openSeason, setOpenSeason] = createSignal<number | null>(null);

  const enumerable = () => props.tmdbId > 0;

  // selfFetching keys off whether the caller OMITTED `seasons`, not off its
  // value. That distinction is the whole point: DetailPopup passes
  // `seasons={detail()?.seasons}`, which is undefined for as long as its own
  // resource is in flight — a value-based check would fire a second, duplicate
  // request for the exact bundle the caller is already fetching, which the
  // optional prop exists to prevent. Presence is fixed for the life of the
  // mount (Solid defines a getter per prop the JSX actually passed), so this is
  // read once at setup and deliberately not reactive.
  const selfFetching = !("seasons" in props);

  // The self-fetch: one request for the season block only. Series is hardcoded
  // rather than taken as a prop because this picker only ever mounts on a
  // Series grab path — Movies have no seasons, and the DTO documents
  // TitleDetail.seasons as always empty for one.
  //
  // The fetcher NEVER rejects. A Solid resource re-throws on read after its
  // fetcher errors (by design, for ErrorBoundary integration), and that throw
  // lands mid-render — the exact shape of the shipped GrabDialog bug where the
  // dialog hung on "Searching and scoring releases…" forever (see CLAUDE.md).
  // Resolving to [] instead routes a failed fetch into state 4, the degraded
  // free-text fallback, which is what a failed enumeration is supposed to do.
  //
  // `?? []` is not defensive noise: TitleDetail.seasons is typed non-nullable,
  // but the handler serializes a nil slice as JSON null unless it is passed
  // through nonNilSlice, so tsc cannot catch this one.
  const [fetched] = createResource(
    () => (selfFetching && props.tmdbId > 0 ? props.tmdbId : false),
    (id) =>
      fetchTitleDetail("series", id, "seasons")
        .then((d) => d.seasons ?? [])
        .catch(() => [] as SeasonSummary[]),
  );

  const seasons = () =>
    (selfFetching ? fetched() : props.seasons) ?? [];
  // In self-fetching mode the component owns the request and therefore knows
  // its own loading state; a caller-managed mount reports its resource's state
  // through the prop instead (state 1 while it is in flight, state 4 only once
  // it has settled without seasons — the two are not the same and must not
  // collapse).
  const loading = () => (selfFetching ? fetched.loading : props.loading === true);

  const current = () =>
    seasons().find((s) => s.seasonNumber === openSeason());

  const selected = (p: EpisodePick) => props.isSelected?.(p) ?? false;

  // pick routes a tile activation to whichever mode's callback is live. One
  // call shape for both modes keeps the tiles free of mode branching.
  const pick = (p: EpisodePick) => {
    if (props.selectionMode === "multi") props.onToggle?.(p);
    else props.onSubmit?.(p.season, p.episode);
  };

  // tileClasses carries only the selection-dependent classes; TILE_CLASS itself
  // goes on the static class attribute (Solid merges the two).
  const tileClasses = (on: boolean) => ({
    "border-accent ring-1 ring-accent": on,
    "border-border": !on,
  });

  return (
    <Show
      when={enumerable()}
      fallback={
        <div>
          <Muted class="mb-2">
            Season list unavailable for this title — enter a season (and
            optionally an episode) instead.
          </Muted>
          <FreeTextPicker onSubmit={(s, e) => pick({ season: s, episode: e })} />
        </div>
      }
    >
      <Show
        when={!loading()}
        fallback={
          <div class={GRID_CLASS} aria-label="Loading seasons">
            <For each={[0, 1, 2, 3, 4, 5]}>
              {() => (
                <div class="h-28 animate-pulse rounded border border-border bg-surface-2" />
              )}
            </For>
          </div>
        }
      >
        <Show
          when={seasons().length > 0}
          fallback={
            <div>
              <Muted class="mb-2">
                Couldn't load seasons from TMDB — enter a season (and optionally
                an episode) instead.
              </Muted>
              <FreeTextPicker
                onSubmit={(s, e) => pick({ season: s, episode: e })}
              />
            </div>
          }
        >
          <Show
            when={current()}
            fallback={
              <div class={GRID_CLASS}>
                <For each={seasons()}>
                  {(s) => {
                    const src = () => tmdbPoster(s.posterPath);
                    const year = () => yearOf(s.airDate);
                    return (
                      <button
                        type="button"
                        class={TILE_CLASS}
                        classList={tileClasses(
                          selected({ season: s.seasonNumber, episode: 0 }),
                        )}
                        onClick={() => setOpenSeason(s.seasonNumber)}
                      >
                        <div class="aspect-[2/3] w-full">
                          <Show
                            when={src()}
                            fallback={<MediaFallbackTile title={seasonLabel(s)} />}
                          >
                            <img
                              src={src()}
                              alt={seasonLabel(s)}
                              class="h-full w-full object-cover"
                              loading="lazy"
                            />
                          </Show>
                        </div>
                        <div class="p-1.5">
                          <div class="truncate text-xs font-medium text-fg">
                            {seasonLabel(s)}
                          </div>
                          <div class="text-[11px] text-muted">
                            {s.episodeCount} ep{s.episodeCount === 1 ? "" : "s"}
                            <Show when={year()}>{(y) => <> · {y()}</>}</Show>
                          </div>
                        </div>
                      </button>
                    );
                  }}
                </For>
              </div>
            }
          >
            {(season) => (
              <div>
                <div class="mb-2 flex items-center gap-2">
                  <Button class="!py-1 text-xs" onClick={() => setOpenSeason(null)}>
                    Back to Seasons
                  </Button>
                  <span class="truncate text-sm font-medium text-fg">
                    {seasonLabel(season())}
                  </span>
                </div>
                <div class={GRID_CLASS}>
                  {/* The whole-season tile leads the grid and is what expresses
                      episode 0 — the shipped "S4 with no E suffix" semantic. */}
                  <button
                    type="button"
                    class={TILE_CLASS}
                    classList={tileClasses(
                      selected({ season: season().seasonNumber, episode: 0 }),
                    )}
                    onClick={() =>
                      pick({ season: season().seasonNumber, episode: 0 })
                    }
                  >
                    <div class="flex aspect-video w-full items-center justify-center bg-surface-2 p-2 text-center text-xs font-medium text-fg">
                      Whole season
                    </div>
                    <div class="p-1.5 text-[11px] text-muted">
                      {season().episodeCount} ep
                      {season().episodeCount === 1 ? "" : "s"}
                    </div>
                  </button>
                  <For
                    each={season().episodes}
                    fallback={
                      <Muted class="col-span-full text-xs">
                        No episode list available for this season.
                      </Muted>
                    }
                  >
                    {(ep) => {
                      const src = () => tmdbStill(ep.stillPath);
                      const label = () =>
                        `E${ep.episodeNumber}${ep.name ? " · " + ep.name : ""}`;
                      return (
                        <button
                          type="button"
                          class={TILE_CLASS}
                          classList={tileClasses(
                            selected({
                              season: season().seasonNumber,
                              episode: ep.episodeNumber,
                            }),
                          )}
                          onClick={() =>
                            pick({
                              season: season().seasonNumber,
                              episode: ep.episodeNumber,
                            })
                          }
                        >
                          {/* stillPath === "" renders NO <img> at all — the
                              title/number-only card is the mandated fallback. */}
                          <div class="aspect-video w-full">
                            <Show
                              when={src()}
                              fallback={<MediaFallbackTile title={label()} />}
                            >
                              <img
                                src={src()}
                                alt={label()}
                                class="h-full w-full object-cover"
                                loading="lazy"
                              />
                            </Show>
                          </div>
                          <div class="p-1.5">
                            <div class="truncate text-xs font-medium text-fg">
                              {label()}
                            </div>
                            <div class="text-[11px] text-muted">
                              <Show when={yearOf(ep.airDate)}>
                                {(y) => <>{y()}</>}
                              </Show>
                              <Show when={ep.runtime > 0}>
                                <> · {ep.runtime}m</>
                              </Show>
                            </div>
                          </div>
                        </button>
                      );
                    }}
                  </For>
                </div>
              </div>
            )}
          </Show>
        </Show>
      </Show>
    </Show>
  );
};
