// Claude 2026-09-26: Downloads card detail popup.
// Reason: cards stay compact; Usenet/torrent telemetry and torrent actions live here.
// Troubleshooting: empty heading — catalogTitle comes from the grab via SSE.
// Review if: a dedicated GET /api/downloads/{gid} replaces list enrichment.

import { type Component, For, Show, createSignal, onCleanup, onMount } from "solid-js";
import type { Download, TorrentFileDetail } from "@dto";
import {
  pauseDownload,
  reannounceDownload,
  recheckDownload,
  resumeDownload,
  setDownloadFilePriority,
} from "../api/downloads";
import { Button } from "../components/ui";

function formatSize(bytes: number): string {
  if (!bytes) return "0 B";
  if (bytes < 1024 * 1024) return `${Math.round(bytes / 1024)} KB`;
  if (bytes < 1024 * 1024 * 1024)
    return `${(bytes / (1024 * 1024)).toFixed(0)} MB`;
  return `${(bytes / (1024 * 1024 * 1024)).toFixed(1)} GB`;
}

function formatAge(sec: number | undefined): string {
  if (sec == null) return "";
  if (sec < 60) return `${sec}s ago`;
  if (sec < 3600) return `${Math.round(sec / 60)}m ago`;
  return `${(sec / 3600).toFixed(1)}h ago`;
}

function Row(props: { label: string; children: string }) {
  return (
    <div class="flex gap-3 text-xs">
      <dt class="w-28 shrink-0 text-muted">{props.label}</dt>
      <dd class="min-w-0 break-all text-fg">{props.children}</dd>
    </div>
  );
}

async function copyText(value: string): Promise<void> {
  await navigator.clipboard.writeText(value);
}

export const DownloadDetailPopup: Component<{
  dl: Download;
  onClose: () => void;
  onAction: (fn: () => Promise<void>) => void;
}> = (props) => {
  onMount(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") props.onClose();
    };
    document.addEventListener("keydown", onKey);
    onCleanup(() => document.removeEventListener("keydown", onKey));
  });

  const isUsenet = () => props.dl.protocol === "usenet";
  const isTorrent = () => props.dl.protocol === "torrent";
  const title = () => props.dl.catalogTitle || props.dl.filename || props.dl.gid;
  const subtitle = () =>
    props.dl.catalogTitle && props.dl.filename && props.dl.catalogTitle !== props.dl.filename
      ? props.dl.filename
      : "";
  const usenet = () => props.dl.usenet;
  const torrent = () => props.dl.torrent;
  const isActive = () =>
    props.dl.status === "active" ||
    props.dl.phase === "precheck" ||
    props.dl.phase === "waiting" ||
    props.dl.phase === "downloading" ||
    props.dl.phase === "repairing" ||
    props.dl.phase === "unpacking";
  const isPaused = () => props.dl.status === "paused";

  return (
    <div
      class="fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4"
      onClick={props.onClose}
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-label="Download details"
        class="max-h-[85vh] w-full max-w-2xl overflow-y-auto rounded-xl border border-border bg-surface p-5 shadow-2xl"
        onClick={(e) => e.stopPropagation()}
      >
        <div class="mb-3 flex items-start justify-between gap-3">
          <div class="min-w-0">
            <h3 class="truncate text-base font-semibold text-fg">{title()}</h3>
            <Show when={subtitle()}>
              <p class="mt-0.5 truncate text-xs text-muted">{subtitle()}</p>
            </Show>
          </div>
          <Button onClick={props.onClose}>Close</Button>
        </div>

        <dl class="flex flex-col gap-1.5">
          <Show when={props.dl.indexer}>
            <Row label="Indexer">{props.dl.indexer ?? ""}</Row>
          </Show>
          <Show when={isUsenet() && usenet()?.stagingPath}>
            <Row label="Staging">{`${usenet()?.stagingPath ?? ""} (${formatSize(usenet()?.bytesOnDisk ?? 0)})`}</Row>
          </Show>
          <Show when={isTorrent() && torrent()?.savePath}>
            <Row label="Save path">{`${torrent()?.savePath ?? ""} (${formatSize(torrent()?.bytesOnDisk ?? 0)})`}</Row>
          </Show>
        </dl>

        <Show when={isUsenet()}>
          <UsenetDetails dl={props.dl} />
        </Show>
        <Show when={isTorrent()}>
          <TorrentDetails
            dl={props.dl}
            onAction={props.onAction}
          />
        </Show>

        <div class="mt-4 flex flex-wrap gap-2">
          <Show when={isActive()}>
            <Button onClick={() => props.onAction(() => pauseDownload(props.dl.gid))}>
              Pause
            </Button>
          </Show>
          <Show when={isPaused()}>
            <Button onClick={() => props.onAction(() => resumeDownload(props.dl.gid))}>
              Resume
            </Button>
          </Show>
          <Show when={isTorrent()}>
            <Button onClick={() => props.onAction(() => reannounceDownload(props.dl.gid))}>
              Force reannounce
            </Button>
            <Button onClick={() => props.onAction(() => recheckDownload(props.dl.gid))}>
              Recheck
            </Button>
          </Show>
        </div>
      </div>
    </div>
  );
};

