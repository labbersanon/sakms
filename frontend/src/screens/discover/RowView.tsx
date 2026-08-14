// Discover View All — full-page listing for one Discover row. Discover only
// (Library never gets this). In-row "Show more" on the browse page is unchanged.
//
// Claude 2026-08-14: widened past Slice 4's 4B set.
//   /discover/row/tmdb/:key
//   /discover/row/adult-newest/:rowId — scene/movie newest rows only
//   /discover/row/slider/:id
//   /discover/row/library and /discover/row/library/:mode
//   /discover/row/trakt-watchlist and /discover/row/trakt-watchlist/:type
//   /discover/row/rssfeed/:id
// Still skipped: calendar (month grid), search/filter (already full-page grids),
// Adult studio/performer (drill-down already is view-all).
// Do not widen sectionForPath — /discover/** stays "discover".
// Review if: entity rows lose drill-down and need ?gender= fan-out.

import {
  type Component,
  createResource,
  createSignal,
  Show,
} from "solid-js";
import { A, useParams } from "@solidjs/router";
import { fetchDiscover, type DiscoverItem } from "../../api/discover";
import {
  fetchAdultNewestRowItems,
  fetchAdultNewestRows,
} from "../../api/adultNewestRows";
import { fetchDiscoverSliders, fetchSliderItems, type Slider } from "../../api/discoverSliders";
import { fetchTrackedItems, type TrackedItem } from "../../api/tag";
import { fetchTraktWatchlist, type TraktWatchlistItem } from "../../api/trakt";
import { fetchRssFeeds, fetchRssFeedItems, isAdultRssTarget, type RssFeedItem, type RssFeedTarget } from "../../api/rssFeeds";
import {
  ErrorText,
  Muted,
  SectionLockOverlay,
  useAdultEnabled,
  useSectionLock,
} from "../../components/ui";
import { DISCOVER_NAV_LINK_CLASS } from "../../components/ViewAllLink";
import { MEDIA_POSTER_GRID_CLASS } from "../../components/media";
import { ADULT_CONTENT_SECTION, sectionLabel } from "../../api/sectionLock";
import { MAINSTREAM_ROWS, PosterCard, LibraryCard } from "./Mainstream";
import { AdultCard, toAdultDiscoverItem } from "./Adult";
import { RssFeedCard } from "./RssFeedCard";
import { WatchlistCard } from "../../components/TraktWatchlistRow";
import {
  ConfigureConnectionModal,
  GrabDialog,
  PaginatedStrip,
  type GrabTarget,
  notConfiguredService,
} from "./shared";
import { type DetailTarget, DetailPopup } from "./DetailPopup";

// Claude 2026-08-13: View All uses Library's 2-col poster grid, not flex-wrap.
// Reason: 220/240px cards in a wrap showed one poster per phone row.
// Cards pass layout="grid" so they fill the cell (w-full), not the carousel
// 220/240px. Browse-row carousels are unchanged.
// Review if: carousel rows also need a mobile size.
const WRAP = MEDIA_POSTER_GRID_CLASS;

// Claude 2026-08-13: chip class shared with ViewAllLink (not text-accent).
// Reason: gold-on-cream wallpaper swallowed the back link on mobile View All.
// mb-3 is BackLink-only — View all sits in a flex header and must not grow.
// Review if: DISCOVER_NAV_LINK_CLASS is retired.
const BackLink: Component<{ href: string }> = (props) => (
  <A href={props.href} class={`${DISCOVER_NAV_LINK_CLASS} mb-3`}>
    Back to Discover
  </A>
);

export const TmdbRowView: Component = () => {
  const params = useParams();
  const row = () => MAINSTREAM_ROWS.find((r) => r.key === params.key);
  const [detailTarget, setDetailTarget] = createSignal<DetailTarget | null>(
    null,
  );
  const [setupError, setSetupError] = createSignal<unknown>(null);
  const [dismissedSetup, setDismissedSetup] = createSignal(false);
  const [reloadToken, setReloadToken] = createSignal(0);
  const configureFor = () => notConfiguredService(setupError());

  return (
    <div>
      <BackLink href="/discover/mainstream" />
      <Show
        when={row()}
        fallback={<Muted class="mt-4">Nothing here yet.</Muted>}
      >
        {(r) => (
          <PaginatedStrip
            title={r().title}
            reloadToken={reloadToken}
            load={(page) => fetchDiscover(r().mode, r().category, page)}
            onError={setSetupError}
            containerClass={WRAP}
          >
            {(item) => (
              <PosterCard
                mode={r().mode}
                item={item}
                onDetail={setDetailTarget}
                layout="grid"
              />
            )}
          </PaginatedStrip>
        )}
      </Show>
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
      <Show when={detailTarget()}>
        {(t) => (
          <DetailPopup target={t()} onClose={() => setDetailTarget(null)} />
        )}
      </Show>
    </div>
  );
};

