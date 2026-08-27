// Claude 2026-08-27: Organize Browse tab — confirm-then-mutate file manager.
// Reason: confirm dialogs are the approval gate, not a Scan/Apply queue.
//   Rename is single-select; move and delete are multi-select.
// Troubleshooting: empty listing — GET /api/organize/browse?path=; 403 — Organize
//   section lock. Tracked badge comes from library path tables, not a rescan.
// Review if: Browse is staged onto the proposals queue.

import {
  type Component,
  For,
  Show,
  createEffect,
  createMemo,
  createResource,
  createSignal,
} from "solid-js";
import type { OrganizeBrowseEntry, OrganizeBrowseOpItem } from "@dto";
import {
  deleteOrganizeBrowse,
  fetchOrganizeBrowse,
  moveOrganizeBrowse,
  renameOrganizeBrowse,
} from "../api/organizeBrowse";
import { Button, ErrorText, Muted, inputClass } from "../components/ui";
import { Modal } from "./discover/shared";
import { ActivityLogPanel } from "./OrganizeChrome";
import Folder from "lucide-solid/icons/folder";
import FileIcon from "lucide-solid/icons/file";
import ArrowUp from "lucide-solid/icons/arrow-up";

type Dialog =
  | { kind: "rename"; path: string; name: string }
  | { kind: "move" }
  | { kind: "delete" };

function formatSize(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  if (n < 1024 * 1024 * 1024) return `${(n / (1024 * 1024)).toFixed(1)} MB`;
  return `${(n / (1024 * 1024 * 1024)).toFixed(1)} GB`;
}

function opError(results: OrganizeBrowseOpItem[]): string {
  return results
    .filter((r) => !r.ok)
    .map((r) => `${r.path}: ${r.error || "failed"}`)
    .join("\n");
}

