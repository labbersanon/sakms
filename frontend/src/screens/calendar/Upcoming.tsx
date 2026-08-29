// Upcoming.tsx — the Calendar screen's forward-looking view (Wave 3 FE-UP of
// .omc/plans/autopilot-impl-calendar-grabs-requests.md). Two genuinely
// different sources, selected by a content-type switch:
//
//   - Series: not-yet-aired episodes of already-tracked series
//     (GET /api/calendar/upcoming/series via api/calendar.ts).
//   - Movies: upcoming TMDB movie releases, open-ended — not bounded to
//     existing Requests (GET /api/calendar/upcoming/movies).
//
// CONTRACT WITH calendar/index.tsx (FE-CAL, a later task): this component is a
// pure view. `year`/`month`/`display` are REQUIRED props with no defaults —
// the parent owns month navigation and the grid/list toggle (plan §7.2: the
// toggle is persisted ONCE under `sakms.calendar.display` for both History and
// Upcoming, so a per-view toggle here would be a second, desynchronised
// control). Required rather than defaulted so a parent that forgets to wire
// one gets a compile error instead of a silently-wrong month.
//
// Movies/Series, NOT ModeTabs (plan §0.6/§7.2). History is three-mode because
// grabs exist for all three; Upcoming has no Adult source at all — Adult is
// TPDB/stash-box-backed with no release calendar, the same reason
// discoverCalendarHandler rejects it outright. A three-mode ModeTabs here
// would render an Adult tab that can only ever be empty. Do not "unify" the
// two selectors. It also renders ScreenTabBar directly rather than ScreenTabs:
// the shell's one tab slot belongs to Queue, and this selector is a
// within-screen control, not a screen-level tab set.
//
// CELL SHAPES DIFFER BY VIEW AND THAT IS INTENTIONAL (plan §7.2, verbatim:
// "Do not force a shared cell component across three shapes; that is the
// abstraction §0.3 already declined"). Series cells are TEXT
// (`Series — S02E05`): TMDB episode data carries no season/episode-level image
// suitable for a small calendar cell within this feature's scope. Movies cells
// are poster-bearing, through /api/images/proxy (tmdbPoster) — never a direct
// image.tmdb.org <img src>.
//
// SERIES EPISODES ARE NEVER CLICKABLE-TO-REQUEST (plan §0.6 / Constraint 2).
// The asymmetry is deliberate, not an oversight: click-to-request is a
// Movies-only affordance in this feature. Adding one to Series "for
// consistency" is out of scope and would need a whole design of its own.
//
// §5.7's toggle-conditional confirmation copy is a CORRECTNESS requirement,
// not polish: with `usenet_autograb_enabled` off — the default on every
// install — the retry loop never starts, so a held row never transitions on
// its own and nothing will ever search for the requested film automatically.
// Promising an automatic search there would be a lie. The toggle is read once
// at mount; an unknown state (still loading, or the read failed) falls back to
// the OFF wording, because the failure mode of over-promising is worse than
// the failure mode of under-promising.

import {
  type Component,
  createMemo,
  createResource,
  createSignal,
  For,
  Show,
} from "solid-js";
import {
  fetchUpcomingMovies,
  fetchUpcomingSeries,
  requestPreRelease,
  type UpcomingMovieEntry,
  type UpcomingSeriesEntry,
} from "../../api/calendar";
import { tmdbPoster } from "../../api/discover";
import { fetchUsenetAutoGrabEnabled } from "../../api/usenet";
import { ErrorText, Muted, ScreenTabBar } from "../../components/ui";
import { matchesQueueSearch } from "../queueSearch";
import { cells, dayKey, monthRange, pad2 } from "./grid";

const WEEKDAYS = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];

// Deliberately not exported: the content switch is internal to this view —
// calendar/index.tsx owns the History/Upcoming switch, not this one.
type UpcomingContent = "movies" | "series";

const CONTENT_TABS = [
  { id: "movies", label: "Movies" },
  { id: "series", label: "Series" },
];

