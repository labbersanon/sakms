// AdultDiscover monitor feature tests — five guarantees:
//
//   1. Resolved performer: Monitor switch enabled; toggling fires PUT and
//      flips optimistically.
//   2. Unresolved performer: switch disabled + reason copy visible.
//   3. Switch visible even with no catalog bio (banner always renders).
//   4. Drilling A then B doesn't leak A's monitor state while B loads
//      (loading gate: switch is disabled during the in-flight fetch).
//   5. MonitoredRow hides itself when the monitored/resolve endpoint returns
//      an empty array; renders "Monitored" when it returns items.
//
// Conventions mirror Adult.grab.test.tsx / Adult.newestdrill.test.tsx:
// stubFetch + calls array + a defaults answerer for mount fetches.

import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { AdultDiscover } from "./Adult";
import { jsonResponse, noContent } from "../../testing/http";


type Call = { url: string; method: string; body: unknown };
type Override = (
  url: string,
  init?: RequestInit,
) => Response | undefined | Promise<Response | undefined>;

const stubFetch = (override?: Override) => {
  const calls: Call[] = [];
  const fn = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = (init?.method ?? "GET").toUpperCase();
    calls.push({
      url,
      method,
      body: init?.body ? JSON.parse(init.body as string) : undefined,
    });
    if (override) {
      const r = await override(url, init);
      if (r) return r;
    }
    throw new Error(`unexpected fetch: ${method} ${url}`);
  });
  vi.stubGlobal("fetch", fn);
  return calls;
};

// defaults answers AdultDiscover's mount fetches with empties so each test
// only special-cases what it asserts on. Monitored resolve returns empty by
// default (so MonitoredRow self-hides and doesn't interfere with other tests).
const defaults = (url: string): Response | null => {
  if (url.includes("/api/discover/rss-feeds")) return jsonResponse([]);
  if (url.includes("/newest-rows/monitored/resolve")) return jsonResponse([]);
  if (url.includes("/api/modes/adult/newest-rows")) return jsonResponse([]);
  return null;
};

afterEach(() => vi.unstubAllGlobals());

// onePerformerRow returns an override that wires a single performer row with
// one card and answers the monitor state GET. The card's drill opens via
// EntityCard's onSelect — clicking the card button.
const onePerformerRow =
  (monitorState: object): Override =>
  (url) => {
    if (url.includes("/discover/monitor"))
      return jsonResponse(monitorState);
    if (url.includes("/api/modes/adult/description")) return jsonResponse({ text: "", source: "" });
    if (url.includes("/newest-rows/performer-genders"))
      return jsonResponse(["female"]);
    if (url.includes("/newest-rows/1/resolve"))
      return jsonResponse([
        {
          id: "p1",
          title: "Aria Fox",
          studio: "",
          date: "",
          image: "https://cdn.theporndb.net/performers/aria.jpg",
          source: "",
          rowType: "performer",
        },
      ]);
    if (url.includes("/api/modes/adult/newest-rows"))
      return jsonResponse([
        {
          id: 1,
          title: "Performers",
          rowType: "performer",
          sortOrder: 0,
          enabled: true,
          createdAt: "2026-01-01T00:00:00Z",
          updatedAt: "2026-01-01T00:00:00Z",
        },
      ]);
    // entity-scenes for drill
    if (url.includes("/discover/newest/entity-scenes"))
      return jsonResponse({ items: [], hasMore: false });
    return defaults(url) ?? undefined;
  };

