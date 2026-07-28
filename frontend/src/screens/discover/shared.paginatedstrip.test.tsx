// PaginatedStrip sparse-page auto-advance (extends DE-2, 2026-07-27) — direct
// unit tests of the pagination engine in isolation, so a custom `perPage` can
// be driven (the merged Discover rows all use the default 20, which can't make
// a "gained >= target within the cap budget" case constructible). These prove
// the three auto-advance STOP conditions independently — cap hit, hasMore
// false, and gained >= perPage — plus the load-bearing per-operation counter
// reset: each fresh "Show more" click gets the full auto-advance budget again,
// so a sparse row keeps delivering ~a page's worth per click instead of
// regressing to one item per click after the first chain caps out.

import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@solidjs/testing-library";
import { PaginatedStrip } from "./shared";

afterEach(() => vi.restoreAllMocks());

type Row = { id: number };

// renderStrip mounts a PaginatedStrip whose loader returns one page per call,
// shaped by pageShape(page). Returns the fetch spy (call count == pages
// fetched) so tests can assert the exact auto-advance budget spent.
function renderStrip(
  pageShape: (page: number) => { items: Row[]; hasMore: boolean },
  perPage?: number,
) {
  const load = vi.fn(async (page: number) => pageShape(page));
  render(() => (
    <PaginatedStrip<Row>
      title="Test Strip"
      reloadToken={() => 0}
      load={load}
      onError={() => {}}
      perPage={perPage}
    >
      {(item) => <div>Row {item.id}</div>}
    </PaginatedStrip>
  ));
  return load;
}

describe("PaginatedStrip auto-advance stop conditions", () => {
  it("stops once gained >= perPage, BEFORE hitting the cap", async () => {
    // perPage=2, one item per page: page 1 -> gained 1 < 2, advance; page 2 ->
    // gained 2 >= 2, stop. Two fetches, well inside the cap=3 budget, so it
    // stops for the RIGHT reason (target reached), not because the cap ran out.
    const load = renderStrip((page) => ({ items: [{ id: page }], hasMore: true }), 2);

    expect(await screen.findByText("Row 1")).toBeInTheDocument();
    expect(screen.getByText("Row 2")).toBeInTheDocument();
    expect(screen.queryByText("Row 3")).not.toBeInTheDocument();
    expect(load).toHaveBeenCalledTimes(2);
    // hasMore still true and not exhausted -> manual control remains.
    expect(screen.getByText("Show more")).toBeInTheDocument();
  });

  it("stops immediately when hasMore is false, even below the perPage target", async () => {
    // One item, hasMore=false: the row is genuinely exhausted, so no
    // auto-advance fires despite gained (1) < perPage (20). Exhausted also
    // hides the manual control.
    const load = renderStrip((page) => ({ items: [{ id: page }], hasMore: false }));

    expect(await screen.findByText("Row 1")).toBeInTheDocument();
    expect(load).toHaveBeenCalledTimes(1);
    expect(screen.queryByText("Show more")).not.toBeInTheDocument();
  });

  it("caps at 1 initial + 3 auto-advances when sparse pages never reach the target", async () => {
    // Default perPage=20, one item per page, hasMore always true: gained never
    // reaches 20 within the budget, so the cap (3 extra fetches) is what stops
    // it -- 4 fetches, 4 rows, and the manual control stays reachable.
    const load = renderStrip((page) => ({ items: [{ id: page }], hasMore: true }));

    expect(await screen.findByText("Row 4")).toBeInTheDocument();
    expect(load).toHaveBeenCalledTimes(4);
    expect(screen.getByText("Show more")).toBeInTheDocument();
  });

  it("re-engages the FULL auto-advance budget on each Show more click (per-operation reset)", async () => {
    // The discriminator between per-operation reset (correct) and a lifetime
    // cap (the bug this fix removes): after the initial chain caps at 4
    // fetches, clicking "Show more" must spend a fresh 4-fetch budget (pages
    // 5-8), NOT a single fetch. A lifetime cap would leave the counter pinned
    // at 3 and yield exactly one new row per click -- the original sparse bug.
    const load = renderStrip((page) => ({ items: [{ id: page }], hasMore: true }));

    expect(await screen.findByText("Row 4")).toBeInTheDocument();
    expect(load).toHaveBeenCalledTimes(4);

    fireEvent.click(screen.getByText("Show more"));

    // Full budget again: page 8 is only reachable if the second operation also
    // did 1 initial + 3 auto-advances.
    expect(await screen.findByText("Row 8")).toBeInTheDocument();
    expect(load).toHaveBeenCalledTimes(8);
  });
});
