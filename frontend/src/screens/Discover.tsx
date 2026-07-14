// Discover — the Seerr-inspired browse landing, now MUTATING (Stage 2). The
// Mainstream tab is a search bar over four stacked, independently-paginated TMDB
// category rows (Trending/Popular × Movies/Series) plus a paginated "In your
// library" row of what's already tracked; the Adult tab is an unchanged TPDB
// scene grid. Discovery is sourced purely from TMDB/TPDB (and the local
// library) — Prowlarr is never consulted here; it's only involved later, when a
// grab actually retrieves a title. Poster/scene art renders ONLY through the
// image proxy (src/api/discover.ts's proxyImage/tmdbPoster), never hot-linked
// from TMDB/TPDB (plan Decision #7).
//
// One-click auto-grab (plan Decision #5): a card's "Grab" triggers the backend
// auto-grab — search + bitrate-quality-floor scoring — which either grabs the
// top qualifier outright or returns a ranked manual pick list when nothing
// clears the floor (never a silent failure, never "grab the least-bad option").
// Per-mode nuance is respected exactly:
//   - Movies: one click grabs directly (the clean 1-poster=1-title case).
//   - Series: one click opens a season/episode picker FIRST — "one click per
//     season/episode selection", since no release exists to score until a
//     specific episode/pack is chosen. Season-0/Specials is preserved:
//     submitting the picker always sets seasonSpecified=true (a bare season
//     number can't tell "Season 0 picked" from "no season picked").
//   - Adult: one click grabs a scene, sourcing the bitrate scorer's runtime
//     from the scene's TPDB durationSeconds.
// No bulk actions anywhere (Guardrail #3): every affordance grabs exactly one
// title/episode/scene per click.

import {
  type Component,
  type JSX,
  createEffect,
  createResource,
  createSignal,
  on,
  For,
  Show,
  Switch,
  Match,
} from "solid-js";
import {
  type AdultCategory,
  type AdultDiscoverItem,
  type DiscoverItem,
  type DiscoverCategory,
  type Mode,
  type PerformerSummary,
  type StudioSummary,
  fetchAdultDiscover,
  fetchAdultDiscoverCategory,
  fetchAdultPerformerScenes,
  fetchAdultPerformers,
  fetchAdultStudioScenes,
  fetchAdultStudios,
  fetchDiscover,
  fetchTitlePoster,
  fetchTmdbSearch,
  proxyImage,
  tmdbPoster,
} from "../api/discover";
import { type TrackedItem, fetchTrackedItems } from "../api/tag";
import {
  type AutoGrabCandidate,
  type AutoGrabRequest,
  type AutoGrabResponse,
  autoGrab,
  libraryRootFolder,
  manualGrab,
} from "../api/grab";
import {
  type TabDef,
  Button,
  ErrorText,
  Muted,
  ScreenTabs,
  yearOf,
} from "../components/ui";
import { buildConnectionUpsertBody, upsertConnection } from "../api/settings";

// MAINSTREAM_TITLE is the mode a merged card belongs to — the per-item mode a
// combined (movies+series) row/grid MUST carry so each card grabs via its own
// path: a Series card first opens the season/episode picker, a Movies card
// grabs directly. Passing one fixed mode across a mixed row would silently
// route a series through the movie grab path, breaking auto-grab.
type ModedTitle = { mode: "movies" | "series"; item: DiscoverItem };

// MAINSTREAM_ROWS is the fixed set of TMDB category rows the Mainstream page
// stacks: both modes × both categories. Each row paginates independently.
const MAINSTREAM_ROWS: {
  title: string;
  mode: "movies" | "series";
  category: DiscoverCategory;
}[] = [
  { title: "Trending Movies", mode: "movies", category: "trending" },
  { title: "Trending Shows", mode: "series", category: "trending" },
  { title: "Popular Movies", mode: "movies", category: "popular" },
  { title: "Popular Shows", mode: "series", category: "popular" },
];

// MAINSTREAM_TABS replaces the old Movies/Series/Adult set: Mainstream (all
// TMDB titles, both modes combined on one page) and Adult (unchanged TPDB view).
const MAINSTREAM_TABS: TabDef[] = [
  { id: "mainstream", label: "Mainstream" },
  { id: "adult", label: "Adult" },
];

// GrabTarget is one pending auto-grab: which mode, a human label for the
// dialog title, and the exact request body the backend needs. For Series the
// season/episode picker has already resolved before a target exists.
type GrabTarget = { mode: Mode; label: string; request: AutoGrabRequest };

// STATUS_COPY turns an autograb.Grade Status into a short human reason for a
// fallback pick-list row — so the operator sees WHY each release wasn't
// auto-picked, not a bare rejected flag.
const STATUS_COPY: Record<string, string> = {
  qualified: "meets the bar",
  "below-floor": "below the quality floor",
  mislabeled: "looks mislabeled",
  "low-seeders": "too few seeders",
  "unknown-bitrate": "runtime unknown — bitrate not scored",
  "unknown-resolution": "resolution not recognized",
};