// episodeCode is the `S02E05` half of a Series cell's text. Zero-padded via
// grid.ts's pad2 so cells align in a column.
const episodeCode = (e: UpcomingSeriesEntry): string =>
  `S${pad2(e.seasonNumber)}E${pad2(e.episodeNumber)}`;

// bucketBy groups entries by their YYYY-MM-DD day key. Local to this file and
// used by the two view-specific memos below — the two entry types share no
// carrier type, so this takes a key function rather than a common field.
function bucketBy<T>(items: T[], key: (t: T) => string): Map<string, T[]> {
  const map = new Map<string, T[]>();
  for (const it of items) {
    const k = key(it);
    if (!k) continue;
    const list = map.get(k);
    if (list) list.push(it);
    else map.set(k, [it]);
  }
  return map;
}

export const Upcoming: Component<{
  year: number;
  month: number;
  display: "grid" | "list";
  search?: string;
}> = (props) => {
  const [content, setContent] = createSignal<UpcomingContent>("movies");

  // Only the SELECTED source is fetched. The movies endpoint costs up to 22
  // TMDB calls per month view (plan §4.3), so firing it while Series is on
  // screen would double the budget for data nobody is looking at.
  const [data] = createResource(
    () => ({ content: content(), year: props.year, month: props.month }),
    async (k) => {
      const { from, to } = monthRange(k.year, k.month);
      if (k.content === "series") {
        return {
          kind: "series" as const,
          series: await fetchUpcomingSeries(from, to),
        };
      }
      return {
        kind: "movies" as const,
        movies: await fetchUpcomingMovies(from, to),
      };
    },
  );

  // Reading a Solid resource after its fetcher rejects RE-THROWS (by design,
  // for ErrorBoundary integration) — a mid-render throw that took down a whole
  // dialog once already (see CLAUDE.md's GrabDialog note). Gate every read on
  // state === "ready" rather than on a truthiness check.
  const loaded = () => (data.state === "ready" ? data() : undefined);

  const query = () => props.search ?? "";
  const movies = (): UpcomingMovieEntry[] => {
    const d = loaded();
    if (!d || d.kind !== "movies") return [];
    return d.movies.filter((m) =>
      matchesQueueSearch(query(), m.title, m.releaseKind, "movies", "Movies"),
    );
  };
  const series = (): UpcomingSeriesEntry[] => {
    const d = loaded();
    if (!d || d.kind !== "series") return [];
    return d.series.filter((e) =>
      matchesQueueSearch(
        query(),
        e.seriesTitle,
        e.episodeTitle,
        episodeCode(e),
        "series",
        "Series",
      ),
    );
  };

  const movieBuckets = createMemo(() =>
    bucketBy(movies(), (m) => (m.releaseDate ?? "").slice(0, 10)),
  );
  const seriesBuckets = createMemo(() =>
    bucketBy(series(), (e) => (e.airDate ?? "").slice(0, 10)),
  );

  // The toggle behind §5.7's copy. Read ONCE at mount, never per click. Same
  // state === "ready" guard as above; anything else (loading, failed) is
  // treated as OFF so the copy never over-promises.
  const [autoGrab] = createResource(fetchUsenetAutoGrabEnabled);
  const autoGrabOn = () => autoGrab.state === "ready" && autoGrab() === true;

  const confirmCopy = (date: string): string =>
    autoGrabOn()
      ? `Requested — it will be searched for automatically after ${date}.`
      : `Requested — it will appear in Requests after ${date}, ready to grab.`;

  // requested is the locally-flipped set layered OVER the server's own
  // alreadyRequested flag: the server computes the flag from library + grab
  // state (never from Prowlarr) and this month's entries are not refetched
  // after a click, so without the local set a second click would re-POST.
  const [requested, setRequested] = createSignal<Set<number>>(new Set());
  const [pending, setPending] = createSignal<number | null>(null);
  const [notice, setNotice] = createSignal("");
  const [failure, setFailure] = createSignal("");

  const isRequested = (e: UpcomingMovieEntry) =>
    e.alreadyRequested || requested().has(e.tmdbId);

  async function request(entry: UpcomingMovieEntry) {
    if (isRequested(entry) || pending() !== null) return;
    setPending(entry.tmdbId);
    setFailure("");
    try {
      // The response's alreadyRequested is INFORMATIONAL, deliberately not
      // branched on (plan §5.2, HOLD-1 review): step 2 of the endpoint —
      // refreshing an already-held request's date — is only reachable from a
      // stale client, since there is no re-click affordance. A stale tab
      // landing there is a normal, harmless outcome, so it gets the same
      // confirmation as a fresh request rather than an error.
      await requestPreRelease({
        tmdbId: entry.tmdbId,
        title: entry.title,
        releaseDate: entry.releaseDate,
      });
      setRequested((prev) => new Set(prev).add(entry.tmdbId));
      setNotice(confirmCopy(entry.releaseDate));
    } catch (e) {
      setFailure((e as Error).message || "Request failed");
    } finally {
      setPending(null);
    }
  }

  // dayKeys walks the month through grid.ts's own geometry (never a second
  // day-count implementation) so the list view and the grid view agree on
  // exactly which days exist.
  const dayKeys = () =>
    cells(props.year, props.month)
      .filter((d): d is number => d !== null)
      .map((d) => dayKey(props.year, props.month, d));

  // hasEntries is derived from the BUCKETED, IN-WINDOW dataset the views
  // actually render (dayKeys × countFor), never from the raw fetch result.
  //
  // The difference is real, not defensive: the backend explicitly documents
  // that a resolved movie's typed release date can legitimately fall OUTSIDE
  // the requested window (calendar_upcoming.go's resolveTypedReleaseDate — "A
  // RETURNED DATE IS NOT GUARANTEED TO FALL INSIDE [from, to] … a month grid
  // should drop such an entry"), and the views do drop them. Counting the
  // unfiltered result meant a month whose every entry resolved out-of-window
  // rendered nothing at all AND suppressed the message explaining why.
  const hasEntries = () => dayKeys().some((k) => countFor(k) > 0);

  const MovieCell: Component<{
    entry: UpcomingMovieEntry;
    layout: "grid" | "list";
  }> = (p) => {
    const poster = () => tmdbPoster(p.entry.posterPath ?? "");
    const planned = () => p.entry.releaseKind === "planned";

    const body = () => (
      <div
        classList={{
          "flex items-center gap-3 text-left": p.layout === "list",
          "flex flex-col gap-1": p.layout === "grid",
        }}
      >
        <Show when={poster()}>
          <img
            src={poster()}
            alt=""
            classList={{
              "w-full rounded object-cover aspect-[2/3]": p.layout === "grid",
              "h-16 w-11 shrink-0 rounded object-cover": p.layout === "list",
            }}
          />
        </Show>
        <div class="min-w-0">
          <div class="truncate text-xs font-medium text-fg">
            {p.entry.title}
          </div>
          <Show when={p.layout === "list"}>
            <div class="text-xs text-muted">{p.entry.releaseDate}</div>
          </Show>
          {/* releaseKind is a display nuance, not a functional gate — a
              "planned" (theatrical-only, no confirmed digital date yet) film
              is exactly as clickable as a "digital" one. The label is text,
              not colour alone, so the distinction survives a colourblind
              operator and is assertable in a test. */}
          <span
            class="mt-0.5 inline-block rounded border px-1 text-[10px] font-medium"
            classList={{
              "border-dashed border-warn text-warn": planned(),
              "border-border text-muted": !planned(),
            }}
            title={
              planned()
                ? "Planned (theatrical) date — no confirmed digital release yet"
                : "Confirmed digital release date"
            }
          >
            {planned() ? "Planned" : "Digital"}
          </span>
        </div>
      </div>
    );

    return (
      <Show
        when={!isRequested(p.entry)}
        fallback={
          <div class="rounded border border-border bg-surface-2 p-1.5">
            {body()}
            <div class="mt-1 text-[10px] font-medium text-muted">Requested</div>
          </div>
        }
      >
        <button
          type="button"
          class="w-full rounded border border-border bg-surface-2 p-1.5 text-left transition hover:border-accent disabled:opacity-50"
          aria-label={`Request ${p.entry.title}`}
          disabled={pending() === p.entry.tmdbId}
          onClick={() => void request(p.entry)}
        >
          {body()}
        </button>
      </Show>
    );
  };

  // Series entries render as plain text with NO interactive element at all —
  // see this file's header. Not a button, not a link, no onClick.
  const SeriesCell: Component<{
    entry: UpcomingSeriesEntry;
    layout: "grid" | "list";
  }> = (p) => (
    <div class="rounded border border-border bg-surface-2 p-1.5">
      <div class="truncate text-xs font-medium text-fg">
        {p.entry.seriesTitle} — {episodeCode(p.entry)}
      </div>
      <Show when={p.entry.episodeTitle}>
        <div class="truncate text-[11px] text-muted">{p.entry.episodeTitle}</div>
      </Show>
      <Show when={p.layout === "list"}>
        <div class="text-[11px] text-muted">{p.entry.airDate}</div>
      </Show>
    </div>
  );

  const DayEntries: Component<{ dayKey: string; layout: "grid" | "list" }> = (
    p,
  ) => (
    <Show
      when={content() === "movies"}
      fallback={
        <For each={seriesBuckets().get(p.dayKey) ?? []}>
          {(e) => <SeriesCell entry={e} layout={p.layout} />}
        </For>
      }
    >
      <For each={movieBuckets().get(p.dayKey) ?? []}>
        {(e) => <MovieCell entry={e} layout={p.layout} />}
      </For>
    </Show>
  );

  const countFor = (key: string) =>
    content() === "movies"
      ? (movieBuckets().get(key) ?? []).length
      : (seriesBuckets().get(key) ?? []).length;

  const GridView: Component = () => (
    <div class="overflow-x-auto">
      {/* Seven columns of a poster-bearing cell; narrower than
          discover/CalendarView's 106rem because this cell's poster is
          cell-width rather than a fixed 220px card. */}
      <div class="min-w-[70rem]">
        <div class="grid grid-cols-7 gap-2">
          <For each={WEEKDAYS}>
            {(w) => (
              <div class="px-1 text-xs font-semibold uppercase tracking-wide text-muted">
                {w}
              </div>
            )}
          </For>
        </div>
        <div class="mt-1 grid grid-cols-7 gap-2">
          <For each={cells(props.year, props.month)}>
            {(day) => (
              <div
                class="min-h-[6rem] rounded-lg border border-border p-1.5"
                classList={{
                  "bg-surface/40": day !== null,
                  "bg-transparent": day === null,
                }}
              >
                <Show when={day !== null}>
                  <div class="mb-1 text-xs font-medium text-muted">{day}</div>
                  <div class="flex flex-col gap-2">
                    <DayEntries
                      dayKey={dayKey(props.year, props.month, day!)}
                      layout="grid"
                    />
                  </div>
                </Show>
              </div>
            )}
          </For>
        </div>
      </div>
    </div>
  );

  const ListView: Component = () => (
    <div class="flex flex-col gap-3">
      <For each={dayKeys().filter((k) => countFor(k) > 0)}>
        {(key) => (
          <div>
            <div class="mb-1 text-xs font-semibold text-muted">{key}</div>
            <div class="flex flex-col gap-1.5">
              <DayEntries dayKey={key} layout="list" />
            </div>
          </div>
        )}
      </For>
      <Show when={!data.loading && !hasEntries()}>
        <Muted>Nothing upcoming this month.</Muted>
      </Show>
    </div>
  );

  return (
    <div>
      <ScreenTabBar
        tabs={CONTENT_TABS}
        current={content}
        onSelect={(id) => setContent(id as UpcomingContent)}
      />

      <Show when={data.loading}>
        <Muted class="!text-xs">Loading…</Muted>
      </Show>
      <Show when={data.error}>
        <ErrorText>Could not load upcoming releases.</ErrorText>
      </Show>
      <Show when={notice()}>
        <div role="status" class="mb-2 text-sm text-fg">
          {notice()}
        </div>
      </Show>
      <Show when={failure()}>
        <ErrorText>{failure()}</ErrorText>
      </Show>

      <Show when={props.display === "grid"} fallback={<ListView />}>
        <GridView />
      </Show>
    </div>
  );
};
