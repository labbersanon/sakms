// FilterSortBar — the Discover filter/sort controls, one file with two
// deliberately-separate components (Mainstream's TMDB backend and Adult's
// TPDB/StashBox backend expose genuinely different filterable surfaces, so no
// forced-shared generic): MainstreamFilterSortBar (content-type + genre + year
// + min-rating + sort-by + clear, all native <select> dropdowns on one row) and
// AdultSortBar (one sort <select>). Both are pure presentational shells over a
// caller-owned filter/sort signal — the parent screen decides what an active
// filter/sort actually renders (a filtered grid replaces the carousels; see
// Mainstream.tsx/Adult.tsx).

import { type Component, type JSX, createResource, For, Match, Show, Switch } from "solid-js";
import { ErrorText, labelClass, FILTER_BAR_FIELDS_CLASS } from "../../components/ui";
import { type AdultSortBy, type DiscoverSortBy } from "../../api/discover";
import { fetchGenres } from "../../api/discoverSliders";

// MainstreamContentType is which TMDB catalog the filter bar targets — Movies
// and TV have separate genre id spaces with no clean 1:1 mapping, so exactly
// one is filtered at a time (the plan's resolved decision, default Movies).
export type MainstreamContentType = "movies" | "series";

// MainstreamFilters is the full filter state the bar reads/writes. contentType
// is which catalog to browse; the rest are the actual filters. A null year/
// minRating/genreId means "unset" (no bound sent) — genre is a single-select
// dropdown, the same single-value shape as year/minRating/sortBy.
export type MainstreamFilters = {
  contentType: MainstreamContentType;
  genreId: number | null;
  year: number | null;
  minRating: number | null;
  sortBy: DiscoverSortBy;
};

export const DEFAULT_MAINSTREAM_FILTERS: MainstreamFilters = {
  contentType: "movies",
  genreId: null,
  year: null,
  minRating: null,
  sortBy: "popularity",
};

// isMainstreamFilterActive decides whether the bar is doing anything that
// should replace the carousels with a filtered grid. contentType alone never
// counts — switching Movies/Series with no other filter set is still a plain
// (unfiltered) browse of the other catalog, which the carousels already cover;
// only a real genre/year/rating filter or a non-default sort is "active".
export function isMainstreamFilterActive(f: MainstreamFilters): boolean {
  return (
    f.genreId != null ||
    f.year != null ||
    f.minRating != null ||
    f.sortBy !== "popularity"
  );
}

const CONTENT_TYPE_OPTIONS: MainstreamContentType[] = ["movies", "series"];
const CONTENT_TYPE_LABELS: Record<MainstreamContentType, string> = {
  movies: "Movies",
  series: "Series",
};

const SORT_BY_OPTIONS: DiscoverSortBy[] = ["popularity", "rating", "newest"];
const SORT_BY_LABELS: Record<DiscoverSortBy, string> = {
  popularity: "Most Popular",
  rating: "Highest Rated",
  newest: "Newest",
};

// MinRatingKey is the select value; "any" maps to null (no min-rating bound),
// every other key to its integer floor.
type MinRatingKey = "any" | "6" | "7" | "8" | "9";
const MIN_RATING_OPTIONS: MinRatingKey[] = ["any", "6", "7", "8", "9"];
const MIN_RATING_LABELS: Record<MinRatingKey, string> = {
  any: "Any rating",
  "6": "6+",
  "7": "7+",
  "8": "8+",
  "9": "9+",
};

// YEAR_OPTIONS runs current-year+1 (so an about-to-release title is reachable)
// down to 1950 — the plain <select>'s option list, "Any year" (null) aside.
const YEAR_OPTIONS: number[] = (() => {
  const max = new Date().getFullYear() + 1;
  const years: number[] = [];
  for (let y = max; y >= 1950; y--) years.push(y);
  return years;
})();

const SELECT_CLASS =
  "mt-1 w-full rounded-md border border-border bg-bg px-3 py-2 text-sm text-fg outline-none focus:border-accent sm:w-auto";

