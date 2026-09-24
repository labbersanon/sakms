// SeriesEpisodesPanel is the owned-series play list: season chips + episode
// rows with TrackedPlayback. SeasonsPanel stays monitor-only.

import { type Component, For, Show, createSignal } from "solid-js";
import type { SeasonEpisode, SeasonState } from "@dto";
import { TrackedPlayback } from "./TrackedPlayback";
import { Muted } from "./ui";
import { putEpisodeProgress } from "../api/seriesProgress";
import {
  defaultSeasonNumber,
  episodeCode,
  episodePlaySrc,
  seasonChipLabel,
} from "../screens/seriesPlay";

export const SeriesEpisodesPanel: Component<{
  seriesID: number;
  seasons: SeasonState[];
  loading?: boolean;
  onReplace?: (season: number, episode: number) => void;
}> = (props) => {
  const [picked, setPicked] = createSignal<number | null>(null);
  const selected = () =>
    picked() ?? defaultSeasonNumber(props.seasons);
  const episodes = (): SeasonEpisode[] =>
    props.seasons.find((s) => s.seasonNumber === selected())?.episodes ?? [];

  return (
    <div class="mb-3 border-t border-border pt-3">
      <p class="mb-2 text-[11px] font-medium uppercase tracking-wide text-muted">
        Episodes
      </p>
      <Show
        when={props.seasons.length > 0}
        fallback={
          <Show when={!props.loading}>
            <Muted>No episodes tracked yet.</Muted>
          </Show>
        }
      >
        <div class="mb-2 flex flex-wrap gap-1">
          <For each={props.seasons}>
            {(s) => (
              <button
                type="button"
                class={`rounded-md border px-2 py-1 text-xs ${
                  s.seasonNumber === selected()
                    ? "border-accent bg-surface-2 text-fg"
                    : "border-border text-muted hover:text-fg"
                }`}
                aria-pressed={s.seasonNumber === selected()}
                onClick={() => setPicked(s.seasonNumber)}
              >
                {seasonChipLabel(s.seasonNumber)}
              </button>
            )}
          </For>
        </div>
        <ul class="space-y-2">
          <For each={episodes()}>
            {(ep) => {
              const src = episodePlaySrc(ep);
              const label = () =>
                ep.title
                  ? `${episodeCode(selected(), ep.episodeNumber)} · ${ep.title}`
                  : episodeCode(selected(), ep.episodeNumber);
              return (
                <li class="rounded bg-surface-2 px-2 py-1.5 text-xs text-fg">
                  <p class="font-medium">{label()}</p>
                  <Show when={ep.airDate}>
                    <p class="text-[11px] text-muted">{ep.airDate}</p>
                  </Show>
                  <Show
                    when={
                      !ep.watched &&
                      (ep.durationSeconds ?? 0) > 0 &&
                      (ep.positionSeconds ?? 0) > 0
                    }
                  >
                    <div
                      class="mt-1 h-1 overflow-hidden rounded bg-border"
                      role="progressbar"
                      aria-label={`${label()} progress`}
                      aria-valuemin={0}
                      aria-valuemax={Math.round(ep.durationSeconds ?? 0)}
                      aria-valuenow={Math.round(ep.positionSeconds ?? 0)}
                    >
                      <div
                        class="h-1 rounded bg-accent"
                        style={{
                          width: `${Math.min(
                            100,
                            ((ep.positionSeconds ?? 0) /
                              (ep.durationSeconds ?? 1)) *
                              100,
                          )}%`,
                        }}
                      />
                    </div>
                  </Show>
                  <Show
                    when={src}
                    fallback={
                      <p class="mt-1 text-muted">
                        {ep.hasFile
                          ? "This format cannot play in the browser"
                          : "missing"}
                      </p>
                    }
                  >
                    <TrackedPlayback
                      src={src}
                      label={label()}
                      onProgress={
                        ep.id
                          ? (info) => {
                              void putEpisodeProgress(props.seriesID, ep.id!, {
                                positionSeconds: info.position,
                                durationSeconds: info.duration,
                                watched: info.ended,
                              }).catch(() => undefined);
                            }
                          : undefined
                      }
                    />
                  </Show>
                  <Show when={props.onReplace}>
                    <button
                      type="button"
                      class="mt-1 text-[11px] text-muted underline hover:text-fg"
                      onClick={() =>
                        props.onReplace?.(selected(), ep.episodeNumber)
                      }
                    >
                      Replace
                    </button>
                  </Show>
                </li>
              );
            }}
          </For>
        </ul>
      </Show>
    </div>
  );
};
