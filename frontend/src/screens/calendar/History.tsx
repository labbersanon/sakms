// History — Calendar's completed/imported grab log, laid out as a month grid or
// a flat list. Read-only, per §1.3 of
// .omc/plans/autopilot-impl-calendar-grabs-requests.md: no Apply, no Remove, no
// multi-select, no retry-now. It replaces the standalone Grabs screen's role
// inside Queue, but is NOT the same screen — Grabs showed every lifecycle
// status, History shows only what actually landed.
//
// PROP CONTRACT (the piece calendar/index.tsx and Upcoming.tsx must agree on):
// this component owns NEITHER the month navigation NOR the grid/list toggle —
// index.tsx owns both (§7.2) and passes them down as plain values (year,
// month, display), matching Upcoming.tsx's own prop shape. `month` is
// 0-based, like JS Date and like grid.ts's helpers. What History DOES own is
// its own mode selection: a three-mode ModeTabs (§0.6 — grabs exist for Movies,
// Series AND Adult, unlike Upcoming, which has no Adult source at all), which
// is why the mode signal cannot be hoisted into index.tsx alongside the other
// two. ModeTabs renders inline here rather than clobbering the shell's single
// tab slot because Queue.tsx wraps its children in a shadowing
// ScreenTabsContext.Provider — that Provider is load-bearing and stays (§0.7).
//
// FILTER: status ∈ {completed, imported}, applied ON THE CLIENT (§2.2), never
// by a new server-side `?status=` param. GET /api/modes/{mode}/grabs
// (listGrabsHandler) is a general-purpose endpoint shared with future
// consumers; narrowing its default would silently change what they see, and
// widening its contract for one caller is the premature abstraction this repo's
// conventions decline. The volume argument agrees: grabs are bounded by an
// operator's real download history, not by catalog size.
// This EXCLUDES `pending_retry`, which matters more than the other exclusions
// (§2.1): a Calendar pre-release request is a `pending_retry` row, and History
// must never show an unreleased movie as something that already landed.
//
// TIMEZONE — a DELIBERATE, DOCUMENTED NON-FIX. DO NOT "CORRECT" IT (§2.4).
// Grab.createdAt is UTC (the `strftime('%Y-%m-%dT%H:%M:%fZ','now')` column
// default at internal/db/migrations/0009_grabs.sql:15). Day buckets are the
// first 10 characters of that string, with NO conversion to local time —
// exactly what discover/CalendarView.tsx:92 already does for its own unrelated
// calendar. Known, accepted consequence: an operator in UTC-5 who grabs
// something at 21:00 local sees it in the NEXT day's cell. Converting would
// mean parsing each timestamp into a local Date and re-deriving the key —
// doable, but it would make this grid disagree with every other timestamp the
// app renders, which is a worse inconsistency than the quirk it would fix.
//
// COPY HONESTY (§2.3): the list's date column header reads "Grabbed", never
// "Completed" or "Landed". The date is createdAt — when the grab STARTED, not
// when it finished. Grid cells carry no date text at all; the cell's position
// in the month IS the date.

import {
  type Component,
  createMemo,
  createResource,
  createSignal,
  For,
  Show,
} from "solid-js";
import { type Grab, fetchGrabs } from "../../api/grab";
import type { Mode } from "../../api/discover";
import { ErrorText, ModeTabs, Muted } from "../../components/ui";
import { cells, dayKey, monthRange } from "./grid";

const WEEKDAYS = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];

const MODE_LABELS: Record<string, string> = {
  movies: "Movies",
  series: "Series",
  adult: "Adult",
};

// HISTORY_STATUSES is the whole filter. grabs.Status also has `queued`,
// `downloading`, `failed` and `pending_retry` (internal/grabs/grabs.go) — every
// one of them is excluded here, see the filter note in the header.
const HISTORY_STATUSES = new Set(["completed", "imported"]);

// ReviewBadge is the advisory post-grab review flag, carried over VERBATIM from
// the Grabs screen it replaces (copy, title attribute and classes) — it is
// re-implemented rather than imported because Grabs.tsx is deleted in this same
// wave. A flagged grab imported FINE; the flag only means the imported file's
// runtime looked inconsistent with its metadata and a human might want to
// eyeball it. The copy must never read as a failure — which is also why a
// flagged grab is still `imported` and stays in scope for History.
const ReviewBadge: Component<{ grab: Grab }> = (props) => (
  <Show when={props.grab.flaggedForReview}>
    <span
      class="inline-block rounded-full bg-warn/20 px-2 py-0.5 text-[11px] font-medium text-warn"
      title={props.grab.flagReason || "flagged for a manual look"}
    >
      review — imported OK, runtime looks off
    </span>
  </Show>
);

const StatusChip: Component<{ status: string }> = (props) => (
  <span class="rounded-full bg-surface-2 px-2 py-0.5 text-[11px] text-muted">
    {props.status}
  </span>
);

