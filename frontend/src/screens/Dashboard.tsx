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
//
// Layout is a 3-column grid (CPU + GPUs left, Memory/Network/Container/Storage
// middle, aggregate Disk I/O right) with two circular arc gauges (CPU, Disk
// I/O), a used/free donut per storage mount, fill bars for Memory and GPU VRAM,
// and sparkline history for CPU and Network — the sakms cream/navy/gold palette
// throughout, via the semantic color tokens (never hard-coded hex).

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

// formatGbps renders an aggregate disk throughput for the Disk I/O gauge label:
// <1e6 → "X KB/s", <1e9 → "X MB/s", else "X.X GB/s" (decimal SI, matching the
// gauge's 500 MB/s ceiling reasoning rather than the binary formatBps above).
function formatGbps(n: number): string {
  if (n < 1e6) return `${Math.round(n / 1e3)} KB/s`;
  if (n < 1e9) return `${Math.round(n / 1e6)} MB/s`;
  return `${(n / 1e9).toFixed(1)} GB/s`;
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

// ArcGauge is a 270° SVG arc gauge with the gap at the bottom. The track and
// fill are two dash-patterned circles rotated so the 90° gap sits at 6 o'clock;
// the fill's dash length scales with percent. color is a text-* token class
// (default gold) that currentColor picks up.
const ARC_R = 40;
const ARC_CIRCUMFERENCE = 2 * Math.PI * ARC_R; // ≈ 251.33
const ARC_LENGTH = ARC_CIRCUMFERENCE * 0.75; // 270° arc

const ArcGauge: Component<{
  percent: number;
  label: string;
  sublabel: string;
  color?: string;
}> = (props) => {
  const clamped = () => Math.max(0, Math.min(100, props.percent));
  const fillLen = () => ARC_LENGTH * (clamped() / 100);
  return (
    <svg viewBox="0 0 100 100" class="mx-auto w-full max-w-[160px]">
      <circle
        cx="50"
        cy="50"
        r={ARC_R}
        fill="none"
        stroke="currentColor"
        stroke-width="10"
        stroke-linecap="round"
        class="text-surface-2"
        stroke-dasharray={`${ARC_LENGTH} ${ARC_CIRCUMFERENCE - ARC_LENGTH}`}
        transform="rotate(135 50 50)"
      />
      <circle
        cx="50"
        cy="50"
        r={ARC_R}
        fill="none"
        stroke="currentColor"
        stroke-width="10"
        stroke-linecap="round"
        class={props.color ?? "text-accent"}
        stroke-dasharray={`${fillLen()} ${ARC_CIRCUMFERENCE - fillLen()}`}
        transform="rotate(135 50 50)"
        style={{ transition: "stroke-dasharray 500ms ease" }}
      />
      <text
        x="50"
        y="46"
        text-anchor="middle"
        class="fill-fg font-bold"
        font-size="18"
      >
        {props.label}
      </text>
      <text
        x="50"
        y="60"
        text-anchor="middle"
        class="fill-muted"
        font-size="9"
      >
        {props.sublabel}
      </text>
    </svg>
  );
};

// Donut is a two-slice used/free ring — a parts-of-a-whole snapshot of how full
// a storage mount is. It's a *meter* (a magnitude read against a track), not a
// categorical two-series chart: the used slice carries the accent, the free
// slice is the same surface-2 track the Bar and ArcGauge above use, so the pair
// reads as "fill vs. remaining" rather than two independent identities. Each
// slice is an explicit arc <path> (rather than ArcGauge's dash-patterned circle)
// because both slices need a real 2px surface gap between them — the white card
// shows through the gap, which is the secondary encoding that keeps the two pale
// arcs distinct without relying on color contrast alone. The parent keeps the
// "{used} used of {total}" text line beside this, so identity is never
// color-alone.
const DONUT_R = 38; // ring radius within the 0..100 viewBox
const DONUT_STROKE = 14; // ring thickness
const DONUT_C = 2 * Math.PI * DONUT_R; // ≈ 238.76
// DONUT_GAP_DEG turns a ~3px surface gap (at the ~160px max render width) into
// degrees of arc, split half onto each end of a slice.
const DONUT_GAP_DEG = (3 / DONUT_C) * 360;

// polarToCartesian maps a clockwise angle measured from 12 o'clock (0° = top)
// to an (x,y) on the ring. SVG's y-axis points down, so adding the standard
// −90° phase makes increasing angles sweep clockwise.
function polarToCartesian(angleDeg: number): { x: number; y: number } {
  const a = ((angleDeg - 90) * Math.PI) / 180;
  return { x: 50 + DONUT_R * Math.cos(a), y: 50 + DONUT_R * Math.sin(a) };
}

// arcPath returns an SVG arc `d` from startAngle to endAngle (clockwise, degrees
// from 12 o'clock). It returns "" for a non-positive sweep so a slice narrower
// than the gap simply draws nothing.
function arcPath(startAngle: number, endAngle: number): string {
  if (endAngle - startAngle <= 0) return "";
  const start = polarToCartesian(startAngle);
  const end = polarToCartesian(endAngle);
  const largeArc = endAngle - startAngle > 180 ? 1 : 0;
  return `M ${start.x} ${start.y} A ${DONUT_R} ${DONUT_R} 0 ${largeArc} 1 ${end.x} ${end.y}`;
}

const Donut: Component<{ percent: number; title?: string }> = (props) => {
  const clamped = () => Math.max(0, Math.min(100, props.percent));
  const usedPct = () => Math.round(clamped());
  const half = DONUT_GAP_DEG / 2;
  const usedAngle = () => (clamped() / 100) * 360;
  const usedD = () => arcPath(half, usedAngle() - half);
  const freeD = () => arcPath(usedAngle() + half, 360 - half);
  return (
    <svg
      viewBox="0 0 100 100"
      class="mx-auto w-full max-w-[160px]"
      role="img"
      aria-label={props.title ?? `${usedPct()}% used`}
      data-used-percent={usedPct()}
    >
      <Show when={props.title}>
        {(t) => <title>{t()}</title>}
      </Show>
      <path
        d={freeD()}
        fill="none"
        stroke="currentColor"
        stroke-width={DONUT_STROKE}
        class="text-surface-2"
      />
      <path
        d={usedD()}
        fill="none"
        stroke="currentColor"
        stroke-width={DONUT_STROKE}
        class="text-accent"
      />
      <text
        x="50"
        y="48"
        text-anchor="middle"
        class="fill-fg font-bold"
        font-size="16"
      >
        {usedPct()}%
      </text>
      <text
        x="50"
        y="61"
        text-anchor="middle"
        class="fill-muted"
        font-size="8"
      >
        used
      </text>
    </svg>
  );
};

// Sparkline draws a history polyline scaled to the tallest sample. It renders
// nothing until there are at least two points (a single point has no line).
const Sparkline: Component<{ values: number[] }> = (props) => {
  const points = () => {
    const vs = props.values;
    const max = Math.max(...vs, 1);
    const denom = vs.length - 1 || 1;
    return vs.map((v, i) => `${i / denom},${1 - v / max}`).join(" ");
  };
  return (
    <Show when={props.values.length >= 2}>
      <svg
        viewBox="0 0 1 1"
        preserveAspectRatio="none"
        class="h-10 w-full"
      >
        <polyline
          points={points()}
          fill="none"
          stroke="currentColor"
          class="text-accent"
          stroke-width="0.025"
          stroke-linecap="round"
        />
      </svg>
    </Show>
  );
};

export const Dashboard: Component = () => {
  const [snap, setSnap] = createSignal<SysinfoSnapshot | null>(null);
  // history keeps the last 60 snapshots for the CPU/Network sparklines.
  const [history, setHistory] = createSignal<SysinfoSnapshot[]>([]);
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
        const s = JSON.parse(ev.data) as SysinfoSnapshot;
        setSnap(s);
        setHistory((h) => [...h.slice(-59), s]);
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

  // totalDiskBps sums read+write across every physical server disk; diskPct
  // maps it onto the gauge against a 500 MB/s ceiling (clamped to 100%).
  const totalDiskBps = () =>
    (snap()?.serverDisks ?? []).reduce((a, d) => a + d.readBps + d.writeBps, 0);
  const diskPct = () => Math.min(100, (totalDiskBps() / 500_000_000) * 100);

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
          <div class="grid grid-cols-1 gap-4 md:grid-cols-[1fr_2fr_1fr]">
            {/* Left column: CPU gauge + history, then per-GPU cards. */}
            <div class="flex flex-col gap-4">
              <Card title="CPU">
                <ArcGauge
                  percent={s().cpuPercent}
                  label={`${s().cpuPercent.toFixed(1)}%`}
                  sublabel="CPU"
                />
                <Sparkline values={history().map((h) => h.cpuPercent)} />
              </Card>

              <For each={s().gpus}>
                {(gpu) => (
                  <Card title={gpu.name}>
                    <Show
                      when={gpu.utilPercent >= 0}
                      fallback={
                        <p class="py-2 text-center text-sm text-muted">
                          Utilization unavailable
                        </p>
                      }
                    >
                      <ArcGauge
                        percent={gpu.utilPercent}
                        label={`${gpu.utilPercent}%`}
                        sublabel="GPU"
                      />
                    </Show>

                    <Show when={gpu.vramTotalBytes > 0}>
                      <div class="mt-2 mb-1 text-xs text-muted">
                        {formatGB(gpu.vramUsedBytes)} / {formatGB(gpu.vramTotalBytes)} VRAM
                      </div>
                      <Bar
                        percent={
                          (gpu.vramUsedBytes / gpu.vramTotalBytes) * 100
                        }
                      />
                    </Show>

                    <Show when={gpu.powerMicrowatts > 0}>
                      <p class="mt-1 text-xs text-muted">
                        {(gpu.powerMicrowatts / 1e6).toFixed(1)} W
                      </p>
                    </Show>
                  </Card>
                )}
              </For>
            </div>

            {/* Middle column: Memory, Network, Container disk, Storage. */}
            <div class="flex flex-col gap-4">
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
                <div class="mb-2 flex gap-6 text-sm text-fg">
                  <span>↓ {formatBps(s().netRxBps)}</span>
                  <span>↑ {formatBps(s().netTxBps)}</span>
                </div>
                <Sparkline
                  values={history().map((h) => (h.netRxBps + h.netTxBps) / 1e6)}
                />
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
                      <Donut
                        percent={
                          mount.totalBytes > 0
                            ? ((mount.totalBytes - mount.availBytes) /
                                mount.totalBytes) *
                              100
                            : 0
                        }
                        title={`${formatGB(
                          mount.totalBytes - mount.availBytes,
                        )} used · ${formatGB(mount.availBytes)} free`}
                      />
                    </Show>
                  </Card>
                )}
              </For>
            </div>

            {/* Right column: aggregate Disk I/O gauge + per-disk breakdown. */}
            <div class="flex flex-col gap-4">
              <Card title="Disk I/O">
                <ArcGauge
                  percent={diskPct()}
                  label={formatGbps(totalDiskBps())}
                  sublabel="Disk I/O"
                />
                <Show
                  when={s().serverDisks.length > 0}
                  fallback={<Muted>No physical disks reported.</Muted>}
                >
                  <ul class="mt-2 flex flex-col gap-1 text-xs text-muted">
                    <For each={s().serverDisks}>
                      {(d) => (
                        <li class="flex items-center gap-3">
                          <span class="w-20 shrink-0 font-medium">{d.name}</span>
                          <span>R: {formatBps(d.readBps)}</span>
                          <span>W: {formatBps(d.writeBps)}</span>
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
