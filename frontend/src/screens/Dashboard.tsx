// Dashboard — a live, container-scoped resource view fed by the SSE stream at
// GET /api/admin/sysinfo/stream (see internal/api/sysinfo.go). It opens one
// EventSource, renders a placeholder until the first snapshot arrives, shows a
// transient reconnecting notice on a transport error (onerror), and a separate
// banner on an in-stream "sampleError" event (a server-side metric read
// failure while the connection stays alive). It registers no screen tabs —
// it's a single view, not a mode/tab-split screen.
//
// The backend emits its first data event ~2s after connect (after its initial
// sample pair), so the loading state is expected on first mount.

import {
  type Component,
  createSignal,
  For,
  onCleanup,
  onMount,
  Show,
} from "solid-js";
import type { SysinfoSnapshot } from "@dto";
import { Card, Muted } from "../components/ui";

// formatBps renders a bytes/sec value: <1024 → "X B/s", <1MB → "X KB/s",
// else "X.X MB/s".
function formatBps(bps: number): string {
  if (bps < 1024) return `${Math.round(bps)} B/s`;
  if (bps < 1024 * 1024) return `${Math.round(bps / 1024)} KB/s`;
  return `${(bps / (1024 * 1024)).toFixed(1)} MB/s`;
}

// formatGB renders a byte count as gibibytes with one decimal.
function formatGB(bytes: number): string {
  return `${(bytes / (1024 * 1024 * 1024)).toFixed(1)} GB`;
}

// Bar is a 0–100% horizontal fill bar.
const Bar: Component<{ percent: number }> = (props) => {
  const clamped = () => Math.max(0, Math.min(100, props.percent));
  return (
    <div class="h-2 w-full overflow-hidden rounded-full bg-surface-2">
      <div
        class="h-full rounded-full bg-accent transition-[width] duration-500"
        style={{ width: `${clamped()}%` }}
      />
    </div>
  );
};

export const Dashboard: Component = () => {
  const [snap, setSnap] = createSignal<SysinfoSnapshot | null>(null);
  const [reconnecting, setReconnecting] = createSignal(false);
  // error holds an in-stream read failure (a "sampleError" SSE event), kept
  // separate from the transport-level reconnecting notice: the connection is
  // still alive, but a metric read failed server-side.
  const [error, setError] = createSignal<string | null>(null);

  let es: EventSource | undefined;

  onMount(() => {
    es = new EventSource("/api/admin/sysinfo/stream");
    es.onmessage = (ev) => {
      try {
        setSnap(JSON.parse(ev.data) as SysinfoSnapshot);
        setReconnecting(false);
        setError(null);
      } catch {
        /* ignore a malformed frame — the next one should be fine */
      }
    };
    // A named "sampleError" event is an in-stream server-side read failure
    // (deliberately not the reserved "error" name, which onerror below owns).
    es.addEventListener("sampleError", (e) => {
      setError(`Metric read failed: ${(e as MessageEvent).data}`);
    });
    es.onerror = () => setReconnecting(true);
  });

  onCleanup(() => es?.close());

  // memPercent is 0 when the limit is unlimited (-1) or unknown — the fill bar
  // just reads empty in that case, and the label says "unlimited".
  const memPercent = () => {
    const s = snap();
    if (!s || s.memLimitBytes <= 0) return 0;
    return (s.memUsedBytes / s.memLimitBytes) * 100;
  };

  return (
    <div>
      <Show when={reconnecting()}>
        <div class="mb-4 rounded-md border border-warn/40 bg-warn/10 px-3 py-2 text-sm text-warn">
          Connection lost — reconnecting…
        </div>
      </Show>

      <Show when={error()}>
        {(msg) => (
          <div class="mb-4 rounded-md border border-warn/40 bg-warn/10 px-3 py-2 text-sm text-warn">
            {msg()}
          </div>
        )}
      </Show>

      <Show
        when={snap()}
        fallback={<Muted>Waiting for the first live reading…</Muted>}
      >
        {(s) => (
          <div class="grid grid-cols-1 gap-4 md:grid-cols-2">
            <Card title="CPU">
              <div class="mb-2 text-2xl font-semibold text-fg">
                {s().cpuPercent.toFixed(1)}%
              </div>
              <Bar percent={s().cpuPercent} />
            </Card>

            <Card title="Memory">
              <div class="mb-2 text-sm text-fg">
                {formatGB(s().memUsedBytes)} used
                {s().memLimitBytes > 0
                  ? ` / ${formatGB(s().memLimitBytes)} limit`
                  : " / unlimited"}
              </div>
              <Bar percent={memPercent()} />
            </Card>

            <Card title="Network">
              <div class="flex gap-6 text-sm text-fg">
                <span>↓ {formatBps(s().netRxBps)}</span>
                <span>↑ {formatBps(s().netTxBps)}</span>
              </div>
            </Card>

            <Card title="Container disk I/O">
              <div class="flex gap-6 text-sm text-fg">
                <span>R: {formatBps(s().containerDiskReadBps)}</span>
                <span>W: {formatBps(s().containerDiskWriteBps)}</span>
              </div>
            </Card>

            <For each={s().storageMounts}>
              {(mount) => (
                <Card title={mount.name}>
                  <Show
                    when={mount.configured}
                    fallback={<Muted>Not configured</Muted>}
                  >
                    <div class="mb-2 text-sm text-fg">
                      {formatGB(mount.totalBytes - mount.availBytes)} used
                      {" of "}
                      {formatGB(mount.totalBytes)}
                    </div>
                    <Bar
                      percent={
                        mount.totalBytes > 0
                          ? ((mount.totalBytes - mount.availBytes) /
                              mount.totalBytes) *
                            100
                          : 0
                      }
                    />
                  </Show>
                </Card>
              )}
            </For>

            <div class="md:col-span-2">
              <Card title="Server disks">
                <Show
                  when={s().serverDisks.length > 0}
                  fallback={<Muted>No physical disks reported.</Muted>}
                >
                  <ul class="flex flex-col gap-1">
                    <For each={s().serverDisks}>
                      {(d) => (
                        <li class="flex items-center gap-4 text-sm text-fg">
                          <span class="w-24 shrink-0 font-medium">{d.name}</span>
                          <span>R {formatBps(d.readBps)}</span>
                          <span>W {formatBps(d.writeBps)}</span>
                        </li>
                      )}
                    </For>
                  </ul>
                </Show>
              </Card>
            </div>
          </div>
        )}
      </Show>
    </div>
  );
};
