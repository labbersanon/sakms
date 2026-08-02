// Discover select-mode selection state (Discover-depth Pass 2, F3 bulk-grab).
//
// This is the safety centerpiece for the plan's pre-mortem #5 — the
// live-acquisition mis-grab vector. The rule it enforces: "Grab all" may only
// ever fire against a title whose card is CURRENTLY rendered, never a
// stale/orphaned selection key left over from a no-longer-visible context. A
// wrong tmdbId landing in the library is direct drift (the worst failure class
// for this app's mission), so this file is deliberately conservative.
//
// Two independent defenses, both required:
//   1. Clear-on-navigation. The selection is reset on tab change AND route
//      change (see index.tsx's selectTab + the guarded useLocation effect), so
//      a selection can't survive the operator leaving the context it was made
//      in.
//   2. Live-registry orphan drop. Every selectable card, while rendered in
//      select-mode, register()s its key + the exact GrabTarget it would grab.
//      buildBatch() submits ONLY selected keys that are still registered (their
//      card is on screen right now), pulling the payload from the LIVE registry
//      — never from a copy captured at toggle time. A selected key whose card
//      is no longer rendered is dropped and REPORTED ("N selected, M submitted"
//      via BulkResultModal), never silently grabbed. This makes a wrong-tmdbId
//      grab structurally impossible even if defense #1 were somehow bypassed.
//
// Key format (documented, since it's load-bearing for uniqueness):
//   - Movies title:             `movies:${tmdbId}`
//   - Adult scene:              `adult:${sceneId}`
//   - A whole Series season:    `series:${tmdbId}:S${season}`
//   - A single Series episode:  `series:${tmdbId}:S${season}E${episode}`
// `...:S4` and `...:S4E1` are distinct strings, so the two Series forms cannot
// collide; nor can `S4E*` prefix-match `S41`, because the prefix carries the
// trailing `E`. A Series card is NEVER directly selectable as a whole — only
// its individual seasons/episodes are (multi-season and multi-episode within
// one series are in scope; whole-series multi-select is explicitly OUT of v1
// scope). SeriesSeasonSelect enumerates both levels from the season/episode
// grid picker — see Mainstream.tsx.
//
// WHOLE-SEASON AND EPISODE-OF-THAT-SEASON ARE MUTUALLY EXCLUSIVE, and that rule
// lives in the CALLER (SeriesSeasonSelect), never here. Dispatching `S4` and
// `S4E7` together would grab a season pack plus a duplicate single episode of
// the same content — two genuinely different releases, so the download-client
// GID dedup cannot catch it. The caller enforces it by enumerating keys() and
// clearing the conflicting ones. This store deliberately parses nothing: it
// stores opaque strings, and teaching it Series key structure would couple the
// safety centerpiece to one screen's naming.

import {
  type JSX,
  createContext,
  createSignal,
  useContext,
} from "solid-js";
import { type AutoGrabBatchItem } from "../../api/grab";
import { type GrabTarget } from "./shared";

// BuiltBatch is what buildBatch() returns: the items actually safe to submit,
// plus the two counts BulkResultModal shows so an orphan drop is never silent.
export type BuiltBatch = {
  items: AutoGrabBatchItem[];
  selectedCount: number;
  submittedCount: number;
};

export type SelectionStore = {
  // selectMode is the opt-in toggle; mutually exclusive with Edit (index.tsx).
  selectMode: () => boolean;
  setSelectMode: (v: boolean) => void;
  // toggle flips a key's membership; has/count/keys are reactive reads.
  toggle: (key: string) => void;
  has: (key: string) => boolean;
  clear: () => void;
  count: () => number;
  // keys enumerates the currently-selected keys, so a caller that owns a key
  // NAMESPACE can find and clear the ones its own rules conflict with — the
  // whole-season vs. episode-of-that-season exclusion in SeriesSeasonSelect
  // (see the file header). READ-ONLY and format-agnostic by construction: it
  // hands back opaque strings and a fresh array, so the store still parses
  // nothing and a caller mutating the result cannot corrupt the selection.
  //
  // This exists because the exclusion is NOT solvable card-locally: the same
  // title can render in two rows at once (Trending and Popular — see
  // register's own doc below), giving two components independent local state
  // over one shared selection set. A season picked on one card and an episode
  // of it picked on the other would otherwise both survive into buildBatch().
  keys: () => string[];
  // register records a currently-rendered selectable card's key + its exact
  // grab target, ref-counted so the same key rendered by two rows (e.g. a movie
  // in both Trending and Popular) survives one of them unmounting. Returns the
  // cleanup to call in onCleanup.
  register: (key: string, target: GrabTarget) => () => void;
  // buildBatch is the orphan-drop gate — see the file header.
  buildBatch: () => BuiltBatch;
};

// createSelection builds the store. Deliberately uses only createSignal (no
// createEffect/createMemo) so it is safe to construct in a plain unit test
// without a reactive root — the crown-jewel pre-mortem #5 test exercises
// register/toggle/buildBatch directly, no router or component tree needed.
export function createSelection(): SelectionStore {
  const [selectMode, setSelectMode] = createSignal(false);
  const [keys, setKeys] = createSignal<Set<string>>(new Set<string>());
  // Ref-counted live registry of on-screen selectable targets.
  const registry = new Map<string, { target: GrabTarget; count: number }>();

  const toggle = (key: string) =>
    setKeys((prev) => {
      const next = new Set<string>(prev);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });

  const clear = () => setKeys(new Set<string>());
  const has = (key: string) => keys().has(key);
  const count = () => keys().size;
  const keyList = () => [...keys()];

  const register = (key: string, target: GrabTarget) => {
    const existing = registry.get(key);
    if (existing) {
      existing.count += 1;
      existing.target = target;
    } else {
      registry.set(key, { target, count: 1 });
    }
    return () => {
      const e = registry.get(key);
      if (!e) return;
      e.count -= 1;
      if (e.count <= 0) registry.delete(key);
    };
  };

  const buildBatch = (): BuiltBatch => {
    const selected = [...keys()];
    const items: AutoGrabBatchItem[] = [];
    for (const key of selected) {
      const entry = registry.get(key);
      // Orphan drop: a selected key whose card is no longer rendered has no
      // live registry entry — it is excluded here (and counted as dropped via
      // selectedCount > submittedCount), never grabbed.
      if (entry) {
        items.push({ mode: entry.target.mode, request: entry.target.request });
      }
    }
    return {
      items,
      selectedCount: selected.length,
      submittedCount: items.length,
    };
  };

  return {
    selectMode,
    setSelectMode,
    toggle,
    has,
    clear,
    count,
    keys: keyList,
    register,
    buildBatch,
  };
}

const SelectionContext = createContext<SelectionStore>();

// SelectionProvider passes a store (created by the Discover shell, so the shell
// can drive selectMode/clear from its tab + route handlers) down to the cards.
export function SelectionProvider(props: {
  store: SelectionStore;
  children: JSX.Element;
}): JSX.Element {
  return (
    <SelectionContext.Provider value={props.store}>
      {props.children}
    </SelectionContext.Provider>
  );
}

// useSelection returns the store, or undefined when a card is rendered outside a
// provider (unit tests, or the DetailPopup "More like this" rail — which never
// runs in select-mode anyway, since the popup can't be open while select-mode
// is active). Cards guard for undefined and fall back to their normal behavior.
export function useSelection(): SelectionStore | undefined {
  return useContext(SelectionContext);
}
