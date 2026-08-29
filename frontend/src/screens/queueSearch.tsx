// Queue-tab search chrome. Each Queue child owns its own signal, so a query
// typed on one tab never carries over to another.

import { type Component } from "solid-js";
import { inputClass, labelClass } from "../components/ui";

/** True when query is empty or any part contains it (case-insensitive). */
export function matchesQueueSearch(
  query: string,
  ...parts: Array<string | null | undefined>
): boolean {
  const q = query.trim().toLowerCase();
  if (!q) return true;
  return parts.some((p) => (p ?? "").toLowerCase().includes(q));
}

/** Labeled search input matching Library's filter-bar field. */
export const QueueSearchField: Component<{
  id: string;
  value: string;
  onInput: (value: string) => void;
  placeholder: string;
}> = (props) => (
  <div class="w-full min-w-0 sm:min-w-[12rem] sm:flex-1">
    <label class={labelClass} for={props.id}>
      Search
    </label>
    <input
      id={props.id}
      type="search"
      class={`${inputClass} mt-1`}
      placeholder={props.placeholder}
      value={props.value}
      onInput={(e) => props.onInput(e.currentTarget.value)}
    />
  </div>
);
