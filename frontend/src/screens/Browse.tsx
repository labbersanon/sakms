// Claude 2026-08-27: Organize Browse tab — confirm-then-mutate file manager.
// Reason: confirm dialogs are the approval gate, not a Scan/Apply queue.
//   Rename is single-select; move and delete are multi-select. Properties
//   live in a persistent side pane; right-click adds Copy path and
//   Play/preview (browser-playable video only).
// Troubleshooting: empty listing — GET /api/organize/browse?path=; 403 —
//   Organize section lock. Stat 404 — path vanished after a mutate.
//   Unreadable table — listing/pane must use bg-surface (white) over the
//   wallpaper; transparent wrappers show cream-ticket paper through navy ink.
// Review if: Browse is staged onto the proposals queue.

import {
  type Component,
  For,
  Show,
  createEffect,
  createMemo,
  createResource,
  createSignal,
  onCleanup,
} from "solid-js";
import type { OrganizeBrowseEntry, OrganizeBrowseOpItem } from "@dto";
import {
  browseVideoUrl,
  deleteOrganizeBrowse,
  fetchOrganizeBrowse,
  fetchOrganizeBrowseStat,
  moveOrganizeBrowse,
  renameOrganizeBrowse,
} from "../api/organizeBrowse";
import { SourcePreviewVideo } from "../components/SourcePreview";
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

type MenuState = { x: number; y: number; path: string };

const dtClass = "text-[11px] uppercase tracking-wide text-muted";
const menuItemClass =
  "block w-full px-3 py-1.5 text-left text-sm text-fg hover:bg-surface-2 disabled:text-muted";
// Same card chrome as Rename/Dedup queues so the listing sits on white
// ticket-surface, not the page wallpaper.
const panelClass =
  "rounded-xl border border-border bg-surface/95 shadow-sm backdrop-blur-md";

function formatSize(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  if (n < 1024 * 1024 * 1024) return `${(n / (1024 * 1024)).toFixed(1)} MB`;
  return `${(n / (1024 * 1024 * 1024)).toFixed(1)} GB`;
}

function formatDuration(sec: number): string {
  if (!sec || sec < 0) return "";
  const s = Math.round(sec);
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const r = s % 60;
  if (h) return `${h}h ${m}m ${r}s`;
  if (m) return `${m}m ${r}s`;
  return `${r}s`;
}

function formatModTime(t: string | undefined): string {
  return t ? t.replace("T", " ").replace("Z", "") : "—";
}

function opError(results: OrganizeBrowseOpItem[]): string {
  return results
    .filter((r) => !r.ok)
    .map((r) => `${r.path}: ${r.error || "failed"}`)
    .join("\n");
}

function kindLabel(kind: string): string {
  switch (kind) {
    case "movie":
      return "Movie";
    case "episode":
      return "Episode";
    case "scene":
      return "Scene";
    default:
      return kind;
  }
}

