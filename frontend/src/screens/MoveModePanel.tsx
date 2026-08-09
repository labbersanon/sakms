// MoveModePanel — the shared cross-mode "move to another section" panel,
// mounted by both Rename (AC5) and Dedup (AC6). Modelled directly on
// Rename.tsx's RepickPanel (search box -> pick a match -> commit) rather than
// reinvented, per .omc/plans/autopilot-impl.md §6.2.

import {
  type Component,
  createResource,
  createSignal,
  For,
  Show,
} from "solid-js";
import type { AdultSceneCandidate, DiscoverItem, MoveModeRequest } from "@dto";
import type { Mode } from "../api/discover";
import {
  adultSceneSearch,
  moveProposalMode,
  tmdbSearch,
} from "../api/rename";
import { SectionLockedError } from "../api/client";
import { ADULT_CONTENT_SECTION, sectionLabel } from "../api/sectionLock";
import { Button, ErrorText, Muted, yearOf } from "../components/ui";

export const MOVE_TARGETS: Mode[] = ["movies", "series", "adult"];
export const otherModes = (current: Mode): Mode[] =>
  MOVE_TARGETS.filter((m) => m !== current);

// searchLockMessage is the SECTION-AGNOSTIC copy for a search 403. It is one
// shared handler for both the Movies/Series branch (tmdb-search, classified
// under Discover) and the Adult branch (scene-search) — a Discover-locked
// install can hit this on a Movies/Series target search too, so the copy must
// never hardcode "Adult". The section name comes off the thrown
// SectionLockedError (api()/client.ts already parses the {code,section} body
// into it), falling back to a neutral message when the name is absent.
function searchLockMessage(err: SectionLockedError): string {
  const label = err.section ? sectionLabel(err.section) : "";
  return label
    ? `${label} is PIN-locked — unlock it to search`
    : "This section is PIN-locked";
}

type SearchResult =
  | { kind: "catalog"; items: DiscoverItem[] }
  | { kind: "adult"; items: AdultSceneCandidate[]; errors?: string[] };

type CommitError = { adultLocked: boolean; message: string };

