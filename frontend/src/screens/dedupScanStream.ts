// useDedupScanStream — the Dedup-ONLY live-progress hook for the async Dedup
// scan. Deliberately NOT in workflowHooks.ts: that file's own doc restricts it
// to patterns "genuinely identical or trivially parameterizable across ALL FOUR"
// workflow screens; a per-file SSE progress stream exists only for Dedup, so it
// belongs beside Dedup (top-level CLAUDE.md "no premature abstraction").
//
// The backend's POST /api/modes/{mode}/dedup/scan now returns 202 and runs the
// scan in a background goroutine, broadcasting liveness over an SSE stream at
// GET /api/modes/{mode}/dedup/scan/stream. This hook opens one EventSource per
// active mode (the house pattern from Dashboard.tsx), drives a stream-fed
// `scanning` signal (NOT the POST promise — the 202 resolves almost immediately,
// so binding `scanning` to it would flip the button back and refetch an empty
// queue while the real scan is still running), renders a bounded log, and
// refetches proposals in the `done` handler — the single place the resolved
// duplicate-group list is fetched.
//
// Liveness backstop (the hard invariant: the scanning state must be recoverable
// without a manual page reload). A plain EventSource does NOT auto-reconnect on
// a silent drop, and an SSE terminal frame CAN be dropped/delayed under load, so
// a rolling quiet-window timer (reset on every received frame) reconciles
// against GET .../dedup/scan/status: if it reports inflight=false, the scan
// finished but this client missed the terminal frame — clear scanning and
// refetch exactly as `done` would; if inflight=true, the scan is genuinely still
// running (a slow file), so keep waiting.

import {
  type Accessor,
  createEffect,
  createSignal,
  on,
  onCleanup,
} from "solid-js";
import type { Mode } from "../api/discover";
import { fetchDedupScanStatus, scanDedup } from "../api/dedup";

// LogLine is one rendered per-file liveness entry.
export interface LogLine {
  name: string;
  current: number;
  total: number;
  phase: string;
}

// DedupScanEvent is one SSE frame from the scan stream. No generated DTO exists
// (the backend type lives in internal/dedupscan, not internal/apidto), so it is
// declared locally. Numeric fields are optional because the backend marshals
// them with `omitempty` — a zero `current`/`total` (notably the synthetic
// "starting" seed) arrives ABSENT, i.e. undefined, not 0.
interface DedupScanEvent {
  type: "progress" | "done" | "error";
  mode: string;
  current?: number;
  total?: number;
  name?: string;
  phase?: string;
  count?: number;
  error?: string;
}

export interface DedupScanStream {
  /** Stream-driven: true from the click/optimistic start (or a reconnect prime)
   * until a terminal frame or the liveness backstop clears it. */
  scanning: Accessor<boolean>;
  /** Bounded (last MAX_LOG_LINES) per-file entries for the current scan. */
  logLines: Accessor<LogLine[]>;
  /** A terminal `error` frame's message, or a rejected initiate POST. */
  scanError: Accessor<string | null>;
  /** Latest {current,total} for the header percentage; null before the first
   * real progress (the "Starting scan…" state) and after the box clears. */
  progress: Accessor<{ current: number; total: number } | null>;
  /** Optimistically flips scanning on, then POSTs scanDedup; a 4xx surfaces via
   * scanError and resets scanning. */
  initiate: (mode: Mode) => void;
}

// MAX_LOG_LINES caps the in-memory log so a huge library cannot grow it
// unbounded — only the most recent entries are kept.
const MAX_LOG_LINES = 500;

// QUIET_WINDOW_MS is how long the stream may go silent while scanning before the
// backstop reconciles against the status endpoint.
const QUIET_WINDOW_MS = 15_000;

export interface DedupScanStreamOptions {
  /** Refetch the proposals queue. Called in the `done` handler and by the
   * liveness backstop's finished-branch — the ONLY places the resolved list is
   * fetched. Dedup wraps its own per-scan state resets into this. */
  refetch: () => unknown | Promise<unknown>;
  /** Adult Organize chip: vertical/horizontal query on the scan POST. */
  aspect?: () => string | undefined;
}

