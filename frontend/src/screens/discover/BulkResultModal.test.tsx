// BulkResultModal tests (F3): the results view must render all three per-item
// states (grabbed / needs-a-pick / error), surface an orphan drop as
// "N selected, M submitted", and NEVER auto-dismiss (a silently closed results
// view is pre-mortem #1 — the operator believing everything grabbed).

import { afterEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@solidjs/testing-library";
import type {
  AutoGrabBatchItem,
  AutoGrabBatchResponse,
} from "@dto";
import { BulkResultModal } from "./BulkResultModal";

const items: AutoGrabBatchItem[] = [
  { mode: "movies", request: { title: "Grabbed Movie", tmdbId: 1 } },
  { mode: "movies", request: { title: "Fallback Movie", tmdbId: 2 } },
  { mode: "series", request: { title: "Error Show", tmdbId: 3, seasonNumber: 1, seasonSpecified: true } },
];

const response: AutoGrabBatchResponse = {
  results: [
    {
      index: 0,
      mode: "movies",
      label: "Grabbed Movie",
      grabbed: true,
      fallback: false,
      message: "Grabbed a 1080p release.",
    },
    {
      index: 1,
      mode: "movies",
      label: "Fallback Movie",
      grabbed: false,
      fallback: true,
      message: "Nothing cleared the quality floor — pick one:",
      candidates: [
        {
          title: "Fallback Movie 1080p WEB",
          indexer: "Indexer A",
          protocol: "torrent",
          downloadUrl: "magnet:?xt=fallback",
          size: 1,
          seeders: 5,
          status: "below-floor",
          score: 0.5,
          impliedMbps: 3,
          floorMbps: 5,
          qualified: false,
        },
      ],
    },
    {
      index: 2,
      mode: "series",
      label: "Error Show S1",
      grabbed: false,
      fallback: false,
      error: "prowlarr search failed: 502",
    },
  ],
};

afterEach(() => vi.restoreAllMocks());

describe("BulkResultModal", () => {
  it("renders all three per-item states and reports the orphan drop, without auto-dismissing", () => {
    const onClose = vi.fn();
    // selectedCount 4 vs 3 submitted → one orphan dropped before submit.
    render(() => (
      <BulkResultModal
        items={items}
        response={response}
        selectedCount={4}
        onClose={onClose}
      />
    ));

    // Header surfaces "N selected, M submitted" and the drop.
    expect(screen.getByText(/4 selected, 3 submitted/)).toBeInTheDocument();
    expect(screen.getByText(/1 dropped/)).toBeInTheDocument();

    // All three state badges render.
    expect(screen.getByText("✓ Grabbed")).toBeInTheDocument();
    expect(screen.getByText("Needs a pick")).toBeInTheDocument();
    expect(screen.getByText("✗ Error")).toBeInTheDocument();

    // The grabbed message, the error message, and the fallback pick list all show.
    expect(screen.getByText("Grabbed a 1080p release.")).toBeInTheDocument();
    expect(screen.getByText("prowlarr search failed: 502")).toBeInTheDocument();
    expect(screen.getByText("Fallback Movie 1080p WEB")).toBeInTheDocument();
    expect(screen.getByText("Grab this")).toBeInTheDocument();

    // It did NOT auto-dismiss.
    expect(onClose).not.toHaveBeenCalled();
  });

  it("groups errors and fallbacks before successes", () => {
    render(() => (
      <BulkResultModal
        items={items}
        response={response}
        selectedCount={3}
        onClose={() => {}}
      />
    ));
    const labels = screen
      .getAllByText(/✓ Grabbed|Needs a pick|✗ Error/)
      .map((el) => el.textContent);
    // Error first, then fallback, then the grabbed success last.
    expect(labels).toEqual(["✗ Error", "Needs a pick", "✓ Grabbed"]);
  });
});
