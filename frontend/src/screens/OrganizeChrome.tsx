// Shared Organize chrome: page size, pagination, activity log panel.
import { type Component, createResource, For, onCleanup, Show } from "solid-js";
import {
  type AdultOrganizeAspect,
  type OrganizeWorkflow,
  PAGE_SIZE_OPTIONS,
  fetchOrganizeEvents,
} from "../api/organize";
import { Button, Muted } from "../components/ui";
import type { OrganizeEvent } from "@dto";

export const PageSizeSelect: Component<{
  value: number;
  onChange: (n: number) => void;
}> = (props) => (
  <label class="flex items-center gap-2 text-sm text-muted">
    Page size
    <select
      class="rounded border border-border bg-surface px-2 py-1 text-fg"
      value={props.value}
      onChange={(e) => props.onChange(Number(e.currentTarget.value))}
    >
      <For each={[...PAGE_SIZE_OPTIONS]}>
        {(n) => <option value={n}>{n}</option>}
      </For>
    </select>
  </label>
);

// Claude 2026-08-05: Show history swaps live ↔ applied/dismissed
//   (deep-interview-organize-hide-applied).
// Reason: default live queue must not show finished rows.
// Troubleshooting: toggle on but live rows still show → API view=history not sent.
// Review if: combined live+history mode returns.
export const ShowHistoryToggle: Component<{
  checked: boolean;
  onChange: (on: boolean) => void;
}> = (props) => (
  <label class="flex items-center gap-2 text-sm text-muted">
    <input
      type="checkbox"
      checked={props.checked}
      onChange={(e) => props.onChange(e.currentTarget.checked)}
    />
    Show history
  </label>
);

export const PaginationBar: Component<{
  total: number;
  limit: number;
  offset: number;
  onPrev: () => void;
  onNext: () => void;
}> = (props) => {
  const page = () => Math.floor(props.offset / props.limit) + 1;
  const pages = () => Math.max(1, Math.ceil(props.total / props.limit));
  return (
    <div class="mt-3 flex items-center gap-3 text-sm">
      <Button
        variant="secondary"
        disabled={props.offset <= 0}
        onClick={props.onPrev}
      >
        Prev
      </Button>
      <Muted>
        Page {page()} / {pages()} ({props.total} total)
      </Muted>
      <Button
        variant="secondary"
        disabled={props.offset + props.limit >= props.total}
        onClick={props.onNext}
      >
        Next
      </Button>
    </div>
  );
};

export const ActivityLogPanel: Component<{
  workflow: OrganizeWorkflow;
  refreshKey: number;
}> = (props) => {
  const [events] = createResource(
    () => `${props.workflow}:${props.refreshKey}`,
    () => {
      const ac = new AbortController();
      onCleanup(() => ac.abort());
      return fetchOrganizeEvents(props.workflow, 40, ac.signal).catch((e) => {
        if (ac.signal.aborted) return [] as OrganizeEvent[];
        throw e;
      });
    },
  );
  return (
    <details class="mt-6 rounded border border-border p-3">
      <summary class="cursor-pointer text-sm font-medium">Activity log</summary>
      <Show when={events.error}>
        <Muted class="mt-2">{(events.error as Error)?.message}</Muted>
      </Show>
      <Show when={!events.loading} fallback={<Muted class="mt-2">Loading…</Muted>}>
        <Show
          when={(events() ?? []).length > 0}
          fallback={<Muted class="mt-2">No events yet.</Muted>}
        >
          <ul class="mt-2 max-h-48 space-y-1 overflow-y-auto text-xs">
            <For each={events() ?? []}>
              {(e: OrganizeEvent) => (
                <li class="border-b border-border/50 py-1 font-mono">
                  <span class="text-muted">{e.createdAt}</span>{" "}
                  <span class="uppercase">{e.kind}</span>{" "}
                  {e.mode ? `[${e.mode}] ` : ""}
                  {e.message}
                  {e.ok === false ? " ✗" : e.ok === true ? " ✓" : ""}
                </li>
              )}
            </For>
          </ul>
        </Show>
      </Show>
    </details>
  );
};

const ADULT_ASPECT_KEY = "sakms.organize.adultAspect";

const ADULT_ASPECT_CHIPS: { id: AdultOrganizeAspect; label: string }[] = [
  { id: "", label: "All" },
  { id: "horizontal", label: "Scenes" },
  { id: "vertical", label: "Movies" },
];

export function loadAdultOrganizeAspect(): AdultOrganizeAspect {
  try {
    const raw = localStorage.getItem(ADULT_ASPECT_KEY);
    if (raw === "vertical" || raw === "horizontal") return raw;
  } catch {
    /* ignore */
  }
  return "";
}

export function saveAdultOrganizeAspect(value: AdultOrganizeAspect): void {
  try {
    localStorage.setItem(ADULT_ASPECT_KEY, value);
  } catch {
    /* ignore */
  }
}

// Claude 2026-08-14: Adult Organize All/Scenes/Movies chips.
// Reason: Library already splits by poster_aspect_class; Rename/Dedup/Clean-up
//   still scanned every library_scenes row. Visible labels stay All/Scenes/
//   Movies; aria-label prefixes Adult so they do not collide with ModeTabs.
// Review if: Organize splits into dedicated Movies/Scenes workflows.
export const AdultOrganizeAspectBar: Component<{
  value: AdultOrganizeAspect;
  onChange: (next: AdultOrganizeAspect) => void;
}> = (props) => (
  <div
    class="mb-4 flex items-center gap-1"
    role="group"
    aria-label="Adult library filter"
  >
    <For each={ADULT_ASPECT_CHIPS}>
      {(t) => (
        <button
          type="button"
          aria-label={`Adult ${t.label}`}
          class="rounded-md px-3 py-1.5 text-sm font-medium transition"
          classList={{
            "bg-accent text-accent-fg": props.value === t.id,
            "bg-surface-2 text-muted hover:text-fg": props.value !== t.id,
          }}
          onClick={() => props.onChange(t.id)}
        >
          {t.label}
        </button>
      )}
    </For>
  </div>
);
