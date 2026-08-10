// SeasonEpisodeAccordion — the collapsible season/episode picker used by ONE
// call site: SearchTakeover's Series step 2 (Rename's Re-pick and both
// Move-to-another-section entry points). Seasons are collapsible rows; an
// expanded season lists its episodes inline as compact text rows.
//
// A SIBLING OF SeasonEpisodePicker, NOT A MODE OF IT — and that is the whole
// design, not an accident. SeasonEpisodePicker is protected by an existing
// Non-Goal from .omc/specs/deep-interview-repick-move-fullpage-takeover.md:
// "No change to SeasonEpisodePicker's own internal behavior, its 5 existing
// mount points, its degraded fallback, or its 'Whole season' (episode: 0)
// semantics — it is consumed as-is, not modified." A `layout` prop on that
// component would have been the smaller diff and was rejected for exactly that
// reason (see .omc/specs/deep-interview-rename-episode-picker-collapsible.md's
// Round-3 reversal). So the pieces this file needs — seasonLabel, the degraded
// FreeTextPicker, the self-fetch — are DUPLICATED here rather than exported
// from there, the same precedent SearchTakeover.tsx's own GRID_CLASS/TILE_CLASS
// duplication set (D-6). SeasonEpisodePicker's six mount points are untouched
// and keep their poster grid.
//
// LIST ROWS, NOT TILES — deliberate, and the inverse of the "GRID, NOT
// CAROUSEL" note in SeasonEpisodePicker. There is no <img>, no still-image
// fetch and no aspect-ratio wrapper anywhere below: a repick operator is
// matching ONE already-downloaded file to ONE episode slot, scanning episode
// numbers and titles, not browsing art. Do NOT "restore" the stills.
//
// FOUR STATES, in this precedence order — the ordering is load-bearing and is
// carried over verbatim from SeasonEpisodePicker:
//   1. tmdbId <= 0    -> the degraded free-text fallback directly, WITHOUT ever
//                        attempting a lookup. "No enumeration possible" does
//                        not become possible by waiting, so this precedes
//                        loading.
//   2. loading         -> skeleton rows.
//   3. seasons present -> the accordion.
//   4. otherwise       -> the degraded free-text fallback.
//
// STATE 4 IS NOT OPTIONAL. Without it, a TMDB outage (or an unconfigured TMDB
// connection) would remove ALL Series repick/move slot assignment. The markup
// is SeasonEpisodePicker's own fallback, aria-labels included, so a degraded
// repick behaves byte-for-byte as it does today.
//
// MULTI-OPEN. Any number of seasons may be expanded at once — an operator
// comparing "is this S3E4 or S4E4?" should not have one collapse the other.
// That is why the open set is a Set<number> and not SeasonEpisodePicker's
// single `openSeason` signal.
//
// Season ordering is the API's decision (ascending by seasonNumber, Specials
// first) and is deliberately not re-sorted here.

import { type Component, For, Show, createResource, createSignal } from "solid-js";
import type { SeasonSummary } from "@dto";
import { fetchTitleDetail } from "../../api/discover";
import { Muted, yearOf } from "../../components/ui";

// seasonLabel prefers TMDB's own season name ("Specials", "Season 4") and falls
// back to a synthesized one when TMDB has none. Duplicated from
// SeasonEpisodePicker.tsx:76-77 — see the file header for why.
const seasonLabel = (s: SeasonSummary): string =>
  s.name || (s.seasonNumber === 0 ? "Specials" : `Season ${s.seasonNumber}`);

// ROW_CLASS is the shared shape of every selectable row (whole-season and
// episode alike) — full width, two columns, no art.
const ROW_CLASS =
  "flex w-full items-baseline gap-2 px-3 py-1.5 text-left transition hover:bg-surface-2";

