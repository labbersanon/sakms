// Claude 2026-10-02: Organize Discs — manual ISO identify then extract.
// Reason: Browse unpack mixed file-manager with import; ISOs are import-only.
//   Identify after the ISO is chosen. Existing library titles start unchecked.
//   Checking one asks replace / keep both / cancel.
// Troubleshooting: empty hits — TMDB search used the volume queries.
// Review if: IFO/ffmpeg starts exposing per-title names (lookup skipped).
//
// Claude 2026-10-02: catalog hits are a dropdown, not chips.
// Reason: TMDB returns several Golden volumes; operator must pick the title.
//   Filename vNdM is volume N, disc M (backend ranks Volume N first).
// Troubleshooting: wrong volume selected — check ParseEdition / dropdown value.
// Review if: identify returns a single unique hit (dropdown still fine).

import {
  type Component,
  For,
  Show,
  createMemo,
  createResource,
  createSignal,
} from "solid-js";
import type { OrganizeDiscHit, OrganizeDiscWork } from "@dto";
import { FolderPicker } from "../components/FolderPicker";
import { Button, ErrorText, Muted, SELECT_CLASS, labelClass } from "../components/ui";
import {
  fetchOrganizeDiscExtract,
  identifyOrganizeDisc,
  startOrganizeDiscExtract,
} from "../api/discs";
import { fetchOrganizeBrowse } from "../api/organizeBrowse";
import { SeasonEpisodeAccordion } from "./discover/SeasonEpisodeAccordion";
import { Modal } from "./discover/shared";
import { ActivityLogPanel } from "./OrganizeChrome";

const panelClass =
  "rounded-xl border border-border bg-surface/95 shadow-sm backdrop-blur-md";

function isDiscName(name: string): boolean {
  return /\.(iso|img)$/i.test(name);
}

function formatDuration(sec: number): string {
  if (!sec || sec < 0) return "";
  const s = Math.round(sec);
  const m = Math.floor(s / 60);
  const r = s % 60;
  if (m >= 60) {
    const h = Math.floor(m / 60);
    return `${h}h ${m % 60}m`;
  }
  if (m) return `${m}m ${r}s`;
  return `${r}s`;
}

type Slot = { season: number; episode: number; title?: string };

function slotsFromHit(hit: OrganizeDiscHit | undefined): Record<string, Slot> {
  const next: Record<string, Slot> = {};
  for (const s of hit?.suggestions ?? []) {
    if (!s.name || s.episode < 1) continue;
    next[s.name] = { season: s.season, episode: s.episode, title: s.title };
  }
  return next;
}

function existingForWork(
  hit: OrganizeDiscHit | undefined,
  _work: OrganizeDiscWork,
  slot?: Slot,
): { path: string; title: string } | null {
  if (!hit) return null;
  if (hit.mode === "movies" && hit.existingPath) {
    return { path: hit.existingPath, title: hit.existingTitle || hit.title };
  }
  if (hit.mode === "series" && slot) {
    const ep = (hit.episodes ?? []).find(
      (e) => e.season === slot.season && e.episode === slot.episode,
    );
    if (ep) return { path: ep.path, title: ep.title || `S${slot.season}E${slot.episode}` };
  }
  return null;
}

