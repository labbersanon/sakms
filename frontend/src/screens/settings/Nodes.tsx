// Settings → Nodes tab: pending approval queue + connected worker nodes.
// Pending nodes poll every 3 seconds while any are waiting so new arrivals
// appear quickly; the poll stops once the queue empties.

import {
  type Component,
  createEffect,
  createResource,
  createSignal,
  For,
  onCleanup,
  Show,
} from "solid-js";
import {
  approveNode,
  fetchNodePathMappings,
  fetchNodes,
  rejectPending,
  updateNodePause,
  updateNodeSettings,
} from "../../api/settings";
import { Modal } from "../discover/shared";
import {
  Button,
  Card,
  ErrorText,
  inputClass,
  labelClass,
  Muted,
  Switch,
} from "../../components/ui";
import type { NodeInfo, NodePathMappingEntry, PendingNodeInfo } from "@dto";

function formatHeartbeat(ts: string): string {
  const ms = Date.now() - new Date(ts).getTime();
  if (ms < 0 || ms < 10_000) return "just now";
  const sec = Math.floor(ms / 1000);
  if (sec < 60) return `${sec}s ago`;
  const min = Math.floor(sec / 60);
  if (min < 60) return `${min}m ago`;
  const hr = Math.floor(min / 60);
  if (hr < 24) return `${hr}h ago`;
  return `${Math.floor(hr / 24)}d ago`;
}

// LIBRARY_PATH_LABELS gives each of the 5 fixed library-path keys a readable
// row label. Order here is display order, top to bottom.
const LIBRARY_PATH_LABELS: Record<string, string> = {
  movies_library_root_folder: "Movies library root",
  series_library_root_folder: "Series library root",
  adult_library_root_folder: "Adult library root",
  movies_kids_root_path: "Movies kids root",
  series_kids_root_path: "Series kids root",
};

// NodePathMappingRow renders one fixed row, entirely read-only: a label +
// the library path's current server-side value (for context — Library
// settings owns configuring it), and the node-reported NodePath as plain
// text. Path mappings are node-owned now (D3/D1 — the operator-auth settings
// write ignores any submitted PathMap; only the node itself can author these
// via its own bearer-authed push), so there is no input and no
// NodeFolderPicker here anymore — this row can only display what the node
// has reported, never collect an edit. A blank NodePath — whether it was
// simply never set, or the node explicitly cleared it (D7) — renders as
// "not set"; the fixed-5-row structure always stays visible either way.
const NodePathMappingRow: Component<{
  entry: NodePathMappingEntry;
}> = (props) => {
  const label = () => LIBRARY_PATH_LABELS[props.entry.key] ?? props.entry.key;

  return (
    <div class="space-y-1 border-b border-border py-2 last:border-b-0">
      <div class="flex items-baseline justify-between gap-2">
        <span class={labelClass}>{label()}</span>
        <Show
          when={props.entry.configured}
          fallback={
            <span class="text-xs text-muted">
              configure this in Library settings first
            </span>
          }
        >
          <span
            class="truncate text-xs text-muted"
            title={props.entry.serverPath}
          >
            {props.entry.serverPath}
          </span>
        </Show>
      </div>
      <div class="text-sm text-fg">
        <Show when={props.entry.nodePath} fallback={<Muted>not set</Muted>}>
          {props.entry.nodePath}
        </Show>
      </div>
    </div>
  );
};