const AdultNewestRowBody: Component<{ rowId: number }> = (props) => {
  const [rows] = createResource(fetchAdultNewestRows);
  const row = () =>
    (rows() ?? []).find(
      (r) =>
        r.id === props.rowId &&
        (r.rowType === "scene" || r.rowType === "movie"),
    );
  const [detailTarget, setDetailTarget] = createSignal<DetailTarget | null>(
    null,
  );
  const [setupError, setSetupError] = createSignal<unknown>(null);
  const [dismissedSetup, setDismissedSetup] = createSignal(false);
  const [reloadToken, setReloadToken] = createSignal(0);
  const configureFor = () => notConfiguredService(setupError());

  return (
    <div>
      <Show
        when={!rows.loading}
        fallback={<Muted class="mt-4">Loading…</Muted>}
      >
        <Show
          when={row()}
          fallback={<Muted class="mt-4">Nothing here yet.</Muted>}
        >
          {(r) => (
            <PaginatedStrip
              title={r().title}
              reloadToken={reloadToken}
              load={(page) => fetchAdultNewestRowItems(r().id, page)}
              onError={setSetupError}
              containerClass={WRAP}
            >
              {(item) => (
                <AdultCard
                  item={toAdultDiscoverItem(item)}
                  onDetail={setDetailTarget}
                  aspect={r().rowType === "movie" ? "poster" : "video"}
                  layout="grid"
                />
              )}
            </PaginatedStrip>
          )}
        </Show>
      </Show>
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
      <Show when={detailTarget()}>
        {(t) => (
          <DetailPopup target={t()} onClose={() => setDetailTarget(null)} />
        )}
      </Show>
    </div>
  );
};

export const AdultNewestRowView: Component = () => {
  const params = useParams();
  const adultEnabled = useAdultEnabled();
  const lock = useSectionLock();
  const rowId = () => {
    const n = Number(params.rowId);
    return Number.isInteger(n) && n > 0 ? n : 0;
  };

  return (
    <Show
      when={adultEnabled()}
      fallback={<Muted class="mt-4">Adult mode is disabled in Settings.</Muted>}
    >
      <BackLink href="/discover/adult" />
      <Show
        when={!lock.isLocked(ADULT_CONTENT_SECTION)}
        fallback={
          <SectionLockOverlay label={sectionLabel(ADULT_CONTENT_SECTION)} />
        }
      >
        <Show
          when={rowId()}
          fallback={<Muted class="mt-4">Nothing here yet.</Muted>}
        >
          {(id) => <AdultNewestRowBody rowId={id()} />}
        </Show>
      </Show>
    </Show>
  );
};

function positiveId(raw: string | undefined): number {
  const n = Number(raw);
  return Number.isInteger(n) && n > 0 ? n : 0;
}

function oncePage<T>(load: () => Promise<T[]>): (page: number) => Promise<T[]> {
  return (page) => (page === 1 ? load() : Promise.resolve([]));
}

function sliderItemMode(
  target: Slider["target"],
  item: DiscoverItem,
): "movies" | "series" {
  if (target === "movie") return "movies";
  if (target === "tv") return "series";
  return item.mediaType === "tv" ? "series" : "movies";
}

function rssFeedTargetMode(
  target: RssFeedTarget,
): "movies" | "series" | "adult" {
  if (target === "movie") return "movies";
  if (target === "tv") return "series";
  return "adult";
}

export const SliderRowView: Component = () => {
  const params = useParams();
  const sliderId = () => positiveId(params.id);
  const [sliders] = createResource(fetchDiscoverSliders);
  const slider = () => (sliders() ?? []).find((s) => s.id === sliderId());
  const [detailTarget, setDetailTarget] = createSignal<DetailTarget | null>(
    null,
  );
  const [setupError, setSetupError] = createSignal<unknown>(null);
  const [dismissedSetup, setDismissedSetup] = createSignal(false);
  const [reloadToken, setReloadToken] = createSignal(0);
  const configureFor = () => notConfiguredService(setupError());

  return (
    <div>
      <BackLink href="/discover/mainstream" />
      <Show
        when={!sliders.loading}
        fallback={<Muted class="mt-4">Loading…</Muted>}
      >
        <Show
          when={slider()}
          fallback={<Muted class="mt-4">Nothing here yet.</Muted>}
        >
          {(s) => (
            <PaginatedStrip
              title={s().title}
              reloadToken={reloadToken}
              load={(page) => fetchSliderItems(s().id, page)}
              onError={setSetupError}
              containerClass={WRAP}
            >
              {(item) => (
                <PosterCard
                  mode={sliderItemMode(s().target, item)}
                  item={item}
                  onDetail={setDetailTarget}
                  layout="grid"
                />
              )}
            </PaginatedStrip>
          )}
        </Show>
      </Show>
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
      <Show when={detailTarget()}>
        {(t) => (
          <DetailPopup target={t()} onClose={() => setDetailTarget(null)} />
        )}
      </Show>
    </div>
  );
};

