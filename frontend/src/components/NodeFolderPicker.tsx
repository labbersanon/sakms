// NodeFolderPicker — the node-mapping equivalent of FolderPicker.tsx, but
// sourced from GET /api/nodes/{id}/browse (a specific connected node's real
// filesystem) instead of GET /api/browse (the server's). Unlike FolderPicker,
// a fetch failure here is genuinely actionable (the node may be offline or
// mid-timeout) rather than "nothing typed yet", so it surfaces an inline
// error instead of silently showing no suggestions.

import {
  type Component,
  createSignal,
  For,
  onCleanup,
  Show,
} from "solid-js";
import type { NodeBrowseEntry } from "@dto";
import { fetchNodeBrowse } from "../api/settings";
import { inputClass } from "./ui";

const DEBOUNCE_MS = 300;

export const NodeFolderPicker: Component<{
  nodeId: string;
  value: () => string;
  onChange: (path: string) => void;
  ariaLabel?: string;
  placeholder?: string;
  disabled?: boolean;
}> = (props) => {
  const [entries, setEntries] = createSignal<NodeBrowseEntry[]>([]);
  const [open, setOpen] = createSignal(false);
  const [err, setErr] = createSignal("");
  let debounceTimer: ReturnType<typeof setTimeout> | undefined;
  let containerRef: HTMLDivElement | undefined;

  const doFetch = async (path: string) => {
    try {
      const r = await fetchNodeBrowse(props.nodeId, path);
      setEntries(r.entries ?? []);
      setErr("");
    } catch (e) {
      setEntries([]);
      setErr(String(e));
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
    if (props.disabled) return;
    if (props.value().trim() === "") void doFetch("");
    else if (entries().length || err()) setOpen(true);
  };

  const pick = (entry: NodeBrowseEntry) => {
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
        class={inputClass}
        placeholder={props.placeholder}
        aria-label={props.ariaLabel}
        value={props.value()}
        disabled={props.disabled}
        onInput={(e) => onInput(e.currentTarget.value)}
        onFocus={onFocus}
        onKeyDown={onKeyDown}
      />
      <Show when={open() && err()}>
        <p class="mt-1 text-xs text-danger">{err()}</p>
      </Show>
      <Show when={open() && !err() && entries().length > 0}>
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
