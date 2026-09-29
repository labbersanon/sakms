// Claude 2026-09-28: Organize Import tab — identify, then MOVE into the library.
// Reason: SAK is the file manager; dump-folder files must leave the source
//   and land in the configured Movies/Series root. Confirm is the approval.
// Troubleshooting: Scan 400 — path outside /media,/downloads,/adult,/staging
//   or no library root. Adult is not a mode here. Unmatched rows stay listed
//   but cannot be imported.
// Review if: Adult import is added.

import {
  type Component,
  For,
  Show,
  createMemo,
  createSignal,
} from "solid-js";
import { FolderPicker } from "../components/FolderPicker";
import { Button, ErrorText, Muted } from "../components/ui";
import {
  type ImportMode,
  type ManualImportItem,
  applyManualImport,
  scanManualImport,
} from "../api/manualImport";
import { Modal } from "./discover/shared";

const panelClass =
  "rounded-xl border border-border bg-surface/95 shadow-sm backdrop-blur-md";

function episodeLabel(item: ManualImportItem): string {
  if (item.mode !== "series" || !item.episodeNumber) return "";
  const s = String(item.seasonNumber ?? 0).padStart(2, "0");
  const e = String(item.episodeNumber).padStart(2, "0");
  return `S${s}E${e}`;
}

function applyError(results: { ok: boolean; sourcePath: string; error?: string }[]): string {
  return results
    .filter((r) => !r.ok)
    .map((r) => `${r.sourcePath}: ${r.error || "failed"}`)
    .join("\n");
}