type LibraryEntry = { mode: "movies" | "series"; item: TrackedItem };

async function loadLibraryEntries(
  mode: string | undefined,
): Promise<LibraryEntry[]> {
  if (mode === "movies" || mode === "series") {
    const items = await fetchTrackedItems(mode).catch(() => [] as TrackedItem[]);
    return items.map((item) => ({ mode, item }));
  }
  const [movies, series] = await Promise.all([
    fetchTrackedItems("movies").catch(() => [] as TrackedItem[]),
    fetchTrackedItems("series").catch(() => [] as TrackedItem[]),
  ]);
  return [
    ...movies.map((item) => ({ mode: "movies" as const, item })),
    ...series.map((item) => ({ mode: "series" as const, item })),
  ];
}

export const LibraryRowView: Component = () => {
  const params = useParams();
  const mode = () => params.mode;
  const [detailTarget, setDetailTarget] = createSignal<DetailTarget | null>(
    null,
  );
  const [setupError, setSetupError] = createSignal<unknown>(null);

  return (
    <div>
      <BackLink href="/discover/mainstream" />
      <PaginatedStrip<LibraryEntry>
        title="In your library"
        reloadToken={() => 0}
        load={oncePage(() => loadLibraryEntries(mode()))}
        onError={setSetupError}
        containerClass={WRAP}
        singlePage
      >
        {(entry) => (
          <LibraryCard
            mode={entry.mode}
            item={entry.item}
            onDetail={setDetailTarget}
            layout="grid"
          />
        )}
      </PaginatedStrip>
      <Show when={setupError()}>
        <ErrorText>{(setupError() as Error)?.message}</ErrorText>
      </Show>
      <Show when={detailTarget()}>
        {(t) => (
          <DetailPopup target={t()} onClose={() => setDetailTarget(null)} />
        )}
      </Show>
    </div>
  );
};

export const TraktWatchlistRowView: Component = () => {
  const params = useParams();
  const [grabTarget, setGrabTarget] = createSignal<GrabTarget | null>(null);
  const [setupError, setSetupError] = createSignal<unknown>(null);

  return (
    <div>
      <BackLink href="/discover/mainstream" />
      <PaginatedStrip<TraktWatchlistItem>
        title="Trakt Watchlist"
        reloadToken={() => 0}
        load={oncePage(async () => {
          const items = await fetchTraktWatchlist();
          const type = params.type;
          if (type === "series") return items.filter((i) => i.type === "show");
          if (type === "movies") return items.filter((i) => i.type === "movie");
          return items;
        })}
        onError={setSetupError}
        containerClass={WRAP}
        singlePage
      >
        {(item) => (
          <WatchlistCard
            item={item}
            onGrab={setGrabTarget}
            layout="grid"
          />
        )}
      </PaginatedStrip>
      <Show when={setupError()}>
        <ErrorText>{(setupError() as Error)?.message}</ErrorText>
      </Show>
      <Show when={grabTarget()}>
        {(t) => <GrabDialog target={t()} onClose={() => setGrabTarget(null)} />}
      </Show>
    </div>
  );
};

export const RssFeedRowView: Component = () => {
  const params = useParams();
  const feedId = () => positiveId(params.id);
  const [feeds] = createResource(fetchRssFeeds);
  const feed = () => (feeds() ?? []).find((f) => f.id === feedId());
  const [setupError, setSetupError] = createSignal<unknown>(null);
  const backHref = () => {
    const t = feed()?.target;
    return isAdultRssTarget(t ?? "")
      ? "/discover/adult"
      : "/discover/mainstream";
  };

  return (
    <div>
      <BackLink href={backHref()} />
      <Show
        when={!feeds.loading}
        fallback={<Muted class="mt-4">Loading…</Muted>}
      >
        <Show
          when={feed()}
          fallback={<Muted class="mt-4">Nothing here yet.</Muted>}
        >
          {(f) => (
            <PaginatedStrip<RssFeedItem>
              title={f().title}
              reloadToken={() => 0}
              load={oncePage(() => fetchRssFeedItems(f().id))}
              onError={setSetupError}
              containerClass={WRAP}
              singlePage
            >
              {(item) => (
                <RssFeedCard
                  item={item}
                  mode={rssFeedTargetMode(f().target as RssFeedTarget)}
                  layout="grid"
                />
              )}
            </PaginatedStrip>
          )}
        </Show>
      </Show>
      <Show when={setupError()}>
        <ErrorText>{(setupError() as Error)?.message}</ErrorText>
      </Show>
    </div>
  );
};
