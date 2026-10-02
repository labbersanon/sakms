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

  it("scans Adult and moves a local scene into the library", async () => {
    const adultItem = {
      sourcePath: "/downloads/raw-scene.mp4",
      sourceName: "raw-scene.mp4",
      destPath: "/adult/Dump - Local Scene (2024-01-02) [phash-importhash].mp4",
      destRoot: "/adult",
      title: "Local Scene",
      box: "local",
      sceneId: "phash:importhash",
      studio: "Dump",
      date: "2024-01-02",
      phash: "importhash",
      status: "pending",
      mode: "adult",
    };
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
          expect(body).toEqual({ mode: "adult", path: "/downloads" });
          return jsonResponse({
            path: "/downloads",
            destRoot: "/adult",
            items: [adultItem],
          });
        }
        if (url.includes("/api/organize/import/apply") && method === "POST") {
          applies.push(JSON.parse(String(init?.body)));
          return jsonResponse({
            results: [
              {
                sourcePath: adultItem.sourcePath,
                destPath: adultItem.destPath,
                ok: true,
              },
            ],
          });
        }
        return jsonResponse({});
      }),
    );

    render(() => <Import />);
    fireEvent.click(screen.getByRole("button", { name: "Adult" }));
    fireEvent.input(screen.getByLabelText("Source folder"), {
      target: { value: "/downloads" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Scan" }));
    expect(await screen.findByText("Local Scene")).toBeInTheDocument();
    expect(screen.getByText(/Library: \/adult/)).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Import selected" }));
    expect(
      await screen.findByText(/1 file will be moved into \/adult/),
    ).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Move files" }));
    await waitFor(() => {
      expect(applies).toEqual([{ mode: "adult", items: [adultItem] }]);
    });
  });

  it("highlights Series when that library chip is clicked", () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => jsonResponse({ path: "", entries: [] })),
    );

    render(() => <Import />);
    const movies = screen.getByRole("button", { name: "Movies" });
    const series = screen.getByRole("button", { name: "Series" });
    expect(movies).toHaveClass("bg-accent");
    expect(series).not.toHaveClass("bg-accent");

    fireEvent.click(series);

    expect(series).toHaveClass("bg-accent");
    expect(movies).not.toHaveClass("bg-accent");
  });

  it("clears a previous scan when the library chip changes", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) => {
        const url = String(input);
        if (url.includes("/api/browse")) {
          return jsonResponse({ path: "", entries: [] });
        }
        if (url.includes("/api/organize/import/scan")) {
          return jsonResponse({
            path: "/downloads",
            destRoot: "/media/movies",
            items: [pendingItem],
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
    fireEvent.click(screen.getByRole("button", { name: "Series" }));
    expect(screen.queryByText("The Matrix (1999)")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Import selected" })).toBeDisabled();
  });

  it("names every dest root in the confirm copy", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) => {
        const url = String(input);
        if (url.includes("/api/browse")) {
          return jsonResponse({ path: "", entries: [] });
        }
        if (url.includes("/api/organize/import/scan")) {
          return jsonResponse({
            path: "/downloads",
            destRoot: "/media/movies",
            items: [
              pendingItem,
              {
                ...pendingItem,
                sourcePath: "/downloads/Kids.Movie.mkv",
                sourceName: "Kids.Movie.mkv",
                destPath: "/media/kids/Kids Movie/Kids Movie.mkv",
                destRoot: "/media/kids",
                title: "Kids Movie",
                tmdbId: 7,
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
    fireEvent.click(screen.getByRole("button", { name: "Import selected" }));
    expect(
      await screen.findByText(
        /2 files will be moved into \/media\/movies and \/media\/kids/,
      ),
    ).toBeInTheDocument();
  });
});