// ApproveModal collects only maxJobs then POSTs to approve the node. The raw
// per-node API key is delivered directly to the node via the pairing SSE
// stream — the operator never sees it. Path mappings are no longer collected
// here (D3/D1) — the node authors its own mappings, from its own filesystem
// view, after it connects post-approval.
const ApproveModal: Component<{
  pending: PendingNodeInfo;
  onClose: () => void;
  onDone: () => void;
}> = (props) => {
  const [maxJobs, setMaxJobs] = createSignal(0);
  const [saving, setSaving] = createSignal(false);
  const [err, setErr] = createSignal("");

  const submit = async () => {
    setSaving(true);
    setErr("");
    try {
      await approveNode(props.pending.id, {
        pathMap: [],
        maxJobs: maxJobs(),
      });
      props.onDone();
      props.onClose();
    } catch (e) {
      setErr(String(e));
    } finally {
      setSaving(false);
    }
  };

  return (
    <Modal title={`Approve "${props.pending.name}"`} onClose={props.onClose}>
      <div class="space-y-4">
        <p class="text-sm text-muted">
          Pairing code:{" "}
          <code class="rounded bg-accent/20 px-1.5 py-0.5 font-mono text-sm text-accent">
            {props.pending.pairingCode}
          </code>
        </p>
        <p class="text-xs text-muted">
          Once approved, the node configures its own path mappings — nothing
          to set here.
        </p>
        <div>
          <label class={labelClass}>
            Max concurrent jobs (0 = unlimited)
          </label>
          <input
            type="number"
            class={inputClass}
            min="0"
            value={maxJobs()}
            onInput={(e) => setMaxJobs(Number(e.currentTarget.value))}
          />
        </div>
        <Show when={err()}>
          <ErrorText>{err()}</ErrorText>
        </Show>
        <div class="flex justify-end gap-2">
          <Button onClick={props.onClose}>Cancel</Button>
          <Button onClick={submit} disabled={saving()}>
            {saving() ? "Approving…" : "Approve"}
          </Button>
        </div>
      </div>
    </Modal>
  );
};

