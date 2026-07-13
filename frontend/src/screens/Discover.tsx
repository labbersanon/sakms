// Discover — the Seerr-inspired browse landing, now MUTATING (Stage 2). Hero
// banner + horizontal category rows for Movies/Series, a scene grid for Adult.
// Poster/scene art renders ONLY through the image proxy (src/api/discover.ts's
// proxyImage/tmdbPoster), never hot-linked from TMDB/TPDB (plan Decision #7).
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
  createResource,
  createSignal,
  For,
  Show,
  Switch,
  Match,
} from "solid-js";
import {
  type AdultDiscoverItem,
  type AvailabilityResponse,
  type DiscoverItem,
  type Mode,
  fetchAdultAvailability,
  fetchAdultDiscover,
  fetchDiscover,
  fetchTitleAvailability,
  proxyImage,
  tmdbHero,
  tmdbPoster,
} from "../api/discover";
import {
  type AutoGrabCandidate,
  type AutoGrabRequest,
  type AutoGrabResponse,
  autoGrab,
  libraryRootFolder,
  manualGrab,
} from "../api/grab";
import {
  Button,
  ErrorText,
  ModeTabs,
  Muted,
  yearOf,
} from "../components/ui";
import { buildConnectionUpsertBody, upsertConnection } from "../api/settings";

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

// AvailabilityBadge renders the outcome of a per-card availability probe. It is
// deliberately quiet on failure: Prowlarr may not be configured (a 400/502),
// which must not break the card — it just shows no badge. Loading shows a
// neutral pill so the grid doesn't jump.
const AvailabilityBadge: Component<{
  result: AvailabilityResponse | null | undefined;
  loading: boolean;
}> = (props) => (
  <Show
    when={!props.loading}
    fallback={
      <span class="inline-block rounded-full bg-surface-2 px-2 py-0.5 text-[11px] text-muted">
        checking…
      </span>
    }
  >
    <Show when={props.result}>
      {(r) => (
        <span
          class="inline-block rounded-full px-2 py-0.5 text-[11px] font-medium"
          classList={{
            "bg-ok/20 text-ok": r().available,
            "bg-surface-2 text-muted": !r().available,
          }}
        >
          {r().available ? `${r().releaseCount} available` : "no release"}
        </span>
      )}
    </Show>
  </Show>
);

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
  const [avail] = createResource(
    () => props.item.id,
    (id) => fetchTitleAvailability(props.mode, id).catch(() => null),
  );
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
      <div class="mt-1">
        <AvailabilityBadge result={avail()} loading={avail.loading} />
      </div>
      <div class="mt-1.5">
        <GrabButton mode={props.mode} item={props.item} onGrab={props.onGrab} />
      </div>
    </div>
  );
};

// Row is one horizontal, scrollable category strip.
const Row: Component<{
  title: string;
  mode: "movies" | "series";
  items: DiscoverItem[] | undefined;
  loading: boolean;
  onGrab: (t: GrabTarget) => void;
}> = (props) => (
  <section class="mt-6">
    <h2 class="mb-2 text-sm font-semibold uppercase tracking-wide text-muted">
      {props.title}
    </h2>
    <Show when={!props.loading} fallback={<Muted>Loading…</Muted>}>
      <Show
        when={props.items && props.items.length > 0}
        fallback={<Muted>Nothing here yet.</Muted>}
      >
        <div class="flex gap-3 overflow-x-auto pb-2">
          <For each={props.items}>
            {(item) => (
              <PosterCard
                mode={props.mode}
                item={item}
                onGrab={props.onGrab}
              />
            )}
          </For>
        </div>
      </Show>
    </Show>
  </section>
);

// Hero is the top trending title, rendered wide with its backdrop/poster and
// overview — the Seerr-style banner, now with its own one-click Grab.
const Hero: Component<{
  item: DiscoverItem | undefined;
  mode: "movies" | "series";
  onGrab: (t: GrabTarget) => void;
}> = (props) => (
  <Show when={props.item}>
    {(item) => {
      const src = () => tmdbHero(item().posterPath);
      return (
        <div class="relative overflow-hidden rounded-xl border border-border bg-surface">
          <Show when={src()}>
            <img
              src={src()}
              alt={item().title}
              class="absolute inset-0 h-full w-full object-cover opacity-30"
            />
          </Show>
          <div class="relative max-w-2xl p-6">
            <h1 class="text-2xl font-semibold text-fg">{item().title}</h1>
            <div class="mt-1 flex items-center gap-3 text-sm text-muted">
              <span>{yearOf(item().releaseDate)}</span>
              <Show when={item().voteAverage > 0}>
                <span>★ {item().voteAverage.toFixed(1)}</span>
              </Show>
            </div>
            <p class="mt-3 line-clamp-3 text-sm text-muted">
              {item().overview}
            </p>
            <div class="mt-4 max-w-[10rem]">
              <GrabButton
                mode={props.mode}
                item={item()}
                onGrab={props.onGrab}
              />
            </div>
          </div>
        </div>
      );
    }}
  </Show>
);

