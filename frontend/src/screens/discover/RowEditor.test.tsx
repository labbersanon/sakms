// RowEditor tests — the `kind` discriminator drives the per-row controls:
// "entity" rows (sliders, adult newest rows, rss feeds) get an Enabled toggle +
// Delete; "structural" rows (built-ins, Studios/Performers, stash-box) get a
// Show/Hide toggle and NO Delete. Reordering is drag-and-drop (via
// @thisbeyond/solid-dnd); a real pointer drag isn't simulable in jsdom, so the
// reorder *logic* is tested directly against the pure reorderKeys() function and
// the callback wiring is asserted at the component level (a drag handle renders
// per row, onReorder is a prop). RowEditor itself is presentational — the caller
// (Mainstream/Adult) owns persistence.

import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, within } from "@solidjs/testing-library";
import { RowEditor, reorderKeys, type RowDescriptor } from "./RowEditor";

const rows: RowDescriptor[] = [
  { key: "trending-movies", label: "Trending Movies", kind: "structural", hidden: false },
  { key: "studios", label: "Studios", kind: "structural", hidden: true },
  { key: "slider:4", label: "Heist Movies", kind: "entity", enabled: true },
  { key: "rssfeed:2", label: "NZBGeek Saved Search", kind: "entity", enabled: false },
];

const renderEditor = (overrides: Partial<Parameters<typeof RowEditor>[0]> = {}) =>
  render(() => (
    <RowEditor
      rows={rows}
      onReorder={overrides.onReorder ?? vi.fn()}
      onToggleEnabled={overrides.onToggleEnabled ?? vi.fn()}
      onToggleHidden={overrides.onToggleHidden ?? vi.fn()}
      onDelete={overrides.onDelete ?? vi.fn()}
    />
  ));

describe("reorderKeys", () => {
  const keys = ["a", "b", "c", "d"];

  it("moves an earlier key down to occupy a later key's slot", () => {
    expect(reorderKeys(keys, "a", "c")).toEqual(["b", "c", "a", "d"]);
  });

  it("moves a later key up to occupy an earlier key's slot", () => {
    expect(reorderKeys(keys, "d", "b")).toEqual(["a", "d", "b", "c"]);
  });

  it("is a no-op when the source and target are the same key", () => {
    expect(reorderKeys(keys, "b", "b")).toEqual(keys);
  });

  it("is a no-op when either key is not present", () => {
    expect(reorderKeys(keys, "a", "z")).toEqual(keys);
    expect(reorderKeys(keys, "z", "a")).toEqual(keys);
  });
});

describe("RowEditor — per-kind controls", () => {
  it("renders a drag handle for every row and no up/down buttons", () => {
    renderEditor();
    expect(screen.getAllByLabelText(/^Drag /)).toHaveLength(rows.length);
    expect(screen.queryByLabelText(/Move .* up/)).not.toBeInTheDocument();
    expect(screen.queryByLabelText(/Move .* down/)).not.toBeInTheDocument();
  });

  it("entity rows render an Enabled toggle + Delete and fire their callbacks", () => {
    const onToggleEnabled = vi.fn();
    const onDelete = vi.fn();
    renderEditor({ onToggleEnabled, onDelete });

    const sliderRow = screen.getByText("Heist Movies").closest("li") as HTMLElement;
    const enabled = within(sliderRow).getByLabelText(
      "Heist Movies enabled",
    ) as HTMLInputElement;
    expect(enabled.checked).toBe(true);
    fireEvent.click(enabled);
    expect(onToggleEnabled).toHaveBeenCalledWith(rows[2]);

    fireEvent.click(within(sliderRow).getByText("Delete"));
    expect(onDelete).toHaveBeenCalledWith(rows[2]);
  });

  it("a disabled entity row reflects its unchecked Enabled state", () => {
    renderEditor();
    const feedRow = screen
      .getByText("NZBGeek Saved Search")
      .closest("li") as HTMLElement;
    const enabled = within(feedRow).getByLabelText(
      "NZBGeek Saved Search enabled",
    ) as HTMLInputElement;
    expect(enabled.checked).toBe(false);
  });

  it("structural rows render a Show/Hide toggle, no Delete, and fire onToggleHidden", () => {
    const onToggleHidden = vi.fn();
    renderEditor({ onToggleHidden });

    const builtin = screen
      .getByText("Trending Movies")
      .closest("li") as HTMLElement;
    // No Delete and no Enabled toggle on a structural row.
    expect(within(builtin).queryByText("Delete")).not.toBeInTheDocument();
    expect(
      within(builtin).queryByLabelText("Trending Movies enabled"),
    ).not.toBeInTheDocument();

    const visible = within(builtin).getByLabelText(
      "Trending Movies visible",
    ) as HTMLInputElement;
    // hidden: false → visible checkbox checked.
    expect(visible.checked).toBe(true);
    fireEvent.click(visible);
    expect(onToggleHidden).toHaveBeenCalledWith(rows[0]);
  });

  it("a hidden structural row still renders with its visible toggle unchecked", () => {
    renderEditor();
    const studios = screen.getByText("Studios").closest("li") as HTMLElement;
    const visible = within(studios).getByLabelText(
      "Studios visible",
    ) as HTMLInputElement;
    // hidden: true → visible checkbox unchecked, but the row is still present
    // (Edit mode is never a dead end).
    expect(visible.checked).toBe(false);
    expect(within(studios).queryByText("Delete")).not.toBeInTheDocument();
  });
});