// SelectField is the one labeled native <select> the filter bar repeats for
// each control (label above, select below) so the whole bar reads as one
// consistent inline row of dropdowns. `for`/`id` pair the label to the select
// so tests (and screen readers) resolve it by label text.
const SelectField: Component<{
  id: string;
  label: string;
  value: string | number;
  onChange: (value: string) => void;
  children: JSX.Element;
}> = (props) => (
  <div class="flex w-full flex-col sm:w-auto">
    <label class={labelClass} for={props.id}>
      {props.label}
    </label>
    <select
      id={props.id}
      class={SELECT_CLASS}
      value={props.value}
      onChange={(e) => props.onChange(e.currentTarget.value)}
    >
      {props.children}
    </select>
  </div>
);

// MainstreamFilterSortBar renders the Movies/Series filter surface as a single
// row of native <select> dropdowns. value is a Solid accessor (the parent owns
// the signal); every control calls onChange with the full next MainstreamFilters.
// The genre options re-fetch on a contentType switch (movie vs. tv genre lists
// differ), and switching content type also clears genreId so a movie genre id
// never reaches the /discover/tv endpoint.
export const MainstreamFilterSortBar: Component<{
  value: () => MainstreamFilters;
  onChange: (f: MainstreamFilters) => void;
  lockedContentType?: MainstreamContentType;
}> = (props) => {
  // When the tab locks the content type its <select> is hidden, but the genre
  // list must still follow the lock rather than a stale filters.contentType
  // carried over from a prior view.
  const genreMode = () => props.lockedContentType ?? props.value().contentType;

  const [genres, { refetch: refetchGenres }] = createResource(genreMode, fetchGenres);

  const genreErrorMessage = (): string => {
    const err: unknown = genres.error;
    return err instanceof Error ? err.message : "Genre list failed to load";
  };

  const patch = (partial: Partial<MainstreamFilters>) =>
    props.onChange({ ...props.value(), ...partial });

  const minRatingKey = (): MinRatingKey => {
    const r = props.value().minRating;
    return r == null ? "any" : (String(r) as MinRatingKey);
  };

  return (
    <div class="mb-4 rounded-xl border border-border bg-surface p-4">
      <div class={FILTER_BAR_FIELDS_CLASS}>
        <Show when={!props.lockedContentType}>
          <SelectField
            id="discover-filter-content-type"
            label="Content type"
            value={props.value().contentType}
            onChange={(v) =>
              patch({ contentType: v as MainstreamContentType, genreId: null })
            }
          >
            <For each={CONTENT_TYPE_OPTIONS}>
              {(ct) => <option value={ct}>{CONTENT_TYPE_LABELS[ct]}</option>}
            </For>
          </SelectField>
        </Show>

        <div class="flex w-full flex-col sm:w-auto">
          <label class={labelClass} for="discover-filter-genre">
            Genre
          </label>
          <Switch>
            <Match when={genres.loading}>
              <select id="discover-filter-genre" class={SELECT_CLASS} disabled>
                <option>Loading genres…</option>
              </select>
            </Match>
            <Match when={genres.error}>
              <select id="discover-filter-genre" class={SELECT_CLASS} disabled>
                <option>Could not load genres</option>
              </select>
              <ErrorText>
                {genreErrorMessage()}
                {" — "}
                <button
                  type="button"
                  class="underline"
                  onClick={() => void refetchGenres()}
                >
                  Retry
                </button>
              </ErrorText>
            </Match>
            <Match when={true}>
              <select
                id="discover-filter-genre"
                class={SELECT_CLASS}
                value={props.value().genreId ?? ""}
                onChange={(e) =>
                  patch({
                    genreId: e.currentTarget.value
                      ? parseInt(e.currentTarget.value, 10)
                      : null,
                  })
                }
              >
                <option value="">All genres</option>
                <For each={genres() ?? []}>
                  {(g) => <option value={String(g.id)}>{g.name}</option>}
                </For>
              </select>
            </Match>
          </Switch>
        </div>

        <SelectField
          id="discover-filter-year"
          label="Year"
          value={props.value().year ?? ""}
          onChange={(v) => patch({ year: v ? parseInt(v, 10) : null })}
        >
          <option value="">Any year</option>
          <For each={YEAR_OPTIONS}>{(y) => <option value={y}>{y}</option>}</For>
        </SelectField>

        <SelectField
          id="discover-filter-min-rating"
          label="Minimum rating"
          value={minRatingKey()}
          onChange={(v) =>
            patch({ minRating: v === "any" ? null : parseInt(v, 10) })
          }
        >
          <For each={MIN_RATING_OPTIONS}>
            {(k) => <option value={k}>{MIN_RATING_LABELS[k]}</option>}
          </For>
        </SelectField>

        <SelectField
          id="discover-filter-sort-by"
          label="Sort by"
          value={props.value().sortBy}
          onChange={(v) => patch({ sortBy: v as DiscoverSortBy })}
        >
          <For each={SORT_BY_OPTIONS}>
            {(s) => <option value={s}>{SORT_BY_LABELS[s]}</option>}
          </For>
        </SelectField>

        <Show when={isMainstreamFilterActive(props.value())}>
          <button
            type="button"
            class="text-sm text-accent underline sm:self-end"
            onClick={() =>
              props.onChange({
                ...DEFAULT_MAINSTREAM_FILTERS,
                contentType:
                  props.lockedContentType ?? DEFAULT_MAINSTREAM_FILTERS.contentType,
              })
            }
          >
            Clear filters
          </button>
        </Show>
      </div>
    </div>
  );
};

