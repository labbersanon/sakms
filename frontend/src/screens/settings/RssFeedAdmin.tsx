// RssFeedAdmin — the Settings "RSS Feeds" panel for target=adult feeds
// (relocated here from the old inline Discover Adult controls). Mirrors
// AdultRowAdmin's list/form/toggle/Edit/Delete shape, with two deliberate
// departures the plan calls for (ralplan-adult-rss-feed-settings-relocation.md):
//
//   1. Reorder is DRAG-AND-DROP, now via the SHARED RowEditor component
//      (screens/discover/RowEditor.tsx) — the same one Discover, SliderAdmin
//      and AdultRowAdmin render, so there is exactly one set of drag mechanics
//      in the app. This file used to carry its own parallel @thisbeyond/solid-dnd
//      wiring; that duplication is gone. The reorder API (reorderRssFeeds →
//      Store.Reorder) still requires the FULL set of every existing feed's ids
//      exactly once, across ALL targets — Mainstream can create movie/tv feeds
//      too — so a drag among the adult feeds is still mapped back onto the full
//      id list (reorderAdultFeedIds) before it's sent, never an adult-only
//      subset (which would 422 as ErrReorderMismatch whenever a non-adult feed
//      exists). RowEditor's sortable keys are bare stringified feed ids, the
//      exact key vocabulary this file's own wiring already used, so that
//      mapping — and its unit tests — are unchanged.
//   2. Protocol is a DERIVED, read-only fact, never an operator-typed field:
//      the add/edit form has no protocol input. Add auto-detects server-side;
//      an existing feed re-detects via Re-scan; an inconclusive detection (Add
//      or Re-scan) falls back to a one-time manual pick pop-up
//      (ProtocolFallbackDialog). The invariant is unchanged but its SURFACE
//      moved: RowEditor rows are title-only, so the old per-row `protocol:`
//      subtitle is gone and the detected protocol is now reported by
//      ProtocolStatusDialog — on Re-scan (scanning → detected/error) and on a
//      successful Add. That dialog is status-only and NEVER collects a
//      protocol. Every PUT this panel makes reconstructs the FULL update body
//      from the current row (protocol/enabled/target carried over, feedUrl null
//      to preserve the masked secret) so an unrelated save never clears a field.

import {
  type Component,
  For,
  Show,
  createResource,
  createSignal,
} from "solid-js";
import Pencil from "lucide-solid/icons/pencil";
import RefreshCw from "lucide-solid/icons/refresh-cw";
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
  ErrorText,
  Muted,
  SaveStatus,
  inputClass,
  labelClass,
  useSaveStatus,
} from "../../components/ui";
import { RowEditor, type RowDescriptor } from "../discover/RowEditor";
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

// ProtocolStatus is what ProtocolStatusDialog renders. Three phases, one
// signal — a null signal means no dialog. `protocol` is the generated DTO's
// plain string, NOT the narrower RssFeedProtocol: it is echoed straight from
// whatever the backend reported for display, so narrowing it here would be an
// unchecked assertion about a field tygo emits as a bare string.
type ProtocolStatus =
  | { phase: "scanning"; title: string }
  | { phase: "detected"; title: string; protocol: string }
  | { phase: "error"; title: string; message: string };