// EditSettingsModal shows the node's self-reported path mappings as
// read-only text (GET /api/nodes/{id}/path-mappings — node-owned, D3/D1) and
// pushes a new maxJobs to an approved node via the settings SSE event.
// maxJobs remains the one operator-editable knob here; saving it never sends
// a PathMap the backend would use (the operator-auth write ignores PathMap
// entirely, but the request body still sends an empty array to make that
// explicit rather than relying on the backend to skip it silently).
//
// maxJobs initializes from props.node.maxJobs (the stored value GET
// /api/nodes already returns), not a hardcoded 0 — updateNodeSettingsOperatorAuth
// applies whatever maxJobs this modal submits unconditionally, so an operator
// who opens this modal only to look at the path mappings and clicks "Save
// settings" without touching the field must not silently reset an existing
// non-zero concurrency cap to 0.
//
// cpuCapPercent (the max-CPU governor slider, node-resource-governor plan Stage
// 5) is a second operator-owned knob riding the SAME batched updateNodeSettings
// write as maxJobs (both operator-owned; pause has its own separate path). It
// preloads from props.node.cpuCapPercent under the exact same anti-footgun
// discipline as maxJobs above — an untouched Save must never zero an existing
// cap. Its enforcement/last-apply reporting reads the STORED value
// (props.node.cpuCapPercent + props.node.cpuCapApply), never the live slider
// signal, so dragging the slider can't spuriously flip the "not enforced" note.
//
// The pause toggle (node-pause-dispatch plan, Stage 4) used to live here as a
// second, independent control in this modal. It has since been relocated
// (Stage 5) onto the node list row itself (NodeRow, below) as a switch, so an
// operator can pause/resume a node without opening this modal at all. See
// NodeRow's togglePause comment for the immediate-apply + rollback contract,
// unchanged by the move — only its screen location changed.
const EditSettingsModal: Component<{
  node: NodeInfo;
  onClose: () => void;
  onDone: () => void;
}> = (props) => {
  const [rows] = createResource(() => props.node.id, fetchNodePathMappings);
  const [maxJobs, setMaxJobs] = createSignal(props.node.maxJobs);
  const [cpuCapPercent, setCpuCapPercent] = createSignal(props.node.cpuCapPercent);
  const [saving, setSaving] = createSignal(false);
  const [err, setErr] = createSignal("");

  // notEnforcedReason reports WHY a capable node's cap isn't actually in force
  // right now, reading the STORED cap (props.node.cpuCapPercent) and the last
  // apply result (props.node.cpuCapApply) — never the live slider signal. A
  // non-empty last-apply error, or an effective quota that disagrees with the
  // stored configured percentage, both mean "capable but not enforcing". Empty
  // string means genuinely enforced (or nothing to enforce).
  const notEnforcedReason = (): string => {
    const apply = props.node.cpuCapApply;
    if (!apply) return "";
    if (apply.error) return apply.error;
    if (apply.effectivePercent !== props.node.cpuCapPercent) {
      return `configured ${props.node.cpuCapPercent}%, effective ${apply.effectivePercent}%`;
    }
    return "";
  };
  // The two reporting states are structurally mutually exclusive by the
  // enforcement discriminator: "unavailable" can never also read "not enforced"
  // (the erroring branch requires "available"), and "" (not yet reported)
  // renders neither — the honest default.
  const enforcementUnavailable = () => props.node.enforcement === "unavailable";
  const enforcementErroring = () =>
    props.node.enforcement === "available" && notEnforcedReason() !== "";

  const submit = async () => {
    setSaving(true);
    setErr("");
    try {
      await updateNodeSettings(props.node.id, {
        pathMap: [],
        maxJobs: maxJobs(),
        cpuCapPercent: cpuCapPercent(),
      });
      props.onDone();
      props.onClose();
    } catch (e) {
      setErr(String(e));
    } finally {
      setSaving(false);
    }
  };

  return (
    <Modal
      title={`Node settings — "${props.node.name}"`}
      onClose={props.onClose}
    >
      <div class="space-y-4">
        <div>
          <span class={labelClass}>Path mappings (library → node)</span>
          <p class="mb-2 text-xs text-muted">
            Reported by the node itself — configure these from the node, not
            here.
          </p>
          <Show when={rows.loading}>
            <Muted>Loading library paths…</Muted>
          </Show>
          <For each={rows()?.entries}>
            {(entry) => <NodePathMappingRow entry={entry} />}
          </For>
        </div>
        <div>
          <label class={labelClass}>
            Max concurrent jobs (0 = unlimited)
          </label>
          <input
            type="number"
            class={inputClass}
            min="0"
            aria-label="Max concurrent jobs"
            value={maxJobs()}
            onInput={(e) => setMaxJobs(Number(e.currentTarget.value))}
          />
        </div>
        <div>
          <label class={labelClass}>Max CPU % (0 = unlimited)</label>
          <div class="mt-1 flex items-center gap-3">
            <input
              type="range"
              min={0}
              max={100}
              value={cpuCapPercent()}
              aria-label="Max CPU percent slider"
              class="h-2 flex-1 accent-accent"
              onInput={(e) => setCpuCapPercent(Number(e.currentTarget.value))}
            />
            <input
              type="number"
              class={`${inputClass} !w-20`}
              min={0}
              max={100}
              aria-label="Max CPU percent"
              value={cpuCapPercent()}
              onInput={(e) => setCpuCapPercent(Number(e.currentTarget.value))}
            />
            <span class="text-xs text-muted">%</span>
          </div>
          <Muted class="mt-1">
            Max % of this node's total CPU for hashing, shared across all
            concurrent frame decodes (currently up to ~16 at once). Note: 'Max
            concurrent jobs' does not yet limit this. 0 = unlimited.
          </Muted>
          <Show when={enforcementUnavailable()}>
            <Muted class="mt-1">
              OS-level enforcement not available on this node
            </Muted>
          </Show>
          <Show when={enforcementErroring()}>
            <p class="mt-1 text-sm text-warn">
              not currently enforced: {notEnforcedReason()}
            </p>
          </Show>
        </div>
        <Show when={err()}>
          <ErrorText>{err()}</ErrorText>
        </Show>
        <div class="flex justify-end gap-2">
          <Button onClick={props.onClose}>Cancel</Button>
          <Button onClick={submit} disabled={saving()}>
            {saving() ? "Saving…" : "Save settings"}
          </Button>
        </div>
      </div>
    </Modal>
  );
};