describe("AdultDiscover — monitor switch: resolved performer", () => {
  it("renders an enabled Monitor switch and fires PUT on toggle", async () => {
    const resolvedState = {
      resolved: true,
      source: "tpdb",
      entityId: "aria-fox",
      entityName: "Aria Fox",
      monitored: false,
      reason: "",
    };
    const calls = stubFetch(onePerformerRow(resolvedState));

    render(() => <AdultDiscover />);

    // Click the performer card to drill in.
    const card = await screen.findByText("Aria Fox");
    fireEvent.click(card.closest("button") as HTMLElement);

    // Back-to-browse confirms the drill-down is open.
    expect(await screen.findByText("Back to browse")).toBeInTheDocument();

    // Monitor switch must be enabled once the GET resolves.
    const sw = await screen.findByRole("switch", { name: /Monitor performer Aria Fox/i });
    await waitFor(() => expect(sw).not.toBeDisabled());

    // Toggle it.
    fireEvent.click(sw);

    await waitFor(() => {
      const put = calls.find(
        (c) => c.method === "PUT" && c.url.includes("/discover/monitor"),
      );
      expect(put).toBeDefined();
      expect(put?.body).toMatchObject({
        kind: "performer",
        name: "Aria Fox",
        monitored: true,
      });
    });
  });

  it("flips switch back on PUT failure (rollback)", async () => {
    const resolvedState = {
      resolved: true,
      source: "tpdb",
      entityId: "aria-fox",
      entityName: "Aria Fox",
      monitored: false,
      reason: "",
    };
    stubFetch((url, init) => {
      const method = (init?.method ?? "GET").toUpperCase();
      if (method === "PUT" && url.includes("/discover/monitor"))
        return jsonResponse({ error: "server error" }, 500);
      return onePerformerRow(resolvedState)(url, init);
    });

    render(() => <AdultDiscover />);

    const card = await screen.findByText("Aria Fox");
    fireEvent.click(card.closest("button") as HTMLElement);
    await screen.findByText("Back to browse");

    const sw = await screen.findByRole("switch", { name: /Monitor performer Aria Fox/i });
    await waitFor(() => expect(sw).not.toBeDisabled());

    // Switch starts unchecked (monitored: false).
    expect(sw).toHaveAttribute("aria-checked", "false");

    fireEvent.click(sw);

    // After optimistic flip, wait for the PUT to fail and roll back.
    await waitFor(() =>
      expect(sw).toHaveAttribute("aria-checked", "false"),
    );
  });
});

describe("AdultDiscover — monitor switch: unresolved performer", () => {
  it("renders a disabled switch with the reason string", async () => {
    const unresolvedState = {
      resolved: false,
      source: "",
      entityId: "",
      entityName: "Unknown Performer",
      monitored: false,
      reason: "No catalog match found for this performer.",
    };
    stubFetch(onePerformerRow(unresolvedState));

    render(() => <AdultDiscover />);

    const card = await screen.findByText("Aria Fox");
    fireEvent.click(card.closest("button") as HTMLElement);
    await screen.findByText("Back to browse");

    const sw = await screen.findByRole("switch", { name: /Monitor performer Aria Fox/i });
    await waitFor(() =>
      expect(
        screen.queryByText("No catalog match found for this performer."),
      ).toBeInTheDocument(),
    );
    expect(sw).toBeDisabled();
  });

  it("shows default copy when reason is empty", async () => {
    const unresolvedState = {
      resolved: false,
      source: "",
      entityId: "",
      entityName: "Aria Fox",
      monitored: false,
      reason: "",
    };
    stubFetch(onePerformerRow(unresolvedState));

    render(() => <AdultDiscover />);

    const card = await screen.findByText("Aria Fox");
    fireEvent.click(card.closest("button") as HTMLElement);
    await screen.findByText("Back to browse");

    await waitFor(() =>
      expect(
        screen.queryByText(
          /Can't monitor — this performer\/studio isn't matched to a catalog entry yet\./,
        ),
      ).toBeInTheDocument(),
    );
  });
});

describe("AdultDiscover — monitor switch visible with no bio", () => {
  it("renders the Monitor switch even when entityDetail returns no text", async () => {
    const resolvedState = {
      resolved: true,
      source: "tpdb",
      entityId: "aria-fox",
      entityName: "Aria Fox",
      monitored: false,
      reason: "",
    };
    // entityDetail returns empty text.
    stubFetch((url, init) => {
      if (url.includes("/api/modes/adult/description"))
        return jsonResponse({ text: "", source: "" });
      return onePerformerRow(resolvedState)(url, init);
    });

    render(() => <AdultDiscover />);

    const card = await screen.findByText("Aria Fox");
    fireEvent.click(card.closest("button") as HTMLElement);
    await screen.findByText("Back to browse");

    // Switch must appear even though there's no bio paragraph.
    const sw = await screen.findByRole("switch", { name: /Monitor performer Aria Fox/i });
    expect(sw).toBeInTheDocument();
  });
});

