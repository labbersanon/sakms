// Activity log tests: compact grab lines, search, and the three review buckets.

import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@solidjs/testing-library";
import type { Grab } from "@dto";
import {
  Activity,
  activityReason,
  activityStamp,
  activityStatus,
  grabInBucket,
} from "./Activity";
import { jsonResponse } from "../testing/http";

const grab = (over: Partial<Grab>): Grab => ({
  id: 1,
  mode: "movies",
  title: "Some Movie",
  indexer: "IndexerA",
  protocol: "usenet",
  downloadClient: "nntp",
  status: "imported",
  rootFolderPath: "/movies",
  createdAt: "2026-10-12T08:13:00.000Z",
  updatedAt: "2026-10-12T08:13:00.000Z",
  ...over,
});

const stubGrabs = (byMode: Partial<Record<string, Grab[]>>) => {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      const m = url.match(/\/api\/modes\/([^/]+)\/grabs/);
      if (m) return jsonResponse(byMode[m[1] ?? ""] ?? []);
      throw new Error("unexpected fetch: " + url);
    }),
  );
};

afterEach(() => vi.unstubAllGlobals());

describe("activity helpers", () => {
  it("stamps UTC createdAt without converting", () => {
    expect(activityStamp("2026-10-12T08:13:00.000Z")).toBe("2026-10-12 08:13");
  });

  it("shortens pending_retry and completed, and names a held request", () => {
    expect(activityStatus(grab({ status: "pending_retry" }))).toBe("retry");
    expect(
      activityStatus(
        grab({ status: "pending_retry", holdUntil: "2026-12-01T00:00:00Z" }),
      ),
    ).toBe("held");
    expect(activityStatus(grab({ status: "completed" }))).toBe("done");
    expect(activityStatus(grab({ status: "failed" }))).toBe("failed");
  });

  it("truncates a reason at an em dash, not by length", () => {
    expect(
      activityReason(
        "the connection to the usenet server dropped — resuming shortly",
      ),
    ).toBe("the connection to the usenet server dropped");
    expect(activityReason("x".repeat(90))).toBe("x".repeat(90));
  });

  it("buckets downloaded / attempted / failed without overlap", () => {
    expect(grabInBucket("imported", "downloaded")).toBe(true);
    expect(grabInBucket("completed", "downloaded")).toBe(true);
    expect(grabInBucket("failed", "downloaded")).toBe(false);
    expect(grabInBucket("queued", "attempted")).toBe(true);
    expect(grabInBucket("downloading", "attempted")).toBe(true);
    expect(grabInBucket("pending_retry", "attempted")).toBe(true);
    expect(grabInBucket("failed", "attempted")).toBe(false);
    expect(grabInBucket("failed", "failed")).toBe(true);
    expect(grabInBucket("imported", "failed")).toBe(false);
    expect(grabInBucket("failed", "all")).toBe(true);
  });
});

describe("Activity screen", () => {
  it("lists a compact line per grab across modes, newest first", async () => {
    stubGrabs({
      movies: [
        grab({
          id: 1,
          title: "Old Movie",
          createdAt: "2026-10-11T01:00:00.000Z",
        }),
      ],
      series: [
        grab({
          id: 2,
          mode: "series",
          title: "New Show S01E01",
          status: "failed",
          retryReason: "article not found — try next",
          createdAt: "2026-10-12T09:00:00.000Z",
        }),
      ],
    });
    render(() => <Activity />);

    expect(await screen.findByText("New Show S01E01")).toBeInTheDocument();
    expect(screen.getByText("Old Movie")).toBeInTheDocument();
    expect(screen.getByText("failed")).toBeInTheDocument();
    expect(screen.getByText("article not found")).toBeInTheDocument();
    expect(screen.queryByText("article not found — try next")).toBeNull();
    const items = screen.getAllByRole("listitem");
    expect(items[0]!.textContent).toContain("New Show S01E01");
    expect(items[1]!.textContent).toContain("Old Movie");
    expect(items[0]!.textContent).not.toContain("series");
    expect(items[1]!.textContent).not.toContain("movies");
    const card = screen.getByRole("list").parentElement;
    expect(card?.className).toContain("bg-surface");
    expect(card?.className).toContain("overflow-auto");
  });

  it("search matches title, status, and reason", async () => {
    stubGrabs({
      movies: [
        grab({ id: 1, title: "Alpha", status: "imported" }),
        grab({
          id: 2,
          title: "Beta",
          status: "failed",
          retryReason: "password protected",
        }),
      ],
    });
    render(() => <Activity />);
    await screen.findByText("Alpha");

    fireEvent.input(screen.getByLabelText("Search"), {
      target: { value: "password" },
    });
    expect(screen.getByText("Beta")).toBeInTheDocument();
    expect(screen.queryByText("Alpha")).toBeNull();
  });

  it("Downloaded / Attempted / Failed chips hide the other buckets", async () => {
    stubGrabs({
      movies: [
        grab({ id: 1, title: "Landed", status: "imported" }),
        grab({ id: 2, title: "Trying", status: "downloading" }),
        grab({ id: 3, title: "Dead", status: "failed" }),
      ],
    });
    render(() => <Activity />);
    await screen.findByText("Landed");

    fireEvent.click(screen.getByRole("button", { name: "Downloaded" }));
    expect(screen.getByText("Landed")).toBeInTheDocument();
    expect(screen.queryByText("Trying")).toBeNull();
    expect(screen.queryByText("Dead")).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "Attempted" }));
    expect(screen.getByText("Trying")).toBeInTheDocument();
    expect(screen.queryByText("Landed")).toBeNull();
    expect(screen.queryByText("Dead")).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "Failed" }));
    expect(screen.getByText("Dead")).toBeInTheDocument();
    expect(screen.queryByText("Landed")).toBeNull();
    expect(screen.queryByText("Trying")).toBeNull();
  });

  it("renders the empty copy when nothing matches", async () => {
    stubGrabs({ movies: [] });
    render(() => <Activity />);
    const empty = await screen.findByText("No matching activity.");
    expect(empty).toBeInTheDocument();
    expect(empty.parentElement?.className).toContain("bg-surface");
  });
});
