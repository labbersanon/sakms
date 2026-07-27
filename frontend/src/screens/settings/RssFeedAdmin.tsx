// RssFeedAdmin — the Settings "RSS Feeds" panel for target=adult feeds
// (relocated here from the old inline Discover Adult controls). Mirrors
// AdultRowAdmin's list/form/toggle/Edit/Delete shape, with two deliberate
// departures the plan calls for (ralplan-adult-rss-feed-settings-relocation.md):
//
//   1. Reorder is DRAG-AND-DROP (a parallel sibling of RowEditor.tsx's
//      @thisbeyond/solid-dnd primitives, not AdultRowAdmin's up/down buttons —
//      the plan explicitly rejects the button reorder as a UX regression from
//      the drag reorder Discover already had). The reorder API
//      (reorderRssFeeds → Store.Reorder) requires the FULL set of every existing
//      feed's ids exactly once, across ALL targets — Mainstream can create
//      movie/tv feeds too — so a drag among the adult feeds is mapped back onto
//      the full id list (reorderAdultFeedIds) before it's sent, never an
//      adult-only subset (which would 422 as ErrReorderMismatch whenever a
//      non-adult feed exists).
//   2. Protocol is a DERIVED, read-only fact, never an operator-typed field:
//      the add/edit form has no protocol input. Add auto-detects server-side;
//      an existing feed re-detects via Re-scan; an inconclusive detection (Add
//      or Re-scan) falls back to a one-time manual pick pop-up. Every PUT this
//      panel makes reconstructs the FULL update body from the current row
//      (protocol/enabled/target carried over, feedUrl null to preserve the
//      masked secret) so an unrelated save never clears a field.

import {
  type Component,
  For,
  Show,
  createResource,
  createSignal,
} from "solid-js";
import {
  DragDropProvider,
  DragDropSensors,
  SortableProvider,
  closestCenter,
  createSortable,
  transformStyle,
  type DragEvent,
} from "@thisbeyond/solid-dnd";
import {
  PROTOCOLS,
  PROTOCOL_UNDETECTED,
  TARGETS,
  createRssFeed,
  deleteRssFeed,
  fetchRssFeeds,
  isProtocolUndetected,
  reorderRssFeeds,
  rescanRssFeed,
  updateRssFeed,
  type RssFeed,
  type RssFeedCreateRequest,
  type RssFeedProtocol,
  type RssFeedTarget,
} from "../../api/rssFeeds";
import {
  Button,
  Card,
  ErrorText,
  Muted,
  SaveStatus,
  inputClass,
  labelClass,
  useSaveStatus,
} from "../../components/ui";
import { reorderKeys } from "../discover/RowEditor";
import { Modal } from "../discover/shared";

// reorderAdultFeedIds maps a new order of the ADULT feeds back onto the full
// id list Store.Reorder demands (every existing feed exactly once, all
// targets). allFeeds is the full list in its current display order (fetchRssFeeds
// returns it already sorted by sortOrder); each adult slot is filled from
// newAdultOrder in sequence while every non-adult feed keeps its exact position,
// so a drag among adult feeds only reorders them relative to each other and
// never disturbs — or omits — a movie/tv feed.
export function reorderAdultFeedIds(
  allFeeds: RssFeed[],
  newAdultOrder: number[],
): number[] {
  let ai = 0;
  return allFeeds.map((f) =>
    f.target === "adult" ? (newAdultOrder[ai++] ?? f.id) : f.id,
  );
}

