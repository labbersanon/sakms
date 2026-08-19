// SeasonsPanel is the per-season monitoring surface for Series. Library
// passes the library_series row id; Discover passes the TMDB id. Both hit
// the same library_season_monitored rows — see api/seasons.ts.
//
// It owns its own resource and refetch deliberately: the parent's post-mutation
// refresh only re-pulls the tag vocabulary and the tracked rows, neither of
// which carries season state. Keyed on seriesID/tmdbId so selecting a different
// card — which does NOT remount this component, since the parent's <Show> is
// non-keyed — re-fetches instead of showing the previous series' seasons.
//
// Every write re-pulls the list rather than flipping a local copy: un-monitoring
// is not a pure flag write server-side (it also cancels that season's queued
// air-date retries), so what the operator sees should be what actually landed.

import {
  type Component,
  For,
  Show,
  createResource,
  createSignal,
} from "solid-js";
import {
  type SeasonKey,
  fetchSeasonStatesFor,
  putAllSeasonsMonitoredFor,
  putSeasonMonitoredFor,
} from "../api/seasons";
import { fetchUsenetAutoGrabEnabled } from "../api/usenet";
import { ErrorText, Muted, Switch } from "./ui";
import Info from "lucide-solid/icons/info";

// SEASON_UNMONITORED_COPY / SEASON_AUTOGRAB_COPY are the two honesty lines for
// per-season monitoring. They used to render as always-visible paragraphs on
// the card; they now live behind the info icon when auto-grab is off. Each
// still renders as its OWN paragraph and as a single unbroken text node — no
// <strong>, no nested span, no <br> — because interleaving an element child
// would split the sentence across nodes and make it unfindable by its own text.
const SEASON_UNMONITORED_COPY =
  "An unmonitored season is never searched automatically, no matter how long ago it aired.";
const SEASON_AUTOGRAB_COPY =
  "Monitoring a season does nothing until Settings → Download → Usenet → Enable auto-grab is on, and that takes effect on restart.";

// seasonLabel names season 0 "Specials". Season 0 is LISTED, not filtered out:
// the backend's season set includes it, so hiding it would let "All seasons"
// quietly touch a season no visible row accounts for.
const seasonLabel = (n: number) => (n === 0 ? "Specials" : `Season ${n}`);

export const SeasonsPanel: Component<SeasonKey> = (props) => {
  const key = (): SeasonKey =>
    props.seriesID != null
      ? { seriesID: props.seriesID }
      : { tmdbId: props.tmdbId };

  const [seasons, { refetch }] = createResource(key, fetchSeasonStatesFor);
  const [autoGrab] = createResource(() =>
    fetchUsenetAutoGrabEnabled().catch(() => false),
  );
  const [busy, setBusy] = createSignal(false);
  const [writeError, setWriteError] = createSignal("");
  const [helpOpen, setHelpOpen] = createSignal(false);

  // Claude 2026-08-14: switches stay off until Settings auto-grab is on.
  // Reason: monitoring a season does nothing while usenet auto-grab is off,
  // and the two honesty paragraphs crowded the Library card.
  // Troubleshooting: operators flipped season monitors and nothing searched.
  // Review if: auto-grab grows a live (no-restart) path.
  const autoGrabOn = () => autoGrab() === true;
  const switchesDisabled = () => busy() || !autoGrabOn();
  const showEnableHelp = () => !autoGrab.loading && !autoGrabOn();

  // Only the PUT is wrapped. A refetch that fails AFTER a successful write is
  // not a failed write, and reporting it as one would tell the operator their
  // un-monitor didn't land when it did (and, with it, the retry cancellation it
  // triggered server-side). The re-read's own failure surfaces through
  // seasons.error, which is rendered separately below.
  const write = async (fn: () => Promise<void>) => {
    setBusy(true);
    setWriteError("");
    try {
      await fn();
    } catch (e) {
      setWriteError((e as Error).message);
      setBusy(false);
      return;
    }
    try {
      await refetch();
    } finally {
      setBusy(false);
    }
  };

  // The seasons.error short-circuit is load-bearing, not defensive noise: a
  // Solid resource's accessor RE-THROWS after its fetcher rejects, so a bare
  // `seasons() ?? []` throws mid-render once the GET fails — the same failure
  // shape that once left Discover's GrabDialog stuck forever (see CLAUDE.md).
  // The error is rendered above instead.
  const rows = () => (seasons.error ? [] : (seasons() ?? []));
  // The bulk switch reads as on only when EVERY season is monitored, so a
  // partially-monitored series shows off and one click monitors the rest.
  const allMonitored = () =>
    rows().length > 0 && rows().every((s) => s.monitored);

  return (
    <div class="mb-3 border-t border-border pt-3">
      <div class="mb-1 flex items-center gap-1">
        <p class="text-[11px] font-medium uppercase tracking-wide text-muted">
          Seasons
        </p>
        <Show when={showEnableHelp()}>
          <button
            type="button"
            class="rounded p-0.5 text-muted hover:text-fg"
            aria-label="How to enable season monitoring"
            aria-expanded={helpOpen()}
            onClick={() => setHelpOpen((open) => !open)}
          >
            <Info class="h-3.5 w-3.5" />
          </button>
        </Show>
      </div>
      <Show when={showEnableHelp() && helpOpen()}>
        <Muted class="mt-1">{SEASON_UNMONITORED_COPY}</Muted>
        <Muted class="mt-1">{SEASON_AUTOGRAB_COPY}</Muted>
      </Show>
      <Show when={seasons.error}>
        <ErrorText>{(seasons.error as Error)?.message}</ErrorText>
      </Show>
      <Show when={writeError()}>
        <ErrorText>{writeError()}</ErrorText>
      </Show>
      <Show
        when={rows().length > 0}
        fallback={
          <Show when={!seasons.loading && !seasons.error}>
            <Muted>No seasons tracked yet.</Muted>
          </Show>
        }
      >
        <div class="mb-1 flex items-center justify-between gap-2 pb-1">
          <span class="text-xs font-medium text-fg">All seasons</span>
          <Switch
            checked={allMonitored()}
            disabled={switchesDisabled()}
            ariaLabel="Monitor all seasons"
            onChange={(next) =>
              void write(() => putAllSeasonsMonitoredFor(key(), next))
            }
          />
        </div>
        <For each={rows()}>
          {(s) => (
            <div class="flex items-center justify-between gap-2 py-1">
              <div class="min-w-0">
                <p class="truncate text-xs text-fg">
                  {seasonLabel(s.seasonNumber)}
                </p>
                {/* episodeCount is every episode ROW the season has, on disk or
                    not — it is NOT an on-disk count. Labelled as the total, with
                    missing shown beside it, so downloaded reads as the
                    difference rather than being misreported. */}
                <p class="text-[11px] text-muted">
                  {`${s.episodeCount} episodes · ${s.missingCount} missing`}
                </p>
              </div>
              <Switch
                checked={s.monitored}
                disabled={switchesDisabled()}
                ariaLabel={`Monitor ${seasonLabel(s.seasonNumber)}`}
                onChange={(next) =>
                  void write(() =>
                    putSeasonMonitoredFor(key(), s.seasonNumber, next),
                  )
                }
              />
            </div>
          )}
        </For>
      </Show>
    </div>
  );
};