const UsenetDetails: Component<{ dl: Download }> = (props) => {
  const u = () => props.dl.usenet;
  return (
    <div class="mt-4 flex flex-col gap-2">
      <h4 class="text-xs font-semibold uppercase tracking-wide text-muted">
        Usenet
      </h4>
      <dl class="flex flex-col gap-1.5">
        <Show when={(u()?.segmentTotal ?? 0) > 0}>
          <Row label="Segments">{`${u()?.segmentDone ?? 0} / ${u()?.segmentTotal}`}</Row>
        </Show>
        <Show when={u()?.currentFile}>
          <Row label="Current file">
            {`${u()?.currentFile ?? ""}${(u()?.currentSegTotal ?? 0) > 0 ? ` ${u()?.currentSeg}/${u()?.currentSegTotal}` : ""}`}
          </Row>
        </Show>
        <Show when={(u()?.statTotal ?? 0) > 0}>
          <Row label="STAT">{`${u()?.statDone ?? 0} / ${u()?.statTotal}`}</Row>
        </Show>
        <Show when={u()?.waitReason}>
          <Row label="Waiting">{u()?.waitReason ?? ""}</Row>
        </Show>
        <Show when={u()?.repairFile}>
          <Row label="PAR2">{u()?.repairFile ?? ""}</Row>
        </Show>
        <Show when={u()?.sidecarAgeSec != null}>
          <Row label="Sidecar">{formatAge(u()?.sidecarAgeSec)}</Row>
        </Show>
        <Show when={(u()?.maxConns ?? 0) > 0}>
          <Row label="Connections">{`${u()?.activeConns ?? 0} / ${u()?.maxConns}`}</Row>
        </Show>
        <Show when={props.dl.errorMessage}>
          <Row label="Error">{props.dl.errorMessage}</Row>
        </Show>
        <Show when={u()?.failingSegment}>
          <Row label="Failing">{u()?.failingSegment ?? ""}</Row>
        </Show>
      </dl>
      <Show when={(u()?.triedReleases?.length ?? 0) > 0}>
        <ul class="mt-1 text-xs text-fg">
          <For each={u()?.triedReleases}>
            {(item) => (
              <li>
                {item.label}
                <Show when={item.reason}>
                  <span class="text-muted"> — {item.reason}</span>
                </Show>
              </li>
            )}
          </For>
        </ul>
      </Show>
    </div>
  );
};