// Modal is a lightweight centered overlay for the grab dialog. Clicking the
// backdrop or Close dismisses it; clicks inside don't bubble out.
const Modal: Component<{
  title: string;
  onClose: () => void;
  children: JSX.Element;
}> = (props) => (
  <div
    class="fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4"
    onClick={props.onClose}
  >
    <div
      class="max-h-[85vh] w-full max-w-lg overflow-y-auto rounded-xl border border-border bg-surface p-5 shadow-lg"
      onClick={(e) => e.stopPropagation()}
    >
      <div class="mb-3 flex items-center justify-between gap-3">
        <h3 class="truncate text-base font-semibold text-fg">{props.title}</h3>
        <Button onClick={props.onClose}>Close</Button>
      </div>
      {props.children}
    </div>
  </div>
);

// NOT_CONFIGURED_SERVICES maps the two external services Discover itself
// depends on (backend errors are the fixed strings "tmdb isn't configured
// yet — add it in Settings first" / "tpdb isn't configured yet — add it in
// Settings first", see internal/api/discover.go and adultdiscover.go) to
// their fixed base URL (both are external APIs with one canonical endpoint,
// not self-hosted — the operator only ever needs to supply a key, unlike
// Prowlarr/qBittorrent/etc.) and the external page to obtain a key. TMDB's
// is well-known and stable; TPDB's was confirmed directly by Wade
// (2026-07-13) rather than guessed, since it isn't discoverable from a
// plain page fetch (the site is JS-rendered).
const NOT_CONFIGURED_SERVICES: Record<
  "tmdb" | "tpdb",
  { label: string; url: string; keyPageUrl: string; keyPageLabel: string }
> = {
  tmdb: {
    label: "TMDB",
    url: "https://api.themoviedb.org/3",
    keyPageUrl: "https://www.themoviedb.org/settings/api",
    keyPageLabel: "themoviedb.org/settings/api",
  },
  tpdb: {
    label: "TPDB",
    url: "https://api.theporndb.net",
    keyPageUrl: "https://theporndb.net/user/api-tokens",
    keyPageLabel: "theporndb.net/user/api-tokens",
  },
};

