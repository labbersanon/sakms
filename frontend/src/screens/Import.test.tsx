import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { Import } from "./Import";
import { jsonResponse } from "../testing/http";

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

const pendingItem = {
  sourcePath: "/downloads/The.Matrix.1999.mkv",
  sourceName: "The.Matrix.1999.mkv",
  destPath: "/media/movies/The Matrix (1999) [tmdbid-603]/The Matrix (1999) [tmdbid-603].mkv",
  destRoot: "/media/movies",
  title: "The Matrix",
  year: 1999,
  tmdbId: 603,
  status: "pending",
  mode: "movies",
};

describe("Import", () => {
  it("scans a folder and moves the identified file into the library", async () => {
    const applies: unknown[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        const method = (init?.method ?? "GET").toUpperCase();
        if (url.includes("/api/browse")) {
          return jsonResponse({ path: "", entries: [] });
        }
        if (url.includes("/api/organize/import/scan") && method === "POST") {
          const body = JSON.parse(String(init?.body)) as {
            mode: string;
            path: string;
          };
          expect(body).toEqual({ mode: "movies", path: "/downloads" });
          return jsonResponse({
            path: "/downloads",
            destRoot: "/media/movies",
            items: [pendingItem],
          });
        }
        if (url.includes("/api/organize/import/apply") && method === "POST") {
          const body = JSON.parse(String(init?.body));
          applies.push(body);
          return jsonResponse({
            results: [
              {
                sourcePath: pendingItem.sourcePath,
                destPath: pendingItem.destPath,
                ok: true,
              },
            ],
          });
        }
        return jsonResponse({});
      }),
    );

    render(() => <Import />);
    fireEvent.input(screen.getByLabelText("Source folder"), {
      target: { value: "/downloads" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Scan" }));
    expect(await screen.findByText("The Matrix (1999)")).toBeInTheDocument();
    expect(screen.getByText(/Library: \/media\/movies/)).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Import selected" }));
    expect(
      await screen.findByText(/1 file will be moved into \/media\/movies/),
    ).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Move files" }));
    await waitFor(() => {
      expect(applies).toEqual([{ mode: "movies", items: [pendingItem] }]);
    });
  });

  it("does not import unmatched rows", async () => {
    const applies: unknown[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        if (url.includes("/api/browse")) {
          return jsonResponse({ path: "", entries: [] });
        }
        if (url.includes("/api/organize/import/scan")) {
          return jsonResponse({
            path: "/downloads",
            destRoot: "/media/movies",
            items: [
              {
                sourcePath: "/downloads/unknown.mkv",
                sourceName: "unknown.mkv",
                status: "unmatched",
                reason: "no year, actor, or duration",
                mode: "movies",
              },
            ],
          });
        }
        if (url.includes("/api/organize/import/apply")) {
          applies.push(JSON.parse(String(init?.body)));
          return jsonResponse({ results: [] });
        }
        return jsonResponse({});
      }),
    );

    render(() => <Import />);
    fireEvent.input(screen.getByLabelText("Source folder"), {
      target: { value: "/downloads" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Scan" }));
    expect(await screen.findByText("unknown.mkv")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Import selected" })).toBeDisabled();
    expect(applies).toEqual([]);
  });
});