export const Discs: Component = () => {
  const [folder, setFolder] = createSignal("");
  const [isoPath, setIsoPath] = createSignal("");
  const [hitKey, setHitKey] = createSignal("");
  const [slots, setSlots] = createSignal<Record<string, Slot>>({});
  const [selected, setSelected] = createSignal<Set<string>>(new Set());
  const [conflicts, setConflicts] = createSignal<Record<string, string>>({});
  const [conflictRow, setConflictRow] = createSignal<{
    name: string;
    path: string;
    title: string;
  } | null>(null);
  const [assignName, setAssignName] = createSignal("");
  const [busy, setBusy] = createSignal(false);
  const [error, setError] = createSignal("");
  const [progress, setProgress] = createSignal("");
  const [logKey, setLogKey] = createSignal(0);

  const [listing] = createResource(
    () => folder().trim(),
    async (dir) => {
      if (!dir) return { entries: [] };
      return fetchOrganizeBrowse(dir);
    },
  );

  const isos = createMemo(() =>
    (listing()?.entries ?? []).filter((e) => !e.isDir && isDiscName(e.name)),
  );

  const [identified] = createResource(
    () => isoPath(),
    async (path) => {
      if (!path) return null;
      setError("");
      setSelected(new Set<string>());
      setConflicts({});
      setSlots({});
      setHitKey("");
      const resp = await identifyOrganizeDisc(path);
      const first = (resp.hits ?? [])[0];
      if (first) setHitKey(`${first.mode}:${first.tmdbId}`);
      const nextSlots = slotsFromHit(first);
      setSlots(nextSlots);
      const initial = new Set<string>();
      for (const w of resp.works ?? []) {
        if (!existingForWork(first, w, nextSlots[w.name])) initial.add(w.name);
      }
      setSelected(initial);
      return resp;
    },
  );

  const hits = () => identified()?.hits ?? [];
  const works = () => identified()?.works ?? [];
  const hit = createMemo(() => {
    const key = hitKey();
    return hits().find((h) => `${h.mode}:${h.tmdbId}` === key);
  });

  const pickHit = (h: OrganizeDiscHit) => {
    setHitKey(`${h.mode}:${h.tmdbId}`);
    const nextSlots = slotsFromHit(h);
    setSlots(nextSlots);
    const next = new Set<string>();
    for (const w of works()) {
      if (!existingForWork(h, w, nextSlots[w.name])) next.add(w.name);
    }
    setSelected(next);
    setConflicts({});
  };

  const trySelect = (w: OrganizeDiscWork, on: boolean) => {
    const exist = existingForWork(hit(), w, slots()[w.name]);
    if (on && exist) {
      setConflictRow({ name: w.name, path: exist.path, title: exist.title });
      return;
    }
    const next = new Set(selected());
    if (on) next.add(w.name);
    else next.delete(w.name);
    setSelected(next);
    if (!on) {
      const c = { ...conflicts() };
      delete c[w.name];
      setConflicts(c);
    }
  };

  const resolveConflict = (action: "replace" | "keep_both") => {
    const row = conflictRow();
    if (!row) return;
    setSelected((cur) => {
      const next = new Set(cur);
      next.add(row.name);
      return next;
    });
    setConflicts((cur) => ({ ...cur, [row.name]: action }));
    setConflictRow(null);
  };

  const assignSlot = (name: string, season: number, episode: number, title?: string) => {
    setSlots((cur) => ({ ...cur, [name]: { season, episode, title } }));
    setAssignName("");
    const w = works().find((x) => x.name === name);
    const h = hit();
    if (!w || !h) return;
    const exist = existingForWork(h, w, { season, episode, title });
    if (exist) {
      setSelected((cur) => {
        const next = new Set(cur);
        next.delete(name);
        return next;
      });
    }
  };

  const extract = async () => {
    const path = isoPath();
    const h = hit();
    const names = [...selected()];
    if (!path || !names.length) return;
    setBusy(true);
    setError("");
    setProgress("Starting…");
    try {
      await startOrganizeDiscExtract({
        path,
        mode: h?.mode,
        tmdbId: h?.tmdbId,
        title: h?.title,
        year: h?.year,
        items: names.map((name) => {
          const slot = slots()[name];
          return {
            name,
            seasonNumber: slot?.season,
            episodeNumber: slot?.episode,
            episodeTitle: slot?.title,
            conflict: conflicts()[name],
          };
        }),
      });
      for (;;) {
        const st = await fetchOrganizeDiscExtract(path);
        setProgress(
          st.status === "extracting"
            ? `Extracting ${st.done}/${st.total || "?"}`
            : st.status,
        );
        if (st.status === "done") break;
        if (st.status === "error") {
          setError(st.error || "Extract failed");
          break;
        }
        await new Promise((r) => setTimeout(r, 1000));
      }
      setLogKey((n) => n + 1);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div>
      <h2 class="mb-1 text-lg font-semibold text-fg">Discs</h2>
      <Muted class="mb-4">
        Pick one DVD ISO. Identification runs after you select it. When
        the image has no title names, Wikipedia (SearXNG as a page finder)
        fills the disc list. Unique TMDB title or duration matches are
        pre-assigned. Titles that already exist in the library start
        unchecked.
      </Muted>

      <label class="mb-3 block">
        <span class="text-xs font-medium text-muted">Folder</span>
        <div class="mt-1">
          <FolderPicker
            value={folder}
            onChange={(p) => {
              setFolder(p);
              setIsoPath("");
            }}
            ariaLabel="Disc folder"
            placeholder="/media"
          />
        </div>
      </label>

      <div class={`mb-4 overflow-x-auto ${panelClass}`}>
        <table class="w-full text-left text-sm">
          <thead class="bg-surface-2 text-muted">
            <tr>
              <th class="px-2 py-2 font-medium">ISO</th>
              <th class="px-2 py-2 font-medium">Size</th>
            </tr>
          </thead>
          <tbody>
            <Show
              when={isos().length > 0}
              fallback={
                <tr>
                  <td colSpan={2} class="px-3 py-6 text-muted">
                    Pick a folder that contains .iso or .img files.
                  </td>
                </tr>
              }
            >
              <For each={isos()}>
                {(e) => (
                  <tr class="border-t border-border/60">
                    <td class="px-2 py-1.5">
                      <label class="flex items-center gap-2 text-fg">
                        <input
                          type="radio"
                          name="disc-iso"
                          aria-label={`Select ${e.name}`}
                          checked={isoPath() === e.path}
                          onChange={() => setIsoPath(e.path)}
                        />
                        <span>{e.name}</span>
                      </label>
                    </td>
                    <td class="px-2 py-1.5 text-muted">
                      {e.size ? `${(e.size / 1024 / 1024).toFixed(0)} MB` : "—"}
                    </td>
                  </tr>
                )}
              </For>
            </Show>
          </tbody>
        </table>
      </div>

      <Show when={identified.loading}>
        <Muted class="mb-3">Identifying disc…</Muted>
      </Show>
      <Show when={identified.error}>
        <ErrorText>{(identified.error as Error).message}</ErrorText>
      </Show>
      <Show when={error()}>
        <ErrorText>{error()}</ErrorText>
      </Show>
      <Show when={progress()}>
        <Muted class="mb-3">{progress()}</Muted>
      </Show>

      <Show when={identified()}>
        {(id) => (
          <div class="mb-4">
            <p class="mb-2 text-sm text-fg">
              Volume <span class="font-mono">{id().volume}</span>
            </p>
            <Show
              when={hits().length > 0}
              fallback={<Muted class="mb-3">No catalog match. Extract still works.</Muted>}
            >
              <div class="mb-3">
                <label class={labelClass} for="disc-catalog-title">
                  Catalog title
                </label>
                <p class="mt-1 text-xs text-muted">
                  Select the correct title. In the filename, V is volume and D
                  is disc (v5d1 is volume 5, disc 1).
                </p>
                <select
                  id="disc-catalog-title"
                  class={`${SELECT_CLASS} mt-1 w-full max-w-2xl sm:w-full`}
                  value={hitKey()}
                  onChange={(e) => {
                    const key = e.currentTarget.value;
                    const h = hits().find((x) => `${x.mode}:${x.tmdbId}` === key);
                    if (h) pickHit(h);
                  }}
                >
                  <For each={hits()}>
                    {(h) => (
                      <option value={`${h.mode}:${h.tmdbId}`}>
                        {h.mode === "series" ? "Series" : "Movie"} · {h.title}
                        {h.year ? ` (${h.year})` : ""}
                        {h.existingPath || (h.episodes ?? []).length
                          ? " · in library"
                          : ""}
                      </option>
                    )}
                  </For>
                </select>
              </div>
            </Show>

            <div class={`overflow-x-auto ${panelClass}`}>
              <table class="w-full text-left text-sm">
                <thead class="bg-surface-2 text-muted">
                  <tr>
                    <th class="w-10 px-2 py-2" />
                    <th class="px-2 py-2 font-medium">Title</th>
                    <th class="px-2 py-2 font-medium">Length</th>
                    <th class="px-2 py-2 font-medium">Library</th>
                  </tr>
                </thead>
                <tbody>
                  <For each={works()}>
                    {(w) => {
                      const exist = () => existingForWork(hit(), w, slots()[w.name]);
                      const slot = () => slots()[w.name];
                      return (
                        <tr class="border-t border-border/60">
                          <td class="px-2 py-1.5">
                            <input
                              type="checkbox"
                              aria-label={`Select ${w.name}`}
                              checked={selected().has(w.name)}
                              onClick={(e) => {
                                // Keep the box off until Replace / Keep both.
                                e.preventDefault();
                                trySelect(w, !selected().has(w.name));
                              }}
                            />
                          </td>
                          <td class="px-2 py-1.5 text-fg">
                            {w.episodeTitle || w.name}
                          </td>
                          <td class="px-2 py-1.5 text-muted">
                            {formatDuration(w.durationS)}
                          </td>
                          <td class="px-2 py-1.5 text-muted">
                            <Show when={exist()} fallback="New">
                              {(ex) => (
                                <span>
                                  Exists · {ex().title}
                                </span>
                              )}
                            </Show>
                            <Show when={hit()?.mode === "series"}>
                              <div class="mt-1">
                                <Button
                                  variant="secondary"
                                  onClick={() => setAssignName(w.name)}
                                >
                                  {slot()
                                    ? `S${String(slot()!.season).padStart(2, "0")}E${String(slot()!.episode).padStart(2, "0")}`
                                    : "Assign episode"}
                                </Button>
                              </div>
                            </Show>
                          </td>
                        </tr>
                      );
                    }}
                  </For>
                </tbody>
              </table>
            </div>

            <div class="mt-3">
              <Button
                variant="primary"
                disabled={busy() || selected().size === 0}
                onClick={() => void extract()}
              >
                Extract selected
              </Button>
            </div>
          </div>
        )}
      </Show>

      <ActivityLogPanel workflow="discs" refreshKey={logKey()} />

      <Show when={conflictRow()}>
        {(row) => (
          <Modal title="Already in the library" onClose={() => setConflictRow(null)}>
            <p class="text-sm text-fg">
              {row().title} already exists at{" "}
              <span class="font-mono text-xs">{row().path}</span>. What should
              happen to the old file?
            </p>
            <div class="mt-4 flex flex-wrap justify-end gap-2">
              <Button variant="secondary" onClick={() => setConflictRow(null)}>
                Cancel
              </Button>
              <Button variant="secondary" onClick={() => resolveConflict("keep_both")}>
                Keep both
              </Button>
              <Button variant="primary" onClick={() => resolveConflict("replace")}>
                Replace old
              </Button>
            </div>
          </Modal>
        )}
      </Show>

      <Show when={assignName() && hit()?.mode === "series" && hit()?.tmdbId}>
        <Modal title="Assign episode" onClose={() => setAssignName("")}>
          <SeasonEpisodeAccordion
            tmdbId={hit()!.tmdbId}
            onSubmit={(season, episode, episodeTitle) => {
              if (episode < 1) return;
              assignSlot(assignName(), season, episode, episodeTitle);
            }}
          />
        </Modal>
      </Show>
    </div>
  );
};