export function useDedupScanStream(
  mode: Accessor<Mode>,
  opts: DedupScanStreamOptions,
): DedupScanStream {
  const [scanning, setScanning] = createSignal(false);
  const [logLines, setLogLines] = createSignal<LogLine[]>([]);
  const [scanError, setScanError] = createSignal<string | null>(null);
  const [progress, setProgress] = createSignal<{
    current: number;
    total: number;
  } | null>(null);

  // quietTimer is the rolling liveness-backstop timer; reset on every frame,
  // cleared on any terminal transition and on mode-switch/unmount cleanup.
  let quietTimer: ReturnType<typeof setTimeout> | undefined;
  const clearQuietTimer = (): void => {
    if (quietTimer !== undefined) {
      clearTimeout(quietTimer);
      quietTimer = undefined;
    }
  };
  const armQuietTimer = (m: Mode): void => {
    clearQuietTimer();
    quietTimer = setTimeout(() => void reconcile(m), QUIET_WINDOW_MS);
  };

  // finish clears scanning + the log/progress and refetches — the shared body of
  // the `done` handler and the backstop's finished-branch (terminal frame lost).
  const finish = async (): Promise<void> => {
    setScanning(false);
    setScanError(null);
    clearQuietTimer();
    setLogLines([]);
    await opts.refetch();
  };

  // reconcile is the quiet-window fire: ask the server whether the scan is
  // really still running. inflight=false ⇒ it finished and we missed the
  // terminal frame ⇒ finish(); inflight=true ⇒ a genuinely slow file ⇒ keep
  // waiting. A status fetch error is treated as "unknown" — re-arm and retry.
  const reconcile = async (m: Mode): Promise<void> => {
    if (!scanning()) return;
    try {
      const { inflight } = await fetchDedupScanStatus(m);
      if (!scanning()) return; // a real terminal frame won the race
      if (inflight) armQuietTimer(m);
      else await finish();
    } catch {
      if (scanning()) armQuietTimer(m);
    }
  };

  const handleProgress = (ev: DedupScanEvent, m: Mode): void => {
    setScanning(true);
    // Clear a stale error from an earlier 409 (e.g. this tab's own initiate()
    // rejected because another tab already started this mode's scan) — a live
    // progress frame proves a scan is genuinely running, so the error no
    // longer applies and must not keep hiding the log box.
    setScanError(null);
    armQuietTimer(m);
    // The synthetic reconnect-priming seed is uniquely marked by phase; its
    // current/total arrive absent (omitempty), so render the neutral
    // "Starting scan…" state (progress stays null) rather than a 0/0 log row.
    if (ev.phase === "starting") return;
    const current = ev.current ?? 0;
    const total = ev.total ?? 0;
    setProgress({ current, total });
    setLogLines((prev) => {
      const next = [
        ...prev,
        { name: ev.name ?? "", current, total, phase: ev.phase ?? "" },
      ];
      return next.length > MAX_LOG_LINES
        ? next.slice(next.length - MAX_LOG_LINES)
        : next;
    });
  };

  const handleDone = (ev: DedupScanEvent): void => {
    // The done event carries the AUTHORITATIVE final total (always 100%); set it
    // before clearing so the completion invariant holds even if a best-effort
    // live total undershot. The box then clears (AC-3) and the cards take over.
    const total = ev.total ?? 0;
    setProgress({ current: total, total });
    void finish();
  };

  const handleError = (ev: DedupScanEvent): void => {
    setScanning(false);
    clearQuietTimer();
    setLogLines([]);
    setProgress(null);
    setScanError(ev.error || "scan failed");
  };

  // One EventSource per active mode. createEffect(on(mode)) re-runs on every mode
  // switch; onCleanup closes the prior stream + timer before the next opens, so
  // no leak across mode switches (AC-3). Runs on initial mount too (no defer), so
  // a reconnect mid-scan is primed by the backend's last-event replay.
  createEffect(
    on(mode, (m) => {
      // Reset per-mode view state on entry so a prior mode's scan never bleeds
      // through; a mid-scan reconnect re-primes from the stream immediately.
      setScanning(false);
      setLogLines([]);
      setScanError(null);
      setProgress(null);
      clearQuietTimer();

      const es = new EventSource(`/api/modes/${m}/dedup/scan/stream`);
      es.onmessage = (evt) => {
        let event: DedupScanEvent;
        try {
          event = JSON.parse(evt.data) as DedupScanEvent;
        } catch {
          return; // ignore a malformed frame; the next should be fine
        }
        switch (event.type) {
          case "progress":
            handleProgress(event, m);
            break;
          case "done":
            handleDone(event);
            break;
          case "error":
            handleError(event);
            break;
        }
      };

      onCleanup(() => {
        es.close();
        clearQuietTimer();
      });
    }),
  );

  const initiate = (m: Mode): void => {
    // Optimistically enter the scanning state so the log box appears on click,
    // before the first stream frame. Arm the backstop immediately in case the
    // stream never delivers a frame at all.
    setScanError(null);
    setLogLines([]);
    setProgress(null);
    setScanning(true);
    armQuietTimer(m);
    void scanDedup(m, m === "adult" ? opts.aspect?.() : undefined).catch((e: unknown) => {
      // A rejected POST (400 bad root, 409 already running, 401 auth) means the
      // scan never started — surface it and reset so the UI is not left stuck.
      setScanning(false);
      clearQuietTimer();
      setScanError((e as Error).message);
    });
  };

  return { scanning, logLines, scanError, progress, initiate };
}