export const Import: Component = () => {
  const [mode, setMode] = createSignal<ImportMode>("movies");
  const [path, setPath] = createSignal("");
  const [items, setItems] = createSignal<ManualImportItem[]>([]);
  const [destRoot, setDestRoot] = createSignal("");
  const [selected, setSelected] = createSignal<Set<string>>(new Set());
  const [busy, setBusy] = createSignal(false);
  const [error, setError] = createSignal("");
  const [confirm, setConfirm] = createSignal(false);

  const pending = createMemo(() =>
    items().filter((i) => i.status === "pending" && i.destPath),
  );
  const selectedPending = createMemo(() =>
    pending().filter((i) => selected().has(i.sourcePath)),
  );

  const toggle = (p: string) => {
    const next = new Set(selected());
    if (next.has(p)) next.delete(p);
    else next.add(p);
    setSelected(next);
  };
  const toggleAllPending = () => {
    const all = pending();
    if (selectedPending().length === all.length) {
      setSelected(new Set());
      return;
    }
    setSelected(new Set(all.map((i) => i.sourcePath)));
  };

  const scan = async () => {
    const src = path().trim();
    if (!src) {
      setError("Pick a source folder first.");
      return;
    }
    setBusy(true);
    setError("");
    try {
      const resp = await scanManualImport(mode(), src);
      setDestRoot(resp.destRoot);
      setItems(resp.items ?? []);
      setSelected(
        new Set(
          (resp.items ?? [])
            .filter((i) => i.status === "pending" && i.destPath)
            .map((i) => i.sourcePath),
        ),
      );
    } catch (e) {
      setError((e as Error).message);
      setItems([]);
      setSelected(new Set());
    } finally {
      setBusy(false);
    }
  };

  const runImport = async () => {
    const chosen = selectedPending();
    if (!chosen.length) return;
    setBusy(true);
    setError("");
    try {
      const resp = await applyManualImport(mode(), chosen);
      const err = applyError(resp.results ?? []);
      if (err) setError(err);
      else setConfirm(false);
      const failed = new Set(
        (resp.results ?? []).filter((r) => !r.ok).map((r) => r.sourcePath),
      );
      setItems((cur) => cur.filter((i) => failed.has(i.sourcePath) || i.status !== "pending" || !selected().has(i.sourcePath)));
      setSelected(failed);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div>
      <h2 class="mb-1 text-lg font-semibold text-fg">Import</h2>
      <Muted class="mb-4">
        Identify videos in a dump folder and move them into the library.
        Source copies are not kept. Movies and Series only.
      </Muted>

      <div class="mb-4 flex flex-wrap gap-2">
        <Button
          variant={mode() === "movies" ? "primary" : "secondary"}
          onClick={() => setMode("movies")}
        >
          Movies
        </Button>
        <Button
          variant={mode() === "series" ? "primary" : "secondary"}
          onClick={() => setMode("series")}
        >
          Series
        </Button>
      </div>

      <label class="mb-3 block">
        <span class="text-xs font-medium text-muted">Source folder</span>
        <div class="mt-1">
          <FolderPicker
            value={path}
            onChange={setPath}
            ariaLabel="Source folder"
            placeholder="/downloads"
          />
        </div>
      </label>

      <div class="mb-4 flex flex-wrap items-center gap-2">
        <Button variant="primary" disabled={busy()} onClick={() => void scan()}>
          Scan
        </Button>
        <Button
          variant="primary"
          disabled={busy() || selectedPending().length === 0}
          onClick={() => setConfirm(true)}
        >
          Import selected
        </Button>
        <Show when={destRoot()}>
          <Muted>Library: {destRoot()}</Muted>
        </Show>
      </div>

      <Show when={error()}>
        <ErrorText>{error()}</ErrorText>
      </Show>

      <div class={`overflow-x-auto ${panelClass}`}>
        <table class="w-full text-left text-sm">
          <thead class="bg-surface-2 text-muted">
            <tr>
              <th class="w-10 px-2 py-2">
                <input
                  type="checkbox"
                  aria-label="Select all identified"
                  checked={
                    pending().length > 0 &&
                    selectedPending().length === pending().length
                  }
                  onChange={toggleAllPending}
                />
              </th>
              <th class="px-2 py-2 font-medium">Source</th>
              <th class="px-2 py-2 font-medium">Title</th>
              <th class="px-2 py-2 font-medium">Library path</th>
              <th class="px-2 py-2 font-medium">Status</th>
            </tr>
          </thead>
          <tbody>
            <Show
              when={items().length > 0}
              fallback={
                <tr>
                  <td colSpan={5} class="px-3 py-6 text-muted">
                    Pick a folder and click Scan.
                  </td>
                </tr>
              }
            >
              <For each={items()}>
                {(item) => {
                  const canImport = item.status === "pending" && !!item.destPath;
                  return (
                    <tr class="border-t border-border/60">
                      <td class="px-2 py-1.5">
                        <input
                          type="checkbox"
                          aria-label={`Select ${item.sourceName}`}
                          disabled={!canImport}
                          checked={selected().has(item.sourcePath)}
                          onChange={() => canImport && toggle(item.sourcePath)}
                        />
                      </td>
                      <td class="px-2 py-1.5">
                        <div class="truncate text-fg">{item.sourceName}</div>
                        <div class="truncate font-mono text-xs text-muted">
                          {item.sourcePath}
                        </div>
                      </td>
                      <td class="px-2 py-1.5 text-fg">
                        <Show when={item.title} fallback="—">
                          {item.title}
                          <Show when={item.year}> ({item.year})</Show>
                          <Show when={episodeLabel(item)}>
                            {" "}
                            {episodeLabel(item)}
                          </Show>
                        </Show>
                      </td>
                      <td class="px-2 py-1.5 font-mono text-xs text-muted">
                        {item.destPath || "—"}
                      </td>
                      <td class="px-2 py-1.5 text-muted">
                        {item.status === "pending" ? "Ready" : item.reason || item.status}
                      </td>
                    </tr>
                  );
                }}
              </For>
            </Show>
          </tbody>
        </table>
      </div>

      <Show when={confirm()}>
        <Modal title="Move into the library?" onClose={() => !busy() && setConfirm(false)}>
          <Muted class="mb-3">
            {selectedPending().length} file
            {selectedPending().length === 1 ? "" : "s"} will be moved into{" "}
            {destRoot() || "the library"}. The source copies are not kept.
          </Muted>
          <div class="mt-4 flex justify-end gap-2">
            <Button
              variant="secondary"
              disabled={busy()}
              onClick={() => setConfirm(false)}
            >
              Cancel
            </Button>
            <Button variant="primary" disabled={busy()} onClick={() => void runImport()}>
              Move files
            </Button>
          </div>
        </Modal>
      </Show>
    </div>
  );
};
