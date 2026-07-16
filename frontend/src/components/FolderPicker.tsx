// FolderPicker — a drop-in replacement for a plain <input type="text"> on the
// Settings root-folder / kids-path fields. It stays a fully free-typed input at
// all times (the picker is a pure suggestion layer, never a hard constraint) and
// adds a dropdown listing the subdirectories of whatever path is currently typed,
// sourced from GET /api/browse (fetchBrowse). The backend lists an exact path's
// children, not a fuzzy prefix search across siblings, so the dropdown fills in
// as the operator drills down: clicking a suggestion sets the value to that
// directory's full path, whose own children are then fetched. A resolved-but-
// nonexistent path returns 200 with no entries (graceful degradation), so a
// half-typed path just shows nothing extra rather than an error.

import {
  type Component,
  createSignal,
  For,
  onCleanup,
  Show,
} from "solid-js";
import type { BrowseEntry } from "@dto";
import { fetchBrowse } from "../api/settings";
import { inputClass } from "./ui";

// DEBOUNCE_MS throttles the as-you-type fetch so each keystroke doesn't fire a
// request; a clicked suggestion reuses the same debounced path so drilling down
// stays one code path.
const DEBOUNCE_MS = 300;

export const FolderPicker: Component<{
  value: () => string;
  onChange: (path: string) => void;
  ariaLabel?: string;
  placeholder?: string;
  // invalid, when it returns true, red-tints the input — used by the Library
  // root-folder field to reflect a failed path test. Optional; other callers
  // (e.g. the kids-path field) omit it and render normally.
  invalid?: () => boolean;
}> = (props) => {
  const [entries, setEntries] = createSignal<BrowseEntry[]>([]);
  const [open, setOpen] = createSignal(false);
  let debounceTimer: ReturnType<typeof setTimeout> | undefined;
  let containerRef: HTMLDivElement | undefined;

  const doFetch = async (path: string) => {
    try {
      const r = await fetchBrowse(path);
      setEntries(r.entries ?? []);
    } catch {
      // Never surface an error mid-word — the backend already 200s an unknown
      // path with no entries, and a genuine failure just means no suggestions.
      setEntries([]);
    }
    setOpen(true);
  };

  const scheduleFetch = (path: string) => {
    if (debounceTimer !== undefined) clearTimeout(debounceTimer);
    debounceTimer = setTimeout(() => void doFetch(path), DEBOUNCE_MS);
  };

  const onInput = (v: string) => {
    props.onChange(v);
    scheduleFetch(v);
  };

  const onFocus = () => {
    // Empty value on focus: immediately show the configured roots as a starting
    // point (no debounce — this is a deliberate open, not a keystroke).
    if (props.value().trim() === "") void doFetch("");
    else if (entries().length) setOpen(true);
  };

  const pick = (entry: BrowseEntry) => {
    props.onChange(entry.path);
    setOpen(false);
    // The value change is a drill-down: fetch the picked directory's children so
    // the operator can continue without extra wiring.
    scheduleFetch(entry.path);
  };

  const onKeyDown = (e: KeyboardEvent) => {
    if (e.key === "Escape") setOpen(false);
  };

  const onDocMouseDown = (e: MouseEvent) => {
    if (containerRef && !containerRef.contains(e.target as Node)) setOpen(false);
  };
  document.addEventListener("mousedown", onDocMouseDown);
  onCleanup(() => {
    document.removeEventListener("mousedown", onDocMouseDown);
    if (debounceTimer !== undefined) clearTimeout(debounceTimer);
  });

  return (
    <div class="relative" ref={containerRef}>
      <input
        type="text"
        class={`${inputClass} mt-1 ${props.invalid?.() ? "border-danger bg-danger/10" : ""}`}
        placeholder={props.placeholder}
        aria-label={props.ariaLabel}
        value={props.value()}
        onInput={(e) => onInput(e.currentTarget.value)}
        onFocus={onFocus}
        onKeyDown={onKeyDown}
      />
      <Show when={open() && entries().length > 0}>
        <ul class="absolute z-10 mt-1 max-h-60 w-full overflow-auto rounded-md border border-border bg-surface shadow-lg">
          <For each={entries()}>
            {(entry) => (
              <li>
                <button
                  type="button"
                  class="block w-full px-3 py-2 text-left text-sm text-fg hover:bg-surface-2"
                  onClick={() => pick(entry)}
                >
                  <span class="text-fg">{entry.name}</span>
                  <span class="ml-2 text-xs text-muted">{entry.path}</span>
                </button>
              </li>
            )}
          </For>
        </ul>
      </Show>
    </div>
  );
};