// AdultSortValue is the sort bar's value: "default" (no sort → today's browse
// rows), "newest" (the TPDB+StashDB merged feed), or a TPDB-only AdultSortBy.
// Note recently_released is in AdultSortBy (the API contract) but is NOT a bar
// option — "Newest Releases"/merged supersedes it — so the bar only offers
// default/newest/recently_created/recently_updated.
export type AdultSortValue = "default" | "newest" | AdultSortBy;

const ADULT_SORT_OPTIONS: AdultSortValue[] = [
  "default",
  "newest",
  "recently_created",
  "recently_updated",
];

const ADULT_SORT_LABELS: Record<AdultSortValue, string> = {
  default: "Default",
  newest: "Newest Releases",
  recently_released: "Recently Released", // in the union but not an offered option
  recently_created: "Recently Added",
  recently_updated: "Recently Updated",
};

// AdultSortBar renders Adult's sort-only control (TPDB/StashBox have no genre/
// year/rating filter surface) as the same native <select> dropdown style the
// Mainstream bar uses. value is a parent-owned accessor; onChange receives the
// next AdultSortValue.
export const AdultSortBar: Component<{
  value: () => AdultSortValue;
  onChange: (v: AdultSortValue) => void;
  // Claude 2026-08-13: Movies has no TPDB BrowseMovies (1A). Omit recently_*.
  // Reason: 2.0b unpark — Movies sort is the pooled Newest Releases feed only.
  // Review if: a movie catalog browse lands.
  includeCatalogBrowse?: boolean;
}> = (props) => {
  const options = () =>
    props.includeCatalogBrowse === false
      ? ADULT_SORT_OPTIONS.filter((o) => o === "default" || o === "newest")
      : ADULT_SORT_OPTIONS;
  return (
    <div class="mb-4 rounded-xl border border-border bg-surface p-4">
      <div class={FILTER_BAR_FIELDS_CLASS}>
        <SelectField
          id="discover-adult-sort"
          label="Sort"
          value={props.value()}
          onChange={(v) => props.onChange(v as AdultSortValue)}
        >
          <For each={options()}>
            {(opt) => <option value={opt}>{ADULT_SORT_LABELS[opt]}</option>}
          </For>
        </SelectField>
      </div>
    </div>
  );
};
