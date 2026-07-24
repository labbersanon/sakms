// FilterSortBar — the Discover filter/sort controls, one file with two
// deliberately-separate components (Mainstream's TMDB backend and Adult's
// TPDB/StashBox backend expose genuinely different filterable surfaces, so no
// forced-shared generic): MainstreamFilterSortBar (content-type toggle + genre
// multi-select + year + min-rating + sort-by + clear) and AdultSortBar (one
// sort pill). Both are pure presentational shells over a caller-owned filter/
// sort signal — the parent screen decides what an active filter/sort actually
// renders (a filtered grid replaces the carousels; see Mainstream.tsx/Adult.tsx).

import { type Component, createResource, For, Show } from "solid-js";
import { PillSelector, labelClass } from "../../components/ui";
import { type AdultSortBy, type DiscoverSortBy } from "../../api/discover";
import { type Genre, fetchGenres } from "../../api/discoverSliders";

// MainstreamContentType is which TMDB catalog the filter bar targets — Movies
// and TV have separate genre id spaces with no clean 1:1 mapping, so exactly
// one is filtered at a time (the plan's resolved decision, default Movies).
export type MainstreamContentType = "movies" | "series";

// MainstreamFilters is the full filter state the bar reads/writes. contentType
// is which catalog to browse; the rest are the actual filters. A null year/
// minRating means "unset" (no bound sent); genreIds is the multi-select set.
export type MainstreamFilters = {
  contentType: MainstreamContentType;
  genreIds: number[];
  year: number | null;
  minRating: number | null;
  sortBy: DiscoverSortBy;
};

export const DEFAULT_MAINSTREAM_FILTERS: MainstreamFilters = {
  contentType: "movies",
  genreIds: [],
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
    f.genreIds.length > 0 ||
    f.year != null ||
    f.minRating != null ||
    f.sortBy !== "popularity"
  );
}

const CONTENT_TYPE_LABELS: Record<MainstreamContentType, string> = {
  movies: "Movies",
  series: "Series",
};

const SORT_BY_LABELS: Record<DiscoverSortBy, string> = {
  popularity: "Most Popular",
  rating: "Highest Rated",
  newest: "Newest",
};

// MinRatingKey is the pill value; "any" maps to null (no min-rating bound),
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

// MainstreamFilterSortBar renders the Movies/Series filter surface. value is a
// Solid accessor (the parent owns the signal); every control calls onChange
// with the full next MainstreamFilters. The genre chips re-fetch on a
// contentType switch (movie vs. tv genre lists differ), and switching content
// type also clears genreIds so a movie genre id never reaches the /discover/tv
// endpoint.
export const MainstreamFilterSortBar: Component<{
  value: () => MainstreamFilters;
  onChange: (f: MainstreamFilters) => void;
}> = (props) => {
  const [genres] = createResource(
    () => props.value().contentType,
    (ct) => fetchGenres(ct).catch(() => [] as Genre[]),
  );

  const patch = (partial: Partial<MainstreamFilters>) =>
    props.onChange({ ...props.value(), ...partial });

  const toggleGenre = (id: number) => {
    const cur = props.value().genreIds;
    patch({
      genreIds: cur.includes(id) ? cur.filter((g) => g !== id) : [...cur, id],
    });
  };

  const minRatingKey = (): MinRatingKey => {
    const r = props.value().minRating;
    return r == null ? "any" : (String(r) as MinRatingKey);
  };

  return (
    <div class="mb-4 rounded-xl border border-border bg-surface p-4">
      <PillSelector<MainstreamContentType>
        label="Content type"
        options={["movies", "series"]}
        optionLabels={CONTENT_TYPE_LABELS}
        selected={props.value().contentType}
        onSelect={(ct) => patch({ contentType: ct, genreIds: [] })}
      />

      <div class="mb-2">
        <div class={labelClass}>Genres</div>
        <div class="mt-1 flex flex-wrap gap-1.5">
          <For each={genres() ?? []}>
            {(g) => (
              <button
                type="button"
                class="rounded-md border px-2 py-1 text-xs font-medium"
                classList={{
                  "border-accent bg-accent text-accent-fg": props
                    .value()
                    .genreIds.includes(g.id),
                  "border-border bg-surface-2 text-fg": !props
                    .value()
                    .genreIds.includes(g.id),
                }}
                onClick={() => toggleGenre(g.id)}
              >
                {g.name}
              </button>
            )}
          </For>
        </div>
      </div>

      <div class="mb-2">
        <label class={labelClass} for="discover-filter-year">
          Year
        </label>
        <div class="mt-1">
          <select
            id="discover-filter-year"
            class="rounded-md border border-border bg-bg px-3 py-2 text-sm text-fg outline-none focus:border-accent"
            value={props.value().year ?? ""}
            onChange={(e) => {
              const v = e.currentTarget.value;
              patch({ year: v ? parseInt(v, 10) : null });
            }}
          >
            <option value="">Any year</option>
            <For each={YEAR_OPTIONS}>{(y) => <option value={y}>{y}</option>}</For>
          </select>
        </div>
      </div>

      <PillSelector<MinRatingKey>
        label="Minimum rating"
        options={MIN_RATING_OPTIONS}
        optionLabels={MIN_RATING_LABELS}
        selected={minRatingKey()}
        onSelect={(k) => patch({ minRating: k === "any" ? null : parseInt(k, 10) })}
      />

      <PillSelector<DiscoverSortBy>
        label="Sort by"
        options={["popularity", "rating", "newest"]}
        optionLabels={SORT_BY_LABELS}
        selected={props.value().sortBy}
        onSelect={(s) => patch({ sortBy: s })}
      />

      <Show when={isMainstreamFilterActive(props.value())}>
        <button
          type="button"
          class="mt-1 text-sm text-accent underline"
          onClick={() => props.onChange(DEFAULT_MAINSTREAM_FILTERS)}
        >
          Clear filters
        </button>
      </Show>
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
// year/rating filter surface). value is a parent-owned accessor; onChange
// receives the next AdultSortValue.
export const AdultSortBar: Component<{
  value: () => AdultSortValue;
  onChange: (v: AdultSortValue) => void;
}> = (props) => (
  <div class="mb-4 rounded-xl border border-border bg-surface p-4">
    <PillSelector<AdultSortValue>
      label="Sort"
      options={ADULT_SORT_OPTIONS}
      optionLabels={ADULT_SORT_LABELS}
      selected={props.value()}
      onSelect={props.onChange}
    />
  </div>
);