export const MoveModePanel: Component<{
  proposalId: number;
  sourceName: string;
  targetMode: Mode;
  workflow: "rename" | "dedup";
  onDone: () => void; // committed — caller refetches
  onCancel: () => void; // NO-OP — caller just unmounts, nothing was written
}> = (props) => {
  // `submitted` deliberately starts EMPTY, not pre-populated from `query()`
  // (unlike RepickPanel, which auto-searches on mount): Cancel's contract is
  // that no request is issued unless the operator explicitly searches, and
  // an eager mount-time fetch here would fire before any click at all.
  const [query, setQuery] = createSignal(props.sourceName);
  const [submitted, setSubmitted] = createSignal("");

  const [results] = createResource(
    submitted,
    async (q): Promise<SearchResult> => {
      if (!q.trim()) {
        return props.targetMode === "adult"
          ? { kind: "adult", items: [] }
          : { kind: "catalog", items: [] };
      }
      if (props.targetMode === "adult") {
        const res = await adultSceneSearch(q);
        return { kind: "adult", items: res.items, errors: res.errors };
      }
      const items = await tmdbSearch(props.targetMode, q);
      return { kind: "catalog", items };
    },
  );

  const catalogItems = (): DiscoverItem[] => {
    const r = results();
    return r && r.kind === "catalog" ? r.items : [];
  };
  const adultItems = (): AdultSceneCandidate[] => {
    const r = results();
    return r && r.kind === "adult" ? r.items : [];
  };
  const adultSoftErrors = (): string[] => {
    const r = results();
    return r && r.kind === "adult" ? (r.errors ?? []) : [];
  };

  // Manual season/episode assignment (series target only) — identical
  // optional-pair, `!= null` semantics to Rename.tsx's RepickPanel
  // (:235-268): season 0 is Specials, a real value an operator can mean, and
  // a truthiness check would silently drop it.
  const [season, setSeason] = createSignal<number | null>(null);
  const [episode, setEpisode] = createSignal<number | null>(null);
  const parseSlot = (raw: string): number | null => {
    const t = raw.trim();
    if (t === "") return null;
    const n = Number(t);
    return Number.isInteger(n) ? n : null;
  };
  const halfPair = () => (season() != null) !== (episode() != null);

  const [busy, setBusy] = createSignal(false);
  const [commitError, setCommitError] = createSignal<CommitError | null>(
    null,
  );

  // commit is the single dispatch path for both the catalog and adult "Use
  // this" buttons. It is a SEPARATE error branch from the search-403 handler
  // above — required, and distinct from it. A move from Adult to Movies
  // while Adult is PIN-locked has a SUCCEEDING search (Movies' own TMDB
  // catalog, which the Adult lock does not touch) and a FAILING commit (the
  // backend gates the commit itself for any Adult-touching move), so reusing
  // the search-403 "the search is blocked, not the move" copy here would be
  // actively wrong. On any error the panel stays mounted and the just-picked
  // candidate stays visible/clickable (the results resource is untouched by
  // a commit failure) so unlock-and-retry is one click — and onDone() is
  // never called, since nothing was written.
  const commit = async (req: MoveModeRequest) => {
    setCommitError(null);
    setBusy(true);
    try {
      await moveProposalMode(props.proposalId, req);
      props.onDone();
    } catch (e) {
      if (e instanceof SectionLockedError && e.section === ADULT_CONTENT_SECTION) {
        setCommitError({
          adultLocked: true,
          message: "Adult is PIN-locked — unlock to continue",
        });
      } else {
        setCommitError({ adultLocked: false, message: (e as Error).message });
      }
    } finally {
      setBusy(false);
    }
  };

  const useCatalogItem = (id: number, title: string, year?: number) => {
    if (props.targetMode === "series" && halfPair()) {
      setCommitError({
        adultLocked: false,
        message: "Enter both a season and an episode, or leave both blank.",
      });
      return;
    }
    const s = season();
    const e = episode();
    const req: MoveModeRequest =
      props.targetMode === "series" && s != null && e != null
        ? {
            targetMode: props.targetMode,
            tmdbId: id,
            title,
            year,
            seasonNumber: s,
            episodeNumber: e,
          }
        : { targetMode: props.targetMode, tmdbId: id, title, year };
    commit(req);
  };

  const useAdultCandidate = (c: AdultSceneCandidate) => {
    commit({
      targetMode: "adult",
      title: c.title,
      box: c.box,
      sceneId: c.sceneId,
      studio: c.studio,
      date: c.date,
    });
  };

  return (
    <div class="mt-4 rounded-xl border border-border bg-surface p-4">
      <h4 class="text-sm font-semibold text-fg">
        Move “{props.sourceName}” to another section
      </h4>
      <form
        class="mt-2 flex items-center gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          setSubmitted(query());
        }}
      >
        <input
          class="w-80 max-w-full rounded-md border border-border bg-bg px-3 py-2 text-sm text-fg outline-none focus:border-accent"
          value={query()}
          onInput={(e) => setQuery(e.currentTarget.value)}
          aria-label="Move search query"
        />
        <Button type="submit">Search</Button>
        <Button onClick={props.onCancel}>Cancel</Button>
      </form>

      <Show when={props.targetMode === "series"}>
        <div class="mt-3 rounded-md border border-border bg-surface-2 p-3">
          <Muted>
            Optional: assign a season and episode directly. Season 0 is
            Specials.
          </Muted>
          <div class="mt-2 flex items-center gap-3">
            <label class="flex items-center gap-2 text-sm text-fg">
              Season
              <input
                type="number"
                min="0"
                step="1"
                class="w-24 rounded-md border border-border bg-bg px-2 py-1 text-sm text-fg outline-none focus:border-accent"
                value={season() ?? ""}
                onInput={(e) => setSeason(parseSlot(e.currentTarget.value))}
                aria-label="Season number"
              />
            </label>
            <label class="flex items-center gap-2 text-sm text-fg">
              Episode
              <input
                type="number"
                min="1"
                step="1"
                class="w-24 rounded-md border border-border bg-bg px-2 py-1 text-sm text-fg outline-none focus:border-accent"
                value={episode() ?? ""}
                onInput={(e) => setEpisode(parseSlot(e.currentTarget.value))}
                aria-label="Episode number"
              />
            </label>
          </div>
          <Show when={halfPair()}>
            <ErrorText>
              Enter both a season and an episode, or leave both blank.
            </ErrorText>
          </Show>
        </div>
      </Show>

      <div class="mt-3 rounded-md border border-border bg-surface-2 p-3">
        <Muted>
          {props.workflow === "dedup"
            ? "Files stay where they are; only the group's mode changes. Run a Rename scan in the target mode to relocate them."
            : "If the target library is on a different filesystem, Apply may fail with a cross-device error. The move itself is reversible."}
        </Muted>
        <Show when={props.targetMode === "adult"}>
          <Muted class="mt-1">
            This file has no perceptual hash yet, so it will be renamed
            without its [phash-...] tag. A later Adult scan will add it.
          </Muted>
        </Show>
      </div>

      <Show when={commitError()}>
        <ErrorText>{commitError()!.message}</ErrorText>
      </Show>

      <div class="mt-3">
        <Show when={results.error}>
          <ErrorText>
            {results.error instanceof SectionLockedError
              ? searchLockMessage(results.error as SectionLockedError)
              : (results.error as Error)?.message}
          </ErrorText>
        </Show>
        {/* Guarded on `!results.error`, and every branch below it too: Solid
            resources RE-THROW on a `results()` read once the fetcher has
            errored (by design, for ErrorBoundary integration) — calling
            catalogItems()/adultItems()/adultSoftErrors() (which all invoke
            results()) while an error is set would throw mid-render. Same bug
            class as the GrabDialog incident documented in CLAUDE.md: sibling
            <Show> blocks let the success branch's resource read execute even
            while .error was set. */}
        <Show when={!results.error}>
          <Show when={adultSoftErrors().length > 0}>
            <Muted>
              Some sources could not be searched:{" "}
              {adultSoftErrors().join(", ")}
            </Muted>
          </Show>
          <Show when={!results.loading} fallback={<Muted>Searching…</Muted>}>
            <Show when={props.targetMode !== "adult"}>
              <Show
                when={catalogItems().length > 0}
                fallback={<Muted>No results.</Muted>}
              >
                <ul class="flex flex-col gap-1">
                  <For each={catalogItems()}>
                    {(item) => {
                      const y = yearOf(item.releaseDate);
                      return (
                        <li class="flex items-center gap-3 rounded-md border border-border bg-surface-2 p-2">
                          <span class="min-w-0 flex-1 truncate text-sm text-fg">
                            {item.title}
                            {y ? ` (${y})` : ""} — TMDB #{item.id}
                          </span>
                          <Button
                            variant="primary"
                            disabled={busy()}
                            onClick={() =>
                              useCatalogItem(item.id, item.title, y)
                            }
                          >
                            Use this
                          </Button>
                        </li>
                      );
                    }}
                  </For>
                </ul>
              </Show>
            </Show>
            <Show when={props.targetMode === "adult"}>
              <Show
                when={adultItems().length > 0}
                fallback={<Muted>No results.</Muted>}
              >
                <ul class="flex flex-col gap-1">
                  <For each={adultItems()}>
                    {(c) => (
                      <li class="flex items-center gap-3 rounded-md border border-border bg-surface-2 p-2">
                        <div class="min-w-0 flex-1 text-sm text-fg">
                          <div class="truncate">{c.title}</div>
                          <Muted>
                            {c.studio ?? "Unknown studio"}
                            {c.date ? ` — ${c.date}` : ""}
                            {" — "}
                            <span class="rounded bg-surface px-1 py-0.5 text-xs uppercase text-muted">
                              {c.box}
                            </span>
                          </Muted>
                        </div>
                        <Button
                          variant="primary"
                          disabled={busy()}
                          onClick={() => useAdultCandidate(c)}
                        >
                          Use this
                        </Button>
                      </li>
                    )}
                  </For>
                </ul>
              </Show>
            </Show>
          </Show>
        </Show>
      </div>
    </div>
  );
};