export const History: Component<{
  year: number;
  month: number;
  display: "grid" | "list";
}> = (props) => {
  const [mode, setMode] = createSignal<Mode>("movies");
  const [grabs] = createResource(mode, (m) => fetchGrabs(m));

  // Reading a Solid resource after its fetcher rejects RE-THROWS (by design,
  // for ErrorBoundary integration) — and this app has no ErrorBoundary
  // anywhere, so an unguarded read throws MID-RENDER and the <Show
  // when={grabs.error}> branch below never gets to run. That is the exact bug
  // that left GrabDialog stuck on "Searching…" forever (see CLAUDE.md), and it
  // is why Upcoming.tsx:131 in this same feature gates on state === "ready".
  // Gate on the state, never on a truthiness check.
  const loaded = () => (grabs.state === "ready" ? grabs() : undefined);

  // completed is the client-side filter (§2.2). Order is left as the endpoint
  // returned it (created_at DESC) — the list view must not re-sort it. Every
  // downstream memo (buckets, monthGrabs) derives from THIS, so routing it
  // through loaded() is what keeps all three reads guarded.
  const completed = createMemo(() =>
    (loaded() ?? []).filter((g) => HISTORY_STATUSES.has(g.status)),
  );

  // buckets keys the filtered grabs by their UTC YYYY-MM-DD grab day. Memoized
  // (not a plain derived function) since <For> reads it once per cell — a plain
  // function would rebuild the whole Map ~30-40 times per render pass.
  const buckets = createMemo(() => {
    const map = new Map<string, Grab[]>();
    for (const g of completed()) {
      const key = (g.createdAt ?? "").slice(0, 10);
      if (!key) continue;
      const list = map.get(key);
      if (list) list.push(g);
      else map.set(key, [g]);
    }
    return map;
  });

  // The LIST is scoped to the same visible month as the grid, deliberately:
  // the two are layouts of one dataset over one month, and index.tsx owns a
  // single month navigator shared by both. If the list ignored it, the month
  // control would silently do nothing half the time — the same class of UI
  // dishonesty the "Grabbed" header rule exists to prevent. YYYY-MM-DD strings
  // compare lexicographically, so no Date parsing is needed (and none should be
  // introduced — see the timezone note above).
  const monthGrabs = createMemo(() => {
    const { from, to } = monthRange(props.year, props.month);
    return completed().filter((g) => {
      const key = (g.createdAt ?? "").slice(0, 10);
      return key >= from && key <= to;
    });
  });

  return (
    <div>
      <ModeTabs current={mode} onSelect={setMode} />

      <Show when={grabs.error}>
        <ErrorText>{(grabs.error as Error)?.message}</ErrorText>
      </Show>

      <Show when={!grabs.loading} fallback={<Muted>Loading…</Muted>}>
        <Show
          when={props.display === "list"}
          fallback={
            <div class="grid grid-cols-7 gap-2">
              <For each={WEEKDAYS}>
                {(w) => (
                  <div class="px-1 text-xs font-semibold uppercase tracking-wide text-muted">
                    {w}
                  </div>
                )}
              </For>
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
                      {/* The day number is the cell's own coordinate, not the
                          grab's date text — see the copy-honesty note above. */}
                      <div class="mb-1 text-xs font-medium text-muted">{day}</div>
                      <div class="flex flex-col gap-1.5">
                        <For
                          each={
                            buckets().get(
                              dayKey(props.year, props.month, day!),
                            ) ?? []
                          }
                        >
                          {(g) => (
                            <div class="rounded-md border border-border bg-surface p-1.5">
                              <div
                                class="truncate text-xs text-fg"
                                title={g.title}
                              >
                                {g.title}
                              </div>
                              <div class="mt-1 flex flex-wrap items-center gap-1">
                                <StatusChip status={g.status} />
                                <ReviewBadge grab={g} />
                              </div>
                            </div>
                          )}
                        </For>
                      </div>
                    </Show>
                  </div>
                )}
              </For>
            </div>
          }
        >
          <Show
            when={monthGrabs().length > 0}
            fallback={<Muted>No completed or imported grabs this month.</Muted>}
          >
            <div class="overflow-x-auto">
              <table class="w-full text-left text-sm">
                <thead>
                  <tr class="border-b border-border text-xs uppercase tracking-wide text-muted">
                    {/* "Grabbed", never "Completed"/"Landed" — createdAt is
                        when the grab started. */}
                    <th class="px-2 py-2 font-medium">Grabbed</th>
                    <th class="px-2 py-2 font-medium">Title</th>
                    <th class="px-2 py-2 font-medium">Mode</th>
                    <th class="px-2 py-2 font-medium">Status</th>
                  </tr>
                </thead>
                <tbody>
                  <For each={monthGrabs()}>
                    {(g) => (
                      <tr class="border-b border-border/60 align-top">
                        <td class="px-2 py-2 text-muted">
                          {(g.createdAt ?? "").slice(0, 10)}
                        </td>
                        <td class="px-2 py-2 text-fg">
                          <div class="truncate" title={g.title}>
                            {g.title}
                          </div>
                          <Show when={g.flaggedForReview}>
                            <div class="mt-1 flex flex-col gap-1">
                              <ReviewBadge grab={g} />
                              <Show when={g.flagReason}>
                                <span class="text-xs text-muted">
                                  {g.flagReason}
                                </span>
                              </Show>
                            </div>
                          </Show>
                        </td>
                        {/* The row's own mode field, not the selected tab —
                            the data is the honest source. */}
                        <td class="px-2 py-2 text-muted">
                          {MODE_LABELS[g.mode] ?? g.mode}
                        </td>
                        <td class="px-2 py-2">
                          <StatusChip status={g.status} />
                        </td>
                      </tr>
                    )}
                  </For>
                </tbody>
              </table>
            </div>
          </Show>
        </Show>
      </Show>
    </div>
  );
};
