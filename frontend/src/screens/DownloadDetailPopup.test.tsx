import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@solidjs/testing-library";
import type { Download } from "@dto";
import {
  DownloadDetailPopup,
  usenetStageIndex,
} from "./DownloadDetailPopup";
import { jsonResponse, noContent } from "../testing/http";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

const usenetDl = (over: Partial<Download> = {}): Download => ({
  gid: "nzb-1",
  status: "active",
  filename: "Show.S01E01.1080p.mkv",
  totalLength: 1000,
  completedLength: 400,
  downloadSpeed: 100,
  seedCount: 0,
  uploadSpeed: 0,
  protocol: "usenet",
  errorMessage: "segment 12: 430",
  catalogTitle: "Show S01E01",
  indexer: "NZBGeek",
  usenet: {
    segmentDone: 59,
    segmentTotal: 79,
    currentFile: "part028.rar",
    currentSeg: 59,
    currentSegTotal: 79,
    statDone: 0,
    statTotal: 0,
    waitReason: "",
    repairFile: "set.vol01.par2",
    sidecarAgeSec: 12,
    activeConns: 8,
    maxConns: 50,
    stagingPath: "/data/nzb-1",
    bytesOnDisk: 4096,
    failingSegment: "segment 12: 430",
    triedReleases: [{ label: "1 prior NZB(s) excluded this episode", reason: "articles missing" }],
  },
  ...over,
});

const torrentDl = (over: Partial<Download> = {}): Download => ({
  gid: "g1",
  status: "active",
  filename: "Movie.1080p.mkv",
  totalLength: 1000,
  completedLength: 400,
  downloadSpeed: 100,
  seedCount: 4,
  uploadSpeed: 20,
  protocol: "torrent",
  errorMessage: "",
  catalogTitle: "The Movie",
  torrent: {
    peerCount: 6,
    availability: 0.8,
    uploaded: 200,
    ratio: 0.2,
    seedRatioGoal: 1,
    seedTimeGoalSec: 3600,
    infoHash: "abc123",
    magnet: "magnet:?xt=urn:btih:abc123",
    piecesHave: 10,
    piecesTotal: 20,
    pieceHeatmap: "990000",
    files: [
      { path: "Movie.mkv", length: 800, completed: 400, priority: "normal" },
    ],
    trackers: [{ url: "udp://tracker.example/announce", status: "working" }],
    savePath: "/data/g1",
    bytesOnDisk: 400,
  },
  ...over,
});

describe("DownloadDetailPopup — usenet", () => {
  it("shows catalog title, NZB name, segments, indexer, and alternate parks", () => {
    render(() => (
      <DownloadDetailPopup dl={usenetDl()} onClose={() => {}} onAction={() => {}} />
    ));
    expect(screen.getByRole("dialog", { name: "Download details" })).toBeInTheDocument();
    expect(screen.getByText("Show S01E01")).toBeInTheDocument();
    expect(screen.getByText("Show.S01E01.1080p.mkv")).toBeInTheDocument();
    expect(screen.getByText("NZBGeek")).toBeInTheDocument();
    expect(screen.getByText("59 / 79")).toBeInTheDocument();
    expect(screen.getByText(/part028.rar 59\/79/)).toBeInTheDocument();
    expect(screen.getByText(/1 prior NZB/)).toBeInTheDocument();
    expect(screen.getByLabelText("Usenet stages")).toBeInTheDocument();
    expect(screen.getByText("Downloading")).toBeInTheDocument();
    expect(screen.queryByText("Force reannounce")).toBeNull();
  });

  it("marks Repairing as the current Usenet stage", () => {
    expect(
      usenetStageIndex(usenetDl({ phase: "repairing" })),
    ).toBe(3);
    render(() => (
      <DownloadDetailPopup
        dl={usenetDl({ phase: "repairing" })}
        onClose={() => {}}
        onAction={() => {}}
      />
    ));
    expect(screen.getByText("Repairing").closest("li")?.querySelector("[aria-current='step']")).toBeTruthy();
  });
});

describe("DownloadDetailPopup — torrent", () => {
  it("shows swarm, files, copy buttons, and torrent actions", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.assign(navigator, { clipboard: { writeText } });

    const calls: string[] = [];
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
      calls.push(String(input));
      return noContent();
    });

    render(() => (
      <DownloadDetailPopup
        dl={torrentDl()}
        onClose={() => {}}
        onAction={(fn) => {
          void fn();
        }}
      />
    ));
    expect(screen.getByText("The Movie")).toBeInTheDocument();
    expect(screen.getByText(/4 seeders/)).toBeInTheDocument();
    expect(screen.getByText("Movie.mkv")).toBeInTheDocument();
    expect(screen.getByText("udp://tracker.example/announce")).toBeInTheDocument();

    fireEvent.click(screen.getByText("Copy infohash"));
    expect(writeText).toHaveBeenCalledWith("abc123");

    fireEvent.click(screen.getByText("Force reannounce"));
    fireEvent.click(screen.getByText("Recheck"));
    expect(calls.some((u) => u.includes("/reannounce"))).toBe(true);
    expect(calls.some((u) => u.includes("/recheck"))).toBe(true);
  });

  it("changes file priority", () => {
    const calls: { url: string; body: unknown }[] = [];
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
      calls.push({
        url: String(input),
        body: init?.body ? JSON.parse(init.body as string) : undefined,
      });
      return jsonResponse({});
    });

    render(() => (
      <DownloadDetailPopup
        dl={torrentDl()}
        onClose={() => {}}
        onAction={(fn) => {
          void fn();
        }}
      />
    ));
    fireEvent.change(screen.getByLabelText("Priority for Movie.mkv"), {
      target: { value: "skip" },
    });
    expect(calls.some((c) => c.url.includes("/files") && (c.body as { priority: string }).priority === "skip")).toBe(true);
  });
});
