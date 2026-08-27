// FolderPicker tests — the as-you-type folder browser used by the Settings
// root-folder / kids-path fields. Covers the debounced fetch (a keystroke does
// not fire a request until the debounce elapses), the dropdown render from a
// mocked BrowseResponse, click-to-fill (a suggestion sets the value to its full
// path), the roots-on-focus-when-empty shortcut, and graceful handling of an
// empty-entries response (no crash, no error surfaced). fetch is stubbed
// globally the same way Settings.test.tsx does it.

import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { FolderPicker } from "./FolderPicker";
import { AdultModeContext } from "./ui";
import { jsonResponse } from "../testing/http";


type BrowseBody = { path: string; entries: { name: string; path: string }[] };

// stub records every fetched URL and answers /api/browse with the supplied
// response (or a per-URL function of the URL). Everything else 204s.
function stub(browse: BrowseBody | ((url: string) => BrowseBody)) {
  const urls: string[] = [];
  const fn = vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    urls.push(url);
    if (url.includes("/api/browse"))
      return jsonResponse(typeof browse === "function" ? browse(url) : browse);
    return new Response(null, { status: 204 });
  });
  vi.stubGlobal("fetch", fn);
  return urls;
}

// Harness owns the path signal exactly as the Library sections do, so the
// picker is exercised as its real callers wire it.
function Harness(props: { initial?: string }) {
  const [val, setVal] = createSignal(props.initial ?? "");
  return (
    <FolderPicker value={val} onChange={setVal} ariaLabel="Folder" />
  );
}

const roots: BrowseBody = {
  path: "",
  entries: [
    { name: "/media", path: "/media" },
    { name: "/downloads", path: "/downloads" },
    { name: "/adult", path: "/adult" },
  ],
};

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("FolderPicker", () => {
  it("debounces the as-you-type fetch — no request until the debounce elapses", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const urls = stub({ path: "/med", entries: [] });
    render(() => <Harness />);
    const input = screen.getByLabelText("Folder");
    fireEvent.input(input, { target: { value: "/med" } });
    // Immediately after typing, nothing has been fetched yet.
    expect(urls.some((u) => u.includes("/api/browse"))).toBe(false);
    await vi.advanceTimersByTimeAsync(300);
    // After the debounce, exactly the typed path is fetched (URL-encoded).
    expect(
      urls.some((u) => u.includes("/api/browse?path=%2Fmed")),
    ).toBe(true);
    vi.useRealTimers();
  });

  it("renders a dropdown of suggestions from the BrowseResponse", async () => {
    stub(roots);
    render(() => <Harness />);
    fireEvent.focus(screen.getByLabelText("Folder"));
    // Focus with an empty value lists the roots (one clickable button each).
    expect(
      await screen.findByRole("button", { name: /\/media/ }),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /\/downloads/ })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /\/adult/ })).toBeInTheDocument();
  });

  it("lists the configured roots on focus when the value is empty", async () => {
    const urls = stub(roots);
    render(() => <Harness />);
    fireEvent.focus(screen.getByLabelText("Folder"));
    await waitFor(() =>
      expect(
        urls.some((u) => u.includes("/api/browse?path=")),
      ).toBe(true),
    );
    // The empty-path fetch carries no path value.
    expect(urls.some((u) => u.endsWith("/api/browse?path="))).toBe(true);
    expect(
      await screen.findByRole("button", { name: /\/media/ }),
    ).toBeInTheDocument();
  });

  it("clicking a suggestion fills the input with that entry's full path", async () => {
    stub(roots);
    render(() => <Harness />);
    const input = screen.getByLabelText("Folder") as HTMLInputElement;
    fireEvent.focus(input);
    fireEvent.click(await screen.findByRole("button", { name: /\/downloads/ }));
    expect(input.value).toBe("/downloads");
    // The dropdown closes after a pick.
    await waitFor(() =>
      expect(screen.queryByRole("button", { name: /\/media/ })).toBeNull(),
    );
  });

  it("handles an empty-entries response gracefully — no dropdown, no error", async () => {
    const urls = stub({ path: "/nope", entries: [] });
    render(() => <Harness initial="/nope" />);
    const input = screen.getByLabelText("Folder");
    // Non-empty value on focus won't fetch, so type to trigger the debounce.
    fireEvent.input(input, { target: { value: "/nope/deeper" } });
    // Wait for the debounced fetch to fire, then confirm nothing rendered and
    // no error text leaked into the DOM.
    await waitFor(() =>
      expect(urls.some((u) => u.includes("/api/browse"))).toBe(true),
    );
    expect(screen.queryByRole("listitem")).toBeNull();
    expect(screen.queryByText(/error/i)).toBeNull();
  });

  it("hides /adult from suggestions when Adult mode is off", async () => {
    stub(roots);
    render(() => (
      <AdultModeContext.Provider
        value={{ enabled: () => false, refetch: () => {} }}
      >
        <Harness />
      </AdultModeContext.Provider>
    ));
    fireEvent.focus(screen.getByLabelText("Folder"));
    expect(
      await screen.findByRole("button", { name: /\/media/ }),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /\/downloads/ })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /\/adult/ })).toBeNull();
  });
});