// PendingRow shows a node waiting for operator approval with its pairing code.
const PendingRow: Component<{
  pending: PendingNodeInfo;
  onApprove: () => void;
  onReject: () => void;
  rejecting: boolean;
}> = (props) => (
  <div class="border-b border-border py-3 last:border-b-0">
    <div class="flex items-center justify-between gap-4">
      <div class="min-w-0 flex-1">
        <div class="flex items-center gap-2">
          <span class="truncate text-sm font-medium text-fg">
            {props.pending.name}
          </span>
          <code class="shrink-0 rounded bg-accent/20 px-1.5 py-0.5 font-mono text-xs text-accent">
            {props.pending.pairingCode}
          </code>
        </div>
        <div class="mt-0.5 text-xs text-muted">
          requested {formatHeartbeat(props.pending.requestedAt)}
        </div>
      </div>
      <div class="flex shrink-0 gap-2">
        <Button onClick={props.onApprove}>Approve</Button>
        <Button onClick={props.onReject} disabled={props.rejecting}>
          {props.rejecting ? "Rejecting…" : "Reject"}
        </Button>
      </div>
    </div>
  </div>
);

// NodeRow shows an approved node with online/offline status, a pause/resume
// switch, and a settings button.
//
// The pause/resume control (node-pause-dispatch plan, Stage 4) used to be a
// checkbox inside EditSettingsModal's settings modal. Relocated here (Stage
// 5) as a switch sitting directly in the row, alongside the status badge, so
// an operator can pause/resume a node without opening the modal at all.
//
// The former "Paused" text badge that used to sit next to the status pill is
// REMOVED, not kept alongside the switch: the switch itself is now in the
// row, and its own fill color (accent = dispatch enabled/running, muted =
// paused) plus thumb position already encode exactly the boolean the badge
// repeated in words. The switch is wired inverted from `paused` (checked =
// !paused) specifically so "on"/accent lines up with this app's existing
// color language for active/selected state (tabs, pills, primary buttons) —
// a paused row reading as "on" would fight that convention. Keeping both the
// switch and the old badge would show the same state twice in the same row;
// dropping the badge trims that duplication rather than adding a second
// component the operator has to reconcile against the switch.
const NodeRow: Component<{
  node: NodeInfo;
  onEdit: () => void;
  onDone: () => void;
}> = (props) => {
  const online = () => props.node.status === "online";
  const caps = () =>
    props.node.capabilities.length > 0
      ? props.node.capabilities.join(", ")
      : "none";

  // paused mirrors props.node.pauseDispatch (the stored value GET /api/nodes
  // already returns) — same preload discipline maxJobs uses in
  // EditSettingsModal, so the switch never shows a state that disagrees with
  // the real server state on first render. <For>'s default keyed-by-
  // reference reconciliation (NodesSection below) recreates this row — and so
  // re-syncs this signal from a fresh prop — whenever `data()` refetches with
  // a new node array, the same "fresh signal per mount" contract
  // EditSettingsModal relied on for its own preload before this toggle lived
  // here.
  const [paused, setPaused] = createSignal(props.node.pauseDispatch);
  const [pauseSaving, setPauseSaving] = createSignal(false);
  const [pauseErr, setPauseErr] = createSignal("");

  // togglePause fires updateNodePause IMMEDIATELY on toggle — relocated
  // verbatim from EditSettingsModal (node-pause-dispatch plan, Stage 4/5).
  // Routing pause through its own call, entirely separate from maxJobs' own
  // Save-gated body over in EditSettingsModal, is what keeps pause off
  // NodeSettingsRequest and makes the P2 footgun (pause and maxJobs sharing
  // one write) structurally impossible on the client — do not fold this into
  // updateNodeSettings or gate it behind any Save button.
  const togglePause = async (next: boolean) => {
    setPaused(next);
    setPauseSaving(true);
    setPauseErr("");
    try {
      await updateNodePause(props.node.id, next);
      props.onDone();
    } catch (e) {
      // Roll back the optimistic flip on failure so the switch never claims
      // a state the server never accepted (mirrors the node daemon's own
      // failed-push rollback behavior in this plan).
      setPaused(!next);
      setPauseErr(String(e));
    } finally {
      setPauseSaving(false);
    }
  };

  return (
    <div class="border-b border-border py-3 last:border-b-0">
      <div class="flex items-center justify-between gap-4">
        <div class="min-w-0 flex-1">
          <div class="truncate text-sm font-medium text-fg">
            {props.node.name}
          </div>
          <div class="mt-0.5 text-xs text-muted">
            capabilities: {caps()} · last heartbeat:{" "}
            {formatHeartbeat(props.node.lastHeartbeat)}
          </div>
          <Show when={pauseErr()}>
            <ErrorText>{pauseErr()}</ErrorText>
          </Show>
        </div>
        <div class="flex shrink-0 items-center gap-2">
          <Button onClick={props.onEdit}>Settings</Button>
          <Switch
            checked={!paused()}
            disabled={pauseSaving()}
            ariaLabel={`${props.node.name} dispatch enabled`}
            onChange={(next) => void togglePause(!next)}
          />
          <span
            class="rounded-full px-2 py-0.5 text-xs font-medium"
            classList={{
              "bg-ok/20 text-ok": online(),
              "bg-surface-2 text-muted": !online(),
            }}
          >
            {props.node.status}
          </span>
        </div>
      </div>
    </div>
  );
};