// FreeTextPicker is the degraded fallback (state 4), duplicated verbatim from
// SeasonEpisodePicker.tsx:105-162 so that a degraded repick keeps EXACTLY the
// behavior it ships today — aria-labels, "Go" submit, and the validation rule
// below all included.
//
// SEASON IS REQUIRED; EPISODE IS NOT. The asymmetry is the whole point, so do
// not "tidy" it into one shared rule:
//   - A blank Episode legitimately means "the whole season" — which this call
//     site then maps to a show-level commit (SearchTakeover's D-1 rule).
//   - A blank Season has no such meaning. Coercing it to 0 via
//     `parseInt("", 10) || 0` yields Season 0 — SPECIALS, a real, dispatchable
//     season that is a legitimate accordion row — so an empty-form submit would
//     stage a genuine Specials selection indistinguishable from a deliberate
//     one. Validating the INPUT (did they type a parseable number?) rather than
//     the VALUE (is it > 0?) is what separates the two.
//
// Claude 2026-08-02 (original fix, SeasonEpisodePicker.tsx), duplicated here
// 2026-08-09, Phase-4 review FIX (code-reviewer).
// Troubleshooting: empty-form submit silently staged a Season 0 selection.
// Also see grabs.Grab's SeasonSpecified bool (Go side) — the deepest reason
// input-validity, not value truthiness, is what's checked: a bare int can
// never distinguish "typed nothing" from "chose Specials" on its own.
// Review if: EITHER copy of this validation rule changes without the other —
// they must stay identical (see the file header's "duplicated, not
// parameterised" note for why there are two copies at all).
const FreeTextPicker: Component<{
  onSubmit: (season: number, episode: number) => void;
}> = (props) => {
  const [season, setSeason] = createSignal("");
  const [episode, setEpisode] = createSignal("");

  // parseNonNegativeInt returns null for anything that is not a bare
  // non-negative integer — "" and "  " included, which is the case that matters.
  // Deliberately NOT parseInt: that accepts "4abc" (→ 4) and "" (→ NaN, then
  // coerced to 0 by a `||`), both of which are how the original bug got in.
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

export const SeasonEpisodeAccordion: Component<{
  tmdbId: number;
  // episode === 0 means "the whole season" — the same call shape
  // SeasonEpisodePicker's single mode uses, so SearchTakeover's D-1 mapping
  // (episode 0 -> show-level commit, both slot fields omitted) is unchanged.
  onSubmit: (season: number, episode: number) => void;
  // currentSlot expands the matching season on mount. It has NO equivalent on
  // SeasonEpisodePicker, whose single mode deliberately has no pre-selection
  // concept — and adding one there would modify a Non-Goal-protected file. It
  // is safe here because this component is new and protects nothing: expanding
  // a row pre-selects nothing, it only saves the operator hunting for the
  // season their file is already matched to.
  currentSlot?: { season: number; episode: number } | null;
}> = (props) => {
  // Lazily seeded from currentSlot and thereafter operator-owned. Deliberately
  // NOT an effect: re-seeding on a later currentSlot change would re-open a
  // season the operator had just collapsed. The prop is fixed for the life of
  // one step-2 mount anyway (SearchTakeover remounts on "Change show").
  const [expanded, setExpanded] = createSignal<ReadonlySet<number>>(
    props.currentSlot ? new Set([props.currentSlot.season]) : new Set(),
  );

  // A NEW Set every time: Solid compares signal values by reference, so
  // mutating and returning the same Set would update nothing.
  const toggle = (n: number) =>
    setExpanded((prev) => {
      const next = new Set(prev);
      if (next.has(n)) next.delete(n);
      else next.add(n);
      return next;
    });

  const enumerable = () => props.tmdbId > 0;

  // The self-fetch: one request for the season block only. Series is hardcoded
  // because this component only ever mounts on a Series repick/move.
  //
  // The fetcher NEVER rejects. A Solid resource re-throws on read after its
  // fetcher errors (by design, for ErrorBoundary integration), and that throw
  // lands mid-render — the exact shape of the shipped GrabDialog bug CLAUDE.md
  // documents. Resolving to [] instead routes a failed fetch into state 4, the
  // degraded free-text fallback, which is what a failed enumeration should do.
  //
  // `?? []` is not defensive noise: TitleDetail.seasons is typed non-nullable,
  // but the handler serializes a nil slice as JSON null unless it is passed
  // through nonNilSlice, so tsc cannot catch this one.
  const [fetched] = createResource(
    () => (props.tmdbId > 0 ? props.tmdbId : false),
    (id) =>
      fetchTitleDetail("series", id, "seasons")
        .then((d) => d.seasons ?? [])
        .catch(() => [] as SeasonSummary[]),
  );

  const seasons = () => fetched() ?? [];

  const degraded = (notice: string) => (
    <div>
      <Muted class="mb-2">{notice}</Muted>
      <FreeTextPicker onSubmit={(s, e) => props.onSubmit(s, e)} />
    </div>
  );

  return (
    <Show
      when={enumerable()}
      fallback={degraded(
        "Season list unavailable for this title — enter a season (and optionally an episode) instead.",
      )}
    >
      <Show
        when={!fetched.loading}
        fallback={
          <div class="space-y-1" aria-label="Loading seasons">
            <For each={[0, 1, 2, 3]}>
              {() => (
                <div class="h-8 animate-pulse rounded border border-border bg-surface-2" />
              )}
            </For>
          </div>
        }
      >
        <Show
          when={seasons().length > 0}
          fallback={degraded(
            "Couldn't load seasons from TMDB — enter a season (and optionally an episode) instead.",
          )}
        >
          <div class="divide-y divide-border rounded border border-border">
            <For each={seasons()}>
              {(s) => {
                const open = () => expanded().has(s.seasonNumber);
                const year = () => yearOf(s.airDate);
                const episodeCount = () =>
                  `${s.episodeCount} ep${s.episodeCount === 1 ? "" : "s"}`;
                return (
                  <div>
                    <button
                      type="button"
                      aria-expanded={open()}
                      class="flex w-full items-baseline gap-2 px-3 py-2 text-left transition hover:bg-surface-2"
                      onClick={() => toggle(s.seasonNumber)}
                    >
                      <span aria-hidden="true" class="text-[11px] text-muted">
                        {open() ? "▾" : "▸"}
                      </span>
                      <span class="min-w-0 flex-1 truncate text-xs font-medium text-fg">
                        {seasonLabel(s)}
                      </span>
                      <span class="shrink-0 text-[11px] text-muted">
                        {episodeCount()}
                        <Show when={year()}>{(y) => <> · {y()}</>}</Show>
                      </span>
                    </button>
                    <Show when={open()}>
                      <div class="border-t border-border bg-surface-2/40">
                        {/* The whole-season row leads the body and is what
                            expresses episode 0 — the shipped "S4 with no E
                            suffix" semantic. SearchTakeover's commitSlot maps
                            it to a show-level commit (D-1); removing it removes
                            that path's only entry point from the accordion. */}
                        <button
                          type="button"
                          class={ROW_CLASS}
                          // Multi-open means more than one "Whole season" row
                          // can be on screen at once (that's the point — an
                          // operator comparing S3E4 vs S4E4 needs both open),
                          // so the accessible name needs the season to stay
                          // unique per row. Visible text is untouched;
                          // `getByText("Whole season")` still matches.
                          aria-label={`Whole season — ${seasonLabel(s)}`}
                          onClick={() => props.onSubmit(s.seasonNumber, 0)}
                        >
                          <span class="min-w-0 flex-1 truncate text-xs font-medium text-fg">
                            Whole season
                          </span>
                          <span class="shrink-0 text-[11px] text-muted">
                            {episodeCount()}
                          </span>
                        </button>
                        <For
                          each={s.episodes}
                          fallback={
                            <Muted class="px-3 py-1.5 text-xs">
                              No episode list available for this season.
                            </Muted>
                          }
                        >
                          {(ep) => (
                            <button
                              type="button"
                              class={ROW_CLASS}
                              onClick={() =>
                                props.onSubmit(s.seasonNumber, ep.episodeNumber)
                              }
                            >
                              <span class="min-w-0 flex-1 truncate text-xs text-fg">
                                {`E${ep.episodeNumber}${ep.name ? " · " + ep.name : ""}`}
                              </span>
                              <span class="shrink-0 text-[11px] text-muted">
                                <Show when={yearOf(ep.airDate)}>
                                  {(y) => <>{y()}</>}
                                </Show>
                                <Show when={ep.runtime > 0}>
                                  <> · {ep.runtime}m</>
                                </Show>
                              </span>
                            </button>
                          )}
                        </For>
                      </div>
                    </Show>
                  </div>
                );
              }}
            </For>
          </div>
        </Show>
      </Show>
    </Show>
  );
};
