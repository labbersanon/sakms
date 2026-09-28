import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { IMDbListIngestCard, TMDBListIngestCard } from "./ListIngest";
import { jsonResponse, noContent } from "../../testing/http";

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("TMDB list ingest card", () => {
  it("toggles ingest and saves list IDs", async () => {
    let enabled = false;
    let listIds = "";
    const puts: unknown[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        const method = (init?.method ?? "GET").toUpperCase();
        if (url.includes("/tmdb/list-ingest") && method === "PUT") {
          const body = JSON.parse(String(init?.body)) as {
            enabled: boolean;
            listIds: string;
          };
          enabled = body.enabled;
          listIds = body.listIds;
          puts.push(body);
          return noContent();
        }
        if (url.includes("/tmdb/list-ingest")) {
          return jsonResponse({
            enabled,
            listIds,
            hasAccountSession: false,
          });
        }
        return jsonResponse({});
      }),
    );
    render(() => <TMDBListIngestCard />);
    const sw = await screen.findByRole("switch", {
      name: "Add TMDB list titles automatically",
    });
    await waitFor(() => {
      expect(sw).not.toBeDisabled();
    });
    const box = screen.getByLabelText("TMDB list IDs");
    fireEvent.input(box, { target: { value: "12345" } });
    fireEvent.click(sw);
    await waitFor(() => {
      expect(puts).toEqual([{ enabled: true, listIds: "12345" }]);
    });
  });
});

describe("IMDb list ingest card", () => {
  it("toggles ingest and saves list IDs", async () => {
    let enabled = false;
    let listIds = "";
    const puts: unknown[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        const method = (init?.method ?? "GET").toUpperCase();
        if (url.includes("/imdb/list-ingest") && method === "PUT") {
          const body = JSON.parse(String(init?.body)) as {
            enabled: boolean;
            listIds: string;
          };
          enabled = body.enabled;
          listIds = body.listIds;
          puts.push(body);
          return noContent();
        }
        if (url.includes("/imdb/list-ingest")) {
          return jsonResponse({ enabled, listIds });
        }
        return jsonResponse({});
      }),
    );
    render(() => <IMDbListIngestCard />);
    const sw = await screen.findByRole("switch", {
      name: "Add IMDb list titles automatically",
    });
    await waitFor(() => {
      expect(sw).not.toBeDisabled();
    });
    const box = screen.getByLabelText("IMDb list IDs");
    fireEvent.input(box, { target: { value: "ls123456789" } });
    fireEvent.click(sw);
    await waitFor(() => {
      expect(puts).toEqual([{ enabled: true, listIds: "ls123456789" }]);
    });
  });
});