// notConfiguredService detects which (if either) of Discover's two external
// dependencies a resource error is reporting missing, by matching the
// backend's fixed error string — returns undefined for any other error (a
// genuine network failure, a 500, etc.), which callers fall back to
// ErrorText for instead of assuming it's a "go configure this" case.
function notConfiguredService(
  err: unknown,
): "tmdb" | "tpdb" | undefined {
  const msg = (err as Error)?.message ?? "";
  if (!/isn't configured yet/i.test(msg)) return undefined;
  if (/\btmdb\b/i.test(msg)) return "tmdb";
  if (/\btpdb\b/i.test(msg)) return "tpdb";
  return undefined;
}

// ConfigureConnectionModal — shown instead of a bare error message when
// Discover detects TMDB/TPDB isn't configured. Saves directly into the same
// connection store Settings' own form writes to (upsertConnection/
// buildConnectionUpsertBody, reused verbatim, not duplicated) so there's
// exactly one place that actually persists a connection — this is just a
// second, more contextual entry point into it. First-time save, so
// hasExistingKey is always false and keyTouched is always true here (see
// buildConnectionUpsertBody's own doc comment on why that combination is
// safe: a first save always sends the key, even if it were left blank).
const ConfigureConnectionModal: Component<{
  service: "tmdb" | "tpdb";
  onClose: () => void;
  onSaved: () => void;
}> = (props) => {
  const info = NOT_CONFIGURED_SERVICES[props.service];
  const [key, setKey] = createSignal("");
  const [saving, setSaving] = createSignal(false);
  const [error, setError] = createSignal("");

  const save = async () => {
    setError("");
    if (!key().trim()) {
      setError("Enter an API key first.");
      return;
    }
    setSaving(true);
    try {
      await upsertConnection(
        props.service,
        buildConnectionUpsertBody({
          url: info.url,
          needsUsername: false,
          keyTouched: true,
          keyValue: key(),
          hasExistingKey: false,
        }),
      );
      props.onSaved();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setSaving(false);
    }
  };

  return (
    <Modal title={`Set up ${info.label}`} onClose={props.onClose}>
      <p class="mb-3 text-sm text-muted">
        {info.label} isn't configured yet — Discover needs it to browse{" "}
        {props.service === "tpdb" ? "Adult scenes" : "titles"}. Paste an API
        key below to enable it now, or add it later in Settings.
      </p>
      <a
        href={info.keyPageUrl}
        target="_blank"
        rel="noreferrer"
        class="mb-3 block text-sm text-accent underline"
      >
        Get an API key at {info.keyPageLabel}
      </a>
      <input
        type="password"
        class="w-full rounded-md border border-border bg-bg px-3 py-2 text-sm text-fg outline-none focus:border-accent"
        placeholder="API key"
        value={key()}
        onInput={(e) => setKey(e.currentTarget.value)}
      />
      <Show when={error()}>
        <ErrorText>{error()}</ErrorText>
      </Show>
      <div class="mt-3 flex justify-end gap-2">
        <Button onClick={props.onClose}>Cancel</Button>
        <Button variant="primary" onClick={save} disabled={saving()}>
          {saving() ? "Saving…" : "Save"}
        </Button>
      </div>
    </Modal>
  );
};

// FallbackPickList renders the ranked manual pick list the backend returns when
// nothing auto-qualified. Each row labels why it wasn't auto-picked and offers
// a single "Grab this" — one release per click, never a batch.
const FallbackPickList: Component<{
  response: AutoGrabResponse;
  onPick: (c: AutoGrabCandidate) => void;
  grabbing: string;
  error: string;
}> = (props) => (
  <div>
    <Muted class="mb-2">{props.response.message}</Muted>
    <Show when={props.error}>
      <ErrorText>{props.error}</ErrorText>
    </Show>
    <Show
      when={(props.response.candidates ?? []).length > 0}
      fallback={<Muted>No releases found for this title.</Muted>}
    >
      <ul class="flex flex-col gap-2">
        <For each={props.response.candidates}>
          {(c) => (
            <li class="flex items-center gap-3 rounded-md border border-border bg-surface-2 p-2">
              <div class="min-w-0 flex-1">
                <div class="truncate text-sm text-fg" title={c.title}>
                  {c.title}
                </div>
                <div class="truncate text-xs text-muted">
                  {[c.indexer, c.protocol, STATUS_COPY[c.status] ?? c.status]
                    .filter(Boolean)
                    .join(" · ")}
                </div>
              </div>
              <Button
                onClick={() => props.onPick(c)}
                disabled={!!props.grabbing}
              >
                {props.grabbing === c.downloadUrl ? "Grabbing…" : "Grab this"}
              </Button>
            </li>
          )}
        </For>
      </ul>
    </Show>
  </div>
);

// GrabDialog fires the auto-grab for a target on mount, then shows the outcome:
// a success line when the backend grabbed the top qualifier, or the manual pick
// list when it fell back. The manual pick reuses the existing /search/grab
// endpoint (auto-grab resolves the root folder server-side; the fallback path
// must fetch it explicitly).
const GrabDialog: Component<{ target: GrabTarget; onClose: () => void }> = (
  props,
) => {
  const [result] = createResource(
    () => props.target,
    (t) => autoGrab(t.mode, t.request),
  );
  const [grabbing, setGrabbing] = createSignal("");
  const [manualError, setManualError] = createSignal("");
  const [manualGrabbed, setManualGrabbed] = createSignal<string | null>(null);

  const pickManual = async (c: AutoGrabCandidate) => {
    setManualError("");
    setGrabbing(c.downloadUrl);
    try {
      const root = await libraryRootFolder(props.target.mode);
      if (!root) {
        throw new Error(
          "no root folder configured for this mode — set one in Settings first",
        );
      }
      await manualGrab(props.target.mode, {
        title: props.target.request.title,
        tmdbId: props.target.request.tmdbId,
        seasonNumber: props.target.request.seasonNumber,
        episodeNumber: props.target.request.episodeNumber,
        seasonSpecified: props.target.request.seasonSpecified,
        indexer: c.indexer,
        protocol: c.protocol,
        downloadUrl: c.downloadUrl,
        rootFolderPath: root,
      });
      setManualGrabbed(c.title);
    } catch (e) {
      setManualError((e as Error).message);
    } finally {
      setGrabbing("");
    }
  };

  return (
    <Modal title={`Grab — ${props.target.label}`} onClose={props.onClose}>
      <Show
        when={!result.loading}
        fallback={<Muted>Searching and scoring releases…</Muted>}
      >
        <Show when={result.error}>
          <ErrorText>{(result.error as Error)?.message}</ErrorText>
        </Show>
        <Show when={result()}>
          {(r) => (
            <Switch>
              <Match when={r().grabbed}>
                <div class="text-sm text-ok">{r().message}</div>
                <Muted class="mt-1">
                  Tracked in the Grabs view — check import there once it finishes
                  downloading.
                </Muted>
              </Match>
              <Match when={r().fallback}>
                <Show
                  when={manualGrabbed()}
                  fallback={
                    <FallbackPickList
                      response={r()}
                      onPick={pickManual}
                      grabbing={grabbing()}
                      error={manualError()}
                    />
                  }
                >
                  <div class="text-sm text-ok">
                    Grabbed “{manualGrabbed()}”. Tracked in the Grabs view.
                  </div>
                </Show>
              </Match>
            </Switch>
          )}
        </Show>
      </Show>
    </Modal>
  );
};

// SeasonEpisodePicker gates a Series grab: no release can be scored until a
// specific season (and optionally episode) is chosen. Submitting always marks
// the season as specified — that is what preserves Season-0/Specials (a bare
// season number can't distinguish "Season 0 picked" from "nothing picked").
const SeasonEpisodePicker: Component<{
  onSubmit: (season: number, episode: number) => void;
}> = (props) => {
  const [season, setSeason] = createSignal("");
  const [episode, setEpisode] = createSignal("");
  return (
    <form
      class="mt-1 flex items-center gap-1"
      onSubmit={(e) => {
        e.preventDefault();
        props.onSubmit(
          parseInt(season(), 10) || 0,
          parseInt(episode(), 10) || 0,
        );
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
        class="rounded bg-accent px-2 py-0.5 text-xs font-medium text-accent-fg"
      >
        Go
      </button>
    </form>
  );
};

// GrabButton is the per-title grab affordance. Movies grab on click. Series
// first reveal the season/episode picker (the gating step) and only build a
// GrabTarget once the picker is submitted.
const GrabButton: Component<{
  mode: "movies" | "series";
  item: DiscoverItem;
  onGrab: (t: GrabTarget) => void;
}> = (props) => {
  const [picking, setPicking] = createSignal(false);

  const grabMovie = () =>
    props.onGrab({
      mode: "movies",
      label: props.item.title,
      request: { title: props.item.title, tmdbId: props.item.id },
    });

  const grabSeries = (season: number, episode: number) => {
    setPicking(false);
    const suffix = `S${season}${episode ? "E" + episode : ""}`;
    props.onGrab({
      mode: "series",
      label: `${props.item.title} ${suffix}`,
      request: {
        title: props.item.title,
        tmdbId: props.item.id,
        seasonNumber: season,
        episodeNumber: episode,
        seasonSpecified: true,
      },
    });
  };

  return (
    <Show
      when={props.mode === "series"}
      fallback={
        <Button class="w-full !py-1 text-xs" onClick={grabMovie}>
          Grab
        </Button>
      }
    >
      <Show
        when={picking()}
        fallback={
          <Button class="w-full !py-1 text-xs" onClick={() => setPicking(true)}>
            Grab
          </Button>
        }
      >
        <SeasonEpisodePicker onSubmit={grabSeries} />
      </Show>
    </Show>
  );
};

// TextPoster is the fallback tile when no art exists (TMDB/TPDB returned a
// blank poster/image) — a titled placeholder that keeps the card's footprint
// identical to an image card so rows don't reflow.
const TextPoster: Component<{ label: string }> = (props) => (
  <div class="flex h-full w-full items-center justify-center bg-surface-2 p-2 text-center text-xs text-muted">
    {props.label}
  </div>
);

// PosterCard is one Movies/Series title. Fixed width so a row scrolls
// horizontally. The title attribute carries the overview as a native tooltip —
// "show more detail" without any click handler that could mutate.
const PosterCard: Component<{
  mode: "movies" | "series";
  item: DiscoverItem;
  onGrab: (t: GrabTarget) => void;
}> = (props) => {
  const src = () => tmdbPoster(props.item.posterPath);
  return (
    <div class="w-36 shrink-0" title={props.item.overview}>
      <div class="aspect-[2/3] overflow-hidden rounded-lg border border-border bg-surface">
        <Show when={src()} fallback={<TextPoster label={props.item.title} />}>
          <img
            src={src()}
            alt={props.item.title}
            loading="lazy"
            class="h-full w-full object-cover"
          />
        </Show>
      </div>
      <div class="mt-1.5 truncate text-sm text-fg" title={props.item.title}>
        {props.item.title}
      </div>
      <div class="flex items-center gap-2 text-xs text-muted">
        <span>{yearOf(props.item.releaseDate) ?? "—"}</span>
        <Show when={props.item.voteAverage > 0}>
          <span>★ {props.item.voteAverage.toFixed(1)}</span>
        </Show>
      </div>
      <div class="mt-1.5">
        <GrabButton mode={props.mode} item={props.item} onGrab={props.onGrab} />
      </div>
    </div>
  );
};

// PaginatedStrip is the generic "Show more" strip every Discover row is built
// from: a title, a horizontal (or, via containerClass, wrapping) list of cards,
// and a "Show more" that APPENDS the next page rather than replacing the strip —
// the accumulator (items) only ever grows. It reloads from page 1 whenever
// reloadToken changes (the setup-modal "I just configured it, refetch" signal).
// Fetch errors are reported up via onError so the parent can raise the
// not-configured setup modal once for the whole page, not per strip. The item
// type T and both the page loader (load) and the per-item renderer (children)
// are supplied by the caller, so one pagination engine backs the Mainstream
// TMDB rows, the Adult scene rows, the Studios/Performers browse rows, and the
// drill-down scene grid alike (plan: reuse the pattern, don't reimplement it).
function PaginatedStrip<T>(props: {
  title: string;
  reloadToken: () => number;
  load: (page: number) => Promise<T[]>;
  onError: (err: unknown) => void;
  containerClass?: string;
  children: (item: T) => JSX.Element;
  // singlePage suppresses "Show more" even when more data may exist — for
  // rows whose ordering is only meaningful within one fetched page (e.g.
  // Adult's "Highest Rated," a same-page rating re-sort with no true
  // server-side popularity sort behind it: paginating would append an
  // independently-resorted page 2 after page 1, producing a visibly
  // non-monotonic rating order under a "Highest Rated" label).
  singlePage?: boolean;
}): JSX.Element {
  const [items, setItems] = createSignal<T[]>([]);
  const [page, setPage] = createSignal(0);
  const [loading, setLoading] = createSignal(false);
  const [exhausted, setExhausted] = createSignal(false);

  const load = async (reset: boolean) => {
    const next = reset ? 1 : page() + 1;
    setLoading(true);
    try {
      const batch = await props.load(next);
      setItems((prev) => (reset ? batch : [...prev, ...batch]));
      setPage(next);
      if (batch.length === 0) setExhausted(true);
    } catch (e) {
      props.onError(e);
    } finally {
      setLoading(false);
    }
  };

  // Initial load AND reload-on-token in one effect (on() runs immediately by
  // default, so no separate onMount is needed).
  createEffect(
    on(props.reloadToken, () => {
      setItems([]);
      setPage(0);
      setExhausted(false);
      void load(true);
    }),
  );

  return (
    <section class="mt-6">
      <h2 class="mb-2 text-sm font-semibold uppercase tracking-wide text-muted">
        {props.title}
      </h2>
      <Show
        when={items().length > 0}
        fallback={
          <Muted>{loading() ? "Loading…" : "Nothing here yet."}</Muted>
        }
      >
        <div class={props.containerClass ?? "flex items-stretch gap-3 overflow-x-auto pb-2"}>
          <For each={items()}>{(item) => props.children(item)}</For>
          <Show when={!exhausted() && !props.singlePage}>
            <div class="flex w-28 shrink-0 items-center justify-center">
              <Button
                class="!py-1 text-xs"
                onClick={() => void load(false)}
                disabled={loading()}
              >
                {loading() ? "Loading…" : "Show more"}
              </Button>
            </div>
          </Show>
        </div>
      </Show>
    </section>
  );
}

// PaginatedRow is the Mainstream TMDB category strip (fixed mode + category) — a
// thin wrapper over PaginatedStrip that loads one TMDB category page and renders
// each result as a PosterCard.
const PaginatedRow: Component<{
  title: string;
  mode: "movies" | "series";
  category: DiscoverCategory;
  reloadToken: () => number;
  onGrab: (t: GrabTarget) => void;
  onError: (err: unknown) => void;
}> = (props) => (
  <PaginatedStrip
    title={props.title}
    reloadToken={props.reloadToken}
    load={(page) => fetchDiscover(props.mode, props.category, page)}
    onError={props.onError}
  >
    {(item) => (
      <PosterCard mode={props.mode} item={item} onGrab={props.onGrab} />
    )}
  </PaginatedStrip>
);

// LibraryCard is one owned-library title on the existing-library row. Its mode
// is per-item (the row mixes movies+series), which drives both the lazy poster
// fetch and the auto-grab path. The library caches no poster art, so the
// poster is resolved on demand by tmdbId (fetchTitlePoster) — one bounded call
// per rendered card, then routed through the image proxy exactly like every
// other card. A synthetic DiscoverItem (id = tmdbId) feeds GrabButton so a
// library card grabs through the identical GrabDialog/autoGrab path a Discover
// card does — Series still gets its season/episode picker.
const LibraryCard: Component<{
  mode: "movies" | "series";
  item: TrackedItem;
  onGrab: (t: GrabTarget) => void;
}> = (props) => {
  const tmdbId = () => props.item.tmdbId ?? 0;
  const [poster] = createResource(tmdbId, (id) =>
    id ? fetchTitlePoster(props.mode, id).catch(() => "") : Promise.resolve(""),
  );
  const src = () => tmdbPoster(poster() ?? "");
  const grabItem = (): DiscoverItem => ({
    id: tmdbId(),
    title: props.item.title,
    posterPath: poster() ?? "",
    overview: "",
    releaseDate: props.item.year ? String(props.item.year) : "",
    voteAverage: 0,
    mediaType: props.mode === "series" ? "tv" : "movie",
  });
  return (
    <div class="w-36 shrink-0" title={props.item.title}>
      <div class="aspect-[2/3] overflow-hidden rounded-lg border border-border bg-surface">
        <Show when={src()} fallback={<TextPoster label={props.item.title} />}>
          <img
            src={src()}
            alt={props.item.title}
            loading="lazy"
            class="h-full w-full object-cover"
          />
        </Show>
      </div>
      <div class="mt-1.5 truncate text-sm text-fg" title={props.item.title}>
        {props.item.title}
      </div>
      <div class="flex items-center gap-2 text-xs text-muted">
        <span>{props.item.year || "—"}</span>
      </div>
      <div class="mt-1.5">
        <GrabButton mode={props.mode} item={grabItem()} onGrab={props.onGrab} />
      </div>
    </div>
  );
};

// LIBRARY_PAGE_SIZE bounds how many library cards render (and therefore how many
// per-card poster fetches fire) at once, mirroring the category rows' "Show
// more" paging. Without this the whole tracked set mounts in one shot, firing a
// poster fetch per card — a real fan-out on a large library.
const LIBRARY_PAGE_SIZE = 20;

// LibraryRow surfaces what's already tracked, movies + series merged into one
// strip (each card tagged with its own mode). The full tracked set is fetched
// once (it's the operator's own bounded library, not TMDB's infinite catalog),
// but only one page's worth is rendered at a time behind a "Show more" — the
// same paging shape PaginatedRow uses — so DOM size and concurrent per-card
// poster fetches stay bounded. Reloads on reloadToken alongside the category
// rows; the visible count resets to one page on every reload.
const LibraryRow: Component<{
  reloadToken: () => number;
  onGrab: (t: GrabTarget) => void;
}> = (props) => {
  const [entries] = createResource(props.reloadToken, async () => {
    const [movies, series] = await Promise.all([
      fetchTrackedItems("movies").catch(() => [] as TrackedItem[]),
      fetchTrackedItems("series").catch(() => [] as TrackedItem[]),
    ]);
    return [
      ...movies.map((item) => ({ mode: "movies" as const, item })),
      ...series.map((item) => ({ mode: "series" as const, item })),
    ];
  });

  const [visible, setVisible] = createSignal(LIBRARY_PAGE_SIZE);
  createEffect(on(props.reloadToken, () => setVisible(LIBRARY_PAGE_SIZE)));

  const shown = () => (entries() ?? []).slice(0, visible());
  const hasMore = () => (entries()?.length ?? 0) > visible();

  return (
    <Show when={(entries()?.length ?? 0) > 0}>
      <section class="mt-6">
        <h2 class="mb-2 text-sm font-semibold uppercase tracking-wide text-muted">
          In your library
        </h2>
        <div class="flex items-stretch gap-3 overflow-x-auto pb-2">
          <For each={shown()}>
            {(e) => (
              <LibraryCard mode={e.mode} item={e.item} onGrab={props.onGrab} />
            )}
          </For>
          <Show when={hasMore()}>
            <div class="flex w-28 shrink-0 items-center justify-center">
              <Button
                class="!py-1 text-xs"
                onClick={() => setVisible((n) => n + LIBRARY_PAGE_SIZE)}
              >
                Show more
              </Button>
            </div>
          </Show>
        </div>
      </section>
    </Show>
  );
};

// MainstreamDiscover is the combined Movies+Series page: a search bar over four
// stacked TMDB category rows plus the existing-library row. Searching replaces
// the rows with one merged (movies+series) result grid; clearing restores the
// rows. It owns the single grab dialog for every card (rows, library, search)
// and the not-configured setup modal, raised once when any row's fetch reports
// TMDB missing.
const MainstreamDiscover: Component = () => {
  const [grabTarget, setGrabTarget] = createSignal<GrabTarget | null>(null);
  const [setupError, setSetupError] = createSignal<unknown>(null);
  const [dismissedSetup, setDismissedSetup] = createSignal(false);
  const [reloadToken, setReloadToken] = createSignal(0);

  // Search: draft is the input value, submitted is the committed query. A
  // non-empty submitted query swaps the rows for the merged result grid.
  const [draft, setDraft] = createSignal("");
  const [submitted, setSubmitted] = createSignal("");
  const searching = () => submitted().trim().length > 0;

  const [results] = createResource(
    () => (searching() ? submitted().trim() : null),
    async (q): Promise<ModedTitle[]> => {
      // A search error is surfaced the same way a category row's is: hand it to
      // setSetupError so a "tmdb isn't configured yet" failure raises the same
      // setup modal (the render's notConfiguredService gate decides modal vs.
      // plain error), instead of being swallowed into an empty "No results
      // found". Reusing the row plumbing keeps one detection path, not two.
      try {
        const [movies, series] = await Promise.all([
          fetchTmdbSearch("movies", q),
          fetchTmdbSearch("series", q),
        ]);
        return [
          ...movies.map((item) => ({ mode: "movies" as const, item })),
          ...series.map((item) => ({ mode: "series" as const, item })),
        ];
      } catch (e) {
        setSetupError(e);
        return [];
      }
    },
  );

  const clearSearch = () => {
    setDraft("");
    setSubmitted("");
  };

  const configureFor = () => notConfiguredService(setupError());

  return (
    <div>
      <form
        class="mb-4 flex gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          setSubmitted(draft());
        }}
      >
        <input
          class="w-full max-w-sm rounded-md border border-border bg-bg px-3 py-2 text-sm text-fg outline-none focus:border-accent"
          placeholder="Search movies & shows…"
          value={draft()}
          onInput={(e) => setDraft(e.currentTarget.value)}
        />
        <Show when={searching()}>
          <Button onClick={clearSearch}>Clear</Button>
        </Show>
      </form>

      <Show when={setupError()}>
        <Show
          when={!dismissedSetup() && configureFor()}
          fallback={<ErrorText>{(setupError() as Error)?.message}</ErrorText>}
        >
          {(service) => (
            <ConfigureConnectionModal
              service={service()}
              onClose={() => setDismissedSetup(true)}
              onSaved={() => {
                setDismissedSetup(true);
                setSetupError(null);
                setReloadToken((n) => n + 1);
              }}
            />
          )}
        </Show>
      </Show>

      <Show
        when={searching()}
        fallback={
          <>
            <For each={MAINSTREAM_ROWS}>
              {(row) => (
                <PaginatedRow
                  title={row.title}
                  mode={row.mode}
                  category={row.category}
                  reloadToken={reloadToken}
                  onGrab={setGrabTarget}
                  onError={setSetupError}
                />
              )}
            </For>
            <LibraryRow reloadToken={reloadToken} onGrab={setGrabTarget} />
          </>
        }
      >
        <section class="mt-2">
          <h2 class="mb-2 text-sm font-semibold uppercase tracking-wide text-muted">
            Search results
          </h2>
          <Show when={!results.loading} fallback={<Muted>Searching…</Muted>}>
            <Show
              when={(results()?.length ?? 0) > 0}
              fallback={<Muted>No results found.</Muted>}
            >
              <div class="flex flex-wrap gap-3">
                <For each={results()}>
                  {(e) => (
                    <PosterCard
                      mode={e.mode}
                      item={e.item}
                      onGrab={setGrabTarget}
                    />
                  )}
                </For>
              </div>
            </Show>
          </Show>
        </section>
      </Show>

      <Show when={grabTarget()}>
        {(t) => <GrabDialog target={t()} onClose={() => setGrabTarget(null)} />}
      </Show>
    </div>
  );
};

// AdultCard is one TPDB scene. TPDB frequently returns no art, so the image is
// Show-guarded with a text fallback (the old frontend rendered Adult text-only;
// this adds art where TPDB provides it, via the proxy).
const AdultCard: Component<{
  item: AdultDiscoverItem;
  onGrab: (t: GrabTarget) => void;
}> = (props) => {
  const src = () => proxyImage(props.item.image);
  const subtitle = () =>
    [props.item.studio, yearOf(props.item.date)].filter(Boolean).join(" · ");
  const grab = () =>
    props.onGrab({
      mode: "adult",
      label: props.item.title,
      request: {
        title: props.item.title,
        studio: props.item.studio,
        durationSeconds: props.item.durationSeconds,
      },
    });
  return (
    <div class="w-40 shrink-0" title={props.item.title}>
      <div class="aspect-video overflow-hidden rounded-lg border border-border bg-surface">
        <Show when={src()} fallback={<TextPoster label={props.item.title} />}>
          <img
            src={src()}
            alt={props.item.title}
            loading="lazy"
            class="h-full w-full object-cover"
          />
        </Show>
      </div>
      <div class="mt-1.5 truncate text-sm text-fg">{props.item.title}</div>
      <div class="truncate text-xs text-muted">{subtitle() || "—"}</div>
      <div class="mt-1.5">
        <Button class="w-full !py-1 text-xs" onClick={grab}>
          Grab
        </Button>
      </div>
    </div>
  );
};

// EntityCard is one Studio or Performer on the Adult browse rows — image-or-text
// tile + name, no grab (these are pure browse/navigation, not gradable items).
// TPDB frequently returns no art, so the image is Show-guarded with a text
// fallback and any non-empty URL is routed through the proxy (never hot-linked).
// The whole card is a button: clicking it drills down into that entity's scenes.
const EntityCard: Component<{
  name: string;
  image: string;
  onSelect: () => void;
}> = (props) => {
  const src = () => proxyImage(props.image);
  return (
    <button
      type="button"
      class="w-40 shrink-0 text-left"
      title={props.name}
      onClick={props.onSelect}
    >
      <div class="aspect-video overflow-hidden rounded-lg border border-border bg-surface">
        <Show when={src()} fallback={<TextPoster label={props.name} />}>
          <img
            src={src()}
            alt={props.name}
            loading="lazy"
            class="h-full w-full object-cover"
          />
        </Show>
      </div>
      <div class="mt-1.5 truncate text-sm text-fg">{props.name}</div>
    </button>
  );
};

// ADULT_SCENE_ROWS is the fixed pair of ordered TPDB scene feeds the Adult
// browse stacks: Recently Released (TPDB's real recency sort, pages normally)
// and Highest Rated (a page-local rating re-sort, honestly NOT a global
// popularity ranking — see internal/api/adultdiscover.go). Highest Rated is
// singlePage: "Show more" would append an independently-resorted page 2 after
// page 1, producing a visibly non-monotonic rating order under that label.
const ADULT_SCENE_ROWS: { title: string; category: AdultCategory; singlePage?: boolean }[] = [
  { title: "Recently Released", category: "recent" },
  { title: "Highest Rated", category: "top-rated", singlePage: true },
];

// AdultDrill is the active drill-down target: which entity kind, its opaque TPDB
// id (passed verbatim to the drill-down endpoint), and its name for the header.
type AdultDrill = { kind: "studio" | "performer"; id: string; name: string };

// AdultDiscover is the scene-shaped browse, row-based like Mainstream: a search
// bar over two ordered scene rows (Recently Released, Highest Rated), a Studios
// row, and a Performers row. Searching swaps the rows for a plain result grid;
// clicking a Studio/Performer card drills down into a paginated grid of just
// that entity's scenes (with a "Back to browse" control). Owns the single grab
// dialog for every scene card (rows, search, drill-down) and the not-configured
// setup modal, raised once when any strip's fetch reports TPDB missing.
const AdultDiscover: Component = () => {
  const [grabTarget, setGrabTarget] = createSignal<GrabTarget | null>(null);
  const [setupError, setSetupError] = createSignal<unknown>(null);
  const [dismissedSetup, setDismissedSetup] = createSignal(false);
  const [reloadToken, setReloadToken] = createSignal(0);

  const [draft, setDraft] = createSignal("");
  const [submitted, setSubmitted] = createSignal("");
  const searching = () => submitted().trim().length > 0;

  // drill is the active Studio/Performer drill-down (null = the browse rows).
  const [drill, setDrill] = createSignal<AdultDrill | null>(null);

  const [results] = createResource(
    () => (searching() ? submitted().trim() : null),
    async (q): Promise<AdultDiscoverItem[]> => {
      // A search error is surfaced the same way a row's is: handed to
      // setSetupError so a "tpdb isn't configured yet" failure raises the same
      // setup modal (the render's notConfiguredService gate decides modal vs.
      // plain error), instead of being swallowed into an empty "No scenes
      // found". One detection path for every Adult fetch, not two.
      try {
        return await fetchAdultDiscover(q);
      } catch (e) {
        setSetupError(e);
        return [];
      }
    },
  );

  const clearSearch = () => {
    setDraft("");
    setSubmitted("");
  };

  const configureFor = () => notConfiguredService(setupError());

  return (
    <div>
      <form
        class="mb-4 flex gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          // A new search takes precedence over any drill-down (Clear returns to
          // the rows, not back into a stale drill).
          setDrill(null);
          setSubmitted(draft());
        }}
      >
        <input
          class="w-full max-w-sm rounded-md border border-border bg-bg px-3 py-2 text-sm text-fg outline-none focus:border-accent"
          placeholder="Search scenes by title…"
          value={draft()}
          onInput={(e) => setDraft(e.currentTarget.value)}
        />
        <Show when={searching()}>
          <Button onClick={clearSearch}>Clear</Button>
        </Show>
      </form>

      <Show when={setupError()}>
        <Show
          when={!dismissedSetup() && configureFor()}
          fallback={<ErrorText>{(setupError() as Error)?.message}</ErrorText>}
        >
          {(service) => (
            <ConfigureConnectionModal
              service={service()}
              onClose={() => setDismissedSetup(true)}
              onSaved={() => {
                setDismissedSetup(true);
                setSetupError(null);
                setReloadToken((n) => n + 1);
              }}
            />
          )}
        </Show>
      </Show>

      <Show
        when={searching()}
        fallback={
          <Show
            when={drill()}
            fallback={
              <>
                <For each={ADULT_SCENE_ROWS}>
                  {(row) => (
                    <PaginatedStrip
                      title={row.title}
                      reloadToken={reloadToken}
                      load={(page) =>
                        fetchAdultDiscoverCategory(row.category, page)
                      }
                      onError={setSetupError}
                      singlePage={row.singlePage}
                    >
                      {(item) => (
                        <AdultCard item={item} onGrab={setGrabTarget} />
                      )}
                    </PaginatedStrip>
                  )}
                </For>
                <PaginatedStrip<StudioSummary>
                  title="Studios"
                  reloadToken={reloadToken}
                  load={(page) => fetchAdultStudios(page)}
                  onError={setSetupError}
                >
                  {(s) => (
                    <EntityCard
                      name={s.name}
                      image={s.image}
                      onSelect={() =>
                        setDrill({ kind: "studio", id: s.id, name: s.name })
                      }
                    />
                  )}
                </PaginatedStrip>
                <PaginatedStrip<PerformerSummary>
                  title="Performers"
                  reloadToken={reloadToken}
                  load={(page) => fetchAdultPerformers(page)}
                  onError={setSetupError}
                >
                  {(p) => (
                    <EntityCard
                      name={p.name}
                      image={p.image}
                      onSelect={() =>
                        setDrill({ kind: "performer", id: p.id, name: p.name })
                      }
                    />
                  )}
                </PaginatedStrip>
              </>
            }
          >
            {(d) => (
              <div>
                <div class="mb-2 flex items-center gap-3">
                  <Button class="!py-1 text-xs" onClick={() => setDrill(null)}>
                    Back to browse
                  </Button>
                </div>
                <PaginatedStrip
                  title={d().name}
                  reloadToken={reloadToken}
                  load={(page) =>
                    d().kind === "studio"
                      ? fetchAdultStudioScenes(d().id, page)
                      : fetchAdultPerformerScenes(d().id, page)
                  }
                  onError={setSetupError}
                  containerClass="flex flex-wrap gap-3"
                >
                  {(item) => <AdultCard item={item} onGrab={setGrabTarget} />}
                </PaginatedStrip>
              </div>
            )}
          </Show>
        }
      >
        <section class="mt-2">
          <h2 class="mb-2 text-sm font-semibold uppercase tracking-wide text-muted">
            Search results
          </h2>
          <Show when={!results.loading} fallback={<Muted>Searching…</Muted>}>
            <Show
              when={(results()?.length ?? 0) > 0}
              fallback={<Muted>No scenes found.</Muted>}
            >
              <div class="flex flex-wrap gap-3">
                <For each={results()}>
                  {(item) => <AdultCard item={item} onGrab={setGrabTarget} />}
                </For>
              </div>
            </Show>
          </Show>
        </section>
      </Show>

      <Show when={grabTarget()}>
        {(t) => <GrabDialog target={t()} onClose={() => setGrabTarget(null)} />}
      </Show>
    </div>
  );
};

// Discover is the tab shell: Mainstream (combined Movies+Series) / Adult. Tabs
// register with the app shell (which draws the bar in its consistent location);
// rendered standalone (a unit test with no shell context) it falls back to
// drawing the bar inline, the same pattern ModeTabs uses — so tests can still
// click "Adult" without mounting the whole shell.
export const Discover: Component = () => {
  const [tab, setTab] = createSignal("mainstream");
  return (
    <div>
      <ScreenTabs
        tabs={MAINSTREAM_TABS}
        current={tab}
        onSelect={setTab}
        class="flex gap-1"
      />
      <div class="mt-4">
        <Switch>
          <Match when={tab() === "adult"}>
            <AdultDiscover />
          </Match>
          <Match when={tab() === "mainstream"}>
            <MainstreamDiscover />
          </Match>
        </Switch>
      </div>
    </div>
  );
};
