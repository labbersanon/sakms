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
import type { NodeInfo, PathMapping, PendingNodeInfo } from "@dto";

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

// PathMapEditor is the shared server→local path mapping form used by both
// the approve and edit-settings modals.
const PathMapEditor: Component<{
  rows: PathMapping[];
  onChange: (rows: PathMapping[]) => void;
}> = (props) => {
  const update = (i: number, field: "server" | "local", value: string) => {
    props.onChange(
      props.rows.map((r, idx) => (idx === i ? { ...r, [field]: value } : r)),
    );
  };
  const remove = (i: number) => {
    props.onChange(props.rows.filter((_, idx) => idx !== i));
  };
  const add = () => props.onChange([...props.rows, { server: "", local: "" }]);

  return (
    <div class="space-y-2">
      <span class={labelClass}>Path mappings (server → node)</span>
      <Show when={props.rows.length === 0}>
        <Muted>No mappings — node and server share the same paths.</Muted>
      </Show>
      <For each={props.rows}>
        {(row, i) => (
          <div class="flex items-center gap-2">
            <input
              class={`${inputClass} flex-1 min-w-0`}
              placeholder="/srv/media"
              value={row.server}
              onInput={(e) => update(i(), "server", e.currentTarget.value)}
            />
            <span class="shrink-0 text-xs text-muted">→</span>
            <input
              class={`${inputClass} flex-1 min-w-0`}
              placeholder="/mnt/media"
              value={row.local}
              onInput={(e) => update(i(), "local", e.currentTarget.value)}
            />
            <button
              type="button"
              class="shrink-0 text-sm text-muted hover:text-fg"
              onClick={() => remove(i())}
              title="Remove mapping"
            >
              ×
            </button>
          </div>
        )}
      </For>
      <Button onClick={add}>+ Add mapping</Button>
    </div>
  );
};

// ApproveModal collects path mappings and maxJobs then POSTs to approve the node.
// The raw per-node API key is delivered directly to the node via the pairing
// SSE stream — the operator never sees it.
const ApproveModal: Component<{
  pending: PendingNodeInfo;
  onClose: () => void;
  onDone: () => void;
}> = (props) => {
  const [pathMap, setPathMap] = createSignal<PathMapping[]>([]);
  const [maxJobs, setMaxJobs] = createSignal(0);
  const [saving, setSaving] = createSignal(false);
  const [err, setErr] = createSignal("");

  const submit = async () => {
    setSaving(true);
    setErr("");
    try {
      await approveNode(props.pending.id, {
        pathMap: pathMap(),
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
        <PathMapEditor rows={pathMap()} onChange={setPathMap} />
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
// via the settings SSE event. Always starts from a blank form — current
// settings live only in the node daemon's config file, not on the server.
const EditSettingsModal: Component<{
  node: NodeInfo;
  onClose: () => void;
  onDone: () => void;
}> = (props) => {
  const [pathMap, setPathMap] = createSignal<PathMapping[]>([]);
  const [maxJobs, setMaxJobs] = createSignal(0);
  const [saving, setSaving] = createSignal(false);
  const [err, setErr] = createSignal("");

  const submit = async () => {
    setSaving(true);
    setErr("");
    try {
      await updateNodeSettings(props.node.id, {
        pathMap: pathMap(),
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
        <PathMapEditor rows={pathMap()} onChange={setPathMap} />
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
