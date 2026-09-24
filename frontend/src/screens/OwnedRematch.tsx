// OwnedRematch is SearchTakeover's fourth caller: wrong-identity on an
// owned Discover/Library title. It is a full-page section, never a Modal.
// Claude 2026-09-24: owned detail Rematch. onCommit writes PUT .../identity.
// Reason: Rename's takeover commits proposals; this one updates the library row.
// Troubleshooting: nesting this inside DetailPopup would stack two overlays.
// Review if: Rematch should also rename files on disk.

import type { Component } from "solid-js";
import { Muted } from "../components/ui";
import type { Mode } from "../api/discover";
import type { TrackedItem } from "../api/tag";
import { SearchTakeover, type TakeoverPick } from "./SearchTakeover";

export const OwnedRematch: Component<{
  mode: Mode;
  item: TrackedItem;
  onCommit: (pick: TakeoverPick) => Promise<void>;
  onDone: () => void;
  onCancel: () => void;
}> = (props) => (
  <SearchTakeover
    heading={`Rematch “${props.item.title}”`}
    subheading={
      <Muted class="mt-1">
        Currently matched: {props.item.title}
        {props.item.year ? ` (${props.item.year})` : ""}
        {props.mode === "adult" && props.item.box
          ? ` · ${props.item.box}`
          : ""}
      </Muted>
    }
    searchMode={props.mode}
    initialQuery={props.item.title}
    autoSearch
    onCommit={props.onCommit}
    onDone={props.onDone}
    onCancel={props.onCancel}
  />
);