// ProtocolStatusDialog surfaces protocol auto-detection status now that rows
// are title-only and no longer carry a `protocol:` subtitle. Two triggers: an
// explicit Re-scan click (scanning → detected/error), and a successful Add
// (straight to detected, so the operator learns what was auto-detected).
// It NEVER collects a protocol — that stays ProtocolFallbackDialog's job on
// the inconclusive-422 path only, preserving this file's "protocol is a
// derived, read-only fact, never an operator-typed field" invariant.
// Modal supplies the single Close button in its own header, so this body adds
// none — a second one would make "Close" ambiguous.
const ProtocolStatusDialog: Component<{
  status: ProtocolStatus;
  onClose: () => void;
}> = (props) => {
  const body = () => {
    const s = props.status;
    if (s.phase === "scanning") return <Muted>Re-scanning “{s.title}”…</Muted>;
    if (s.phase === "detected")
      return (
        <div class="text-sm text-fg">
          Detected protocol for “{s.title}”: <strong>{s.protocol}</strong>
        </div>
      );
    return <ErrorText>{s.message}</ErrorText>;
  };
  return (
    <Modal title="Protocol detection" onClose={props.onClose}>
      {body()}
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
  const [status, setStatus] = createSignal<ProtocolStatus | null>(null);

  // statusToken guards ProtocolStatusDialog against two races. Every path that
  // reports a status captures a fresh token before its first await and writes
  // setStatus only while the token is still current, so (a) a dialog the
  // operator dismissed can't be resurrected by a slow response landing after
  // the fact — closing the dialog moves the token too, which is what makes a
  // dismissal stick — and (b) of two overlapping scans (a rapid double-click,
  // or a Re-scan started right after an Add) the LAST STARTED one owns the
  // dialog, rather than whichever happens to resolve last showing feed B's
  // protocol under feed A's title. A plain `let` rather than a signal on
  // purpose: it is an identity marker read imperatively after an await, never
  // rendered, so making it reactive would only add a spurious dependency.
  let statusToken = {};

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
  // so the backend auto-detects; a confident detection opens the status dialog
  // (the only place the operator ever learns what was detected, now that rows
  // are title-only), while an inconclusive one surfaces as a thrown
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
    const token = (statusToken = {});
    try {
      const created = await createRssFeed(body);
      closeForm();
      // Only the STATUS REPORT is stale-able here — a superseded Add still
      // created a real feed, so closeForm/refetch run unconditionally and only
      // the dialog write is skipped.
      if (statusToken === token)
        setStatus({
          phase: "detected",
          title: created.title,
          protocol: created.protocol,
        });
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

  // rescan reports progress and the result through ProtocolStatusDialog. The
  // inconclusive (422) branch is unchanged: it closes the status dialog and
  // hands off to the manual-pick fallback exactly as before. The catch
  // deliberately no longer writes listError — the dialog owns the message.
  const rescan = async (feed: RssFeed) => {
    setListError("");
    const token = (statusToken = {});
    setStatus({ phase: "scanning", title: feed.title });
    try {
      const result = await rescanRssFeed(feed.id);
      const current = () => statusToken === token;
      if (isProtocolUndetected(result)) {
        // A dismissed or superseded scan must not pop the manual picker
        // either — that is the same resurrection bug wearing a different
        // dialog.
        if (current()) {
          setStatus(null);
          setFallback({ mode: "rescan", feed });
        }
        return;
      }
      if (current())
        setStatus({
          phase: "detected",
          title: feed.title,
          protocol: result.protocol,
        });
      // Deliberately OUTSIDE the guard: a superseded scan's detection still
      // landed server-side, and feeds() is what every PUT body this panel
      // reconstructs reads its protocol from — skipping the refetch would
      // silently resend the pre-scan protocol on the next save.
      await refetch();
    } catch (e) {
      if (statusToken !== token) return;
      setStatus({
        phase: "error",
        title: feed.title,
        message: (e as Error).message,
      });
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

  // RowEditor reports the new order as its own row keys, which are the bare
  // stringified feed ids descriptors() sets — so Number-mapping them back is
  // exactly the adult-only order reorderAdultFeedIds expects.
  const feedByKey = (key: string): RssFeed | undefined =>
    adultFeeds().find((f) => String(f.id) === key);

  const descriptors = (): RowDescriptor[] =>
    adultFeeds().map((f) => ({
      key: String(f.id),
      label: f.title,
      kind: "entity",
      enabled: f.enabled,
      actions: [
        {
          label: `Edit ${f.title}`,
          icon: <Pencil size={14} />,
          onClick: () => setEditing(f.id),
        },
        {
          label: `Re-scan ${f.title}`,
          icon: <RefreshCw size={14} />,
          onClick: () => void rescan(f),
        },
      ],
    }));

  return (
    <>
      <RowEditor
        title="RSS feeds"
        description={
          <>
            <Muted class="mb-3">
              Admin-defined raw RSS feeds for Adult Discover (NZBGeek
              saved-search style URLs). The download protocol is detected
              automatically when a feed is added, re-detectable per feed with
              Re-scan, and only asked for manually when detection is
              inconclusive.
            </Muted>
            <Show when={feeds.error}>
              <ErrorText>{(feeds.error as Error)?.message}</ErrorText>
            </Show>
            <Show when={listError()}>
              <ErrorText>{listError()}</ErrorText>
            </Show>
          </>
        }
        footer={
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
        }
        rows={descriptors()}
        onReorder={(keys) =>
          void persistReorder(
            reorderAdultFeedIds(feeds() ?? [], keys.map(Number)),
          )
        }
        onToggleEnabled={(r) => {
          const f = feedByKey(r.key);
          if (f) void toggleEnabled(f);
        }}
        onDelete={(r) => {
          const f = feedByKey(r.key);
          if (f) void remove(f);
        }}
      />

      <Show when={fallback()}>
        <ProtocolFallbackDialog
          title={fallbackTitle()}
          url={fallbackUrl()}
          onCancel={() => setFallback(null)}
          onPick={applyFallback}
        />
      </Show>

      <Show when={status()}>
        {(s) => (
          <ProtocolStatusDialog
            status={s()}
            onClose={() => {
              // Moving the token is what makes a dismissal STICK: without it
              // an in-flight scan's own token still matches on arrival and it
              // reopens the dialog the operator just closed.
              statusToken = {};
              setStatus(null);
            }}
          />
        )}
      </Show>
    </>
  );
};