// RssFeedForm creates a new adult feed (feed prop undefined) or edits an
// existing one (feed prop present). Deliberately has NO protocol field — the
// protocol is derived server-side (Add) or via Re-scan, never typed here. On
// edit, feedUrl starts blank and a blank value preserves the stored (masked)
// URL; only a non-empty value replaces it.
const RssFeedForm: Component<{
  feed?: RssFeed;
  onSave: (v: {
    title: string;
    feedUrl: string;
    target: RssFeedTarget;
  }) => Promise<void>;
  onCancel: () => void;
}> = (props) => {
  const [title, setTitle] = createSignal(props.feed?.title ?? "");
  const [feedUrl, setFeedUrl] = createSignal("");
  const [target, setTarget] = createSignal<RssFeedTarget>(
    (props.feed?.target as RssFeedTarget) ?? "adult",
  );
  const status = useSaveStatus();

  const save = async () => {
    if (!title().trim()) {
      status.failed(new Error("title is required"));
      return;
    }
    // Add needs a real URL; edit leaves it blank to preserve the stored secret.
    if (!props.feed && !feedUrl().trim()) {
      status.failed(new Error("feed URL is required"));
      return;
    }
    try {
      await props.onSave({
        title: title().trim(),
        feedUrl: feedUrl().trim(),
        target: target(),
      });
    } catch (e) {
      status.failed(e);
    }
  };

  return (
    <div class="mt-3 rounded-md border border-dashed border-border p-3">
      <form onSubmit={(e) => (e.preventDefault(), void save())}>
        <label class="mb-2 block">
          <span class={labelClass}>Title</span>
          <input
            type="text"
            class={`${inputClass} mt-1`}
            aria-label="Feed title"
            value={title()}
            onInput={(e) => setTitle(e.currentTarget.value)}
          />
        </label>
        <label class="mb-2 block">
          <span class={labelClass}>Feed URL</span>
          <input
            type="text"
            class={`${inputClass} mt-1`}
            aria-label="Feed URL"
            placeholder={
              props.feed
                ? "Leave blank to keep the stored URL"
                : "https://nzbgeek.info/rss?..."
            }
            value={feedUrl()}
            onInput={(e) => setFeedUrl(e.currentTarget.value)}
          />
        </label>
        <label class="block">
          <span class={labelClass}>Target</span>
          <select
            class={`${inputClass} mt-1`}
            aria-label="Target"
            value={target()}
            onChange={(e) => setTarget(e.currentTarget.value as RssFeedTarget)}
          >
            <For each={TARGETS}>{(t) => <option value={t}>{t}</option>}</For>
          </select>
        </label>
        <div class="mt-3 flex items-center gap-2">
          <Button variant="primary" type="submit">
            {props.feed ? "Save changes" : "Create feed"}
          </Button>
          <Button onClick={props.onCancel}>Cancel</Button>
          <SaveStatus text={status.status().text} error={status.status().error} />
        </div>
      </form>
    </div>
  );
};

// FeedRow is one existing feed's sortable list entry: a drag grip (the ONLY
// drag initiator, so the controls stay independently clickable), title, the
// read-only detected protocol, an inline Enabled toggle (immediate save),
// Edit/Re-scan/Delete.
const FeedRow: Component<{
  feed: RssFeed;
  editing: boolean;
  onEdit: () => void;
  onRescan: () => void;
  onDelete: () => void;
  onToggleEnabled: () => void;
}> = (props) => {
  const sortable = createSortable(String(props.feed.id));
  return (
    <li
      ref={sortable.ref}
      style={transformStyle(sortable.transform)}
      class="flex items-center gap-3 border-b border-border/60 py-2"
      classList={{ "opacity-25": sortable.isActiveDraggable }}
    >
      <span
        {...sortable.dragActivators}
        role="button"
        aria-label={`Drag ${props.feed.title}`}
        class="cursor-grab touch-none select-none rounded border border-border px-1.5 text-xs text-muted"
      >
        ⠿
      </span>
      <div class="min-w-0 flex-1">
        <div class="truncate text-sm text-fg">{props.feed.title}</div>
        <div class="truncate text-xs text-muted">
          protocol: <span>{props.feed.protocol}</span>
        </div>
      </div>
      <label class="flex items-center gap-1 text-xs text-muted">
        <input
          type="checkbox"
          aria-label={`${props.feed.title} enabled`}
          checked={props.feed.enabled}
          onChange={props.onToggleEnabled}
        />
        enabled
      </label>
      <div class="flex gap-1">
        <Button class="!px-2 !py-1 !text-xs" onClick={props.onEdit}>
          {props.editing ? "Editing…" : "Edit"}
        </Button>
        <Button class="!px-2 !py-1 !text-xs" onClick={props.onRescan}>
          Re-scan
        </Button>
        <Button class="!px-2 !py-1 !text-xs" onClick={props.onDelete}>
          Delete
        </Button>
      </div>
    </li>
  );
};

