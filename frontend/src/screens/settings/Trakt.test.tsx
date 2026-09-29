import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { TraktConnectionSection } from "./Trakt";
import { jsonResponse, noContent } from "../../testing/http";

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("Trakt watchlist ingest switch", () => {
  it("hides ingest until Trakt is linked", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) => {
        const url = String(input);
        if (url.includes("/trakt/status")) {
          return jsonResponse({ configured: true, linked: false, clientId: "abc" });
        }
        if (url.includes("/watchlist-ingest")) {
          return jsonResponse({ enabled: false });
        }
        return jsonResponse({});
      }),
    );
    render(() => <TraktConnectionSection />);
    await screen.findByLabelText("Trakt client ID");
    expect(
      screen.queryByRole("switch", {
        name: "Add watchlist titles automatically",
      }),
    ).toBeNull();
  });

  it("toggles ingest when Trakt is linked", async () => {
    let ingest = false;
    const puts: unknown[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        const method = (init?.method ?? "GET").toUpperCase();
        if (url.includes("/trakt/status")) {
          return jsonResponse({
            configured: true,
            linked: true,
            clientId: "abc",
          });
        }
        if (url.includes("/watchlist-ingest") && method === "PUT") {
          ingest = (JSON.parse(String(init?.body)) as { enabled: boolean })
            .enabled;
          puts.push({ enabled: ingest });
          return noContent();
        }
        if (url.includes("/watchlist-ingest")) {
          return jsonResponse({ enabled: ingest });
        }
        return jsonResponse({});
      }),
    );
    render(() => <TraktConnectionSection />);
    const sw = await screen.findByRole("switch", {
      name: "Add watchlist titles automatically",
    });
    await waitFor(() => {
      expect(sw).not.toBeDisabled();
    });
    expect(sw).toHaveAttribute("aria-checked", "false");
    fireEvent.click(sw);
    await waitFor(() => {
      expect(puts).toEqual([{ enabled: true }]);
    });
  });
});
