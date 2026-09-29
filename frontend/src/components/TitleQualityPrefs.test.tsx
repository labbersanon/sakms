import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { TitleQualityPrefs } from "./TitleQualityPrefs";
import { jsonResponse } from "../testing/http";

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("TitleQualityPrefs movie upgrade-watch", () => {
  it("hides the watch switch when the movie is not in the library", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse({
          floor: "high",
          minResolution: 0,
          tiers: ["high", "lossless"],
          inherited: true,
        }),
      ),
    );
    render(() => (
      <TitleQualityPrefs mode="movies" titleKey={{ tmdbId: 42 }} />
    ));
    await screen.findByText("Minimum quality");
    expect(
      screen.queryByRole("switch", { name: "Watch for better release" }),
    ).toBeNull();
  });

  it("toggles watch for an owned movie", async () => {
    let watch = false;
    const calls: { url: string; method: string; body?: unknown }[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        const method = (init?.method ?? "GET").toUpperCase();
        calls.push({
          url,
          method,
          body: init?.body ? JSON.parse(String(init.body)) : undefined,
        });
        if (url.includes("/upgrade-watch") && method === "PUT") {
          watch = (init?.body
            ? (JSON.parse(String(init.body)) as { upgradeWatch: boolean })
                .upgradeWatch
            : false);
          return jsonResponse({ upgradeWatch: watch });
        }
        return jsonResponse({
          floor: "high",
          minResolution: 0,
          tiers: ["high", "lossless"],
          inherited: true,
          upgradeWatchAvailable: true,
          upgradeWatch: watch,
        });
      }),
    );
    render(() => (
      <TitleQualityPrefs mode="movies" titleKey={{ tmdbId: 42 }} />
    ));
    const sw = await screen.findByRole("switch", {
      name: "Watch for better release",
    });
    expect(sw).toHaveAttribute("aria-checked", "false");
    fireEvent.click(sw);
    await waitFor(() => {
      expect(
        calls.some(
          (c) =>
            c.method === "PUT" &&
            c.url.includes("/library/tmdb/42/upgrade-watch") &&
            (c.body as { upgradeWatch: boolean }).upgradeWatch === true,
        ),
      ).toBe(true);
    });
  });

  it("does not show the watch switch for series", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse({
          floor: "high",
          minResolution: 0,
          tiers: ["high", "lossless"],
          inherited: true,
        }),
      ),
    );
    render(() => (
      <TitleQualityPrefs mode="series" titleKey={{ seriesID: 7 }} />
    ));
    await screen.findByText("Minimum quality");
    expect(
      screen.queryByRole("switch", { name: "Watch for better release" }),
    ).toBeNull();
  });
});