// ProtocolFallbackDialog is the one-time manual protocol pick shown when Add or
// Re-scan detection is inconclusive (the backend's protocol_undetected 422).
// It only collects the operator's torrent/usenet choice; the caller (onPick)
// owns the actual retry — a create-with-protocol for the Add path, a
// protocol-only PUT for the Re-scan path.
const ProtocolFallbackDialog: Component<{
  title: string;
  url?: string;
  onCancel: () => void;
  onPick: (protocol: RssFeedProtocol) => Promise<void>;
}> = (props) => {
  const [protocol, setProtocol] = createSignal<RssFeedProtocol>("usenet");
  const [saving, setSaving] = createSignal(false);
  const [error, setError] = createSignal("");
  const submit = async () => {
    setError("");
    setSaving(true);
    try {
      await props.onPick(protocol());
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setSaving(false);
    }
  };
  return (
    <Modal title="Choose a protocol" onClose={props.onCancel}>
      <Muted class="mb-3">
        The download protocol couldn't be detected automatically for “
        {props.title}”. Pick it manually to finish.
      </Muted>
      <Show when={props.url}>
        <div class="mb-3 break-all text-xs text-muted">{props.url}</div>
      </Show>
      <label class="block">
        <span class={labelClass}>Protocol</span>
        <select
          class={`${inputClass} mt-1`}
          aria-label="Protocol"
          value={protocol()}
          onChange={(e) =>
            setProtocol(e.currentTarget.value as RssFeedProtocol)
          }
        >
          <For each={PROTOCOLS}>{(p) => <option value={p}>{p}</option>}</For>
        </select>
      </label>
      <Show when={error()}>
        <ErrorText>{error()}</ErrorText>
      </Show>
      <div class="mt-3 flex justify-end gap-2">
        <Button onClick={props.onCancel}>Cancel</Button>
        <Button
          variant="primary"
          onClick={() => void submit()}
          disabled={saving()}
        >
          {saving() ? "Saving…" : "Save protocol"}
        </Button>
      </div>
    </Modal>
  );
};

// Fallback carries the context needed to retry once the operator picks a
// protocol: an Add retry re-submits the SAME stashed create body (the backend
// created nothing on the inconclusive attempt, so this yields exactly one row,
// never a duplicate); a Re-scan retry does a protocol-only PUT reconstructed
// from the current feed row.
type Fallback =
  | { mode: "add"; body: RssFeedCreateRequest }
  | { mode: "rescan"; feed: RssFeed };

