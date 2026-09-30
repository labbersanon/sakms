// Claude 2026-09-29: catalog-image picker for an owned Adult scene.
// Reason: operator overwrite is a pick from stash-box/TPDB images, not a
//   pasted URL. Persist is PUT /scenes/{id}/poster.
// Review if: untracked Discover cards gain a persist target.

import { type Component, For, Show, createResource, createSignal } from "solid-js";
import { proxyImage } from "../api/discover";
import {
  fetchAdultCatalogPosters,
  setAdultScenePoster,
} from "../api/adultPoster";
import { Button, ErrorText, Muted } from "./ui";
import { Modal } from "../screens/discover/shared";

export const AdultPosterPicker: Component<{
  sceneId: number;
  onClose: () => void;
  onPicked: (url: string) => void;
}> = (props) => {
  const [urls] = createResource(() => props.sceneId, fetchAdultCatalogPosters);
  const [saving, setSaving] = createSignal<string | null>(null);
  const [error, setError] = createSignal("");

  const pick = async (url: string) => {
    if (saving()) return;
    setSaving(url);
    setError("");
    try {
      await setAdultScenePoster(props.sceneId, url);
      props.onPicked(url);
      props.onClose();
    } catch (e) {
      setError((e as Error).message || "Could not save poster");
    } finally {
      setSaving(null);
    }
  };

  return (
    <Modal title="Change poster" onClose={props.onClose}>
      <Show when={error()}>
        <ErrorText>{error()}</ErrorText>
      </Show>
      <Show when={!urls.loading} fallback={<Muted>Loading catalog images…</Muted>}>
        <Show
          when={(urls() ?? []).length > 0}
          fallback={<Muted>No catalog images for this scene.</Muted>}
        >
          <div
            class="grid grid-cols-2 gap-2 sm:grid-cols-3"
            data-testid="adult-poster-picker"
          >
            <For each={urls() ?? []}>
              {(url) => (
                <button
                  type="button"
                  class="overflow-hidden rounded-lg border border-border bg-surface-2 disabled:opacity-60"
                  disabled={!!saving()}
                  onClick={() => void pick(url)}
                >
                  <img
                    src={proxyImage(url)}
                    alt=""
                    class="aspect-[2/3] h-auto w-full object-cover"
                  />
                </button>
              )}
            </For>
          </div>
        </Show>
      </Show>
      <div class="mt-3">
        <Button onClick={props.onClose}>Cancel</Button>
      </div>
    </Modal>
  );
};
