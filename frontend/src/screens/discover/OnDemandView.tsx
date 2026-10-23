// OnDemandView is Discover's verified-NZB grid (?view=ondemand). Card click
// opens a quality picker. Play is not in this slice.

import {
  type Component,
  For,
  Show,
  createMemo,
  createResource,
  createSignal,
} from "solid-js";
import type { OndemandTitle } from "@dto";
import { fetchOndemand } from "../../api/ondemand";
import {
  type Mode,
  cardPosterSrc,
  fetchTitleCard,
} from "../../api/discover";
import {
  MediaCardShell,
  MediaFallbackTile,
  MediaGridSkeleton,
  MEDIA_POSTER_GRID_CLASS,
} from "../../components/media";
import { FilterChip, Muted } from "../../components/ui";
import { Modal } from "./shared";

export const OnDemandView: Component<{
  mode: Mode;
  query?: string;
  posterAspect?: string;
}> = (props) => {
  const [list] = createResource(
    () => props.mode,
    (mode) => fetchOndemand(mode),
  );
  const [picked, setPicked] = createSignal<OndemandTitle | null>(null);
  const [quality, setQuality] = createSignal("");

  const visible = createMemo(() => {
    const needle = (props.query ?? "").trim().toLowerCase();
    // No TMDB id → no poster path. Hide letter tiles; backend also omits these.
    const items = (list()?.items ?? []).filter((item) => (item.tmdbId ?? 0) > 0);
    if (!needle) return items;
    return items.filter((item) => {
      const hay = `${item.title} ${item.year ?? ""} ${item.imdbId ?? ""}`.toLowerCase();
      return hay.includes(needle);
    });
  });

  const open = (item: OndemandTitle) => {
    setPicked(item);
    setQuality(item.qualities[0]?.label ?? "");
  };

  const loading = () => list.loading && list() === undefined;

  return (
    <section class="mt-2">
      <h2 class="mb-2 text-sm font-semibold uppercase tracking-wide text-muted">
        On Demand
      </h2>
      <Show when={!loading()} fallback={<MediaGridSkeleton aspect={props.posterAspect} />}>
        <Show
          when={visible().length > 0}
          fallback={
            <Muted>
              {props.query?.trim()
                ? "No On Demand titles match this search."
                : "No verified On Demand titles yet."}
            </Muted>
          }
        >
          <div class={MEDIA_POSTER_GRID_CLASS}>
            <For each={visible()}>
              {(item) => (
                <OnDemandCard
                  mode={props.mode}
                  item={item}
                  posterAspect={props.posterAspect}
                  onClick={() => open(item)}
                />
              )}
            </For>
          </div>
        </Show>
      </Show>
      <Show when={picked()}>
        {(item) => (
          <Modal
            title={item().year ? `${item().title} (${item().year})` : item().title}
            onClose={() => setPicked(null)}
          >
            <p class="mb-3 text-sm text-muted">Quality</p>
            <div class="flex flex-wrap gap-1">
              <For each={item().qualities}>
                {(q) => (
                  <FilterChip
                    label={q.label}
                    size="sm"
                    active={() => quality() === q.label}
                    onToggle={() => setQuality(q.label)}
                  />
                )}
              </For>
            </div>
          </Modal>
        )}
      </Show>
    </section>
  );
};

const OnDemandCard: Component<{
  mode: Mode;
  item: OndemandTitle;
  posterAspect?: string;
  onClick: () => void;
}> = (props) => {
  const [card] = createResource(
    () =>
      props.mode !== "adult" && props.item.tmdbId
        ? { mode: props.mode as Exclude<Mode, "adult">, tmdbId: props.item.tmdbId }
        : undefined,
    ({ mode, tmdbId }) =>
      fetchTitleCard(mode, tmdbId).catch(() => ({
        posterPath: "",
        posterUrl: "",
        overview: "",
      })),
  );
  const src = () => {
    const c = card();
    return c ? cardPosterSrc(c) : "";
  };
  const labels = () =>
    props.item.qualities.map((q) => q.label).join(" · ");
  return (
    <MediaCardShell label={props.item.title} onClick={props.onClick}>
      <div
        class="relative w-full overflow-hidden bg-surface-2"
        style={{ "aspect-ratio": props.posterAspect ?? "2 / 3" }}
      >
        <Show
          when={src()}
          fallback={<MediaFallbackTile title={props.item.title} loading={card.loading} />}
        >
          <img
            src={src()}
            alt=""
            loading="lazy"
            class="h-full w-full object-cover"
          />
        </Show>
      </div>
      <div class="flex-1 p-2">
        <p class="line-clamp-2 text-xs font-medium text-fg leading-tight">
          {props.item.title}
        </p>
        <Show when={props.item.year}>
          <p class="mt-0.5 text-[11px] text-muted">{props.item.year}</p>
        </Show>
        <Show when={labels()}>
          <p class="mt-0.5 text-[11px] text-muted">{labels()}</p>
        </Show>
      </div>
    </MediaCardShell>
  );
};
