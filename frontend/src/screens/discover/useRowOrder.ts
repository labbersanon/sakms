// useRowOrder — the per-screen display-prefs state machine Mainstream.tsx and
// Adult.tsx both drive: loads the screen's stored key order once, merges it
// against the screen's current known keys (mergeRowOrder), and persists a
// reorder immediately (no separate Save step, same per-click-persists
// convention as SliderAdmin/AdultRowAdmin). It also owns the sibling per-screen
// hidden-structural-row set: loaded once, exposed as isHidden(key)/toggleHidden(
// key), persisted the same immediate way. Extracted after both screens ended up
// with byte-for-byte identical load/merge/persist logic, parameterized only by
// screen name and knownKeys — see mergeRowOrder's own doc comment for why
// persistence itself stays deliberately loose (best-effort, not validated
// against a fixed id set the way rssfeeds.Store.Reorder is).

import { createEffect, createSignal } from "solid-js";
import {
  type DiscoverScreen,
  fetchRowHidden,
  fetchRowOrder,
  mergeRowOrder,
  saveRowHidden,
  saveRowOrder,
} from "../../api/rowOrder";

export function useRowOrder(screen: DiscoverScreen, knownKeys: () => string[]) {
  const [error, setError] = createSignal("");

  const [storedKeys, setStoredKeys] = createSignal<string[] | null>(null);
  createEffect(() => {
    if (storedKeys() === null) {
      fetchRowOrder(screen)
        .then(setStoredKeys)
        .catch(() => setStoredKeys([]));
    }
  });

  const orderedKeys = () => mergeRowOrder(storedKeys() ?? [], knownKeys());

  const persistOrder = (keys: string[]) => {
    setStoredKeys(keys);
    void saveRowOrder(screen, keys).catch((e) => setError((e as Error).message));
  };

  // Hidden structural rows — the sibling row-hidden store. null = not yet
  // loaded; isHidden defaults to false (visible) while loading, mirroring
  // orderedKeys' storedKeys() ?? [] pattern, so rows never flash hidden during
  // the initial fetch.
  const [hiddenKeys, setHiddenKeys] = createSignal<string[] | null>(null);
  createEffect(() => {
    if (hiddenKeys() === null) {
      fetchRowHidden(screen)
        .then(setHiddenKeys)
        .catch(() => setHiddenKeys([]));
    }
  });

  const isHidden = (key: string) => (hiddenKeys() ?? []).includes(key);

  const toggleHidden = (key: string) => {
    const current = hiddenKeys() ?? [];
    const next = current.includes(key)
      ? current.filter((k) => k !== key)
      : [...current, key];
    setHiddenKeys(next);
    void saveRowHidden(screen, next).catch((e) =>
      setError((e as Error).message),
    );
  };

  return { orderedKeys, persistOrder, isHidden, toggleHidden, error };
}
