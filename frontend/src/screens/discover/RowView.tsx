// Discover View All — full-page listing for one Discover row. Discover only
// (Library never gets this). In-row "Show more" on the browse page is unchanged.
//
// Claude 2026-08-13: two route families (Slice 4 sign-off).
//   /discover/row/tmdb/:key          — MAINSTREAM_ROWS keys, not :mode/:category
//   /discover/row/adult-newest/:rowId — scene/movie newest rows only
// Reason: entity studio/performer drill-down already is "view all"; 4B skips
// Trakt, in-library, sliders, calendar, search/filter grids.
// Troubleshooting: unknown TMDB key must not fetch. Adult View All must overlay
// adult-content itself — sectionForPath maps /discover/** to "discover".
// Review if: entity rows lose drill-down and need ?gender= fan-out.

import {
  type Component,
  createResource,
  createSignal,
  Show,
} from "solid-js";
import { A, useParams } from "@solidjs/router";
import { fetchDiscover } from "../../api/discover";
import {
  fetchAdultNewestRowItems,
  fetchAdultNewestRows,
} from "../../api/adultNewestRows";
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
import { MAINSTREAM_ROWS, PosterCard } from "./Mainstream";
import { AdultCard, toAdultDiscoverItem } from "./Adult";
import {
  ConfigureConnectionModal,
  PaginatedStrip,
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