// RssFeedAdminSection is the Settings "RSS Feeds" tab's whole panel.
export const RssFeedAdminSection: Component = () => {
  const [feeds, { refetch }] = createResource(fetchRssFeeds, {
    initialValue: [],
  });
  const [editing, setEditing] = createSignal<number | "new" | null>(null);
  const [listError, setListError] = createSignal("");
  const [fallback, setFallback] = createSignal<Fallback | null>(null);

  // Only adult feeds are managed here; movie/tv feeds live in Mainstream's own
  // (deferred) UI. The full list is still kept for the reorder id mapping.
  const adultFeeds = () => (feeds() ?? []).filter((f) => f.target === "adult");

  const closeForm = () => setEditing(null);
  const editingFeed = (): RssFeed | undefined => {
    const e = editing();
    if (e === null || e === "new") return undefined;
    return adultFeeds().find((f) => f.id === e);
  };

  // submitForm handles both the add and edit submits from RssFeedForm. Edit
  // reconstructs the FULL update body from the current row (only title/target,
  // and URL if the operator typed one, come from the form). Add omits protocol
  // so the backend auto-detects; an inconclusive detection surfaces as a thrown
  // PROTOCOL_UNDETECTED, which opens the fallback pop-up (stashing the create
  // body for a genuine same-request retry) instead of erroring.
  const submitForm = async (v: {
    title: string;
    feedUrl: string;
    target: RssFeedTarget;
  }) => {
    setListError("");
    const feed = editingFeed();
    if (feed) {
      await updateRssFeed(feed.id, {
        title: v.title,
        // Blank means preserve the stored (masked) URL — send null, never "".
        feedUrl: v.feedUrl ? v.feedUrl : null,
        target: v.target,
        protocol: feed.protocol,
        enabled: feed.enabled,
      });
      closeForm();
      await refetch();
      return;
    }
    const body: RssFeedCreateRequest = {
      title: v.title,
      feedUrl: v.feedUrl,
      target: v.target,
      enabled: true,
    };
    try {
      await createRssFeed(body);
      closeForm();
      await refetch();
    } catch (e) {
      if (e instanceof Error && e.message === PROTOCOL_UNDETECTED) {
        setFallback({ mode: "add", body });
        closeForm();
        return;
      }
      throw e;
    }
  };

  const toggleEnabled = async (feed: RssFeed) => {
    setListError("");
    try {
      await updateRssFeed(feed.id, {
        title: feed.title,
        feedUrl: null,
        target: feed.target,
        protocol: feed.protocol,
        enabled: !feed.enabled,
      });
      await refetch();
    } catch (e) {
      setListError((e as Error).message);
    }
  };

  const rescan = async (feed: RssFeed) => {
    setListError("");
    try {
      const result = await rescanRssFeed(feed.id);
      if (isProtocolUndetected(result)) {
        setFallback({ mode: "rescan", feed });
        return;
      }
      await refetch();
    } catch (e) {
      setListError((e as Error).message);
    }
  };

  const remove = async (feed: RssFeed) => {
    if (!confirm(`Delete the "${feed.title}" feed?`)) return;
    setListError("");
    try {
      await deleteRssFeed(feed.id);
      if (editing() === feed.id) closeForm();
      await refetch();
    } catch (e) {
      setListError((e as Error).message);
    }
  };

  // applyFallback runs the operator's manual protocol pick: retry the same
  // create for Add, or a protocol-only PUT for Re-scan. Errors propagate to the
  // dialog; success closes it and refreshes the list.
  const applyFallback = async (protocol: RssFeedProtocol) => {
    const fb = fallback();
    if (!fb) return;
    if (fb.mode === "add") {
      await createRssFeed({ ...fb.body, protocol });
    } else {
      await updateRssFeed(fb.feed.id, {
        title: fb.feed.title,
        feedUrl: null,
        target: fb.feed.target,
        protocol,
        enabled: fb.feed.enabled,
      });
    }
    setFallback(null);
    await refetch();
  };

  const persistReorder = async (fullIds: number[]) => {
    setListError("");
    try {
      await reorderRssFeeds(fullIds);
      await refetch();
    } catch (e) {
      setListError((e as Error).message);
    }
  };

  const onDragEnd = ({ draggable, droppable }: DragEvent) => {
    if (!droppable) return;
    const adultIds = adultFeeds().map((f) => f.id);
    const newAdultOrder = reorderKeys(
      adultIds.map(String),
      String(draggable.id),
      String(droppable.id),
    ).map(Number);
    void persistReorder(reorderAdultFeedIds(feeds() ?? [], newAdultOrder));
  };

  const fallbackTitle = (): string => {
    const fb = fallback();
    if (!fb) return "";
    return fb.mode === "add" ? fb.body.title : fb.feed.title;
  };
  const fallbackUrl = (): string | undefined => {
    const fb = fallback();
    // Only the Add path has the (just-typed) URL in hand; a stored feed's URL
    // is masked, so Re-scan shows the title alone.
    return fb?.mode === "add" ? fb.body.feedUrl : undefined;
  };

  return (
    <Card title="RSS feeds">
      <Muted class="mb-3">
        Admin-defined raw RSS feeds for Adult Discover (NZBGeek saved-search
        style URLs). The download protocol is detected automatically when a feed
        is added, re-detectable per feed with Re-scan, and only asked for
        manually when detection is inconclusive.
      </Muted>
      <Show when={feeds.error}>
        <ErrorText>{(feeds.error as Error)?.message}</ErrorText>
      </Show>
      <Show when={listError()}>
        <ErrorText>{listError()}</ErrorText>
      </Show>
      <Show
        when={adultFeeds().length > 0}
        fallback={<Muted>No RSS feeds yet.</Muted>}
      >
        <DragDropProvider
          onDragEnd={onDragEnd}
          collisionDetector={closestCenter}
        >
          <DragDropSensors />
          <SortableProvider ids={adultFeeds().map((f) => String(f.id))}>
            <ul>
              <For each={adultFeeds()}>
                {(feed) => (
                  <FeedRow
                    feed={feed}
                    editing={editing() === feed.id}
                    onEdit={() => setEditing(feed.id)}
                    onRescan={() => void rescan(feed)}
                    onDelete={() => void remove(feed)}
                    onToggleEnabled={() => void toggleEnabled(feed)}
                  />
                )}
              </For>
            </ul>
          </SortableProvider>
        </DragDropProvider>
      </Show>

      <Show
        when={editing() !== null}
        fallback={
          <div class="mt-3">
            <Button variant="primary" onClick={() => setEditing("new")}>
              + New feed
            </Button>
          </div>
        }
      >
        <RssFeedForm
          feed={editingFeed()}
          onSave={submitForm}
          onCancel={closeForm}
        />
      </Show>

      <Show when={fallback()}>
        <ProtocolFallbackDialog
          title={fallbackTitle()}
          url={fallbackUrl()}
          onCancel={() => setFallback(null)}
          onPick={applyFallback}
        />
      </Show>
    </Card>
  );
};