const TorrentDetails: Component<{
  dl: Download;
  onAction: (fn: () => Promise<void>) => void;
}> = (props) => {
  const t = () => props.dl.torrent;
  const [copied, setCopied] = createSignal("");

  const copy = (kind: string, value: string) => {
    void copyText(value).then(() => {
      setCopied(kind);
      setTimeout(() => setCopied(""), 1500);
    });
  };

  return (
    <div class="mt-4 flex flex-col gap-3">
      <h4 class="text-xs font-semibold uppercase tracking-wide text-muted">
        Torrent
      </h4>
      <dl class="flex flex-col gap-1.5">
        <Row label="Swarm">
          {`${props.dl.seedCount} seeders · ${t()?.peerCount ?? 0} peers · ${(((t()?.availability ?? 0) * 100).toFixed(0))}% available`}
        </Row>
        <Row label="Uploaded">
          {`${formatSize(t()?.uploaded ?? 0)} · ratio ${(t()?.ratio ?? 0).toFixed(2)}${(t()?.seedRatioGoal ?? 0) > 0 ? ` / ${t()?.seedRatioGoal}` : ""}${(t()?.seedTimeGoalSec ?? 0) > 0 ? ` · seed ${Math.round((t()?.seedTimeGoalSec ?? 0) / 60)}m` : ""}`}
        </Row>
        <Show when={(t()?.piecesTotal ?? 0) > 0}>
          <Row label="Pieces">{`${t()?.piecesHave ?? 0} / ${t()?.piecesTotal}`}</Row>
        </Show>
      </dl>
      <Show when={t()?.pieceHeatmap}>
        <div
          class="font-mono text-[10px] leading-none tracking-tight text-muted"
          aria-label="Piece heatmap"
        >
          {t()?.pieceHeatmap}
        </div>
      </Show>
      <div class="flex flex-wrap gap-2">
        <Show when={t()?.infoHash}>
          <Button
            class="!px-2 !py-1 !text-xs"
            onClick={() => copy("hash", t()?.infoHash ?? "")}
          >
            {copied() === "hash" ? "Copied hash" : "Copy infohash"}
          </Button>
        </Show>
        <Show when={t()?.magnet}>
          <Button
            class="!px-2 !py-1 !text-xs"
            onClick={() => copy("magnet", t()?.magnet ?? "")}
          >
            {copied() === "magnet" ? "Copied magnet" : "Copy magnet"}
          </Button>
        </Show>
      </div>
      <Show when={(t()?.trackers?.length ?? 0) > 0}>
        <ul class="text-xs text-fg">
          <For each={t()?.trackers}>
            {(tr) => (
              <li class="truncate">
                <span class="text-muted">{tr.status}</span> {tr.url}
              </li>
            )}
          </For>
        </ul>
      </Show>
      <Show when={(t()?.files?.length ?? 0) > 0}>
        <ul class="flex flex-col gap-1">
          <For each={t()?.files}>
            {(file) => (
              <TorrentFileRow
                gid={props.dl.gid}
                file={file}
                onAction={props.onAction}
              />
            )}
          </For>
        </ul>
      </Show>
    </div>
  );
};

const TorrentFileRow: Component<{
  gid: string;
  file: TorrentFileDetail;
  onAction: (fn: () => Promise<void>) => void;
}> = (props) => {
  const pct = () =>
    props.file.length > 0
      ? Math.round((props.file.completed / props.file.length) * 100)
      : 0;
  return (
    <li class="flex items-center gap-2 text-xs">
      <span class="min-w-0 flex-1 truncate text-fg" title={props.file.path}>
        {props.file.path}
      </span>
      <span class="shrink-0 text-muted">
        {pct()}% · {formatSize(props.file.length)}
      </span>
      <select
        class="rounded border border-border bg-bg px-1 py-0.5 text-xs text-fg"
        aria-label={`Priority for ${props.file.path}`}
        value={props.file.priority || "normal"}
        onChange={(e) => {
          const priority = e.currentTarget.value;
          props.onAction(() =>
            setDownloadFilePriority(props.gid, props.file.path, priority),
          );
        }}
      >
        <option value="skip">Skip</option>
        <option value="normal">Normal</option>
        <option value="high">High</option>
      </select>
    </li>
  );
};
