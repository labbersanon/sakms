// RequestsSeriesDetail — missing-episode list for one series request, with
// per-episode Grab and Search & pick.

import {
  type Component,
  For,
  Show,
  createResource,
  createSignal,
} from "solid-js";
import type { AutoGrabRequest } from "@dto";
import type { Mode } from "../api/discover";
import { fetchMissingEpisodes } from "../api/requests";
import { Button, ErrorText, Muted } from "../components/ui";
import { type GrabTarget, GrabDialog, ReleasePickDialog } from "./discover/shared";

export type SeriesDetailSource = {
  title: string;
  tmdbId: number;
};

function episodeSlot(season: number, episode: number): string {
  return `S${String(season).padStart(2, "0")}E${String(episode).padStart(2, "0")}`;
}

function episodeGrabTarget(
  series: SeriesDetailSource,
  season: number,
  episode: number,
  episodeTitle: string,
): GrabTarget {
  const request: AutoGrabRequest = {
    title: series.title,
    tmdbId: series.tmdbId,
    seasonNumber: season,
    episodeNumber: episode,
    seasonSpecified: true,
  };
  const slot = episodeSlot(season, episode);
  const label = episodeTitle
    ? `${series.title} — ${slot} (${episodeTitle})`
    : `${series.title} — ${slot}`;
  return { mode: "series" as Mode, label, request };
}

export const RequestsSeriesDetail: Component<{
  series: SeriesDetailSource;
  onBack: () => void;
}> = (props) => {
  const [data] = createResource(
    () => props.series.tmdbId,
    (id) => fetchMissingEpisodes(id),
  );
  const [grabTarget, setGrabTarget] = createSignal<GrabTarget | null>(null);
  const [pickTarget, setPickTarget] = createSignal<GrabTarget | null>(null);

  return (
    <div>
      <div class="mb-3 flex items-center gap-2">
        <Button onClick={() => props.onBack()}>Back</Button>
        <h2 class="min-w-0 truncate text-base font-medium text-fg">
          {props.series.title}
        </h2>
      </div>
      <Muted class="mb-3 block text-xs">
        Missing episodes — Grab auto-picks the best match; Search & pick lists
        download alternatives for this episode.
      </Muted>

      <Show when={data.error}>
        <ErrorText>{(data.error as Error).message}</ErrorText>
      </Show>
      <Show when={data.loading}>
        <Muted>Loading missing episodes…</Muted>
      </Show>
      <Show when={!data.loading && data()?.episodes}>
        {(episodes) => (
          <Show
            when={episodes().length > 0}
            fallback={<Muted>No missing episodes.</Muted>}
          >
            <ul class="flex flex-col gap-2">
              <For each={episodes()}>
                {(ep) => (
                  <li class="flex flex-wrap items-center gap-2 rounded-md border border-border bg-surface p-3">
                    <div class="min-w-0 flex-1">
                      <div class="text-sm text-fg">
                        {episodeSlot(ep.seasonNumber, ep.episodeNumber)}
                        <Show when={ep.title}> · {ep.title}</Show>
                      </div>
                      <Show when={ep.airDate}>
                        <div class="text-xs text-muted">{ep.airDate}</div>
                      </Show>
                    </div>
                    <Button
                      onClick={() =>
                        setGrabTarget(
                          episodeGrabTarget(
                            props.series,
                            ep.seasonNumber,
                            ep.episodeNumber,
                            ep.title ?? "",
                          ),
                        )
                      }
                    >
                      Grab
                    </Button>
                    {/* Claude 2026-09-26: episode Search & pick is a pick list.
                        Reason: DetailPopup was show-level Discover chrome and
                          hid the NZB/torrent alternatives for this slot.
                        Review if: GET /search accepts season/episode params. */}
                    <Button
                      onClick={() =>
                        setPickTarget(
                          episodeGrabTarget(
                            props.series,
                            ep.seasonNumber,
                            ep.episodeNumber,
                            ep.title ?? "",
                          ),
                        )
                      }
                    >
                      Search & pick
                    </Button>
                  </li>
                )}
              </For>
            </ul>
          </Show>
        )}
      </Show>

      <Show when={grabTarget()}>
        {(t) => <GrabDialog target={t()} onClose={() => setGrabTarget(null)} />}
      </Show>
      <Show when={pickTarget()}>
        {(t) => (
          <ReleasePickDialog
            target={t()}
            onClose={() => setPickTarget(null)}
          />
        )}
      </Show>
    </div>
  );
};
