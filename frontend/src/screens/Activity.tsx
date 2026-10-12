// Activity — Queue's compact grab log. One line per grab: when, mode, title,
// short status, and a truncated reason when there is one. Search and the
// Downloaded / Attempted / Failed chips are the whole UI.
//
// Claude 2026-10-12: new Queue tab over GET /api/modes/{mode}/grabs.
// Reason: Downloads is live engine state, Calendar History is landed-only,
//   Requests is outstanding work — none of them is a review log of attempts
//   and failures. The operator asked for minimum verbosity.
// Troubleshooting: Queue → Activity empty while grabs exist — fetch all
//   three modes (Adult skipped when the Adult toggle is off).
// Review if: a dedicated download-events table replaces the grab snapshot.

import {
  type Component,
  createMemo,
  createResource,
  createSignal,
  For,
  Show,
} from "solid-js";
import type { Grab } from "@dto";
import { fetchGrabs } from "../api/grab";
import {
  ErrorText,
  Muted,
  ScreenTabBar,
  type TabDef,
  useAdultEnabled,
} from "../components/ui";
import { matchesQueueSearch, QueueSearchField } from "./queueSearch";

export type ActivityBucket = "all" | "downloaded" | "attempted" | "failed";

const BUCKETS: TabDef[] = [
  { id: "all", label: "All" },
  { id: "downloaded", label: "Downloaded" },
  { id: "attempted", label: "Attempted" },
  { id: "failed", label: "Failed" },
];

const DOWNLOADED = new Set(["imported", "completed"]);
const ATTEMPTED = new Set(["queued", "downloading", "pending_retry"]);
const FAILED = new Set(["failed"]);

export function grabInBucket(status: string, bucket: ActivityBucket): boolean {
  if (bucket === "all") return true;
  if (bucket === "downloaded") return DOWNLOADED.has(status);
  if (bucket === "attempted") return ATTEMPTED.has(status);
  return FAILED.has(status);
}

export function activityStamp(iso: string): string {
  return (iso ?? "").slice(0, 16).replace("T", " ");
}

export function activityStatus(g: Grab): string {
  if (g.status === "pending_retry" && g.holdUntil) return "held";
  if (g.status === "pending_retry") return "retry";
  if (g.status === "completed") return "done";
  return g.status;
}

export function activityReason(raw: string | undefined): string {
  const s = (raw ?? "").replace(/\s+/g, " ").trim();
  if (!s) return "";
  const dash = s.indexOf(" — ");
  const base = dash > 0 ? s.slice(0, dash) : s;
  return base.length > 80 ? `${base.slice(0, 77)}…` : base;
}

function bucketOf(id: string): ActivityBucket {
  return BUCKETS.some((b) => b.id === id) ? (id as ActivityBucket) : "all";
}

export const Activity: Component = () => {
  const adultEnabled = useAdultEnabled();
  const [query, setQuery] = createSignal("");
  const [bucket, setBucket] = createSignal<ActivityBucket>("all");

  const [rows] = createResource(
    () => ({ adult: adultEnabled() }),
    async ({ adult }) => {
      const modes = adult
        ? (["movies", "series", "adult"] as const)
        : (["movies", "series"] as const);
      const lists = await Promise.all(
        modes.map((m) => fetchGrabs(m).catch(() => [] as Grab[])),
      );
      const flat = lists.flat();
      flat.sort((a, b) => {
        if (a.createdAt < b.createdAt) return 1;
        if (a.createdAt > b.createdAt) return -1;
        return b.id - a.id;
      });
      return flat;
    },
  );

  const loaded = () => (rows.state === "ready" ? rows() : undefined);

  const visible = createMemo(() => {
    const q = query();
    const b = bucket();
    return (loaded() ?? []).filter(
      (g) =>
        grabInBucket(g.status, b) &&
        matchesQueueSearch(
          q,
          g.title,
          g.status,
          activityStatus(g),
          g.mode,
          g.retryReason,
          g.indexer,
        ),
    );
  });

  return (
    <div>
      <div class="mb-3">
        <QueueSearchField
          id="activity-search"
          value={query()}
          onInput={setQuery}
          placeholder="Search titles, status, reason…"
        />
      </div>
      <ScreenTabBar
        tabs={BUCKETS}
        current={bucket}
        onSelect={(id) => setBucket(bucketOf(id))}
      />

      <Show when={rows.error}>
        <ErrorText>{(rows.error as Error)?.message}</ErrorText>
      </Show>
      {/* Claude 2026-10-12: wrap the log in bg-surface (white).
          Reason: Queue pages sit on cream wallpaper (--color-bg); a
            transparent <ul> lets navy mono show through ticket paper.
          Troubleshooting: Activity hard to read — same Browse failure
            (listing must use bg-surface over the wallpaper).
          Review if: Activity rows become per-item Download cards. */}
      <div class="mt-2 max-h-[70vh] overflow-y-auto rounded-xl border border-border bg-surface px-3 py-2">
        <Show when={!rows.loading} fallback={<Muted>Loading…</Muted>}>
          <Show
            when={visible().length > 0}
            fallback={<Muted>No matching activity.</Muted>}
          >
            <ul class="space-y-0.5 text-xs">
              <For each={visible()}>
                {(g) => {
                  const reason = activityReason(g.retryReason);
                  return (
                    <li class="flex items-baseline gap-3 border-b border-border/40 py-1 font-mono">
                      <span class="w-[9.5rem] shrink-0 text-muted">
                        {activityStamp(g.createdAt)}
                      </span>
                      <span class="w-14 shrink-0 text-muted">{g.mode}</span>
                      <span class="min-w-0 flex-1 truncate text-fg" title={g.title}>
                        {g.title}
                      </span>
                      <span class="shrink-0 text-fg">{activityStatus(g)}</span>
                      <Show when={reason}>
                        <span class="max-w-[20rem] min-w-0 truncate text-muted" title={g.retryReason}>
                          {reason}
                        </span>
                      </Show>
                    </li>
                  );
                }}
              </For>
            </ul>
          </Show>
        </Show>
      </div>
    </div>
  );
};