// TitleDiscover backs Movies and Series (both TMDB title-shaped). Both category
// resources re-run when props.mode changes, so switching tabs refetches. It
// owns the single grab dialog for its titles.
const TitleDiscover: Component<{ mode: "movies" | "series" }> = (props) => {
  const [trending, { refetch: refetchTrending }] = createResource(
    () => props.mode,
    (m) => fetchDiscover(m, "trending"),
  );
  const [popular, { refetch: refetchPopular }] = createResource(
    () => props.mode,
    (m) => fetchDiscover(m, "popular"),
  );
  const [grabTarget, setGrabTarget] = createSignal<GrabTarget | null>(null);
  const [dismissedSetup, setDismissedSetup] = createSignal(false);
  const configureFor = () =>
    notConfiguredService(trending.error ?? popular.error);

  return (
    <div>
      <Show
        when={trending.error || popular.error}
      >
        <Show
          when={!dismissedSetup() && configureFor()}
          fallback={
            <ErrorText>
              {((trending.error ?? popular.error) as Error)?.message}
            </ErrorText>
          }
        >
          {(service) => (
            <ConfigureConnectionModal
              service={service()}
              onClose={() => setDismissedSetup(true)}
              onSaved={() => {
                setDismissedSetup(true);
                refetchTrending();
                refetchPopular();
              }}
            />
          )}
        </Show>
      </Show>
      {/* Guards every trending()/popular() read below behind "no error" —
          Solid throws synchronously when a resource accessor is read while
          its resource is in an error state (e.g. TMDB not configured yet),
          which without this guard escaped as an uncaught exception and left
          the view stuck showing "Loading…" forever instead of the ErrorText
          above ever getting a chance to be the only thing rendered. */}
      <Show when={!trending.error && !popular.error}>
        {/* The top trending title deliberately appears BOTH as the hero
            banner and as the first card of the "Trending this week" row
            below — the Seerr/Netflix-style featured-item-also-in-its-row
            treatment, not a bug (Discover.test.tsx locks this). Don't skip
            index 0 in the row. */}
        <Show when={!trending.loading}>
          <Hero item={trending()?.[0]} mode={props.mode} onGrab={setGrabTarget} />
        </Show>
        <Row
          title="Trending this week"
          mode={props.mode}
          items={trending()}
          loading={trending.loading}
          onGrab={setGrabTarget}
        />
        <Row
          title="Popular"
          mode={props.mode}
          items={popular()}
          loading={popular.loading}
          onGrab={setGrabTarget}
        />
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
  const [avail] = createResource(
    () => props.item.id,
    () =>
      fetchAdultAvailability(props.item.studio, props.item.title).catch(
        () => null,
      ),
  );
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
      <div class="mt-1">
        <AvailabilityBadge result={avail()} loading={avail.loading} />
      </div>
      <div class="mt-1.5">
        <Button class="w-full !py-1 text-xs" onClick={grab}>
          Grab
        </Button>
      </div>
    </div>
  );
};

// AdultDiscover is the scene-shaped browse: a search box over TPDB's catalog,
// plain paginated browse when the box is empty. No hero (scenes have no single
// "featured" title); a wrapping grid of scene cards. Owns its own grab dialog.
const AdultDiscover: Component = () => {
  const [submitted, setSubmitted] = createSignal("");
  const [draft, setDraft] = createSignal("");
  const [scenes, { refetch: refetchScenes }] = createResource(submitted, (q) =>
    fetchAdultDiscover(q),
  );
  const [grabTarget, setGrabTarget] = createSignal<GrabTarget | null>(null);
  const [dismissedSetup, setDismissedSetup] = createSignal(false);

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
          placeholder="Search scenes by title…"
          value={draft()}
          onInput={(e) => setDraft(e.currentTarget.value)}
        />
      </form>
      <Show when={scenes.error}>
        <Show
          when={!dismissedSetup() && notConfiguredService(scenes.error)}
          fallback={<ErrorText>{(scenes.error as Error)?.message}</ErrorText>}
        >
          {(service) => (
            <ConfigureConnectionModal
              service={service()}
              onClose={() => setDismissedSetup(true)}
              onSaved={() => {
                setDismissedSetup(true);
                refetchScenes();
              }}
            />
          )}
        </Show>
      </Show>
      {/* Same accessor-in-error-state guard as TitleDiscover above — reading
          scenes() while it's errored throws synchronously in Solid. */}
      <Show when={!scenes.error}>
        <Show when={!scenes.loading} fallback={<Muted>Loading…</Muted>}>
          <Show
            when={scenes() && scenes()!.length > 0}
            fallback={<Muted>No scenes found.</Muted>}
          >
            <div class="flex flex-wrap gap-3">
              <For each={scenes()}>
                {(item) => <AdultCard item={item} onGrab={setGrabTarget} />}
              </For>
            </div>
          </Show>
        </Show>
      </Show>
      <Show when={grabTarget()}>
        {(t) => <GrabDialog target={t()} onClose={() => setGrabTarget(null)} />}
      </Show>
    </div>
  );
};

// Discover is the mode-switching shell: tab bar (Movies/Series/Adult) over the
// matching sub-view.
export const Discover: Component = () => {
  const [mode, setMode] = createSignal<Mode>("movies");
  return (
    <div>
      <ModeTabs current={mode} onSelect={setMode} class="flex gap-1" />
      <div class="mt-4">
        <Switch>
          <Match when={mode() === "adult"}>
            <AdultDiscover />
          </Match>
          <Match when={mode() === "movies" || mode() === "series"}>
            <TitleDiscover mode={mode() as "movies" | "series"} />
          </Match>
        </Switch>
      </div>
    </div>
  );
};
