// Settings → Nodes tab — read-only list of connected (and recently
// disconnected) worker nodes. Fetched once on mount via createResource;
// no mutations in v1.

import { type Component, createResource, For, Show } from "solid-js";
import { fetchNodes } from "../../api/settings";
import { Card, ErrorText, Muted } from "../../components/ui";
import type { NodeInfo } from "@dto";

// formatHeartbeat renders a LastHeartbeat RFC3339 timestamp as a relative
// human-readable string ("2 minutes ago", "just now", etc.) for quick
// at-a-glance freshness.
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

const NodeRow: Component<{ node: NodeInfo }> = (props) => {
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
        <span
          class="shrink-0 rounded-full px-2 py-0.5 text-xs font-medium"
          classList={{
            "bg-ok/20 text-ok": online(),
            "bg-surface-2 text-muted": !online(),
          }}
        >
          {props.node.status}
        </span>
      </div>
    </div>
  );
};

export const NodesSection: Component = () => {
  const [nodes] = createResource(fetchNodes);

  return (
    <Card title="Worker Nodes">
      <Show when={nodes.error}>
        <ErrorText>Failed to load nodes: {String(nodes.error)}</ErrorText>
      </Show>

      <Show when={!nodes.loading && !nodes.error && (nodes()?.nodes.length ?? 0) === 0}>
        <Muted>
          No worker nodes connected. Install sakms-node on a machine with
          better GPU hardware and configure it to connect to this server.
        </Muted>
      </Show>

      <Show when={(nodes()?.nodes.length ?? 0) > 0}>
        <For each={nodes()?.nodes}>
          {(node) => <NodeRow node={node} />}
        </For>
      </Show>
    </Card>
  );
};