describe("AdultDiscover — monitor switch A→B drill doesn't leak state", () => {
  it("disables the switch while loading (no stale A state shown for B)", async () => {
    // Both entities are resolved but B's monitor fetch is deliberately slow.
    let resolveB: (r: Response) => void;
    const bMonitorPending = new Promise<Response>((res) => {
      resolveB = res;
    });

    const stateA = {
      resolved: true, source: "tpdb", entityId: "aria-fox",
      entityName: "Aria Fox", monitored: true, reason: "",
    };
    const stateB = {
      resolved: true, source: "tpdb", entityId: "bella-rose",
      entityName: "Bella Rose", monitored: false, reason: "",
    };

    stubFetch((url, init) => {
      if (url.includes("/api/modes/adult/description"))
        return jsonResponse({ text: "", source: "" });
      if (url.includes("/discover/monitor")) {
        // encodeURIComponent encodes spaces as %20, not +.
        if (url.includes("Aria"))
          return jsonResponse(stateA);
        if (url.includes("Bella"))
          return bMonitorPending;
      }
      if (url.includes("/newest-rows/performer-genders"))
        return jsonResponse(["female"]);
      if (url.includes("/newest-rows/1/resolve"))
        return jsonResponse([
          { id: "p1", title: "Aria Fox", studio: "", date: "", image: "https://cdn.theporndb.net/a.jpg", source: "", rowType: "performer" },
          { id: "p2", title: "Bella Rose", studio: "", date: "", image: "https://cdn.theporndb.net/b.jpg", source: "", rowType: "performer" },
        ]);
      if (url.includes("/api/modes/adult/newest-rows"))
        return jsonResponse([{ id: 1, title: "Performers", rowType: "performer", sortOrder: 0, enabled: true, createdAt: "2026-01-01T00:00:00Z", updatedAt: "2026-01-01T00:00:00Z" }]);
      if (url.includes("/discover/newest/entity-scenes"))
        return jsonResponse({ items: [], hasMore: false });
      return defaults(url) ?? undefined;
    });

    render(() => <AdultDiscover />);

    // Drill into A.
    const cardA = await screen.findByText("Aria Fox");
    fireEvent.click(cardA.closest("button") as HTMLElement);
    await screen.findByText("Back to browse");

    // A's switch resolves to enabled (monitored: true).
    const swA = await screen.findByRole("switch", { name: /Monitor performer Aria Fox/i });
    await waitFor(() => expect(swA).not.toBeDisabled());

    // Go back and drill into B.
    const back = screen.getByText("Back to browse");
    fireEvent.click(back);
    await screen.findByText("Female Performers");

    const cardB = await screen.findByText("Bella Rose");
    fireEvent.click(cardB.closest("button") as HTMLElement);
    await screen.findByText("Back to browse");

    // While B's monitor fetch is pending, the switch must be disabled
    // (loading guard prevents showing A's stale state).
    const swB = await screen.findByRole("switch", { name: /Monitor performer Bella Rose/i });
    expect(swB).toBeDisabled();

    // Unblock B's fetch.
    resolveB!(jsonResponse(stateB));
    await waitFor(() => expect(swB).not.toBeDisabled());
    expect(swB).toHaveAttribute("aria-checked", "false");
  });
});

describe("MonitoredRow — self-hide when empty", () => {
  it("renders nothing when monitored/resolve returns empty", async () => {
    stubFetch((url) => {
      if (url.includes("/newest-rows/monitored/resolve")) return jsonResponse([]);
      return defaults(url) ?? undefined;
    });

    render(() => <AdultDiscover />);

    // Wait for the component to settle.
    await screen.findByPlaceholderText("Search scenes by title…");
    expect(screen.queryByText("Monitored")).toBeNull();
  });

  it("renders the Monitored strip when resolve returns items", async () => {
    stubFetch((url) => {
      if (url.includes("/newest-rows/monitored/resolve"))
        return jsonResponse([
          {
            id: "m1",
            title: "Monitored Scene",
            studio: "Mon Studio",
            date: "2026-01-01",
            image: "https://cdn.theporndb.net/scenes/m1.jpg",
            source: "tpdb",
            rowType: "scene",
            durationSeconds: 0,
            releaseTitle: "",
          },
        ]);
      return defaults(url) ?? undefined;
    });

    render(() => <AdultDiscover />);

    expect(await screen.findByText("Monitored")).toBeInTheDocument();
    expect(await screen.findByText("Monitored Scene")).toBeInTheDocument();
  });
});
