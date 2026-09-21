// FolderPicker — free-typed path input with subdirectory suggestions from
// GET /api/browse for Settings path fields. Suggestions list an exact path's
// children (drill-down on click); unknown paths return empty entries, not errors.

import {
  type Component,
  createSignal,
  For,
  onCleanup,
  Show,
} from "solid-js";
import type { BrowseEntry } from "@dto";
import { fetchBrowse, isAdultBrowsablePath } from "../api/settings";
import { inputClass, useAdultEnabled } from "./ui";

const DEBOUNCE_MS = 300;

export const FolderPicker: Component<{
  value: () => string;
  onChange: (path: string) => void;
  ariaLabel?: string;
  placeholder?: string;
  invalid?: () => boolean;
  // Claude 2026-09-20: optional disabled for gated path fields (off-data staging).
  // Reason: Usenet off-data staging input is inert until the enable toggle is on;
  //   browse must not fire while disabled.
  // Troubleshooting: focus/type on a disabled FolderPicker must not hit /api/browse.
  // Review if: every caller that needs disabled has migrated — then keep the prop.
  disabled?: () => boolean;
}> = (props) => {
  const adultEnabled = useAdultEnabled();
  const [entries, setEntries] = createSignal<BrowseEntry[]>([]);
  const [open, setOpen] = createSignal(false);
  let debounceTimer: ReturnType<typeof setTimeout> | undefined;
  let containerRef: HTMLDivElement | undefined;

  const isDisabled = () => props.disabled?.() === true;

  const doFetch = async (path: string) => {
    if (isDisabled()) return;
    try {
      const r = await fetchBrowse(path);
      setEntries(r.entries ?? []);
    } catch {
      setEntries([]);
    }
    setOpen(true);
  };

  const scheduleFetch = (path: string) => {
    if (debounceTimer !== undefined) clearTimeout(debounceTimer);
    debounceTimer = setTimeout(() => void doFetch(path), DEBOUNCE_MS);
  };

  const visibleEntries = () =>
    adultEnabled()
      ? entries()
      : entries().filter((e) => !isAdultBrowsablePath(e.path));

  const onInput = (v: string) => {
    if (isDisabled()) return;
    props.onChange(v);
    scheduleFetch(v);
  };

  const onFocus = () => {
    if (isDisabled()) return;
    if (props.value().trim() === "") void doFetch("");
    else if (entries().length) setOpen(true);
  };

  const pick = (entry: BrowseEntry) => {
    props.onChange(entry.path);
    setOpen(false);
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
        disabled={isDisabled()}
        onInput={(e) => onInput(e.currentTarget.value)}
        onFocus={onFocus}
        onKeyDown={onKeyDown}
      />
      <Show when={!isDisabled() && open() && visibleEntries().length > 0}>
        <ul class="absolute z-10 mt-1 max-h-60 w-full overflow-auto rounded-md border border-border bg-surface shadow-lg">
          <For each={visibleEntries()}>
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