export const NodesSection: Component = () => {
  const [data, { refetch }] = createResource(fetchNodes);
  const [approvingNode, setApprovingNode] = createSignal<PendingNodeInfo | null>(null);
  const [editingNode, setEditingNode] = createSignal<NodeInfo | null>(null);
  const [rejectingId, setRejectingId] = createSignal<string | null>(null);
  const [rejectErr, setRejectErr] = createSignal("");

  // Poll every 3 s while there are pending nodes so new arrivals surface quickly.
  let pollTimer: number | undefined;
  createEffect(() => {
    const hasPending = (data()?.pending.length ?? 0) > 0;
    if (hasPending && pollTimer === undefined) {
      pollTimer = setInterval(() => refetch(), 3_000) as unknown as number;
    } else if (!hasPending && pollTimer !== undefined) {
      clearInterval(pollTimer);
      pollTimer = undefined;
    }
  });
  onCleanup(() => {
    if (pollTimer !== undefined) clearInterval(pollTimer);
  });

  const handleReject = async (id: string) => {
    setRejectingId(id);
    setRejectErr("");
    try {
      await rejectPending(id);
      refetch();
    } catch (e) {
      setRejectErr(String(e));
    } finally {
      setRejectingId(null);
    }
  };

  return (
    <>
      <Show when={approvingNode()}>
        {(node) => (
          <ApproveModal
            pending={node()}
            onClose={() => setApprovingNode(null)}
            onDone={() => refetch()}
          />
        )}
      </Show>

      <Show when={editingNode()}>
        {(node) => (
          <EditSettingsModal
            node={node()}
            onClose={() => setEditingNode(null)}
            onDone={() => refetch()}
          />
        )}
      </Show>

      <Show when={(data()?.pending.length ?? 0) > 0}>
        <Card title="Pending Approval">
          <Show when={rejectErr()}>
            <ErrorText>{rejectErr()}</ErrorText>
          </Show>
          <For each={data()?.pending}>
            {(pending) => (
              <PendingRow
                pending={pending}
                onApprove={() => setApprovingNode(pending)}
                onReject={() => handleReject(pending.id)}
                rejecting={rejectingId() === pending.id}
              />
            )}
          </For>
        </Card>
      </Show>

      <Card title="Worker Nodes">
        <Show when={data.error}>
          <ErrorText>Failed to load nodes: {String(data.error)}</ErrorText>
        </Show>

        <Show
          when={
            !data.loading && !data.error && (data()?.nodes.length ?? 0) === 0
          }
        >
          <Muted>
            No worker nodes connected. Install sakms-node on a machine with
            better GPU hardware and configure it to connect to this server.
          </Muted>
        </Show>

        <Show when={(data()?.nodes.length ?? 0) > 0}>
          <For each={data()?.nodes}>
            {(node) => (
              <NodeRow
                node={node}
                onEdit={() => setEditingNode(node)}
                onDone={() => refetch()}
              />
            )}
          </For>
        </Show>
      </Card>
    </>
  );
};
