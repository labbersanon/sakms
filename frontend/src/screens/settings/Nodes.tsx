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
} from "../../components/ui";
import { NodeFolderPicker } from "../../components/NodeFolderPicker";
import type {
  NodeInfo,
  NodePathMappingEntry,
  NodePathMappingInput,
  PendingNodeInfo,
} from "@dto";

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

function pathMapInput(
  nodePaths: Record<string, string>,
): NodePathMappingInput[] {
  return Object.entries(nodePaths).map(([key, nodePath]) => ({
    key,
    nodePath,
  }));
}

// NodePathMappingRow renders one fixed row: a read-only label + the library
// path's current server-side value (for context — Library settings owns
// configuring it, not this form), and either a live node-browsable picker
// (nodeId present — an already-approved, connected node) or a plain text
// input (ApproveModal, before live browsing is available). A row whose
// library path isn't configured yet renders disabled/grayed with a note,
// rather than being hidden — the fixed-5-row structure always stays visible.
const NodePathMappingRow: Component<{
  entry: NodePathMappingEntry;
  value: string;
  onChange: (nodePath: string) => void;
  nodeId?: string;
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
      <Show
        when={props.nodeId}
        fallback={
          <input
            class={inputClass}
            placeholder="/mnt/media"
            value={props.value}
            disabled={!props.entry.configured}
            onInput={(e) => props.onChange(e.currentTarget.value)}
          />
        }
      >
        {(nodeId) => (
          <NodeFolderPicker
            nodeId={nodeId()}
            value={() => props.value}
            onChange={props.onChange}
            placeholder="/mnt/media"
            disabled={!props.entry.configured}
          />
        )}
      </Show>
    </div>
  );
};

// useNodePathMappings loads the fixed 5-row list for a node id (works for a
// not-yet-approved pending id too — see fetchNodePathMappings' doc comment)
// and tracks the operator's in-progress NodePath edits keyed by library path
// key, seeded from each row's persisted value.
function useNodePathMappings(nodeId: () => string) {
  const [rows] = createResource(nodeId, fetchNodePathMappings);
  const [nodePaths, setNodePaths] = createSignal<Record<string, string>>({});

  createEffect(() => {
    const entries = rows()?.entries;
    if (!entries) return;
    const seeded: Record<string, string> = {};
    for (const e of entries) seeded[e.key] = e.nodePath;
    setNodePaths(seeded);
  });

  const setOne = (key: string, value: string) =>
    setNodePaths((prev) => ({ ...prev, [key]: value }));

  return { rows, nodePaths, setOne };
}

// ApproveModal collects path mappings and maxJobs then POSTs to approve the
// node. The raw per-node API key is delivered directly to the node via the
// pairing SSE stream — the operator never sees it. Node paths are plain text
// here (no live browse yet — the node isn't approved/connected until this
// submits), per the approved decision that live browsing is available only
// after approval.
const ApproveModal: Component<{
  pending: PendingNodeInfo;
  onClose: () => void;
  onDone: () => void;
}> = (props) => {
  const { rows, nodePaths, setOne } = useNodePathMappings(
    () => props.pending.id,
  );
  const [maxJobs, setMaxJobs] = createSignal(0);
  const [saving, setSaving] = createSignal(false);
  const [err, setErr] = createSignal("");

  const submit = async () => {
    setSaving(true);
    setErr("");
    try {
      await approveNode(props.pending.id, {
        pathMap: pathMapInput(nodePaths()),
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
        <div>
          <span class={labelClass}>Path mappings (library → node)</span>
          <p class="mb-2 text-xs text-muted">
            Live directory browsing is available after approval — type the
            node-side path for now.
          </p>
          <Show when={rows.loading}>
            <Muted>Loading library paths…</Muted>
          </Show>
          <For each={rows()?.entries}>
            {(entry) => (
              <NodePathMappingRow
                entry={entry}
                value={nodePaths()[entry.key] ?? ""}
                onChange={(v) => setOne(entry.key, v)}
              />
            )}
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

// EditSettingsModal pushes new path mappings and maxJobs to an approved node
// via the settings SSE event. Loads the node's real persisted current values
// (GET /api/nodes/{id}/path-mappings) instead of always starting blank, and
// offers a live node-browsable picker per row since this node is already
// approved and (usually) connected.
const EditSettingsModal: Component<{
  node: NodeInfo;
  onClose: () => void;
  onDone: () => void;
}> = (props) => {
  const { rows, nodePaths, setOne } = useNodePathMappings(
    () => props.node.id,
  );
  const [maxJobs, setMaxJobs] = createSignal(0);
  const [saving, setSaving] = createSignal(false);
  const [err, setErr] = createSignal("");

  const submit = async () => {
    setSaving(true);
    setErr("");
    try {
      await updateNodeSettings(props.node.id, {
        pathMap: pathMapInput(nodePaths()),
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
    <Modal
      title={`Node settings — "${props.node.name}"`}
      onClose={props.onClose}
    >
      <div class="space-y-4">
        <div>
          <span class={labelClass}>Path mappings (library → node)</span>
          <Show when={rows.loading}>
            <Muted>Loading library paths…</Muted>
          </Show>
          <For each={rows()?.entries}>
            {(entry) => (
              <NodePathMappingRow
                entry={entry}
                value={nodePaths()[entry.key] ?? ""}
                onChange={(v) => setOne(entry.key, v)}
                nodeId={props.node.id}
              />
            )}
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

// NodeRow shows an approved node with online/offline status and a settings button.
const NodeRow: Component<{ node: NodeInfo; onEdit: () => void }> = (props) => {
  const online = () => props.node.status === "online";
  const caps = () =>
    props.node.capabilities.length > 0
      ? props.node.capabilities.join(", ")
      : "none";

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
        </div>
        <div class="flex shrink-0 items-center gap-2">
          <Button onClick={props.onEdit}>Settings</Button>
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
              <NodeRow node={node} onEdit={() => setEditingNode(node)} />
            )}
          </For>
        </Show>
      </Card>
    </>
  );
};
