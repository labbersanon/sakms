// FE-4 — a successful unlock refetches SectionLockContext and the overlay
// clears.
//
// The harness deliberately uses AppShell's OWN createSectionLockControl()
// rather than a hand-built stub control: the whole thing being tested is that
// isLocked()'s three-way conjunction (in lockedSections AND not unlocked AND
// enforcement available) flips the moment the refetched status reports a live
// ticket. A stub control would test the harness, not the shipped code.

import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { Show } from "solid-js";
import {
  SectionLockContext,
  SectionLockOverlay,
  useSectionLock,
} from "./ui";
import { createSectionLockControl } from "../screens/AppShell";

afterEach(() => vi.unstubAllGlobals());

const status = (over: Record<string, unknown> = {}) =>
  new Response(
    JSON.stringify({
      pinSet: true,
      lockedSections: ["discover"],
      unlocked: false,
      enforcementAvailable: true,
      ...over,
    }),
    { status: 200, headers: { "Content-Type": "application/json" } },
  );

// Gated mirrors the shell's own content-area structure: the screen renders,
// OR the overlay does, never both.
const Gated = () => {
  const lock = useSectionLock();
  return (
    <Show
      when={lock.isLocked("discover")}
      fallback={<div>DISCOVER CONTENT</div>}
    >
      <SectionLockOverlay label="Discover" />
    </Show>
  );
};

const Harness = () => (
  <SectionLockContext.Provider value={createSectionLockControl()}>
    <Gated />
  </SectionLockContext.Provider>
);

describe("FE-4 — unlock refetches the context and the overlay clears", () => {
  it("PIN entry unlocks, the status is refetched, and the screen renders", async () => {
    let unlocked = false;
    const fetchMock = vi.fn(
      async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        if (url === "/api/section-lock/unlock" && init?.method === "POST") {
          unlocked = true;
          return new Response(null, { status: 204 });
        }
        if (url === "/api/section-lock/status") return status({ unlocked });
        throw new Error("unexpected fetch: " + url);
      },
    );
    vi.stubGlobal("fetch", fetchMock);

    render(() => <Harness />);

    // Locked first: the overlay is up and the screen is NOT mounted.
    expect(await screen.findByText("Discover is locked")).toBeInTheDocument();
    expect(screen.queryByText("DISCOVER CONTENT")).toBeNull();

    fireEvent.input(screen.getByLabelText("Section PIN"), {
      target: { value: "hunter2" },
    });
    fireEvent.click(screen.getByText("Unlock"));

    // The PIN went to the unlock route...
    await waitFor(() =>
      expect(
        fetchMock.mock.calls.some(
          ([u]) => String(u) === "/api/section-lock/unlock",
        ),
      ).toBe(true),
    );
    const unlockCall = fetchMock.mock.calls.find(
      ([u]) => String(u) === "/api/section-lock/unlock",
    )!;
    expect(JSON.parse(String(unlockCall[1]?.body)).pin).toBe("hunter2");

    // ...and the refetched status clears the overlay.
    expect(await screen.findByText("DISCOVER CONTENT")).toBeInTheDocument();
    expect(screen.queryByText("Discover is locked")).toBeNull();
    // The status route was hit at least twice: boot, then the post-unlock
    // refetch. That refetch IS the mechanism under test.
    expect(
      fetchMock.mock.calls.filter(
        ([u]) => String(u) === "/api/section-lock/status",
      ).length,
    ).toBeGreaterThanOrEqual(2);
  });

  it("a wrong PIN says INCORRECT PIN, not the server's 'section locked'", async () => {
    // The backend answers a wrong PIN with the SAME 403 section_locked body
    // the path gate writes, so surfacing err.message verbatim would tell an
    // operator who mistyped their PIN that the section is locked — which is
    // the screen they are already looking at. Nothing on the frontend pins
    // this string except this test, because the string comes from the server.
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) => {
        const url = String(input);
        if (url === "/api/section-lock/unlock")
          return new Response(
            JSON.stringify({
              error: "section locked",
              code: "section_locked",
              section: "",
            }),
            { status: 403, headers: { "Content-Type": "application/json" } },
          );
        if (url === "/api/section-lock/status") return status();
        throw new Error("unexpected fetch: " + url);
      }),
    );

    render(() => <Harness />);
    await screen.findByText("Discover is locked");
    fireEvent.input(screen.getByLabelText("Section PIN"), {
      target: { value: "wrong1" },
    });
    fireEvent.click(screen.getByText("Unlock"));

    expect(await screen.findByText("Incorrect PIN.")).toBeInTheDocument();
    // The overlay stays up, and does not claim anything about section state.
    expect(screen.getByText("Discover is locked")).toBeInTheDocument();
  });

  it("a brute-force lockout says WAIT, it does not just say wrong PIN", async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url === "/api/section-lock/unlock")
        return new Response(
          JSON.stringify({
            error: "too many failed PIN attempts",
            code: "section_lock_lockout",
            retryAfter: 60,
          }),
          { status: 429, headers: { "Content-Type": "application/json" } },
        );
      if (url === "/api/section-lock/status") return status();
      throw new Error("unexpected fetch: " + url);
    });
    vi.stubGlobal("fetch", fetchMock);

    render(() => <Harness />);
    await screen.findByText("Discover is locked");
    fireEvent.input(screen.getByLabelText("Section PIN"), {
      target: { value: "wrong1" },
    });
    fireEvent.click(screen.getByText("Unlock"));

    expect(
      await screen.findByText(/try again in 60 seconds/i),
    ).toBeInTheDocument();
  });

  it("nothing is locked while enforcement is unavailable, whatever is stored", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => status({ enforcementAvailable: false })),
    );
    render(() => <Harness />);
    // The server still reports lockedSections: ["discover"] here — but it
    // gates nothing on this instance, so an overlay would hide content the
    // backend serves happily.
    expect(await screen.findByText("DISCOVER CONTENT")).toBeInTheDocument();
  });
});