export const Browse: Component = () => {
  const [path, setPath] = createSignal("");
  const [refresh, setRefresh] = createSignal(0);
  const [selected, setSelected] = createSignal<Set<string>>(new Set());
  const [dialog, setDialog] = createSignal<Dialog | null>(null);
  const [renameTo, setRenameTo] = createSignal("");
  const [destDir, setDestDir] = createSignal("");
  const [busy, setBusy] = createSignal(false);
  const [error, setError] = createSignal("");
  const [logKey, setLogKey] = createSignal(0);

  const [listing] = createResource(
    () => `${path()}:${refresh()}`,
    () => fetchOrganizeBrowse(path()),
  );
  const [destListing] = createResource(
    () => (dialog()?.kind === "move" ? `${destDir()}:${refresh()}` : null),
    () => fetchOrganizeBrowse(destDir()),
  );

  createEffect(() => {
    listing();
    setSelected(new Set<string>());
  });

  const entries = () => listing()?.entries ?? [];
  const parent = () => listing()?.parent ?? "";
  const currentPath = () => listing()?.path ?? path();
  const selectedEntries = createMemo(() => {
    const ids = selected();
    return entries().filter((e) => ids.has(e.path));
  });
  const selectedCount = () => selected().size;
  const oneSelected = () => selectedEntries().length === 1;

  const toggle = (p: string) => {
    const next = new Set(selected());
    if (next.has(p)) next.delete(p);
    else next.add(p);
    setSelected(next);
  };
  const toggleAll = () => {
    if (selectedCount() === entries().length) {
      setSelected(new Set<string>());
      return;
    }
    setSelected(new Set(entries().map((e) => e.path)));
  };
  const openDir = (p: string) => {
    setError("");
    setPath(p);
  };
  const goUp = () => {
    setError("");
    setPath(parent());
  };

  const runOp = async (fn: () => Promise<{ results: OrganizeBrowseOpItem[] }>) => {
    setBusy(true);
    setError("");
    try {
      const resp = await fn();
      const err = opError(resp.results ?? []);
      if (err) setError(err);
      else setDialog(null);
      setRefresh((n) => n + 1);
      setLogKey((n) => n + 1);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const crumbs = createMemo(() => {
    const current = currentPath();
    if (!current) return [] as { label: string; path: string }[];
    const out = [{ label: "Roots", path: "" }];
    let acc = "";
    for (const part of current.split("/").filter(Boolean)) {
      acc += `/${part}`;
      out.push({ label: part, path: acc });
    }
    return out;
  });

  return (
    <div>
      <h2 class="mb-1 text-lg font-semibold text-fg">Browse</h2>
      <Muted class="mb-4">
        Rename, move, or delete under /media, /downloads, and /adult. Tracked
        library titles stay in sync with the filesystem.
      </Muted>

      <div class="mb-3 flex flex-wrap items-center gap-2">
        <Button
          variant="secondary"
          disabled={listing.loading || (path() === "" && !parent())}
          onClick={goUp}
          aria-label="Up one directory"
        >
          <span class="inline-flex items-center gap-1">
            <ArrowUp size={16} />
            Up
          </span>
        </Button>
        <Button
          variant="secondary"
          disabled={!oneSelected() || busy()}
          onClick={() => {
            const e = selectedEntries()[0];
            if (!e) return;
            setRenameTo(e.name);
            setDialog({ kind: "rename", path: e.path, name: e.name });
          }}
        >
          Rename
        </Button>
        <Button
          variant="secondary"
          disabled={selectedCount() === 0 || busy()}
          onClick={() => {
            setDestDir(path());
            setDialog({ kind: "move" });
          }}
        >
          Move
        </Button>
        <Button
          variant="secondary"
          class="border-danger text-danger"
          disabled={selectedCount() === 0 || busy()}
          onClick={() => setDialog({ kind: "delete" })}
        >
          Delete
        </Button>
      </div>

      <nav class="mb-3 flex flex-wrap items-center gap-1 text-sm" aria-label="Path">
        <For each={crumbs()}>
          {(c, i) => (
            <>
              <Show when={i() > 0}>
                <span class="text-muted">/</span>
              </Show>
              <button
                type="button"
                class="rounded px-1 text-accent hover:underline"
                classList={{ "font-medium text-fg no-underline": c.path === currentPath() }}
                onClick={() => openDir(c.path)}
              >
                {c.label}
              </button>
            </>
          )}
        </For>
        <Show when={crumbs().length === 0}>
          <span class="text-muted">Roots</span>
        </Show>
      </nav>

      <Show when={error()}>
        <ErrorText>{error()}</ErrorText>
      </Show>
      <Show when={listing.error}>
        <ErrorText>{(listing.error as Error).message}</ErrorText>
      </Show>

      <div class="overflow-x-auto rounded border border-border">
        <table class="w-full text-left text-sm">
          <thead class="bg-surface-2 text-muted">
            <tr>
              <th class="w-10 px-2 py-2">
                <input
                  type="checkbox"
                  aria-label="Select all"
                  checked={entries().length > 0 && selectedCount() === entries().length}
                  onChange={toggleAll}
                />
              </th>
              <th class="px-2 py-2 font-medium">Name</th>
              <th class="px-2 py-2 font-medium">Size</th>
              <th class="px-2 py-2 font-medium">Modified</th>
            </tr>
          </thead>
          <tbody>
            <Show when={!listing.loading} fallback={
              <tr>
                <td colSpan={4} class="px-3 py-6 text-muted">Loading…</td>
              </tr>
            }>
              <Show
                when={entries().length > 0}
                fallback={
                  <tr>
                    <td colSpan={4} class="px-3 py-6 text-muted">This folder is empty.</td>
                  </tr>
                }
              >
                <For each={entries()}>
                  {(e) => (
                    <tr class="border-t border-border/60 hover:bg-surface-2/60">
                      <td class="px-2 py-1.5">
                        <input
                          type="checkbox"
                          aria-label={`Select ${e.name}`}
                          checked={selected().has(e.path)}
                          onChange={() => toggle(e.path)}
                        />
                      </td>
                      <td class="px-2 py-1.5">
                        <button
                          type="button"
                          class="inline-flex max-w-full items-center gap-2 text-left"
                          classList={{ "text-accent": e.isDir }}
                          onClick={() => (e.isDir ? openDir(e.path) : toggle(e.path))}
                        >
                          <Show when={e.isDir} fallback={<FileIcon size={16} class="shrink-0 text-muted" />}>
                            <Folder size={16} class="shrink-0 text-accent" />
                          </Show>
                          <span class="truncate">{e.name}</span>
                          <Show when={e.tracked}>
                            <span class="rounded bg-surface-2 px-1.5 py-0.5 text-[10px] uppercase tracking-wide text-muted">
                              Tracked
                            </span>
                          </Show>
                        </button>
                      </td>
                      <td class="whitespace-nowrap px-2 py-1.5 text-muted">
                        {e.isDir ? "—" : formatSize(e.size ?? 0)}
                      </td>
                      <td class="whitespace-nowrap px-2 py-1.5 text-muted">
                        {e.modTime ? e.modTime.replace("T", " ").replace("Z", "") : "—"}
                      </td>
                    </tr>
                  )}
                </For>
              </Show>
            </Show>
          </tbody>
        </table>
      </div>

      <ActivityLogPanel workflow="browse" refreshKey={logKey()} />

      <Show when={dialog()?.kind === "rename"}>
        <Modal title="Rename" onClose={() => !busy() && setDialog(null)}>
          <label class="block text-sm">
            <span class="text-muted">New name</span>
            <input
              class={`${inputClass} mt-1`}
              value={renameTo()}
              onInput={(e) => setRenameTo(e.currentTarget.value)}
              aria-label="New name"
            />
          </label>
          <div class="mt-4 flex justify-end gap-2">
            <Button variant="secondary" disabled={busy()} onClick={() => setDialog(null)}>
              Cancel
            </Button>
            <Button
              variant="primary"
              disabled={busy() || !renameTo().trim()}
              onClick={() => {
                const d = dialog();
                if (d?.kind !== "rename") return;
                void runOp(() => renameOrganizeBrowse(d.path, renameTo().trim()));
              }}
            >
              Rename
            </Button>
          </div>
        </Modal>
      </Show>

      <Show when={dialog()?.kind === "move"}>
        <Modal title="Move to…" onClose={() => !busy() && setDialog(null)}>
          <Muted class="mb-2">Choose a destination folder.</Muted>
          <p class="mb-2 font-mono text-xs text-muted">{destDir() || "(roots)"}</p>
          <div class="mb-2 flex gap-2">
            <Button
              variant="secondary"
              disabled={!destListing()?.parent && destDir() === ""}
              onClick={() => setDestDir(destListing()?.parent ?? "")}
            >
              Up
            </Button>
          </div>
          <ul class="max-h-56 overflow-auto rounded border border-border">
            <For each={(destListing()?.entries ?? []).filter((e: OrganizeBrowseEntry) => e.isDir)}>
              {(e) => (
                <li>
                  <button
                    type="button"
                    class="block w-full px-3 py-2 text-left text-sm hover:bg-surface-2"
                    onClick={() => setDestDir(e.path)}
                  >
                    {e.name}
                  </button>
                </li>
              )}
            </For>
          </ul>
          <div class="mt-4 flex justify-end gap-2">
            <Button variant="secondary" disabled={busy()} onClick={() => setDialog(null)}>
              Cancel
            </Button>
            <Button
              variant="primary"
              disabled={busy() || destDir() === ""}
              onClick={() => {
                const paths = [...selected()];
                void runOp(() => moveOrganizeBrowse(paths, destDir()));
              }}
            >
              Move here
            </Button>
          </div>
        </Modal>
      </Show>

      <Show when={dialog()?.kind === "delete"}>
        <Modal title="Delete" onClose={() => !busy() && setDialog(null)}>
          <p class="text-sm text-fg">
            Permanently delete {selectedCount()} item{selectedCount() === 1 ? "" : "s"}?
            Folders are removed recursively.
          </p>
          <ul class="mt-2 max-h-40 overflow-auto font-mono text-xs text-muted">
            <For each={selectedEntries()}>
              {(e) => (
                <li>
                  {e.path}
                  <Show when={e.tracked}>
                    <span class="ml-2 text-danger">tracked</span>
                  </Show>
                </li>
              )}
            </For>
          </ul>
          <Show when={selectedEntries().some((e) => e.tracked)}>
            <p class="mt-2 text-sm text-danger">
              Tracked library titles will be updated so they do not point at
              deleted files.
            </p>
          </Show>
          <div class="mt-4 flex justify-end gap-2">
            <Button variant="secondary" disabled={busy()} onClick={() => setDialog(null)}>
              Cancel
            </Button>
            <Button
              variant="primary"
              class="bg-danger text-accent-fg"
              disabled={busy()}
              onClick={() => {
                const paths = [...selected()];
                void runOp(() => deleteOrganizeBrowse(paths));
              }}
            >
              Delete
            </Button>
          </div>
        </Modal>
      </Show>
    </div>
  );
};