export const Browse: Component = () => {
  const [path, setPath] = createSignal("");
  const [refresh, setRefresh] = createSignal(0);
  const [selected, setSelected] = createSignal<Set<string>>(new Set());
  const [detailPath, setDetailPath] = createSignal("");
  const [menu, setMenu] = createSignal<MenuState | null>(null);
  const [preview, setPreview] = createSignal<OrganizeBrowseEntry | null>(null);
  const [dialog, setDialog] = createSignal<Dialog | null>(null);
  const [renameTo, setRenameTo] = createSignal("");
  const [destDir, setDestDir] = createSignal("");
  const [busy, setBusy] = createSignal(false);
  const [error, setError] = createSignal("");
  const [logKey, setLogKey] = createSignal(0);
  const [copyStatus, setCopyStatus] = createSignal<"idle" | "copied" | "failed">(
    "idle",
  );
  let copyTimer: ReturnType<typeof setTimeout> | undefined;

  const [listing] = createResource(
    () => `${path()}:${refresh()}`,
    () => fetchOrganizeBrowse(path()),
  );
  const [destListing] = createResource(
    () => (dialog()?.kind === "move" ? `${destDir()}:${refresh()}` : null),
    () => fetchOrganizeBrowse(destDir()),
  );
  const [stat] = createResource(
    () => detailPath() || null,
    (p) => fetchOrganizeBrowseStat(p),
  );

  createEffect(() => {
    listing();
    setSelected(new Set<string>());
    setMenu(null);
  });

  createEffect(() => {
    if (!menu()) return;
    const close = () => setMenu(null);
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") close();
    };
    document.addEventListener("click", close);
    document.addEventListener("keydown", onKey);
    onCleanup(() => {
      document.removeEventListener("click", close);
      document.removeEventListener("keydown", onKey);
    });
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
  const menuEntry = createMemo(() => {
    const m = menu();
    if (!m) return undefined;
    return entries().find((e) => e.path === m.path);
  });

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
    setDetailPath("");
    setPath(p);
  };
  const goUp = () => {
    setError("");
    setDetailPath("");
    setPath(parent());
  };
  const selectOne = (e: OrganizeBrowseEntry) => {
    setSelected(new Set([e.path]));
    setDetailPath(e.path);
  };
  const renameSelected = () => {
    const e = selectedEntries()[0];
    if (!e) return;
    setRenameTo(e.name);
    setDialog({ kind: "rename", path: e.path, name: e.name });
  };
  const showSelectedProperties = () => {
    const e = selectedEntries()[0];
    if (e) setDetailPath(e.path);
  };
  const openMove = () => {
    setDestDir(path());
    setDialog({ kind: "move" });
  };
  const openDelete = () => setDialog({ kind: "delete" });

  const onRowContext = (ev: MouseEvent, e: OrganizeBrowseEntry) => {
    ev.preventDefault();
    ev.stopPropagation();
    if (!selected().has(e.path)) {
      setSelected(new Set([e.path]));
    }
    const x = Math.min(ev.clientX, window.innerWidth - 220);
    const y = Math.min(ev.clientY, window.innerHeight - 280);
    setMenu({ x, y, path: e.path });
  };

  const MenuItem: Component<{
    label: string;
    disabled?: boolean;
    onSelect: () => void;
  }> = (props) => (
    <button
      type="button"
      role="menuitem"
      class={menuItemClass}
      disabled={props.disabled}
      onClick={() => {
        props.onSelect();
        setMenu(null);
      }}
    >
      {props.label}
    </button>
  );

  const copyPath = async (p: string) => {
    clearTimeout(copyTimer);
    try {
      if (!navigator.clipboard) throw new Error("clipboard API unavailable");
      await navigator.clipboard.writeText(p);
      setCopyStatus("copied");
    } catch {
      setCopyStatus("failed");
    }
    copyTimer = setTimeout(() => setCopyStatus("idle"), 2000);
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
    const out = [{ label: "Files", path: "" }];
    if (!current) return out;
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
        library titles stay in sync with the filesystem. Right-click a row for
        Properties, Copy path, or Play/preview.
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
          onClick={renameSelected}
        >
          Rename
        </Button>
        <Button
          variant="secondary"
          disabled={selectedCount() === 0 || busy()}
          onClick={openMove}
        >
          Move
        </Button>
        <Button
          variant="secondary"
          class="border-danger text-danger"
          disabled={selectedCount() === 0 || busy()}
          onClick={openDelete}
        >
          Delete
        </Button>
        <Button
          variant="secondary"
          disabled={!oneSelected() || busy()}
          onClick={showSelectedProperties}
        >
          Properties
        </Button>
        <Show when={copyStatus() !== "idle"}>
          <span class="text-xs text-muted">
            {copyStatus() === "copied" ? "Path copied" : "Couldn't copy path"}
          </span>
        </Show>
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
      </nav>

      <Show when={error()}>
        <ErrorText>{error()}</ErrorText>
      </Show>
      <Show when={listing.error}>
        <ErrorText>{(listing.error as Error).message}</ErrorText>
      </Show>

      <div class="flex flex-col gap-3 lg:flex-row lg:items-start">
        <div class={`min-w-0 flex-1 overflow-x-auto ${panelClass}`}>
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
                      <tr
                        class="border-t border-border/60 hover:bg-surface-2/60"
                        classList={{ "bg-surface-2/80": detailPath() === e.path }}
                        onContextMenu={(ev) => onRowContext(ev, e)}
                        onClick={(ev) => {
                          const t = ev.target as HTMLElement;
                          if (t.closest("input[type=checkbox]")) return;
                          if (t.closest("button")) return;
                          selectOne(e);
                        }}
                      >
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
                            onClick={() => (e.isDir ? openDir(e.path) : selectOne(e))}
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
                          {formatModTime(e.modTime)}
                        </td>
                      </tr>
                    )}
                  </For>
                </Show>
              </Show>
            </tbody>
          </table>
        </div>

        <aside
          class={`w-full shrink-0 p-3 lg:w-80 ${panelClass}`}
          aria-label="Properties"
        >
          <h3 class="mb-2 text-sm font-semibold text-fg">Properties</h3>
          <Show
            when={detailPath()}
            fallback={<Muted>Select an item to see properties.</Muted>}
          >
            <Show when={stat.loading}>
              <Muted>Loading…</Muted>
            </Show>
            <Show when={stat.error}>
              <ErrorText>{(stat.error as Error).message}</ErrorText>
            </Show>
            <Show when={stat()}>
              {(s) => (
                <dl class="space-y-2 text-sm">
                  <div>
                    <dt class={dtClass}>Name</dt>
                    <dd class="break-all text-fg">{s().name}</dd>
                  </div>
                  <div>
                    <dt class={dtClass}>Path</dt>
                    <dd class="break-all font-mono text-xs text-fg">{s().path}</dd>
                    <Button
                      variant="secondary"
                      class="mt-1"
                      onClick={() => void copyPath(s().path)}
                    >
                      Copy path
                    </Button>
                  </div>
                  <div>
                    <dt class={dtClass}>Type</dt>
                    <dd class="text-fg">{s().isDir ? "Folder" : "File"}</dd>
                  </div>
                  <Show when={!s().isDir}>
                    <div>
                      <dt class={dtClass}>Size</dt>
                      <dd class="text-fg">{formatSize(s().size ?? 0)}</dd>
                    </div>
                  </Show>
                  <Show when={s().isDir}>
                    <div>
                      <dt class={dtClass}>Contents</dt>
                      <dd class="text-fg">
                        {s().itemCount ?? 0} item{(s().itemCount ?? 0) === 1 ? "" : "s"}
                        {" · "}
                        {formatSize(s().totalSize ?? 0)}
                        <Show when={s().truncated}>
                          <span class="block text-xs text-muted">Counts are partial</span>
                        </Show>
                      </dd>
                    </div>
                  </Show>
                  <div>
                    <dt class={dtClass}>Modified</dt>
                    <dd class="text-fg">{formatModTime(s().modTime)}</dd>
                  </div>
                  <div>
                    <dt class={dtClass}>Library</dt>
                    <dd class="text-fg">
                      <Show when={s().tracked} fallback="Not tracked">
                        Tracked
                      </Show>
                    </dd>
                  </div>
                  <Show when={(s().library ?? []).length > 0}>
                    <div>
                      <dt class={dtClass}>
                        Titles
                        <Show when={(s().libraryTotal ?? 0) > (s().library ?? []).length}>
                          {` (${s().library?.length} of ${s().libraryTotal})`}
                        </Show>
                      </dt>
                      <dd>
                        <ul class="mt-1 space-y-1">
                          <For each={s().library}>
                            {(h) => (
                              <li class="text-fg">
                                <span class="text-muted">{kindLabel(h.kind)} · </span>
                                {h.series ? `${h.series} — ` : ""}
                                {h.title}
                                <Show when={h.year}>
                                  {` (${h.year})`}
                                </Show>
                              </li>
                            )}
                          </For>
                        </ul>
                      </dd>
                    </div>
                  </Show>
                  <Show when={s().probe}>
                    <div>
                      <dt class={dtClass}>Media</dt>
                      <dd class="text-fg">
                        {[
                          s().probe?.width && s().probe?.height
                            ? `${s().probe?.width}×${s().probe?.height}`
                            : "",
                          s().probe?.codec,
                          formatDuration(s().probe?.duration ?? 0),
                        ]
                          .filter(Boolean)
                          .join(" · ")}
                      </dd>
                    </div>
                  </Show>
                  <Show when={s().probeError}>
                    <Muted>Could not probe media: {s().probeError}</Muted>
                  </Show>
                  <Show when={s().playable && s().videoUrl}>
                    <div>
                      <dt class={`mb-1 ${dtClass}`}>Preview</dt>
                      <dd>
                        <SourcePreviewVideo src={s().videoUrl ?? ""} label={s().name} />
                      </dd>
                    </div>
                  </Show>
                  <Show when={!s().isDir && !s().playable && !s().probeError && s().probe}>
                    <Muted>This format cannot play in the browser</Muted>
                  </Show>
                </dl>
              )}
            </Show>
          </Show>
        </aside>
      </div>

      <ActivityLogPanel workflow="browse" refreshKey={logKey()} />

      <Show when={menu()}>
        {(m) => (
          <div
            role="menu"
            aria-label="Browse actions"
            class="fixed z-50 min-w-44 rounded border border-border bg-surface py-1 shadow-lg"
            style={{ left: `${m().x}px`, top: `${m().y}px` }}
            onClick={(ev) => ev.stopPropagation()}
            onContextMenu={(ev) => ev.preventDefault()}
          >
            <MenuItem
              label="Rename"
              disabled={!oneSelected() || busy()}
              onSelect={renameSelected}
            />
            <MenuItem
              label="Move"
              disabled={selectedCount() === 0 || busy()}
              onSelect={openMove}
            />
            <MenuItem
              label="Delete"
              disabled={selectedCount() === 0 || busy()}
              onSelect={openDelete}
            />
            <MenuItem label="Copy path" onSelect={() => void copyPath(m().path)} />
            <Show when={menuEntry()?.playable}>
              <MenuItem
                label="Play/preview"
                onSelect={() => {
                  const e = menuEntry();
                  if (e) {
                    setDetailPath(e.path);
                    setPreview(e);
                  }
                }}
              />
            </Show>
            <MenuItem
              label="Properties"
              disabled={!oneSelected()}
              onSelect={showSelectedProperties}
            />
          </div>
        )}
      </Show>

      <Show when={preview()}>
        {(e) => (
          <Modal title={e().name} onClose={() => setPreview(null)}>
            <SourcePreviewVideo src={browseVideoUrl(e().path)} label={e().name} />
          </Modal>
        )}
      </Show>

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
          <p class="mb-2 font-mono text-xs text-muted">{destDir() || "Files"}</p>
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
